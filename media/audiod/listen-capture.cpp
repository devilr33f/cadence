#include "audiod/listen-capture.h"

#include "audiod/sparse.h"

#include <algorithm>
#include <chrono>
#include <condition_variable>
#include <mutex>
#include <system_error>
#include <utility>

namespace cadence::audiod {

namespace {

constexpr std::chrono::milliseconds kReadTimeout(20);
constexpr std::chrono::milliseconds kRetryDelay(100);
constexpr std::uint64_t kEncodeFailureLimit = 5;

std::int64_t nowMs() {
  return std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::system_clock::now().time_since_epoch())
      .count();
}

}  // namespace

ListenCapture::ListenCapture(std::unique_ptr<PcmSource> source, ListenEvents events, const Log& log)
    : source_(std::move(source)), events_(std::move(events)), log_(log) {}

ListenCapture::~ListenCapture() { stop(); }

std::optional<std::string_view> ListenCapture::start(const CaptureLayout& layout) {
  layout_ = layout;
  auto downmix = MicDownmix::create(layout_);
  if (!downmix.ok())
    return std::string_view("format");
  downmix_.emplace(std::move(downmix.value()));
  const auto granted = source_->open({.sampleRate = layout_.sampleRate,
                                      .channels = layout_.channels,
                                      .periodFrames = layout_.periodFrames,
                                      .bufferFrames = layout_.bufferFrames});
  if (!granted.ok())
    return std::string_view(granted.error().code == CaptureErrorCode::FormatMismatch ? "format" : "open");
  opened_ = true;
  try {
    reader_ = std::jthread([this](std::stop_token stop) { read(stop); });
  } catch (const std::system_error&) {
    stop();
    return std::string_view("thread");
  }
  return std::nullopt;
}

void ListenCapture::stop() {
  if (stopped_)
    return;
  stopped_ = true;
  reader_.request_stop();
  if (reader_.joinable())
    reader_.join();
  if (opened_)
    source_->close();
}

void ListenCapture::setMuted(bool muted) {
  const auto epoch = muteEpoch_.load();
  if ((epoch % 2 == 1) != muted)
    muteEpoch_.store(epoch + 1);
}

void ListenCapture::open(std::uint64_t session, std::int64_t prerollFromMs) {
  prerollFrom_.store(prerollFromMs);
  session_.store(session);
}

void ListenCapture::close() {
  session_.store(0);
  closed_.store(true);
}

ListenStats ListenCapture::stats() const {
  return {.frames = seq_, .recoveries = recoveries_, .readErrors = readErrors_};
}

void ListenCapture::read(const std::stop_token& stop) {
  std::vector<std::int16_t> captured(layout_.periodFrames * layout_.channels);
  std::vector<std::int16_t> mono;
  std::array<std::int16_t, kEncoderFrameSamples> frame{};
  std::uint64_t seenEpoch = muteEpoch_.load();
  std::size_t silence = 0;
  bool gap = true;
  std::mutex retryMutex;
  std::condition_variable_any retry;
  while (!stop.stop_requested()) {
    const auto got = source_->read(captured, kReadTimeout);
    switch (got.kind) {
      case CaptureRead::Kind::Idle:
        break;
      case CaptureRead::Kind::Frames: {
        const auto frames = std::min(got.frames, layout_.periodFrames);
        const auto samples = std::span(captured).first(frames * layout_.channels);
        const auto epoch = muteEpoch_.load();
        const bool muted = epoch % 2 == 1;
        const bool unmutedSince = !muted && epoch != seenEpoch;
        seenEpoch = epoch;
        if (muted) {
          mono.clear();
          preroll_.clear();
          gap = true;
          silence = layout_.bufferFrames;
          break;
        }
        if (unmutedSince) {
          mono.clear();
          preroll_.clear();
          gap = true;
          std::fill(samples.begin(), samples.end(), std::int16_t{0});
          silence = layout_.bufferFrames;
        } else if (silence > 0) {
          const auto silenced = std::min(frames, silence);
          std::fill_n(samples.begin(), silenced * layout_.channels, std::int16_t{0});
          silence -= silenced;
        }
        downmix_->push(samples, mono);
        const auto atMs = nowMs();
        std::size_t used = 0;
        for (; mono.size() - used >= frame.size(); used += frame.size()) {
          std::copy_n(mono.begin() + static_cast<std::ptrdiff_t>(used), frame.size(), frame.begin());
          heard(frame, atMs, std::exchange(gap, false));
        }
        mono.erase(mono.begin(), mono.begin() + static_cast<std::ptrdiff_t>(used));
        break;
      }
      case CaptureRead::Kind::Recovered:
        if (sparse(++recoveries_))
          log_.warn("capture_recovered", {{"count", recoveries_}});
        mono.clear();
        gap = true;
        break;
      case CaptureRead::Kind::Failed: {
        if (sparse(++readErrors_))
          log_.warn("capture_read_failed", {{"count", readErrors_}});
        mono.clear();
        gap = true;
        std::unique_lock lock(retryMutex);
        retry.wait_for(lock, stop, kRetryDelay, [] { return false; });
        break;
      }
    }
  }
}

void ListenCapture::heard(const std::array<std::int16_t, kEncoderFrameSamples>& pcm, std::int64_t atMs, bool gap) {
  ++seq_;
  if (closed_.exchange(false)) {
    preroll_.clear();
    gap = true;
  }
  const auto session = session_.load();
  if (session == 0) {
    preroll_.push_back({.pcm = pcm, .atMs = atMs});
    if (preroll_.size() > kListenPrerollFrames)
      preroll_.pop_front();
    if (events_.pcm)
      events_.pcm(seq_, atMs, std::vector<std::int16_t>(pcm.begin(), pcm.end()), gap);
    return;
  }
  if (session != encoding_) {
    encoding_ = session;
    encodeStreak_ = 0;
    encodeFailed_ = false;
    encoder_ = std::make_unique<OpusEncoder>();
    if (!encoder_->open().ok()) {
      encodeFailed_ = true;
      events_.failed(session);
    }
    if (const auto from = prerollFrom_.load(); from > 0) {
      for (const auto& kept : preroll_)
        if (kept.atMs >= from)
          encode(session, kept.pcm);
    }
    preroll_.clear();
  }
  encode(session, pcm);
}

void ListenCapture::encode(std::uint64_t session, std::span<const std::int16_t> pcm) {
  if (encodeFailed_)
    return;
  auto packet = encoder_->encode(pcm);
  if (!packet.ok()) {
    if (++encodeStreak_ == kEncodeFailureLimit) {
      encodeFailed_ = true;
      events_.failed(session);
    }
    return;
  }
  encodeStreak_ = 0;
  events_.packet(session, std::move(packet.value()));
}

}  // namespace cadence::audiod
