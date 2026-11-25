#include "audiod/soloist-handlers.h"

#include "audiod/sender-volume.h"
#include "audiod/soloist-source.h"

#include <algorithm>
#include <chrono>
#include <cstddef>
#include <cstring>
#include <string_view>
#include <utility>

namespace cadence::audiod {

namespace {

constexpr std::chrono::milliseconds kResendEvery{100};
// what a client that stopped reading may owe: past it the oldest is dropped, logged
constexpr std::size_t kMaxQueuedResults = 8;

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

cadence_audiod_v1_SoloistState toWire(SoloistState state) {
  switch (state) {
    case SoloistState::Off:
      return cadence_audiod_v1_SoloistState_SOLOIST_STATE_OFF;
    case SoloistState::KeyMissing:
      return cadence_audiod_v1_SoloistState_SOLOIST_STATE_KEY_MISSING;
    case SoloistState::NoBuild:
      return cadence_audiod_v1_SoloistState_SOLOIST_STATE_NO_BUILD;
    case SoloistState::Starting:
      return cadence_audiod_v1_SoloistState_SOLOIST_STATE_STARTING;
    case SoloistState::Ready:
      return cadence_audiod_v1_SoloistState_SOLOIST_STATE_READY;
    case SoloistState::Expired:
      return cadence_audiod_v1_SoloistState_SOLOIST_STATE_EXPIRED;
    case SoloistState::Failed:
      return cadence_audiod_v1_SoloistState_SOLOIST_STATE_FAILED;
  }
  return cadence_audiod_v1_SoloistState_SOLOIST_STATE_UNSPECIFIED;
}

}  // namespace

SoloistHandlers::SoloistHandlers(Loop& loop, IpcServer& ipc, const Log& log, Playback& playback, MediaHandlers& media,
                                 SoloistOptions options)
    : loop_(loop), ipc_(ipc), log_(log) {
  SoloistHooks hooks{
      .status = [this](const SoloistStatus& status) { loop_.post([this, status] { this->status(status); }); },
      .probed =
          [this](const std::string& requestId, const SoloistProbe& probe) {
            loop_.post([this, requestId, probe] { probed(requestId, probe); });
          },
      .senderVolume = [this](std::uint8_t level) { loop_.post([this, level] { senderVolume(level); }); },
      .post = [this](std::function<void()> task) { loop_.post(std::move(task)); },
  };
  supervisor_ = std::make_shared<SoloistSupervisor>(log, std::move(hooks), std::move(options));
  media.attach([this](std::shared_ptr<MediaSourceEvents> events) {
    supervisor_->events(std::move(events));
    return std::make_unique<SoloistSource>(supervisor_, [this](std::function<void()> task) {
      loop_.post(std::move(task));
    });
  });
  ipc_.on(cadence_audiod_v1_Envelope_soloist_config_tag,
          [this](const Envelope& envelope) { config(envelope.body.soloist_config); });
  ipc_.on(cadence_audiod_v1_Envelope_soloist_probe_tag,
          [this](const Envelope& envelope) { probe(envelope.body.soloist_probe); });
  ipc_.on(cadence_audiod_v1_Envelope_soloist_switch_tag, [this](const Envelope&) { supervisor_->switchBuild(); });
  media.onSenderLevel(cadence_audiod_v1_Sender_SENDER_SOLOIST,
                      [this](std::uint8_t level) { supervisor_->speakerLevel(level); });
  playback.onGateOpen([this] { supervisor_->gate(true); });
  playback.onGateShut([this] { supervisor_->gate(false); });
  supervisor_->start();
}

SoloistHandlers::~SoloistHandlers() { supervisor_->stop(); }

void SoloistHandlers::configure(const Configure& /*configuration*/) {
  announced_ = false;
  supervisor_->gate(false);
}

void SoloistHandlers::reset() {
  announced_ = false;
  statusPending_ = false;
  if (!results_.empty())
    log_.warn("soloist_probe_dropped", {{"reason", "gone"}, {"count", static_cast<std::uint64_t>(results_.size())}});
  results_.clear();
  if (resend_)
    loop_.cancel(*resend_);
  resend_.reset();
  supervisor_->gate(false);
}

void SoloistHandlers::config(const cadence_audiod_v1_SoloistConfig& request) {
  supervisor_->configure(SoloistSettings{.enabled = request.enabled,
                                         .apiKey = textOf(request.api_key),
                                         .deviceName = textOf(request.device_name),
                                         .dir = textOf(request.dir),
                                         .scratchDir = textOf(request.scratch_dir),
                                         .caFile = textOf(request.ca_file),
                                         .initialVolume = request.initial_volume});
  announced_ = true;
  statusPending_ = true;
  flush();
}

void SoloistHandlers::probe(const cadence_audiod_v1_SoloistProbe& request) {
  supervisor_->probe(textOf(request.request_id), textOf(request.path));
}

// a request for a cadenced that is gone, or has not configured soloist on this connection, is dropped: nothing moves
void SoloistHandlers::senderVolume(std::uint8_t level) {
  if (!announced_)
    return;
  Envelope request = cadence_audiod_v1_Envelope_init_zero;
  request.which_body = cadence_audiod_v1_Envelope_sender_volume_tag;
  request.body.sender_volume.sender = cadence_audiod_v1_Sender_SENDER_SOLOIST;
  request.body.sender_volume.level = level;
  if (!ipc_.send(request))
    log_.warn("soloist_sender_volume_dropped", {{"level", level}});
}

void SoloistHandlers::status(const SoloistStatus& status) {
  status_ = status;
  statusPending_ = true;
  flush();
}

void SoloistHandlers::probed(const std::string& requestId, const SoloistProbe& probe) {
  Envelope reply = cadence_audiod_v1_Envelope_init_zero;
  reply.which_body = cadence_audiod_v1_Envelope_soloist_probe_result_tag;
  auto& result = reply.body.soloist_probe_result;
  copyText(result.request_id, requestId);
  result.ok = probe.code.empty();
  copyText(result.code, probe.code);
  if (probe.build) {
    copyText(result.version, probe.build->version);
    result.build_epoch = probe.build->epoch;
  }
  if (!announced_) {
    log_.warn("soloist_probe_dropped", {{"reason", "unannounced"}});
    return;
  }
  if (results_.size() == kMaxQueuedResults) {
    log_.warn("soloist_probe_dropped", {{"reason", "full"}});
    results_.pop_front();
  }
  results_.push_back(reply);
  flush();
}

void SoloistHandlers::flush() {
  bool refused = false;
  if (announced_ && statusPending_ && status_) {
    Envelope reply = cadence_audiod_v1_Envelope_init_zero;
    reply.which_body = cadence_audiod_v1_Envelope_soloist_status_tag;
    auto& out = reply.body.soloist_status;
    out.state = toWire(status_->state);
    copyText(out.code, status_->code);
    copyText(out.version, status_->version);
    out.build_epoch = status_->buildEpoch;
    out.exit_code = status_->exitCode;
    out.silent_starts = status_->silentStarts;
    refused = !ipc_.send(reply);
    statusPending_ = refused;
  }
  while (!refused && !results_.empty()) {
    refused = !ipc_.send(results_.front());
    if (!refused)
      results_.pop_front();
  }
  if (!refused) {
    if (resend_)
      loop_.cancel(*resend_);
    resend_.reset();
    return;
  }
  if (resend_ || !ipc_.hasClient())
    return;
  // the client stopped reading and its socket is full: the rest goes out once it takes frames again
  resend_ = loop_.after(kResendEvery, [this] {
    resend_.reset();
    flush();
  });
}

}  // namespace cadence::audiod
