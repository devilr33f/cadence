#include "audiod/bluetooth-receiver.h"

#include <fcntl.h>
#include <unistd.h>

#include <algorithm>
#include <cerrno>
#include <cstring>
#include <utility>

namespace cadence::audiod {

namespace {

// what one read takes: about 90 ms of 44.1 kHz stereo
constexpr std::size_t kReadBytes = 16 * 1024;
// reads per wake, so a writer faster than real time cannot hold the loop
constexpr int kReadsPerWake = 16;
constexpr std::uint32_t kMinRate = 8000;
constexpr std::uint32_t kMaxRate = 192000;
constexpr std::size_t kMaxIdBytes = 64;

bool validId(std::string_view id) {
  return !id.empty() && id.size() <= kMaxIdBytes && std::all_of(id.begin(), id.end(), [](char c) {
    return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-';
  });
}

}  // namespace

BluetoothReceiver::BluetoothReceiver(const Log& log, BluetoothReceiverHooks hooks)
    : log_(log), hooks_(std::move(hooks)), chunk_(kReadBytes) {}

BluetoothReceiver::~BluetoothReceiver() { shutdown(); }

void BluetoothReceiver::configure(bool enabled) {
  if (enabled != enabled_)
    log_.info("bluetooth_receive", {{"enabled", enabled ? "true" : "false"}});
  enabled_ = enabled;
  if (enabled)
    return;
  if (stream_)
    endStream("disabled", true);
  player_.reset();
}

void BluetoothReceiver::stream(const BluetoothStreamInfo& info, UniqueFd fd) {
  const auto refuse = [&](std::string_view reason, int code = 0) {
    log_.warn("bluetooth_stream_refused", {{"reason", reason}, {"errno", code}});
  };
  if (!enabled_)
    return refuse("disabled");
  if (!validId(info.id) || info.rate < kMinRate || info.rate > kMaxRate || info.channels < 1 || info.channels > 2)
    return refuse("invalid");
  const int flags = ::fcntl(fd.get(), F_GETFL);
  if (flags < 0 || ::fcntl(fd.get(), F_SETFL, flags | O_NONBLOCK) < 0)
    return refuse("fcntl", errno);

  if (stream_ && stream_->info.id == info.id) {
    // the same phone's pcm opened again (a codec change): the session goes on
    if (hooks_.unwatch)
      hooks_.unwatch();
    stream_->info = info;
    stream_->fd = std::move(fd);
    stream_->partial.clear();
    log_.info("bluetooth_stream_replaced", {{"id", info.id}, {"rate", info.rate}, {"channels", info.channels}});
  } else {
    if (stream_)
      endStream("replaced", true);
    held_ = false;
    stream_.emplace();
    stream_->info = info;
    stream_->fd = std::move(fd);
    log_.info("bluetooth_stream_open", {{"id", info.id}, {"rate", info.rate}, {"channels", info.channels}});
  }
  if (!hooks_.watch || !hooks_.watch(stream_->fd.get(), [this] { readable(); })) {
    endStream("watch_failed", true);
    return;
  }
  publishMetadata();
  update();
}

void BluetoothReceiver::streamEnd(std::string_view id) {
  if (stream_ && stream_->info.id == id)
    endStream("closed", true);
}

void BluetoothReceiver::player(const BluetoothPlayerInfo& info) {
  const bool was = playerPlaying();
  player_ = info;
  if (stream_ && info.streamId == stream_->info.id && held_ && !was && info.playing) {
    // the phone was played again by hand: what held it is lifted
    held_ = false;
  }
  publishMetadata();
  update();
}

void BluetoothReceiver::gate(bool open) { gate_ = open; }

void BluetoothReceiver::reset() {
  enabled_ = false;
  if (stream_)
    endStream("client_gone", true);
  player_.reset();
}

void BluetoothReceiver::shutdown() {
  events_.reset();
  if (stream_)
    endStream("shutdown", false);
  hooks_.control = nullptr;
  hooks_.watch = nullptr;
  hooks_.unwatch = nullptr;
  output_ = nullptr;
}

void BluetoothReceiver::readable() {
  for (int reads = 0; reads < kReadsPerWake && stream_; ++reads) {
    const auto got = ::read(stream_->fd.get(), chunk_.data(), chunk_.size());
    if (got < 0 && errno == EINTR)
      continue;
    if (got < 0 && (errno == EAGAIN || errno == EWOULDBLOCK))
      return;
    if (got < 0) {
      log_.warn("bluetooth_read_failed", {{"id", stream_->info.id}, {"errno", errno}});
      endStream("read_failed", true);
      return;
    }
    if (got == 0) {
      endStream("eof", true);
      return;
    }
    auto& stream = *stream_;
    const auto size = static_cast<std::size_t>(got);
    stream.bytes += size;
    stream.lastData = hooks_.now();
    stream.partial.insert(stream.partial.end(), chunk_.begin(), chunk_.begin() + static_cast<std::ptrdiff_t>(size));
    const std::size_t frameBytes = stream.info.channels * sizeof(std::int16_t);
    const auto whole = stream.partial.size() / frameBytes * frameBytes;
    if (!stream.flowing) {
      stream.flowing = true;
      // a playing report may open the output before the first chunk goes out
      update();
      if (!stream_)
        return;
    }
    if (whole == 0)
      continue;
    // the bytes need not be aligned for their samples
    samples_.resize(whole / sizeof(std::int16_t));
    std::memcpy(samples_.data(), stream_->partial.data(), whole);
    stream_->partial.erase(stream_->partial.begin(), stream_->partial.begin() + static_cast<std::ptrdiff_t>(whole));
    deliver(samples_);
  }
}

void BluetoothReceiver::deliver(std::span<const std::int16_t> samples) {
  auto& stream = *stream_;
  const auto frames = samples.size() / stream.info.channels;
  if (output_ == nullptr || !gate_ || held_) {
    stream.droppedFrames += frames;
    return;
  }
  output_->tryPush(samples, stream.info.rate, stream.info.channels);
  stream.pushedFrames += frames;
}

void BluetoothReceiver::tick() {
  if (!stream_ || !stream_->flowing || hooks_.now() - stream_->lastData < kBluetoothSilence)
    return;
  stream_->flowing = false;
  update();
}

MediaActions BluetoothReceiver::actions() const {
  if (!stream_)
    return {};
  return {MediaAction::Pause, MediaAction::Resume, MediaAction::Stop, MediaAction::Next, MediaAction::Previous};
}

void BluetoothReceiver::openOutput(PcmInput& music) {
  output_ = &music;
  music.attach();
}

void BluetoothReceiver::closeOutput() {
  if (output_ != nullptr)
    output_->detach();
  output_ = nullptr;
}

// silent before it returns: nothing pushes from here, and what was queued is cut
void BluetoothReceiver::hold() {
  held_ = true;
  if (output_ != nullptr)
    output_->clear();
}

std::string_view BluetoothReceiver::pause() {
  control(MediaAction::Pause);
  hold();
  update();
  return kMediaOk;
}

std::string_view BluetoothReceiver::resume() {
  control(MediaAction::Resume);
  held_ = false;
  update();
  return kMediaOk;
}

// the sessions forget the source's state with the session, so the next pcm reports playing afresh
std::string_view BluetoothReceiver::stop() {
  control(MediaAction::Stop);
  hold();
  reported_ = Reported::None;
  return kMediaOk;
}

std::string_view BluetoothReceiver::next() {
  control(MediaAction::Next);
  return kMediaOk;
}

std::string_view BluetoothReceiver::previous() {
  control(MediaAction::Previous);
  return kMediaOk;
}

void BluetoothReceiver::control(MediaAction action) {
  if (stream_ && hooks_.control)
    hooks_.control(stream_->info.id, action);
}

void BluetoothReceiver::endStream(std::string_view reason, bool report) {
  auto& stream = *stream_;
  log_.info("bluetooth_stream_end", {{"id", stream.info.id},
                                     {"reason", reason},
                                     {"bytes", stream.bytes},
                                     {"frames", stream.pushedFrames},
                                     {"dropped", stream.droppedFrames}});
  if (hooks_.unwatch)
    hooks_.unwatch();
  stream_.reset();
  held_ = false;
  reported_ = Reported::None;
  sentMetadata_.reset();
  sentDuration_ = 0;
  sentPosition_.reset();
  if (report && events_)
    events_->ended();
}

void BluetoothReceiver::update() {
  if (!events_)
    return;
  const bool playing = stream_ && stream_->flowing && playerPlaying() && !held_;
  if (playing && reported_ != Reported::Playing) {
    reported_ = Reported::Playing;
    events_->playing();
  } else if (!playing && reported_ == Reported::Playing) {
    reported_ = Reported::Paused;
    events_->paused();
  }
}

void BluetoothReceiver::publishMetadata() {
  if (!events_ || !stream_)
    return;
  const auto* player = matchingPlayer();
  MediaMetadata metadata = player != nullptr ? player->metadata : MediaMetadata{};
  metadata.sender = stream_->info.sender;
  if (metadata != sentMetadata_) {
    sentMetadata_ = metadata;
    events_->metadata(metadata);
  }
  if (player == nullptr)
    return;
  if (player->durationMs != sentDuration_) {
    sentDuration_ = player->durationMs;
    events_->duration(sentDuration_);
  }
  if (player->positionMs != sentPosition_) {
    sentPosition_ = player->positionMs;
    events_->position(player->positionMs);
  }
}

bool BluetoothReceiver::playerPlaying() const {
  const auto* player = matchingPlayer();
  return player == nullptr || player->playing;
}

const BluetoothPlayerInfo* BluetoothReceiver::matchingPlayer() const {
  if (!stream_ || !player_ || player_->streamId != stream_->info.id)
    return nullptr;
  return &*player_;
}

}  // namespace cadence::audiod
