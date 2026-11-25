#include "audiod/playback.h"

#include "dsp/level-curve.h"

#include "audiod/media-settings.h"
#include "audiod/sparse.h"
#include "playback/alarm-tones.h"
#include "playback/earcons.h"

#ifdef CADENCE_MEDIA_AAUDIO
#include "aaudio/aaudio-playback.h"
#endif

#include <algorithm>
#include <cstring>
#include <span>
#include <utility>

namespace cadence::audiod {

namespace {

constexpr std::size_t kSamplesPerMs = static_cast<std::size_t>(kPlaybackSampleRate / 1000);

template <std::size_t N>
void copyText(char (&out)[N], std::string_view text) {
  const auto size = std::min(text.size(), N - 1);
  std::memcpy(out, text.data(), size);
  out[size] = '\0';
}

std::string_view token(OpusErrorCode code) {
  switch (code) {
    case OpusErrorCode::EmptyPacket:
      return "empty";
    case OpusErrorCode::PacketTooLarge:
      return "too_large";
    case OpusErrorCode::InvalidPacket:
      return "invalid";
    case OpusErrorCode::WrongDuration:
      return "duration";
    default:
      return "decoder";
  }
}

std::string_view token(PlaybackErrorCode code) {
  switch (code) {
    case PlaybackErrorCode::OpenFailed:
      return "open";
    case PlaybackErrorCode::ConfigureFailed:
      return "configure";
    case PlaybackErrorCode::FormatMismatch:
      return "format";
    default:
      return "device";
  }
}

std::string_view vizToken(bool enabled, VizSource source) {
  if (!enabled)
    return "off";
  return source == VizSource::All ? "all" : "music";
}

}  // namespace

Playback::Playback(Loop& loop, IpcServer& ipc, const Log& log, OutputFactory open, VolumeRetry retry)
    : loop_(loop),
      ipc_(ipc),
      log_(log),
      open_(std::move(open)),
      retry_(retry),
      wakeTone_(earconPcm(Earcon::Wake)),
      errorTone_(earconPcm(Earcon::Failure)),
      timerRing_(ringPeriodPcm(RingKind::Timer)),
      alarmRing_(ringPeriodPcm(RingKind::Alarm)),
      reminderChime_(ringPeriodPcm(RingKind::Reminder)) {
  ipc_.on(cadence_audiod_v1_Envelope_stream_start_tag,
          [this](const Envelope& envelope) { start(envelope.body.stream_start); });
  ipc_.on(cadence_audiod_v1_Envelope_audio_down_tag,
          [this](const Envelope& envelope) { audio(envelope.body.audio_down); });
  ipc_.on(cadence_audiod_v1_Envelope_stream_end_tag,
          [this](const Envelope& envelope) { end(envelope.body.stream_end); });
  ipc_.on(cadence_audiod_v1_Envelope_stream_cancel_tag,
          [this](const Envelope& envelope) { cancelStream(envelope.body.stream_cancel); });
  ipc_.on(cadence_audiod_v1_Envelope_volume_set_tag, [this](const Envelope& envelope) {
    const auto& request = envelope.body.volume_set;
    if (request.transient)
      setTransientVolume(request.level);
    else
      setVolume(request.level);
  });
  ipc_.on(cadence_audiod_v1_Envelope_tone_tag, [this](const Envelope& envelope) { tone(envelope.body.tone); });
  ipc_.on(cadence_audiod_v1_Envelope_tone_start_tag,
          [this](const Envelope& envelope) { startRing(envelope.body.tone_start); });
  ipc_.on(cadence_audiod_v1_Envelope_tone_stop_tag,
          [this](const Envelope& envelope) { stopRing(envelope.body.tone_stop.id); });
  ipc_.on(cadence_audiod_v1_Envelope_duck_set_tag,
          [this](const Envelope& envelope) { duck(envelope.body.duck_set.active); });
  ipc_.on(cadence_audiod_v1_Envelope_media_settings_tag,
          [this](const Envelope& envelope) { settings(envelope.body.media_settings); });
  ipc_.on(cadence_audiod_v1_Envelope_output_delay_tag, [this](const Envelope& envelope) {
    setDelayTrim(envelope.body.output_delay.delay_ms);
    log_.info("output_delay", {{"ms", static_cast<std::int64_t>(controls_.delayTrimMs)}});
  });
}

void Playback::configure(const Configure& configuration) {
  close(true);
  prompts_.setDir(configuration.prompts_dir);
  caFile_ = configuration.ca_file;
  if (!configuration.has_output)
    return;
  controls_.delayTrimMs = std::clamp(configuration.output.delay_ms, -PcmBackend::kMaxDelayTrimMs,
                                     PcmBackend::kMaxDelayTrimMs);
  auto opened = open_(configuration.output);
  if (!opened.ok()) {
    log_.warn("playback_failed", {{"stage", "output"}, {"reason", opened.error()}});
    return;
  }
  auto& output = opened.value();
  auto config = kPlayerConfig;
  // the daemon refused more than 2
  config.channels = configuration.output.channels == 1 ? 1 : kPlaybackChannels;
  applyLayout(config, configuration.output);
  profile_ = configuration.output.has_eq ? toEq(configuration.output.eq) : dsp::EqSettings{};
  auto player = std::make_unique<AlsaPlayer>(*output.sink, config, PcmBackendInputs{&music_, &overlay_, &external_});
  player->onCompletion([this, epoch = epoch_ + 1](std::string key, bool success) {
    loop_.post([this, epoch, key = std::move(key), success] { settled(epoch, key, success); });
  });
  if (traces_ != nullptr)
    player->traceTo(traces_->tracer());
  player->onViz([this, epoch = epoch_ + 1](const dsp::VizFrame& frame) {
    // one frame on its way at a time, so a busy loop never queues them up
    if (vizPending_.exchange(true))
      return;
    loop_.post([this, epoch, frame] { sendViz(epoch, frame); });
  });
  // silent until a VolumeSet sets the level, in case anything ever slipped past the gate
  if (!output.volume)
    player->setMasterGain(0.0f);
  if (const auto started = player->start(); !started.ok()) {
    log_.warn("playback_failed", {{"stage", "start"}, {"reason", token(started.error().code)}});
    return;
  }
  ++epoch_;
  sink_ = std::move(output.sink);
  volume_ = std::move(output.volume);
  system_ = dynamic_cast<SystemVolumeControl*>(volume_.get());
  if (system_ != nullptr) {
    // the host's notice comes on any thread; the stream is read again on the loop's
    onSystemVolumeChanged([this, epoch = epoch_] { loop_.post([this, epoch] { systemChanged(epoch); }); });
  }
  recordDir_ = std::move(output.recordDir);
  player_ = std::move(player);
  burstFrames_ = config.periodFrames;
#ifdef CADENCE_MEDIA_AAUDIO
  // aaudio's callback takes a burst of its own choosing, which a screen's ring must hold twice over (s5)
  if (const auto* a = dynamic_cast<const AAudioPlayback*>(sink_.get()))
    burstFrames_ = std::max<std::size_t>(burstFrames_, static_cast<std::size_t>(a->status().framesPerBurst));
#endif
  applyControls();
  log_.info("playback_opened",
            {{"backend", configuration.output.backend == cadence_audiod_v1_Backend_BACKEND_ALSA     ? "alsa"
                         : configuration.output.backend == cadence_audiod_v1_Backend_BACKEND_AAUDIO ? "aaudio"
                                                                                                     : "file"},
             {"volume", system_ != nullptr ? "system" : volume_ ? "hardware" : "software"},
             {"channels", static_cast<std::uint64_t>(config.channels)}});
}

Playback::~Playback() {
  if (system_ != nullptr)
    onSystemVolumeChanged({});
}

void Playback::fillReady(cadence_audiod_v1_Ready& ready) {
  if (system_ == nullptr)
    return;
  const auto now = system_->current();
  ready.has_system_volume = true;
  ready.system_volume.level = static_cast<std::uint32_t>(now.level);
  ready.system_volume.muted = now.muted;
  log_.info("system_volume",
            {{"level", static_cast<std::int64_t>(now.level)}, {"muted", static_cast<std::uint64_t>(now.muted)}});
}

void Playback::systemChanged(std::uint64_t epoch) {
  if (epoch != epoch_ || system_ == nullptr)
    return;
  const auto change = system_->poll();
  if (!change)
    return;
  log_.info("system_volume_changed", {{"level", static_cast<std::int64_t>(change->level)},
                                      {"muted", static_cast<std::uint64_t>(change->muted)}});
  Envelope e = cadence_audiod_v1_Envelope_init_zero;
  e.which_body = cadence_audiod_v1_Envelope_system_volume_tag;
  e.body.system_volume.level = static_cast<std::uint32_t>(change->level);
  e.body.system_volume.muted = change->muted;
  ipc_.send(e);
}

void Playback::reset() {
  close(false);
  // cadenced sends its own again; a duck it held must not outlive it
  controls_ = MediaControls{};
}

void Playback::serverDuck(bool active, float level, std::uint32_t rampMs) {
  controls_.serverDucked = active;
  controls_.serverDuckLevel = level;
  controls_.serverDuckRampMs = rampMs;
  if (player_)
    player_->backend().setServerDuck(active, level, rampMs);
}

void Playback::setMuted(bool muted) {
  controls_.muted = muted;
  if (player_)
    player_->backend().setMuted(muted);
}

void Playback::setUserEq(const cadence_audiod_v1_EqSettings& eq) {
  controls_.userEq = toEq(eq);
  if (player_)
    player_->backend().setEq(profile_, controls_.userEq);
}

void Playback::setDelayTrim(std::int32_t ms) {
  controls_.delayTrimMs = ms;
  if (player_)
    player_->backend().setDelayTrim(ms);
}

void Playback::setSnapshots(bool enabled) {
  controls_.snapshots = enabled;
  if (player_)
    player_->backend().setSnapshots(enabled);
}

bool Playback::snapshot(PcmSnapshot& out) const { return player_ && player_->backend().snapshots().take(out); }

PresentationClock* Playback::clock() { return player_ ? &player_->backend().clock() : nullptr; }

std::optional<OutputWindow> Playback::takeOutputWindow() {
  if (!player_)
    return std::nullopt;
  OutputWindow out;
  const auto status = player_->status();
  out.priority = status.priority;
  out.recoveries = status.recoveries - std::min(status.recoveries, seenRecoveries_);
  seenRecoveries_ = status.recoveries;
#ifdef CADENCE_MEDIA_AAUDIO
  if (auto* a = dynamic_cast<AAudioPlayback*>(sink_.get())) {
    const auto s = a->status();
    const auto window = a->takeWindow();
    out.aaudio = true;
    out.underruns = s.underruns - std::min(s.underruns, seenUnderruns_);
    seenUnderruns_ = s.underruns;
    // a reopened stream counts its xruns afresh
    out.xruns = s.xruns >= seenXruns_ ? s.xruns - seenXruns_ : s.xruns;
    seenXruns_ = s.xruns;
    out.burstFrames = s.framesPerBurst;
    out.fillFrames = s.fillFrames;
    out.fillMinFrames = window.fillMinFrames;
    out.period = window.period;
  }
#endif
  return out;
}

void Playback::duck(bool active) {
  controls_.ducked = active;
  if (player_)
    player_->backend().setDucked(controls_.ducked || groupDucked_);
  for (const auto& hook : duckHooks_)
    hook(active);
}

void Playback::setGroupDuck(bool active) {
  groupDucked_ = active;
  if (player_)
    player_->backend().setDucked(controls_.ducked || groupDucked_);
}

// the whole of the media settings each time: an absent eq is flat and an absent viz is off
void Playback::settings(const cadence_audiod_v1_MediaSettings& request) {
  controls_.duckMode = toDuckMode(request.duck_mode);
  controls_.userEq = request.has_eq ? toEq(request.eq) : dsp::EqSettings{};
  controls_.vizEnabled = request.has_viz && request.viz.enabled;
  controls_.vizSource = request.has_viz ? toVizSource(request.viz.source) : VizSource::Music;
  applyControls();
  log_.info("media_settings", {{"duck_mode", controls_.duckMode == dsp::DuckMode::Muffle ? "muffle" : "duck"},
                               {"eq_bands", static_cast<std::uint64_t>(controls_.userEq.bands.size())},
                               {"viz", vizToken(controls_.vizEnabled, controls_.vizSource)}});
}

void Playback::applyControls() {
  if (!player_)
    return;
  auto& backend = player_->backend();
  backend.setDucked(controls_.ducked || groupDucked_);
  backend.setDuckMode(controls_.duckMode);
  backend.setEq(profile_, controls_.userEq);
  backend.setViz(controls_.vizEnabled, controls_.vizSource);
  backend.setServerDuck(controls_.serverDucked, controls_.serverDuckLevel, controls_.serverDuckRampMs);
  backend.setMuted(controls_.muted);
  backend.setSnapshots(controls_.snapshots);
  backend.setDelayTrim(controls_.delayTrimMs);
}

void Playback::start(const cadence_audiod_v1_StreamStart& request) {
  const std::string_view id = request.stream_id;
  const auto traced = request.has_trace ? trace::traceId(request.trace.traceparent) : std::string_view{};
  if (!gateOpen_) {
    log_.info("playback_gated", {{"kind", "stream"}});
    sendDone(id, 0);
    return;
  }
  if (!safeStreamId(id))
    return refuse(id, "stream_id", traced);
  // the live stream of that id answers for itself
  if (find(id) != streams_.end()) {
    log_.warn("stream_refused", {{"reason", "duplicate"}});
    return;
  }
  // a stream still arriving is replaced, and the player drops everything queued with it
  if (!streams_.empty() && !streams_.back().ended)
    cut(true);
  if (decoder_.reset())
    return refuse(id, "decoder", traced);
  if (!player_->begin(id))
    return refuse(id, "retired", traced);

  Stream stream{std::string(id), 0, false, std::nullopt, voiceUnderruns()};
  if (recordDir_) {
    auto recorder = StreamRecorder::create(*recordDir_, id);
    if (recorder.ok())
      stream.recorder.emplace(std::move(recorder.value()));
    else
      log_.warn("record_failed", {{"reason", recorder.error()}});
  }
  streams_.push_back(std::move(stream));
  if (traces_ != nullptr)
    traces_->mark(trace::Stage::PlaybackOpened, trace::Target::Stream, id);
  armNext();
}

void Playback::audio(const cadence_audiod_v1_AudioDown& packet) {
  // packets of a refused, cut or ended stream still arrive while cadenced catches up
  if (streams_.empty() || streams_.back().ended || streams_.back().id != packet.stream_id)
    return;
  auto& stream = streams_.back();
  const auto decoded = decoder_.decode(std::span(packet.opus.bytes, packet.opus.size));
  if (!decoded.ok()) {
    if (sparse(++decodeErrors_))
      log_.warn("decode_failed", {{"reason", token(decoded.error().code)}, {"count", decodeErrors_}});
    return;
  }
  if (stream.recorder) {
    if (const auto failed = stream.recorder->write(decoded.value()))
      log_.warn("record_failed", {{"reason", *failed}});
  }
  switch (player_->push(stream.id, decoded.value())) {
    case PushResult::Accepted:
      stream.accepted += decoded.value().size();
      break;
    case PushResult::Overflow:
      if (sparse(++overflows_))
        log_.warn("playback_overflow", {{"count", overflows_}});
      break;
    case PushResult::Stale:
      break;
  }
}

void Playback::end(const cadence_audiod_v1_StreamEnd& request) {
  if (streams_.empty() || streams_.back().ended || streams_.back().id != request.stream_id)
    return;
  auto& stream = streams_.back();
  stream.ended = true;
  player_->end(stream.id);
  if (stream.recorder) {
    if (const auto failed = stream.recorder->finish())
      log_.warn("record_failed", {{"reason", *failed}});
  }
}

// the named stream and the ones queued after it are cut and report once; an earlier one still playing plays out
// (a cancelled follow-up leaves the reply before it alone). an id that is unknown or finished is ignored
void Playback::cancelStream(const cadence_audiod_v1_StreamCancel& request) {
  const auto named = find(request.stream_id);
  if (named == streams_.end())
    return;
  cut(true, static_cast<std::size_t>(named - streams_.begin()));
}

void Playback::setVolume(std::uint32_t level) {
  cancelVolumeRetry();
  retries_ = 0;
  retryDelay_ = retry_.min;
  const auto failure = applyVolume(level);
  if (!failure)
    return volumeHeld(level);
  log_.warn("volume_failed", {{"level", level}, {"reason", failure->reason}});
  volumeFailed();
  sendApplied(level, false, false, failure->reason);
  if (failure->retry)
    armVolumeRetry(level);
}

// an alarm's ramp moves the hardware, never the level in force: while the gate is shut no level held, so there is
// nothing to ramp from and the control is left to the retry; with no ring sounding there is no ramp, so a step that
// comes after the ring's stop (and after the plain set that restores) never leaves the hardware above the level in
// force (rule 7)
void Playback::setTransientVolume(std::uint32_t level) {
  std::string_view refused;
  if (!gateOpen_)
    refused = "gated";
  else if (!ringing_)
    refused = "no_ring";
  else if (const auto failure = applyVolume(level))
    refused = failure->reason;
  if (!refused.empty()) {
    log_.warn("volume_transient_failed", {{"level", level}, {"reason", refused}});
    return sendApplied(level, false, true, refused);
  }
  lastApplied_ = level;
  log_.info("volume_transient_applied", {{"level", level}, {"control", volume_ ? "hardware" : "software"}});
  sendApplied(level, true, true);
}

// the gate is shut and cadenced has its answer: only the outcome that opens the gate is sent
void Playback::retryVolume(std::uint32_t level) {
  volumeRetry_.reset();
  ++retries_;
  const auto failure = applyVolume(level);
  if (!failure)
    return volumeHeld(level);
  if (sparse(retries_))
    log_.warn("volume_retry_failed", {{"level", level}, {"reason", failure->reason}, {"retries", retries_}});
  if (failure->retry)
    armVolumeRetry(level);
}

std::optional<Playback::VolumeFailure> Playback::applyVolume(std::uint32_t level) {
  if (level > 100)
    return VolumeFailure{"range"};
  if (!player_)
    return VolumeFailure{"output"};
  if (!volume_) {
    player_->setMasterGain(dsp::levelGain(level));
    return std::nullopt;
  }
  if (volume_->setPercent(static_cast<int>(level)))
    return VolumeFailure{"control", true};
  if (const auto unread = volume_->verifyPercent(static_cast<int>(level))) {
    if (const auto& off = unread->mismatch)
      log_.warn("volume_readback_mismatch", {{"wanted", static_cast<std::int64_t>(off->wanted)},
                                             {"held", static_cast<std::int64_t>(off->held)},
                                             {"ch", static_cast<std::int64_t>(off->channel)}});
    return VolumeFailure{"readback", true};
  }
  return std::nullopt;
}

void Playback::volumeHeld(std::uint32_t level) {
  const bool wasShut = !gateOpen_;
  gateOpen_ = true;
  player_->setGateOpen(true);
  const auto control = volume_ ? "hardware" : "software";
  // the re-check re-applies before every reply: info only for a change or an opened gate
  const bool news = wasShut || lastApplied_ != level || retries_ != 0;
  lastApplied_ = level;
  const auto log = [this, news](std::string_view msg, std::initializer_list<LogField> fields) {
    news ? log_.info(msg, fields) : log_.debug(msg, fields);
  };
  if (retries_ == 0)
    log("volume_applied", {{"level", level}, {"control", control}});
  else
    log("volume_applied", {{"level", level}, {"control", control}, {"retries", retries_}});
  sendApplied(level, true);
  if (wasShut)
    for (const auto& hook : gateOpened_)
      hook();
}

// fail closed: nothing plays at a level that may not be the one asked for
void Playback::volumeFailed() {
  const bool wasOpen = gateOpen_;
  if (wasOpen) {
    player_->setGateOpen(false);
    cut(true);
    player_->cancelTones();
    silenceRing("gated", true);
  }
  gateOpen_ = false;
  if (wasOpen)
    for (const auto& hook : gateShut_)
      hook();
}

void Playback::armVolumeRetry(std::uint32_t level) {
  volumeRetry_ = loop_.after(retryDelay_, [this, level] { retryVolume(level); });
  retryDelay_ = std::min(retryDelay_ * 2, retry_.max);
}

void Playback::cancelVolumeRetry() {
  if (const auto armed = std::exchange(volumeRetry_, std::nullopt))
    loop_.cancel(*armed);
}

const std::vector<std::int16_t>* Playback::ringPeriod(cadence_audiod_v1_ToneKind kind) const {
  switch (kind) {
    case cadence_audiod_v1_ToneKind_TONE_KIND_ALARM:
      return &alarmRing_;
    case cadence_audiod_v1_ToneKind_TONE_KIND_TIMER:
      return &timerRing_;
    case cadence_audiod_v1_ToneKind_TONE_KIND_REMINDER:
      return &reminderChime_;
    default:
      return nullptr;
  }
}

std::optional<RingPart> Playback::ringPart(const cadence_audiod_v1_RingSegment& segment, bool streams,
                                           const std::string& authorization) {
  const std::string_view promptId = segment.prompt_id;
  const std::string_view url = segment.url;
  const int set = (segment.tone != cadence_audiod_v1_ToneKind_TONE_KIND_UNSPECIFIED ? 1 : 0) +
                  (promptId.empty() ? 0 : 1) + (url.empty() ? 0 : 1) + (segment.silence_ms > 0 ? 1 : 0);
  if (set != 1)
    return std::nullopt;
  RingPart part;
  if (segment.silence_ms > 0) {
    // at most a minute of it
    part.silence = static_cast<std::size_t>(std::min<std::uint32_t>(segment.silence_ms, 60000)) * kSamplesPerMs;
  } else if (!url.empty()) {
    if (!streams)
      return std::nullopt;
    if (ringStreams_)
      part.stream = ringStreams_({.url = std::string(url), .authorization = authorization, .caFile = caFile_});
    if (!part.stream) {
      // a build without a url source: the ring falls back at its first read
      class Unsupported final : public RingStream {
       public:
        Read read(std::span<std::int16_t>) override { return {}; }
        void rewind() override {}
        [[nodiscard]] std::string_view failure() const override { return "unsupported"; }
        void stop() override {}
      };
      part.stream = std::make_shared<Unsupported>();
    }
  } else if (!promptId.empty()) {
    const auto prompt = prompts_.get(segment.lang, promptId);
    if (prompt.pcm == nullptr) {
      // a missing prompt is the reminder's chime, as a missing earcon prompt is its earcon
      log_.info("prompt_fallback", {{"id", Prompts::validId(promptId) ? promptId : "invalid"}, {"reason", prompt.reason}});
      part.pcm = std::make_shared<const std::vector<std::int16_t>>(reminderChime_);
    } else {
      part.pcm = std::make_shared<const std::vector<std::int16_t>>(*prompt.pcm);
    }
  } else {
    const auto* period = ringPeriod(segment.tone);
    if (period == nullptr)
      return std::nullopt;
    part.pcm = std::make_shared<const std::vector<std::int16_t>>(*period);
  }
  return part;
}

std::shared_ptr<RingSequence> Playback::ringSequence(const cadence_audiod_v1_ToneStart& request,
                                                     const std::vector<std::int16_t>& period) {
  const std::string authorization = request.url_authorization;
  std::vector<RingPart> parts;
  for (pb_size_t index = 0; index < request.segments_count; ++index) {
    auto part = ringPart(request.segments[index], true, authorization);
    if (!part)
      return nullptr;
    parts.push_back(std::move(*part));
  }
  std::vector<RingPart> fallback;
  for (pb_size_t index = 0; index < request.fallback_count; ++index) {
    auto part = ringPart(request.fallback[index], false, authorization);
    if (!part)
      return nullptr;
    fallback.push_back(std::move(*part));
  }
  if (fallback.empty())
    fallback.push_back({.pcm = std::make_shared<const std::vector<std::int16_t>>(period)});
  // the hook runs on the playback thread: it only posts, and the loop drops what an older ring posted
  return std::make_shared<RingSequence>(
      std::move(parts), request.loop, std::move(fallback),
      [this, serial = ringSerial_ + 1](std::string_view code) {
        loop_.post([this, serial, code = std::string(code)] { ringFellBack(serial, code); });
      });
}

void Playback::ringFellBack(std::uint64_t serial, const std::string& code) {
  if (!ringing_ || !ringSequence_ || serial != ringSerial_)
    return;
  // a url's code is a fixed token; anything else is not logged as it came
  const bool token = !code.empty() && code.size() <= 32 &&
                     std::all_of(code.begin(), code.end(), [](char c) { return (c >= 'a' && c <= 'z') || c == '_'; });
  const std::string_view shown = token ? std::string_view(code) : std::string_view("unknown");
  log_.warn("ring_fallback", {{"code", shown}});
  sendToneState(*ringing_, true, shown, true);
}

void Playback::startRing(const cadence_audiod_v1_ToneStart& request) {
  const std::string_view id = request.id;
  const auto* period = ringPeriod(request.kind);
  if (id.empty() || period == nullptr) {
    log_.warn("tone_loop_refused", {{"reason", "invalid"}, {"kind", static_cast<std::int64_t>(request.kind)}});
    return sendToneState(id, false, "invalid");
  }
  if (!gateOpen_) {
    log_.info("playback_gated", {{"kind", "ring"}});
    return sendToneState(id, false, "gated");
  }
  if (ringing_ && *ringing_ != id) {
    log_.info("tone_loop_refused", {{"reason", "busy"}});
    return sendToneState(id, false, "busy");
  }
  const bool sequence = request.segments_count > 0;
  // the sequence that sounds, asked for again, sounds on as it was
  if (sequence && ringing_ && ringSequence_)
    return sendToneState(id, true, "");
  if (sequence) {
    auto feed = ringSequence(request, *period);
    if (!feed) {
      log_.warn("tone_loop_refused", {{"reason", "invalid"}, {"kind", static_cast<std::int64_t>(request.kind)}});
      return sendToneState(id, false, "invalid");
    }
    ++ringSerial_;
    player_->loopFeed(std::move(feed));
    ringing_ = std::string(id);
    ringPeriod_ = nullptr;
    ringSequence_ = true;
    log_.info("tone_loop_started", {{"kind", "sequence"}, {"segments", static_cast<std::uint64_t>(request.segments_count)}});
    return sendToneState(id, true, "");
  }
  // the ring that sounds, asked for again, sounds on; asked for with the other kind, it loops that one instead
  if (!ringing_ || ringPeriod_ != period) {
    ++ringSerial_;
    player_->loopTone(*period);
    ringing_ = std::string(id);
    ringPeriod_ = period;
    ringSequence_ = false;
    const std::string_view kind = period == &alarmRing_ ? "alarm" : period == &timerRing_ ? "timer" : "reminder";
    log_.info("tone_loop_started", {{"kind", kind}});
  }
  sendToneState(id, true, "");
}

// an id that does not sound is ignored
void Playback::stopRing(std::string_view id) {
  if (ringing_ && *ringing_ == id)
    silenceRing("stopped", true);
}

void Playback::silenceRing(std::string_view code, bool report) {
  const auto id = std::exchange(ringing_, std::nullopt);
  ringPeriod_ = nullptr;
  ringSequence_ = false;
  if (!id)
    return;
  if (player_)
    player_->stopLoop();
  log_.info("tone_loop_stopped", {{"reason", code}});
  if (report)
    sendToneState(*id, false, code);
}

void Playback::tone(const cadence_audiod_v1_Tone& request) {
  const auto kind = request.kind;
  if (kind == cadence_audiod_v1_ToneKind_TONE_KIND_ALARM || kind == cadence_audiod_v1_ToneKind_TONE_KIND_TIMER) {
    log_.warn("tone_invalid", {{"kind", static_cast<std::int64_t>(kind)}});
    return;
  }
  if (!gateOpen_) {
    log_.info("playback_gated", {{"kind", "tone"}});
    return;
  }
  const std::vector<std::int16_t>* pcm = nullptr;
  switch (kind) {
    case cadence_audiod_v1_ToneKind_TONE_KIND_WAKE:
      pcm = &wakeTone_;
      break;
    case cadence_audiod_v1_ToneKind_TONE_KIND_DONE:
      // done has no earcon of its own: one period of the timer's ring
      pcm = &timerRing_;
      break;
    case cadence_audiod_v1_ToneKind_TONE_KIND_REMINDER:
      pcm = &reminderChime_;
      break;
    case cadence_audiod_v1_ToneKind_TONE_KIND_ERROR:
    case cadence_audiod_v1_ToneKind_TONE_KIND_UNSPECIFIED:
      // a prompt without a kind falls back to the error earcon
      pcm = &errorTone_;
      break;
    default:
      log_.warn("tone_refused", {{"reason", "kind"}, {"kind", static_cast<std::int64_t>(kind)}});
      return;
  }
  const std::string_view promptId = request.prompt_id;
  if (!promptId.empty()) {
    // the id and lang are tokens Prompts checked before any path was built; one that is not is never logged
    const auto prompt = prompts_.get(request.lang, promptId);
    if (prompt.pcm != nullptr) {
      pcm = prompt.pcm;
      log_.info("prompt_played", {{"id", Prompts::validId(promptId) ? promptId : "invalid"}});
    } else {
      log_.info("prompt_fallback", {{"id", Prompts::validId(promptId) ? promptId : "invalid"}, {"reason", prompt.reason}});
    }
  } else if (kind == cadence_audiod_v1_ToneKind_TONE_KIND_UNSPECIFIED) {
    log_.warn("tone_refused", {{"reason", "kind"}, {"kind", static_cast<std::int64_t>(kind)}});
    return;
  }
  if (player_->playTone("tone-" + std::to_string(++tones_), *pcm) != PushResult::Accepted)
    log_.warn("tone_refused", {{"reason", "full"}});
}

void Playback::close(bool report) {
  cancelVolumeRetry();
  silenceRing("gated", report);
  cut(report);
  if (player_) {
    player_->stop();
    log_.info("playback_closed");
  }
  player_.reset();
  sink_.reset();
  if (system_ != nullptr) {
    onSystemVolumeChanged({});
    system_ = nullptr;
  }
  volume_.reset();
  recordDir_.reset();
  // a settlement the stopped player posted carries its old epoch
  watched_.reset();
  gateOpen_ = false;
  // a new player starts from empty inputs
  music_.clear();
  overlay_.clear();
}

void Playback::cut(bool report, std::size_t from) {
  if (from >= streams_.size())
    return;
  std::size_t queued = player_ ? player_->status().queue.queuedSamples : 0;
  if (player_) {
    if (from == 0) {
      player_->cancel();
    } else {
      // the named stream may have left the queue already: the first one still queued drops the rest, and each
      // one before it is only retired
      for (std::size_t index = from; index < streams_.size(); ++index)
        player_->cancelFrom(streams_[index].id);
    }
  }
  // the queue plays in order, so what it still held is the newest streams' tails; what left it was read, at most
  // the device's fill short of heard
  std::vector<std::size_t> read(streams_.size());
  for (std::size_t index = streams_.size(); index-- > from;) {
    const auto unread = std::min(streams_[index].accepted, queued);
    queued -= unread;
    read[index] = streams_[index].accepted - unread;
  }
  for (std::size_t index = from; index < streams_.size(); ++index)
    finish(streams_[index], read[index], report);
  streams_.erase(streams_.begin() + static_cast<std::ptrdiff_t>(from), streams_.end());
}

void Playback::settled(std::uint64_t epoch, const std::string& key, bool success) {
  if (epoch != epoch_)
    return;
  if (watched_ == key)
    watched_.reset();
  // a failure for a stream still here came from the device (a recovery, a failed write or delay), not from a
  // cut: the stream plays on, so its watch is armed again
  if (const auto stream = find(key); success && stream != streams_.end()) {
    finish(*stream, stream->accepted, true);
    streams_.erase(stream);
  }
  armNext();
}

void Playback::armNext() {
  if (!player_ || watched_)
    return;
  for (auto stream = streams_.begin(); stream != streams_.end();) {
    if (player_->watchCompletion(stream->id)) {
      watched_ = stream->id;
      return;
    }
    // the player runs and holds no watch, so the stream left the queue: its last sample was read before a watch
    // could be armed for it. one still arriving never leaves it that way
    if (!stream->ended)
      return;
    finish(*stream, stream->accepted, true);
    stream = streams_.erase(stream);
  }
}

void Playback::finish(Stream& stream, std::size_t playedSamples, bool report) {
  if (stream.recorder) {
    if (const auto failed = stream.recorder->finish())
      log_.warn("record_failed", {{"reason", *failed}});
  }
  if (report)
    sendDone(stream.id, playedSamples, voiceUnderruns() - std::min(voiceUnderruns(), stream.underruns));
}

void Playback::refuse(std::string_view id, std::string_view reason, std::string_view traceId) {
  if (traceId.empty())
    log_.warn("stream_refused", {{"reason", reason}});
  else
    log_.warn("stream_refused", {{"reason", reason}, {"trace", traceId}});
  sendDone(id, 0);
}

std::uint64_t Playback::voiceUnderruns() const {
  if (traces_ == nullptr || !trace::kEnabled || !player_)
    return 0;
  return player_->status().queue.underruns;
}

void Playback::sendDone(std::string_view id, std::size_t playedSamples, std::uint64_t underruns) {
  // before the StreamDone, which ends cadenced's span of the stream
  if (traces_ != nullptr)
    traces_->mark(trace::Stage::StreamDone, trace::Target::Stream, id,
                  {{"played_ms", static_cast<double>(playedSamples / kSamplesPerMs)},
                   {"underruns", static_cast<double>(underruns)}});
  Envelope reply = cadence_audiod_v1_Envelope_init_zero;
  reply.which_body = cadence_audiod_v1_Envelope_stream_done_tag;
  copyText(reply.body.stream_done.stream_id, id);
  reply.body.stream_done.played_ms = static_cast<std::uint32_t>(playedSamples / kSamplesPerMs);
  ipc_.send(reply);
}

void Playback::sendApplied(std::uint32_t level, bool ok, bool transient, std::string_view code) {
  Envelope reply = cadence_audiod_v1_Envelope_init_zero;
  reply.which_body = cadence_audiod_v1_Envelope_volume_applied_tag;
  reply.body.volume_applied.level = level;
  reply.body.volume_applied.ok = ok;
  reply.body.volume_applied.transient = transient;
  copyText(reply.body.volume_applied.code, code);
  ipc_.send(reply);
}

void Playback::sendToneState(std::string_view id, bool sounding, std::string_view code, bool fallback) {
  Envelope reply = cadence_audiod_v1_Envelope_init_zero;
  reply.which_body = cadence_audiod_v1_Envelope_tone_state_tag;
  copyText(reply.body.tone_state.id, id);
  reply.body.tone_state.sounding = sounding;
  copyText(reply.body.tone_state.code, code);
  reply.body.tone_state.fallback = fallback;
  ipc_.send(reply);
}

void Playback::sendViz(std::uint64_t epoch, const dsp::VizFrame& frame) {
  vizPending_ = false;
  // a frame a stopped player posted, or one that came just before the viz went off
  if (epoch != epoch_ || !controls_.vizEnabled)
    return;
  Envelope reply = cadence_audiod_v1_Envelope_init_zero;
  reply.which_body = cadence_audiod_v1_Envelope_viz_frame_tag;
  static_assert(sizeof reply.body.viz_frame.bands.bytes == dsp::kVizBands);
  std::copy(frame.begin(), frame.end(), reply.body.viz_frame.bands.bytes);
  reply.body.viz_frame.bands.size = static_cast<pb_size_t>(frame.size());
  ipc_.send(reply);
  for (const auto& hook : vizHooks_)
    hook(frame);
}

std::deque<Playback::Stream>::iterator Playback::find(std::string_view id) {
  return std::find_if(streams_.begin(), streams_.end(), [id](const Stream& stream) { return stream.id == id; });
}

}  // namespace cadence::audiod
