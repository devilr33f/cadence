#include "audiod/external-sources.h"

#include "dsp/format.h"

#include <algorithm>
#include <cstddef>
#include <cstring>
#include <optional>
#include <utility>

namespace cadence::audiod {

namespace {

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

// the media settings' duck depth, which a duck of level 0 asks for (dsp::DuckEnvelope's)
constexpr float kDefaultDuckLevel = 0.15f;

std::optional<MediaKind> toKind(cadence_audiod_v1_MediaSourceKind kind) {
  switch (kind) {
    case cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_VIDEO:
      return MediaKind::Video;
    case cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_SCREENCAST:
      return MediaKind::Screencast;
    case cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_EXTERNAL:
      return MediaKind::External;
    default:
      return std::nullopt;
  }
}

std::optional<PlayState> toState(cadence_audiod_v1_PlaybackState state) {
  switch (state) {
    case cadence_audiod_v1_PlaybackState_PLAYBACK_STATE_IDLE:
      return PlayState::Idle;
    case cadence_audiod_v1_PlaybackState_PLAYBACK_STATE_PLAYING:
      return PlayState::Playing;
    case cadence_audiod_v1_PlaybackState_PLAYBACK_STATE_PAUSED:
      return PlayState::Paused;
    case cadence_audiod_v1_PlaybackState_PLAYBACK_STATE_BUFFERING:
      return PlayState::Buffering;
    default:
      return std::nullopt;
  }
}

std::optional<AudioRoute> toRoute(cadence_audiod_v1_AudioRoute route) {
  switch (route) {
    case cadence_audiod_v1_AudioRoute_AUDIO_ROUTE_MIXER:
      return AudioRoute::Mixer;
    case cadence_audiod_v1_AudioRoute_AUDIO_ROUTE_DIRECT:
      return AudioRoute::Direct;
    case cadence_audiod_v1_AudioRoute_AUDIO_ROUTE_EXTERNAL:
      return AudioRoute::External;
    case cadence_audiod_v1_AudioRoute_AUDIO_ROUTE_REMOTE:
      return AudioRoute::Remote;
    case cadence_audiod_v1_AudioRoute_AUDIO_ROUTE_NONE:
      return AudioRoute::None;
    default:
      return std::nullopt;
  }
}

std::optional<AudioControl> toControl(cadence_audiod_v1_AudioControl control) {
  switch (control) {
    case cadence_audiod_v1_AudioControl_AUDIO_CONTROL_VOLUME:
      return AudioControl::Volume;
    case cadence_audiod_v1_AudioControl_AUDIO_CONTROL_DUCK:
      return AudioControl::Duck;
    case cadence_audiod_v1_AudioControl_AUDIO_CONTROL_MUTE:
      return AudioControl::Mute;
    case cadence_audiod_v1_AudioControl_AUDIO_CONTROL_EQ:
      return AudioControl::Eq;
    case cadence_audiod_v1_AudioControl_AUDIO_CONTROL_VIZ_PCM:
      return AudioControl::VizPcm;
    case cadence_audiod_v1_AudioControl_AUDIO_CONTROL_VIZ_BANDS:
      return AudioControl::VizBands;
    default:
      return std::nullopt;
  }
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

std::string_view controlToken(pb_size_t which) {
  switch (which) {
    case cadence_audiod_v1_SessionControl_duck_tag:
      return "duck";
    case cadence_audiod_v1_SessionControl_set_level_tag:
      return "set_level";
    case cadence_audiod_v1_SessionControl_set_eq_tag:
      return "set_eq";
    case cadence_audiod_v1_SessionControl_mute_tag:
      return "mute";
    default:
      return "unknown";
  }
}

// what the control asks of a route, as the source lists its controls
std::optional<AudioControl> controlOf(pb_size_t which) {
  switch (which) {
    case cadence_audiod_v1_SessionControl_duck_tag:
      return AudioControl::Duck;
    case cadence_audiod_v1_SessionControl_set_level_tag:
      return AudioControl::Volume;
    case cadence_audiod_v1_SessionControl_set_eq_tag:
      return AudioControl::Eq;
    case cadence_audiod_v1_SessionControl_mute_tag:
      return AudioControl::Mute;
    default:
      return std::nullopt;
  }
}

float duckLevel(const cadence_audiod_v1_Duck& duck) {
  // a duck never raises a level, and nan is silence (rule 7)
  if (duck.level == 0.0f)
    return kDefaultDuckLevel;
  return duck.level >= 0.0f ? std::min(duck.level, 1.0f) : 0.0f;
}

std::string_view codeToken(std::string_view code) { return code.empty() ? "ok" : code; }

}  // namespace

std::unique_ptr<RingInput> noRings(std::size_t /*burstFrames*/) { return nullptr; }

std::unique_ptr<RingInput> sizedRing(std::size_t burstFrames) {
  auto made = RingInput::create(RingInput::capacityFor(burstFrames));
  return made.ok() ? std::move(made.value()) : nullptr;
}

ExternalSources::ExternalSources(IpcServer& ipc, const Log& log, MediaSessions& sessions, Playback& playback,
                                 RingFactory rings, Loop* loop)
    : ipc_(ipc), log_(log), sessions_(sessions), playback_(playback), rings_(std::move(rings)), loop_(loop) {
  ipc_.on(cadence_audiod_v1_Envelope_external_source_tag,
          [this](const Envelope& envelope) { update(envelope.body.external_source); });
  ipc_.on(cadence_audiod_v1_Envelope_session_control_tag,
          [this](const Envelope& envelope) { control(envelope.body.session_control); });
}

ExternalSources::~ExternalSources() {
  reset();
  if (stats_ && loop_ != nullptr)
    loop_->cancel(*stats_);
}

void ExternalSources::reset() {
  auto registered = std::move(registered_);
  registered_.clear();
  for (const auto& [id, entry] : registered) {
    if (const auto* ring = entry.source->ring())
      logRing(id, *ring, "end");
    sessions_.remove(entry.serial);
  }
}

void ExternalSources::logRing(std::string_view id, const RingInput& ring, std::string_view phase) const {
  const auto s = ring.status();
  const auto queued = s.writePos > s.readPos ? s.writePos - s.readPos : 0;
  const auto token = [](std::uint32_t state) -> std::string_view {
    switch (state) {
      case CADENCE_RING_STREAMING:
        return "streaming";
      case CADENCE_RING_PAUSED:
        return "paused";
      default:
        return "stopped";
    }
  };
  log_.info("external_ring",
            {{"id", id},
             {"phase", phase},
             {"open", s.open ? "yes" : "no"},
             {"writer", token(s.writerState)},
             {"frames_read", s.framesRead},
             {"frames_written", s.writePos},
             {"reads", s.reads},
             {"empty_reads", s.emptyReads},
             {"underruns", s.underruns},
             {"held_reads", s.heldReads},
             {"refill_timeouts", s.refillTimeouts},
             {"refilling", s.refilling ? "yes" : "no"},
             {"fill_ms", queued * 1000 / dsp::kRate},
             {"capacity", static_cast<std::uint64_t>(ring.capacity())},
             {"generation", static_cast<std::uint64_t>(s.generation)},
             {"latency_ms", s.latencyNs / 1'000'000},
             {"gain_pct", static_cast<std::int64_t>(s.gain * 100.0f + 0.5f)},
             {"muted", s.muted ? "yes" : "no"},
             {"gate", playback_.gateOpen() ? "open" : "shut"},
             {"ducked", playback_.ducked() ? "yes" : "no"},
             {"mix_muted", playback_.muted() ? "yes" : "no"}});
}

void ExternalSources::armStats() {
  if (loop_ == nullptr || stats_)
    return;
  stats_ = loop_->after(kRingStatsEvery, [this] {
    stats_.reset();
    statsTick();
  });
}

void ExternalSources::statsTick() {
  bool any = false;
  for (const auto& [id, entry] : registered_) {
    const auto* ring = entry.source->ring();
    if (ring == nullptr)
      continue;
    any = true;
    if (ring->isOpen())
      logRing(id, *ring, "tick");
  }
  if (any)
    armStats();
}

void ExternalSources::useGroupFeed(GroupFeed* feed) {
  groupFeed_ = feed;
  if (feed != nullptr)
    return;
  for (const auto& [id, entry] : registered_)
    entry.source->leaveGroupFeed();
}

RingInput* ExternalSources::ring(std::string_view id) const {
  const auto found = registered_.find(id);
  return found == registered_.end() ? nullptr : found->second.source->ring();
}

void ExternalSources::eachRing(const std::function<void(std::string_view, const RingInput&)>& visit) const {
  for (const auto& [id, entry] : registered_)
    if (const auto* ring = entry.source->ring())
      visit(id, *ring);
}

void ExternalSources::update(const cadence_audiod_v1_ExternalSource& request) {
  const auto id = textOf(request.id);
  if (id.empty()) {
    log_.warn("external_refused", {{"reason", "invalid"}});
    return;
  }
  const auto found = registered_.find(id);
  if (request.op == cadence_audiod_v1_ExternalOp_EXTERNAL_OP_UNREGISTER) {
    if (found == registered_.end())
      return;
    const auto serial = found->second.serial;
    if (const auto* ring = found->second.source->ring())
      logRing(id, *ring, "end");
    registered_.erase(found);
    log_.info("external_unregistered", {{"id", id}});
    sessions_.remove(serial);
    return;
  }
  const auto kind = toKind(request.kind);
  const auto state = toState(request.state);
  const auto route = toRoute(request.audio_route);
  if (request.op != cadence_audiod_v1_ExternalOp_EXTERNAL_OP_UPDATE || !kind || !state || !route) {
    log_.warn("external_refused", {{"id", id}, {"reason", "invalid"}});
    return;
  }
  // where the player is, when it says (a group leader's epoch of its ring starts there)
  const auto position =
      request.position_at_ms != 0 ? std::optional<std::uint64_t>(request.position_ms) : std::nullopt;
  AudioControls controls;
  for (pb_size_t i = 0; i < request.audio_controls_count; ++i)
    if (const auto control = toControl(request.audio_controls[i]))
      controls.add(*control);
  if (found == registered_.end()) {
    ExternalSource* made = nullptr;
    auto ring = *route == AudioRoute::Mixer && rings_ ? rings_(playback_.burstFrames()) : nullptr;
    const auto serial = sessions_.add([&](std::shared_ptr<MediaSourceEvents> events) {
      auto source = std::make_unique<ExternalSource>(
          id, *kind, std::move(events),
          [this, id](MediaAction action, std::uint64_t positionMs, std::string_view reason) {
            act(id, action, positionMs, reason);
          }, playback_.external(),
          std::move(ring), &groupFeed_);
      made = source.get();
      return source;
    });
    registered_.emplace(id, Registered{serial, made});
    log_.info("external_registered", {{"id", id}, {"ring", made->ring() != nullptr ? "yes" : "no"}});
    made->update(*state, *route, controls, position);
    if (made->ring() != nullptr) {
      if (onRing_)
        onRing_(id, *made->ring());
      armStats();
    }
    return;
  }
  if (*state == PlayState::Idle && found->second.source->state() != PlayState::Idle)
    if (const auto* ring = found->second.source->ring())
      logRing(id, *ring, "end");
  found->second.source->update(*state, *route, controls, position);
}

void ExternalSources::act(const std::string& id, MediaAction action, std::uint64_t positionMs,
                          std::string_view reason) {
  log_.info("external_action",
            {{"id", id}, {"action", actionToken(action)}, {"reason", reason.empty() ? "command" : reason}});
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_external_action_tag;
  auto& out = envelope.body.external_action;
  copyText(out.id, id);
  out.action = toWire(action);
  copyText(out.reason, reason);
  out.position_ms = action == MediaAction::Seek ? positionMs : 0;
  ipc_.send(envelope);
}

void ExternalSources::control(const cadence_audiod_v1_SessionControl& request) {
  const auto id = textOf(request.session_id);
  std::string_view code = kMediaNoSession;
  if (id.empty()) {
    code = mix(request);
  } else if (const auto found = registered_.find(id); found != registered_.end()) {
    code = session(*found->second.source, request);
  } else if (const auto& current = sessions_.session(); !current.id.empty() && current.id == id) {
    // audiod's own source mixes: its duck is the mix's
    code = request.which_control == cadence_audiod_v1_SessionControl_duck_tag ? mix(request) : kMediaUnsupported;
  }
  log_.info("session_control",
            {{"control", controlToken(request.which_control)}, {"scope", id.empty() ? "mix" : "session"},
             {"code", codeToken(code)}});
  answer(textOf(request.request_id), code);
}

std::string_view ExternalSources::mix(const cadence_audiod_v1_SessionControl& request) {
  const auto& control = request.control;
  switch (request.which_control) {
    case cadence_audiod_v1_SessionControl_duck_tag:
      playback_.serverDuck(control.duck.active, duckLevel(control.duck), control.duck.ramp_ms);
      break;
    case cadence_audiod_v1_SessionControl_set_level_tag:
      playback_.setLevel(control.set_level.level);
      break;
    case cadence_audiod_v1_SessionControl_set_eq_tag: {
      // no preset is defined yet: the bands alone
      if (control.set_eq.preset[0] != '\0')
        return kMediaUnsupported;
      cadence_audiod_v1_EqSettings flat = cadence_audiod_v1_EqSettings_init_zero;
      playback_.setUserEq(control.set_eq.has_eq ? control.set_eq.eq : flat);
      break;
    }
    case cadence_audiod_v1_SessionControl_mute_tag:
      playback_.setMuted(control.mute.muted);
      break;
    default:
      return kMediaInvalid;
  }
  // a direct player renders past the mixer, so it applies the mix's control itself
  if (auto* owner = sessions_.ownerSource(); owner != nullptr && owner->audioRoute() == AudioRoute::Direct) {
    const auto wanted = controlOf(request.which_control);
    if (wanted && owner->audioControls().has(*wanted))
      routeBack(owner->sessionId(), request);
  }
  return kMediaOk;
}

std::string_view ExternalSources::session(ExternalSource& source, const cadence_audiod_v1_SessionControl& request) {
  const auto wanted = controlOf(request.which_control);
  if (!wanted)
    return kMediaInvalid;
  switch (source.audioRoute()) {
    case AudioRoute::Mixer: {
      auto* ring = source.ring();
      if (ring == nullptr)
        return kMediaUnsupported;
      if (request.which_control == cadence_audiod_v1_SessionControl_duck_tag) {
        const auto& duck = request.control.duck;
        ring->setGain(duck.active ? duckLevel(duck) : 1.0f, duck.ramp_ms);
        return kMediaOk;
      }
      if (request.which_control == cadence_audiod_v1_SessionControl_mute_tag) {
        ring->setMuted(request.control.mute.muted);
        return kMediaOk;
      }
      // the level is the device's and the eq the mix's: neither is a session's own
      return kMediaUnsupported;
    }
    case AudioRoute::Direct:
      if (!source.audioControls().has(*wanted))
        return kMediaUnsupported;
      routeBack(source.sessionId(), request);
      return kMediaOk;
    case AudioRoute::External:
    case AudioRoute::Remote:
    case AudioRoute::None:
      return kMediaUnsupported;
  }
  return kMediaUnsupported;
}

void ExternalSources::routeBack(std::string_view id, const cadence_audiod_v1_SessionControl& request) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_route_control_tag;
  auto& out = envelope.body.route_control;
  copyText(out.session_id, id);
  // the two oneofs list the same controls in the same order, one field number apart
  switch (request.which_control) {
    case cadence_audiod_v1_SessionControl_duck_tag:
      out.which_control = cadence_audiod_v1_RouteControl_duck_tag;
      out.control.duck = request.control.duck;
      out.control.duck.level = duckLevel(request.control.duck);
      break;
    case cadence_audiod_v1_SessionControl_set_level_tag:
      out.which_control = cadence_audiod_v1_RouteControl_set_level_tag;
      out.control.set_level = request.control.set_level;
      break;
    case cadence_audiod_v1_SessionControl_set_eq_tag:
      out.which_control = cadence_audiod_v1_RouteControl_set_eq_tag;
      out.control.set_eq = request.control.set_eq;
      break;
    case cadence_audiod_v1_SessionControl_mute_tag:
      out.which_control = cadence_audiod_v1_RouteControl_mute_tag;
      out.control.mute = request.control.mute;
      break;
    default:
      return;
  }
  ipc_.send(envelope);
}

void ExternalSources::answer(std::string_view requestId, std::string_view code) {
  Envelope reply = cadence_audiod_v1_Envelope_init_zero;
  reply.which_body = cadence_audiod_v1_Envelope_command_result_tag;
  auto& result = reply.body.command_result;
  copyText(result.request_id, requestId);
  result.ok = code.empty();
  copyText(result.code, code);
  ipc_.send(reply);
}

}  // namespace cadence::audiod
