#include "audiod/bluetooth-handlers.h"

#include "audiod/bluetooth-source.h"

#include <sys/epoll.h>

#include <algorithm>
#include <chrono>
#include <cstddef>
#include <cstring>
#include <string>
#include <string_view>
#include <utility>

namespace cadence::audiod {

namespace {

// how often an open stream is checked for silence: a pause shows within kBluetoothSilence and this
constexpr std::chrono::milliseconds kTickEvery{250};

template <std::size_t N>
void copyText(char (&out)[N], std::string_view text) {
  const auto size = std::min(text.size(), N - 1);
  std::memcpy(out, text.data(), size);
  out[size] = '\0';
}

template <std::size_t N>
std::string textOf(const char (&field)[N]) {
  return {field, static_cast<std::size_t>(std::find(field, field + N, '\0') - field)};
}

cadence_audiod_v1_MediaAction toWire(MediaAction action) {
  switch (action) {
    case MediaAction::Pause:
      return cadence_audiod_v1_MediaAction_MEDIA_ACTION_PAUSE;
    case MediaAction::Resume:
      return cadence_audiod_v1_MediaAction_MEDIA_ACTION_RESUME;
    case MediaAction::Stop:
      return cadence_audiod_v1_MediaAction_MEDIA_ACTION_STOP;
    case MediaAction::Seek:
      return cadence_audiod_v1_MediaAction_MEDIA_ACTION_SEEK;
    case MediaAction::Next:
      return cadence_audiod_v1_MediaAction_MEDIA_ACTION_NEXT;
    case MediaAction::Previous:
      return cadence_audiod_v1_MediaAction_MEDIA_ACTION_PREVIOUS;
  }
  return cadence_audiod_v1_MediaAction_MEDIA_ACTION_UNSPECIFIED;
}

std::string_view actionToken(MediaAction action) {
  switch (action) {
    case MediaAction::Pause:
      return "pause";
    case MediaAction::Resume:
      return "resume";
    case MediaAction::Stop:
      return "stop";
    case MediaAction::Seek:
      return "seek";
    case MediaAction::Next:
      return "next";
    case MediaAction::Previous:
      return "previous";
  }
  return "unknown";
}

}  // namespace

BluetoothHandlers::BluetoothHandlers(Loop& loop, IpcServer& ipc, const Log& log, Playback& playback,
                                     MediaHandlers& media)
    : loop_(loop), ipc_(ipc), log_(log) {
  BluetoothReceiverHooks hooks{
      .control =
          [this](std::string_view streamId, MediaAction action) {
            Envelope request = cadence_audiod_v1_Envelope_init_zero;
            request.which_body = cadence_audiod_v1_Envelope_bluetooth_control_tag;
            copyText(request.body.bluetooth_control.stream_id, streamId);
            request.body.bluetooth_control.action = toWire(action);
            if (!ipc_.send(request))
              log_.warn("bluetooth_control_dropped", {{"id", streamId}, {"action", actionToken(action)}});
          },
      .watch =
          [this](int fd, std::function<void()> readable) {
            const auto watched = loop_.watch(fd, EPOLLIN | EPOLLRDHUP,
                                             [readable = std::move(readable)](std::uint32_t) { readable(); });
            if (!watched.ok()) {
              log_.warn("bluetooth_watch_failed", {{"errno", watched.error().code}});
              return false;
            }
            watch_ = watched.value();
            armTick();
            return true;
          },
      .unwatch =
          [this] {
            if (watch_)
              loop_.unwatch(*watch_);
            watch_.reset();
          },
  };
  receiver_ = std::make_shared<BluetoothReceiver>(log, std::move(hooks));
  media.attach([receiver = receiver_](std::shared_ptr<MediaSourceEvents> events) {
    receiver->events(std::move(events));
    return std::make_unique<BluetoothSource>(receiver);
  });
  ipc_.on(cadence_audiod_v1_Envelope_bluetooth_config_tag, [this](const Envelope& envelope) {
    receiver_->configure(envelope.body.bluetooth_config.enabled);
  });
  ipc_.onFd(cadence_audiod_v1_Envelope_bluetooth_stream_tag, [this](const Envelope& envelope, UniqueFd fd) {
    const auto& stream = envelope.body.bluetooth_stream;
    receiver_->stream(BluetoothStreamInfo{.id = textOf(stream.stream_id),
                                          .rate = stream.rate,
                                          .channels = stream.channels,
                                          .sender = textOf(stream.sender)},
                      std::move(fd));
  });
  ipc_.on(cadence_audiod_v1_Envelope_bluetooth_stream_end_tag, [this](const Envelope& envelope) {
    receiver_->streamEnd(textOf(envelope.body.bluetooth_stream_end.stream_id));
  });
  ipc_.on(cadence_audiod_v1_Envelope_bluetooth_player_tag, [this](const Envelope& envelope) {
    const auto& player = envelope.body.bluetooth_player;
    BluetoothPlayerInfo info{.streamId = textOf(player.stream_id),
                             .playing = player.playing,
                             .metadata = {},
                             .durationMs = player.duration_ms,
                             .positionMs = player.position_ms};
    if (player.has_metadata) {
      info.metadata.title = textOf(player.metadata.title);
      info.metadata.artist = textOf(player.metadata.artist);
      info.metadata.album = textOf(player.metadata.album);
    }
    receiver_->player(info);
  });
  playback.onGateOpen([this] { receiver_->gate(true); });
  playback.onGateShut([this] { receiver_->gate(false); });
}

BluetoothHandlers::~BluetoothHandlers() {
  receiver_->shutdown();
  if (tick_)
    loop_.cancel(*tick_);
}

void BluetoothHandlers::reset() { receiver_->reset(); }

// runs only while a stream is open, so an idle receiver never wakes the loop
void BluetoothHandlers::armTick() {
  if (tick_)
    return;
  tick_ = loop_.after(kTickEvery, [this] {
    tick_.reset();
    receiver_->tick();
    if (receiver_->hasStream())
      armTick();
  });
}

}  // namespace cadence::audiod
