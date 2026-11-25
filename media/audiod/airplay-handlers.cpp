#include "audiod/airplay-handlers.h"

#include "audiod/airplay-source.h"

#include <algorithm>
#include <chrono>
#include <cstddef>
#include <cstring>
#include <string>
#include <string_view>
#include <utility>

namespace cadence::audiod {

namespace {

constexpr std::chrono::milliseconds kResendEvery{100};

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

cadence_audiod_v1_AirplayState toWire(AirplayState state) {
  switch (state) {
    case AirplayState::Off:
      return cadence_audiod_v1_AirplayState_AIRPLAY_STATE_OFF;
    case AirplayState::Starting:
      return cadence_audiod_v1_AirplayState_AIRPLAY_STATE_STARTING;
    case AirplayState::Ready:
      return cadence_audiod_v1_AirplayState_AIRPLAY_STATE_READY;
    case AirplayState::Failed:
      return cadence_audiod_v1_AirplayState_AIRPLAY_STATE_FAILED;
  }
  return cadence_audiod_v1_AirplayState_AIRPLAY_STATE_UNSPECIFIED;
}

}  // namespace

AirplayHandlers::AirplayHandlers(Loop& loop, IpcServer& ipc, const Log& log, Playback& playback, MediaHandlers& media,
                                 AirplayOptions options, AirplayControlTimings control)
    : loop_(loop), ipc_(ipc), log_(log) {
  AirplayHooks hooks{
      .status = [this](const AirplayStatus& status) { loop_.post([this, status] { this->status(status); }); },
  };
  supervisor_ = std::make_shared<AirplaySupervisor>(log, std::move(hooks), std::move(options));
  auto* supervisor = supervisor_.get();
  control_ = std::make_shared<AirplayControl>(
      log,
      AirplayControlHooks{
          .post = [this](std::function<void()> task) { loop_.post(std::move(task)); },
          .hold = [supervisor] { supervisor->holdOutput(true); },
          .release = [supervisor](std::uint64_t pausesTaken) { supervisor->releaseOutput(pausesTaken); },
          .pauses = [supervisor] { return supervisor->outputPauses(); },
          .senderVolume = [this](std::uint8_t level) { loop_.post([this, level] { senderVolume(level); }); }},
      control);
  supervisor_->receiver(control_);
  media.attach([this](std::shared_ptr<MediaSourceEvents> events) {
    control_->events(std::move(events));
    // the source outlives this part's members on the way down: it keeps the supervisor
    AirplaySourceLink link{.openOutput = [supervisor = supervisor_](PcmInput& music) { supervisor->openOutput(music); },
                           .closeOutput = [supervisor = supervisor_] { supervisor->closeOutput(); },
                           .pauseOutput = [supervisor = supervisor_] { return supervisor->pauseOutput(); },
                           .run = [supervisor = supervisor_](std::function<void()> task) {
                             supervisor->post(std::move(task));
                           }};
    return std::make_unique<AirplaySource>(std::move(link), control_,
                                           [this](std::function<void()> task) { loop_.post(std::move(task)); });
  });
  media.onSenderLevel(cadence_audiod_v1_Sender_SENDER_AIRPLAY, [this](std::uint8_t level) {
    supervisor_->post([control = control_, level] { control->speakerLevel(level); });
  });
  ipc_.on(cadence_audiod_v1_Envelope_airplay_config_tag,
          [this](const Envelope& envelope) { config(envelope.body.airplay_config); });
  playback.onGateOpen([this] { supervisor_->gate(true); });
  playback.onGateShut([this] { supervisor_->gate(false); });
  supervisor_->start();
}

AirplayHandlers::~AirplayHandlers() { supervisor_->stop(); }

void AirplayHandlers::configure(const Configure& /*configuration*/) {
  announced_ = false;
  supervisor_->gate(false);
}

void AirplayHandlers::reset() {
  announced_ = false;
  statusPending_ = false;
  if (resend_)
    loop_.cancel(*resend_);
  resend_.reset();
  supervisor_->gate(false);
}

void AirplayHandlers::config(const cadence_audiod_v1_AirplayConfig& request) {
  supervisor_->configure(AirplaySettings{.enabled = request.enabled,
                                         .name = textOf(request.name),
                                         .dir = textOf(request.dir),
                                         .scratchDir = textOf(request.scratch_dir)});
  announced_ = true;
  statusPending_ = true;
  flush();
}

// a request for a cadenced that is gone, or has not configured airplay on this connection, is dropped: nothing moves
void AirplayHandlers::senderVolume(std::uint8_t level) {
  if (!announced_)
    return;
  Envelope request = cadence_audiod_v1_Envelope_init_zero;
  request.which_body = cadence_audiod_v1_Envelope_sender_volume_tag;
  request.body.sender_volume.sender = cadence_audiod_v1_Sender_SENDER_AIRPLAY;
  request.body.sender_volume.level = level;
  if (!ipc_.send(request))
    log_.warn("airplay_sender_volume_dropped", {{"level", level}});
}

void AirplayHandlers::status(const AirplayStatus& status) {
  status_ = status;
  statusPending_ = true;
  flush();
}

void AirplayHandlers::flush() {
  if (!announced_ || !statusPending_ || !status_)
    return;
  Envelope reply = cadence_audiod_v1_Envelope_init_zero;
  reply.which_body = cadence_audiod_v1_Envelope_airplay_status_tag;
  reply.body.airplay_status.state = toWire(status_->state);
  copyText(reply.body.airplay_status.code, status_->code);
  if (ipc_.send(reply)) {
    statusPending_ = false;
    if (resend_)
      loop_.cancel(*resend_);
    resend_.reset();
    return;
  }
  if (resend_ || !ipc_.hasClient())
    return;
  // the client stopped reading and its socket is full: the latest goes out once it takes frames again
  resend_ = loop_.after(kResendEvery, [this] {
    resend_.reset();
    flush();
  });
}

}  // namespace cadence::audiod
