#include "audiod/airplay-uxplay-handlers.h"

#include "audiod/airplay-files.h"
#include "audiod/airplay-uxplay-source.h"

#include <fcntl.h>
#include <unistd.h>

#include <algorithm>
#include <array>
#include <cstdio>
#include <cstring>
#include <fstream>
#include <string>
#include <string_view>
#include <utility>

namespace cadence::audiod::uxplay {

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

cadence_audiod_v1_AirplayState toWire(ReceiverState state) {
  switch (state) {
    case ReceiverState::Off:
      return cadence_audiod_v1_AirplayState_AIRPLAY_STATE_OFF;
    case ReceiverState::Starting:
      return cadence_audiod_v1_AirplayState_AIRPLAY_STATE_STARTING;
    case ReceiverState::Ready:
      return cadence_audiod_v1_AirplayState_AIRPLAY_STATE_READY;
    case ReceiverState::Failed:
      return cadence_audiod_v1_AirplayState_AIRPLAY_STATE_FAILED;
  }
  return cadence_audiod_v1_AirplayState_AIRPLAY_STATE_UNSPECIFIED;
}

std::optional<std::array<std::uint8_t, 6>> parseHex(std::string_view text) {
  if (text.size() < 12)
    return std::nullopt;
  std::array<std::uint8_t, 6> out{};
  for (std::size_t i = 0; i < out.size(); ++i) {
    unsigned value = 0;
    if (std::sscanf(std::string(text.substr(2 * i, 2)).c_str(), "%2x", &value) != 1)
      return std::nullopt;
    out[i] = static_cast<std::uint8_t>(value);
  }
  return out;
}

}  // namespace

bool UxplayHandlers::Settings::operator==(const Settings& other) const {
  const auto& a = receiver;
  const auto& b = other.receiver;
  return enabled == other.enabled && a.name == b.name && a.mirroring == b.mirroring && a.maxWidth == b.maxWidth &&
         a.maxHeight == b.maxHeight && a.h265 == b.h265 && a.requirePin == b.requirePin && a.hls == b.hls &&
         session.scratchDir == other.session.scratchDir;
}

UxplayHandlers::UxplayHandlers(Loop& loop, IpcServer& ipc, const Log& log, Playback& playback, MediaHandlers& media,
                               TapServer& tap, UxplayOptions options)
    : loop_(loop),
      ipc_(ipc),
      log_(log),
      playback_(playback),
      tap_(tap),
      options_(std::move(options)),
      forwarder_([this](std::span<const std::uint8_t> encoded) {
        switch (tap_.sendMirror(encoded)) {
          case TapServer::Sent::Sent:
            return SendResult::Sent;
          case TapServer::Sent::Dropped:
            return SendResult::Dropped;
          case TapServer::Sent::NoClient:
            break;
        }
        return SendResult::NoClient;
      }) {
  SessionHooks hooks{
      .post = [this](std::function<void()> task) { loop_.post(std::move(task)); },
      .senderVolume = [this](std::uint8_t level) { senderVolume(level); },
      .mirrorState = [this](const MirrorReport& report) { mirrorState(report); },
      .pin = [this](const PinReport& report) { pin(report); },
      .mirrorUnit = [this](const std::string& id, const AccessUnit& unit,
                           std::int64_t pts) { forwarder_.unit(unit, pts); (void)id; },
      .hls = [this](const HlsRequest& request) { hls(request); },
      // libairplay's own: it only marks the connections, its httpd thread drops them
      .disconnect = [this] { loop_.post([this] { if (receiver_) receiver_->disconnect(); }); },
  };
  session_ = std::make_shared<Session>(log, playback.music(), std::move(hooks), SessionSettings{}, options_.clocks);
  media.attach([session = session_](std::shared_ptr<MediaSourceEvents> events) {
    session->events(std::move(events));
    return std::make_unique<Source>(session);
  });
  media.onSenderLevel(cadence_audiod_v1_Sender_SENDER_AIRPLAY,
                      [session = session_](std::uint8_t level) { session->speakerLevel(level); });
  ipc_.on(cadence_audiod_v1_Envelope_airplay_config_tag,
          [this](const Envelope& envelope) { config(envelope.body.airplay_config); });
  ipc_.on(cadence_audiod_v1_Envelope_hls_playback_info_tag,
          [this](const Envelope& envelope) { playbackInfo(envelope.body.hls_playback_info); });
  tap_.onClient([this] { forwarder_.clientConnected(); });
  playback.onGateOpen([this] {
    gateOpen_ = true;
    apply();
  });
  playback.onGateShut([this] {
    gateOpen_ = false;
    apply();
  });
}

// the receiver's threads stop before the session they call goes
UxplayHandlers::~UxplayHandlers() {
  if (timer_)
    loop_.cancel(*timer_);
  if (resend_)
    loop_.cancel(*resend_);
  receiver_.reset();
  session_->shutdown("disabled");
}

void UxplayHandlers::configure(const Configure& /*configuration*/) {
  announced_ = false;
  gateOpen_ = false;
  apply();
}

void UxplayHandlers::reset() {
  announced_ = false;
  statusPending_ = false;
  if (resend_)
    loop_.cancel(*resend_);
  resend_.reset();
  gateOpen_ = false;
  apply();
}

void UxplayHandlers::config(const cadence_audiod_v1_AirplayConfig& request) {
  Settings next;
  next.enabled = request.enabled;
  next.receiver.name = textOf(request.name);
  next.receiver.mirroring = request.mirroring;
  next.receiver.maxWidth = request.max_width;
  next.receiver.maxHeight = request.max_height;
  next.receiver.h265 = request.h265;
  next.receiver.requirePin = request.require_pin;
  // libairplay runs on screens alone, whose player opens a cast's url
  next.receiver.hls = true;
  next.receiver.port = options_.port;
  next.session.scratchDir = textOf(request.scratch_dir);
  next.session.mirroring = request.mirroring;
  next.session.h265 = request.h265;
  next.session.requirePin = request.require_pin;
  announced_ = true;
  statusPending_ = true;
  if (request.enabled && request.backend == cadence_audiod_v1_AirplayBackend_AIRPLAY_BACKEND_SHAIRPORT) {
    // this build has libairplay alone
    stopReceiver("disabled");
    settings_ = Settings{};
    status(ReceiverState::Failed, "backend_unavailable");
    return;
  }
  const bool changed = !(next == settings_);
  settings_ = std::move(next);
  if (changed)
    stopReceiver("disabled");
  apply();
  flushStatus();
}

void UxplayHandlers::apply() {
  const bool wanted = settings_.enabled && gateOpen_;
  if (!settings_.enabled) {
    stopReceiver("disabled");
    status(ReceiverState::Off);
    return;
  }
  if (!wanted) {
    // a shut gate keeps the receiver going, its audio unheard (the sessions hold it); a receiver not yet started waits
    if (!receiver_)
      status(ReceiverState::Starting);
    return;
  }
  if (!receiver_)
    startReceiver();
}

std::optional<std::array<std::uint8_t, 6>> UxplayHandlers::deviceId(const std::string& scratch) const {
  const auto path = scratch + "/device-id";
  {
    std::ifstream in(path);
    std::string line;
    if (std::getline(in, line))
      if (auto known = parseHex(line))
        return known;
  }
  std::array<std::uint8_t, 6> id{};
  const int fd = ::open("/dev/urandom", O_RDONLY | O_CLOEXEC);
  if (fd < 0)
    return std::nullopt;
  const auto got = ::read(fd, id.data(), id.size());
  ::close(fd);
  if (got != static_cast<ssize_t>(id.size()))
    return std::nullopt;
  // a locally administered unicast address
  id[0] = static_cast<std::uint8_t>((id[0] & 0xfc) | 0x02);
  char text[13];
  std::snprintf(text, sizeof text, "%02x%02x%02x%02x%02x%02x", id[0], id[1], id[2], id[3], id[4], id[5]);
  if (airplay::writePrivate(path, std::string(text) + "\n"))
    return std::nullopt;
  return id;
}

void UxplayHandlers::startReceiver() {
  const auto& scratch = settings_.session.scratchDir;
  if (scratch.empty() || settings_.receiver.name.empty()) {
    status(ReceiverState::Failed, "invalid_config");
    return;
  }
  if (const auto error = airplay::privateDir(scratch)) {
    log_.warn("airplay_scratch_failed", {{"call", error->call}, {"errno", error->code}});
    status(ReceiverState::Failed, "scratch_failed");
    return;
  }
  const auto id = deviceId(scratch);
  if (!id) {
    status(ReceiverState::Failed, "scratch_failed");
    return;
  }
  auto receiver = settings_.receiver;
  receiver.hwAddress = *id;
  receiver.keyFile = scratch + "/airplay.pem";
  session_->configure(settings_.session);
  status(ReceiverState::Starting);
  receiver_ = Receiver::start(log_, receiver, *session_);
  if (!receiver_) {
    status(ReceiverState::Failed, "start_failed");
    return;
  }
  status(ReceiverState::Ready);
  arm();
}

void UxplayHandlers::stopReceiver(std::string_view reason) {
  if (!receiver_)
    return;
  receiver_.reset();
  session_->shutdown(reason);
  forwarder_.stop();
  if (anchored_)
    if (auto* clock = playback_.clock())
      clock->clearAnchor();
  anchored_ = false;
  if (timer_)
    loop_.cancel(*timer_);
  timer_.reset();
  log_.info("airplay_receiver_stopped", {{"reason", reason}});
}

void UxplayHandlers::arm() {
  if (timer_ || !receiver_)
    return;
  timer_ = loop_.after(kTickEvery, [this] {
    timer_.reset();
    tick();
    arm();
  });
}

void UxplayHandlers::tick() {
  auto* clock = playback_.clock();
  const auto playhead = clock != nullptr ? clock->playhead() : std::nullopt;
  if (playhead)
    session_->playhead(*playhead);
  session_->tick();
  if (const auto now = options_.clocks.steady(); now - statsAt_ >= kAudioStatsEvery) {
    statsAt_ = now;
    logAudioStats();
  }
  if (clock == nullptr)
    return;
  if (const auto anchor = session_->anchor(); anchor && playhead) {
    clock->anchor(anchor->mediaNs, frameAt(anchor->monoNs, playhead->frames, playhead->heardNs));
    anchored_ = true;
  } else if (anchored_ && !session_->mirroring()) {
    clock->clearAnchor();
    anchored_ = false;
  }
}

void UxplayHandlers::logAudioStats() {
  const auto s = session_->takeStats();
  const auto window = playback_.takeOutputWindow();
  // a quiet receiver says nothing: no line without a packet in the window
  if (s.packets == 0)
    return;
  const OutputWindow out = window.value_or(OutputWindow{});
  const auto ms = [](std::int64_t ns) { return ns / 1'000'000; };
  const auto us = [](std::int64_t ns) { return ns / 1'000; };
  const auto ms44 = [](std::uint64_t frames) { return frames * 1000 / kStreamRate; };
  const auto ms48 = [](std::size_t frames) { return static_cast<std::uint64_t>(frames) * 1000 / dsp::kRate; };
  log_.info("airplay_audio", {{"packets", s.packets},
                              {"lost", s.lost},
                              {"late", s.late},
                              {"stall_max_ms", ms(s.stallMaxNs)},
                              {"lead_min_ms", ms(s.leadMinNs)},
                              {"lead_avg_ms", ms(s.leadAvgNs)},
                              {"queue_min_ms", ms(s.queueMinNs)},
                              {"queue_avg_ms", ms(s.queueAvgNs)},
                              {"decode_p95_us", us(s.decode.p95Ns)},
                              {"decode_max_us", us(s.decode.maxNs)},
                              {"concealed_ms", ms44(s.concealedFrames)},
                              {"overlap_ms", ms44(s.overlapFrames)},
                              {"silence_ms", ms44(s.silenceFrames)},
                              {"dropped_ms", ms44(s.droppedFrames)},
                              {"hard_cuts", s.hardCuts},
                              {"shifts", s.shifts},
                              {"recoveries", s.recoveries},
                              {"steer_ppm", s.steerPpm},
                              {"steer_min_ppm", s.steerMinPpm},
                              {"steer_max_ppm", s.steerMaxPpm},
                              {"error_ms", ms(s.errorNs)},
                              {"delay_ms", ms(s.delayNs)},
                              {"in_overruns", s.overruns},
                              {"in_underruns", s.underruns},
                              {"rtp_nice", s.rtpPriority.nice},
                              {"player_nice", out.priority.nice},
                              {"player_nice_errno", out.priority.error},
                              {"out_recoveries", out.recoveries},
                              {"out_underruns", out.underruns},
                              {"xruns", out.xruns},
                              {"burst", out.burstFrames},
                              {"fill_ms", ms48(out.fillFrames)},
                              {"fill_min_ms", ms48(out.fillMinFrames)},
                              {"callback_p95_ms", ms(out.period.p95Ns)},
                              {"callback_max_ms", ms(out.period.maxNs)}});
}

void UxplayHandlers::status(ReceiverState state, std::string_view code) {
  if (state == state_ && code == code_)
    return;
  state_ = state;
  code_ = std::string(code);
  statusPending_ = true;
  flushStatus();
}

void UxplayHandlers::flushStatus() {
  if (!announced_ || !statusPending_)
    return;
  Envelope reply = cadence_audiod_v1_Envelope_init_zero;
  reply.which_body = cadence_audiod_v1_Envelope_airplay_status_tag;
  reply.body.airplay_status.state = toWire(state_);
  copyText(reply.body.airplay_status.code, code_);
  if (ipc_.send(reply)) {
    statusPending_ = false;
    return;
  }
  if (resend_ || !ipc_.hasClient())
    return;
  resend_ = loop_.after(kResendEvery, [this] {
    resend_.reset();
    flushStatus();
  });
}

void UxplayHandlers::mirrorState(const MirrorReport& report) {
  if (report.active)
    forwarder_.start(report.sessionId);
  else
    forwarder_.stop();
  log_.info("airplay_mirror_state", {{"id", report.sessionId},
                                     {"active", report.active ? "yes" : "no"},
                                     {"codec", report.h265 ? "h265" : "h264"},
                                     {"width", report.width},
                                     {"height", report.height},
                                     {"reason", report.reason}});
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_mirror_state_tag;
  auto& state = envelope.body.mirror_state;
  copyText(state.session_id, report.sessionId);
  state.active = report.active;
  state.codec = report.h265 ? cadence_audiod_v1_MirrorCodec_MIRROR_CODEC_H265 : cadence_audiod_v1_MirrorCodec_MIRROR_CODEC_H264;
  state.width = report.width;
  state.height = report.height;
  copyText(state.client_name, report.clientName);
  copyText(state.reason, report.reason);
  if (!ipc_.send(envelope))
    log_.warn("airplay_mirror_state_dropped");
}

void UxplayHandlers::pin(const PinReport& report) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_airplay_pin_tag;
  envelope.body.airplay_pin.active = report.active;
  copyText(envelope.body.airplay_pin.pin, report.pin);
  copyText(envelope.body.airplay_pin.client_name, report.clientName);
  if (!ipc_.send(envelope))
    log_.warn("airplay_pin_dropped", {{"active", report.active ? "yes" : "no"}});
}

// a request for a cadenced that is gone, or has not configured airplay on this connection, is dropped
void UxplayHandlers::senderVolume(std::uint8_t level) {
  if (!announced_)
    return;
  Envelope request = cadence_audiod_v1_Envelope_init_zero;
  request.which_body = cadence_audiod_v1_Envelope_sender_volume_tag;
  request.body.sender_volume.sender = cadence_audiod_v1_Sender_SENDER_AIRPLAY;
  request.body.sender_volume.level = level;
  if (!ipc_.send(request))
    log_.warn("airplay_sender_volume_dropped", {{"level", level}});
}

void UxplayHandlers::hls(const HlsRequest& request) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_hls_cast_tag;
  auto& cast = envelope.body.hls_cast;
  const auto ms = [](float s) { return s > 0 ? static_cast<std::uint64_t>(static_cast<double>(s) * 1000.0) : 0; };
  switch (request.op) {
    case HlsRequest::Op::Play:
      // a new cast is a new session, which displaces the one before on the screen as any play does
      cast_ = "airplay-hls-" + std::to_string(++casts_);
      cast.action = cadence_audiod_v1_HlsCastAction_HLS_CAST_ACTION_PLAY;
      // a url cut short would open something else
      if (request.url.empty() || request.url.size() >= sizeof cast.url) {
        log_.warn("airplay_hls_refused", {{"id", cast_}, {"code", "url_too_long"}});
        cast_.clear();
        if (receiver_)
          receiver_->disconnect();
        return;
      }
      copyText(cast.url, request.url);
      cast.start_ms = ms(request.value);
      copyText(cast.client_name, request.clientName);
      break;
    case HlsRequest::Op::Scrub:
      cast.action = cadence_audiod_v1_HlsCastAction_HLS_CAST_ACTION_SCRUB;
      cast.position_ms = ms(request.value);
      break;
    case HlsRequest::Op::Rate:
      cast.action = cadence_audiod_v1_HlsCastAction_HLS_CAST_ACTION_RATE;
      // a sender sends 0 or 1; the rate never sets a level (rule 7)
      cast.rate = request.value > 0 ? 1.0f : 0.0f;
      break;
    case HlsRequest::Op::Stop:
      cast.action = cadence_audiod_v1_HlsCastAction_HLS_CAST_ACTION_STOP;
      break;
  }
  // scrub, rate and stop act on the cast that plays; with none (ended, or a stop's echo) there is nothing to tell
  if (cast_.empty())
    return;
  copyText(cast.session_id, cast_);
  log_.info("airplay_hls_cast", {{"id", cast_}, {"action", static_cast<int>(cast.action)}});
  if (request.op == HlsRequest::Op::Stop)
    cast_.clear();
  if (!announced_ || !ipc_.send(envelope))
    log_.warn("airplay_hls_cast_dropped", {{"action", static_cast<int>(cast.action)}});
}

void UxplayHandlers::playbackInfo(const cadence_audiod_v1_HlsPlaybackInfo& info) {
  // a late report of a cast that is gone changes nothing
  if (cast_.empty() || cast_ != info.session_id)
    return;
  const auto s = [](std::uint64_t ms) { return static_cast<double>(ms) / 1000.0; };
  session_->playbackInfo(HlsInfo{
      .durationS = s(info.duration_ms),
      .positionS = s(info.position_ms),
      .rate = info.rate,
      .ready = info.ready,
      .bufferedS = s(info.buffered_ms),
      .bufferEmpty = info.buffer_empty,
      .likelyToKeepUp = info.likely_to_keep_up,
  });
  if (!info.ended)
    return;
  // the cast finished on the screen (its end, an error, a displacement): the sender's session ends with it
  log_.info("airplay_hls_ended", {{"id", cast_}, {"reason", textOf(info.reason)}});
  cast_.clear();
  if (receiver_)
    receiver_->disconnect();
}

}  // namespace cadence::audiod::uxplay
