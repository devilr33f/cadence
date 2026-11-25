#include "audiod/media-handlers.h"

#include "audiod/sender-volume.h"

#include <algorithm>
#include <cstddef>
#include <cstring>
#include <iterator>
#include <memory>
#include <optional>
#include <utility>

namespace cadence::audiod {

namespace {

static_assert(sizeof(cadence_audiod_v1_MediaMetadata::title) == kMetadataTextBytes + 1);
static_assert(sizeof(cadence_audiod_v1_MediaMetadata::artist) == kMetadataTextBytes + 1);
static_assert(sizeof(cadence_audiod_v1_MediaMetadata::album) == kMetadataTextBytes + 1);
static_assert(sizeof(cadence_audiod_v1_MediaMetadata::artwork_url) == kArtworkUrlBytes + 1);
static_assert(sizeof(cadence_audiod_v1_MediaMetadata::artwork_id) == kArtworkIdBytes + 1);
static_assert(sizeof(cadence_audiod_v1_MediaMetadata::sender) == kMetadataTextBytes + 1);
static_assert(std::size(kMediaActions) <=
              sizeof(cadence_audiod_v1_MediaState::actions) / sizeof(cadence_audiod_v1_MediaAction));
static_assert(std::size(kAudioControls) <=
              sizeof(cadence_audiod_v1_MediaState::audio_controls) / sizeof(cadence_audiod_v1_AudioControl));

template <std::size_t N>
void copyText(char (&out)[N], std::string_view text) {
  const auto size = std::min(text.size(), N - 1);
  std::memcpy(out, text.data(), size);
  out[size] = '\0';
}

// a decoded string field, which nanopb terminates within its bound
template <std::size_t N>
std::string textOf(const char (&field)[N]) {
  return {field, static_cast<std::size_t>(std::find(field, field + N, '\0') - field)};
}

std::optional<MediaAction> toAction(cadence_audiod_v1_MediaAction action) {
  switch (action) {
    case cadence_audiod_v1_MediaAction_MEDIA_ACTION_PAUSE:
      return MediaAction::Pause;
    case cadence_audiod_v1_MediaAction_MEDIA_ACTION_RESUME:
      return MediaAction::Resume;
    case cadence_audiod_v1_MediaAction_MEDIA_ACTION_STOP:
      return MediaAction::Stop;
    case cadence_audiod_v1_MediaAction_MEDIA_ACTION_SEEK:
      return MediaAction::Seek;
    case cadence_audiod_v1_MediaAction_MEDIA_ACTION_NEXT:
      return MediaAction::Next;
    case cadence_audiod_v1_MediaAction_MEDIA_ACTION_PREVIOUS:
      return MediaAction::Previous;
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

cadence_audiod_v1_MediaSourceKind toWire(MediaKind kind) {
  switch (kind) {
    case MediaKind::Url:
      return cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_URL;
    case MediaKind::Soloist:
      return cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_SOLOIST;
    case MediaKind::Airplay:
      return cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_AIRPLAY;
    case MediaKind::Stream:
      return cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_STREAM;
    case MediaKind::Video:
      return cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_VIDEO;
    case MediaKind::Screencast:
      return cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_SCREENCAST;
    case MediaKind::External:
      return cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_EXTERNAL;
    case MediaKind::Group:
      return cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_GROUP;
    case MediaKind::Bluetooth:
      return cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_BLUETOOTH;
  }
  return cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_UNSPECIFIED;
}

cadence_audiod_v1_PlaybackState toWire(PlayState state) {
  switch (state) {
    case PlayState::Idle:
      return cadence_audiod_v1_PlaybackState_PLAYBACK_STATE_IDLE;
    case PlayState::Playing:
      return cadence_audiod_v1_PlaybackState_PLAYBACK_STATE_PLAYING;
    case PlayState::Paused:
      return cadence_audiod_v1_PlaybackState_PLAYBACK_STATE_PAUSED;
    case PlayState::Buffering:
      return cadence_audiod_v1_PlaybackState_PLAYBACK_STATE_BUFFERING;
  }
  return cadence_audiod_v1_PlaybackState_PLAYBACK_STATE_UNSPECIFIED;
}

cadence_audiod_v1_AudioRoute toWire(AudioRoute route) {
  switch (route) {
    case AudioRoute::Mixer:
      return cadence_audiod_v1_AudioRoute_AUDIO_ROUTE_MIXER;
    case AudioRoute::Direct:
      return cadence_audiod_v1_AudioRoute_AUDIO_ROUTE_DIRECT;
    case AudioRoute::External:
      return cadence_audiod_v1_AudioRoute_AUDIO_ROUTE_EXTERNAL;
    case AudioRoute::Remote:
      return cadence_audiod_v1_AudioRoute_AUDIO_ROUTE_REMOTE;
    case AudioRoute::None:
      return cadence_audiod_v1_AudioRoute_AUDIO_ROUTE_NONE;
  }
  return cadence_audiod_v1_AudioRoute_AUDIO_ROUTE_UNSPECIFIED;
}

cadence_audiod_v1_AudioControl toWire(AudioControl control) {
  switch (control) {
    case AudioControl::Volume:
      return cadence_audiod_v1_AudioControl_AUDIO_CONTROL_VOLUME;
    case AudioControl::Duck:
      return cadence_audiod_v1_AudioControl_AUDIO_CONTROL_DUCK;
    case AudioControl::Mute:
      return cadence_audiod_v1_AudioControl_AUDIO_CONTROL_MUTE;
    case AudioControl::Eq:
      return cadence_audiod_v1_AudioControl_AUDIO_CONTROL_EQ;
    case AudioControl::VizPcm:
      return cadence_audiod_v1_AudioControl_AUDIO_CONTROL_VIZ_PCM;
    case AudioControl::VizBands:
      return cadence_audiod_v1_AudioControl_AUDIO_CONTROL_VIZ_BANDS;
  }
  return cadence_audiod_v1_AudioControl_AUDIO_CONTROL_UNSPECIFIED;
}

std::string_view actionToken(std::optional<MediaAction> action) {
  if (!action)
    return "unknown";
  switch (*action) {
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

std::string_view modeToken(cadence_audiod_v1_UrlMode mode) {
  switch (mode) {
    case cadence_audiod_v1_UrlMode_URL_MODE_SESSION:
      return "session";
    case cadence_audiod_v1_UrlMode_URL_MODE_OVERLAY:
      return "overlay";
    default:
      return "none";
  }
}

std::string_view codeToken(std::string_view code) { return code.empty() ? "ok" : code; }

constexpr std::chrono::milliseconds kResendEvery{100};

}  // namespace

MediaHandlers::MediaHandlers(Loop& loop, IpcServer& ipc, const Log& log, Playback& playback,
                             MediaSourceFactory factory, WallClock now, std::chrono::milliseconds playBound,
                             RingFactory rings)
    : loop_(loop),
      ipc_(ipc),
      log_(log),
      playBound_(playBound),
      sessions_(log, playback.music(), [&playback] { return playback.gateOpen(); }, factory,
                [this](const MediaSession& session) { send(session); }, std::move(now)),
      overlay_(log, playback.overlay(), [&playback] { return playback.gateOpen(); }, std::move(factory)),
      externals_(ipc, log, sessions_, playback, std::move(rings), &loop) {
  ipc_.on(cadence_audiod_v1_Envelope_url_play_tag,
          [this](const Envelope& envelope) { urlPlay(envelope.body.url_play); });
  ipc_.on(cadence_audiod_v1_Envelope_media_command_tag,
          [this](const Envelope& envelope) { command(envelope.body.media_command); });
  ipc_.on(cadence_audiod_v1_Envelope_sender_volume_set_tag, [this](const Envelope& envelope) {
    const auto& request = envelope.body.sender_volume_set;
    if (const auto found = senderLevels_.find(request.sender); found != senderLevels_.end())
      found->second(static_cast<std::uint8_t>(std::min<std::uint32_t>(request.level, kMaxSenderLevel)));
  });
  playback.onGateShut([this] { gateShut(); });
}

void MediaHandlers::onSenderLevel(cadence_audiod_v1_Sender sender, std::function<void(std::uint8_t level)> handler) {
  senderLevels_.insert_or_assign(sender, std::move(handler));
}

// a new Configure shuts the gate; the session it leaves goes out after the Ready
void MediaHandlers::configure(const Configure& configuration) {
  announced_ = false;
  caFile_ = textOf(configuration.ca_file);
  gateShut();
}

void MediaHandlers::ready() {
  announced_ = true;
  send(sessions_.session());
}

// a session that waits paused outlives cadenced, and the next one learns it after its Ready
void MediaHandlers::reset() {
  announced_ = false;
  externals_.reset();
  ++connection_;
  if (resend_)
    loop_.cancel(*resend_);
  resend_.reset();
  caFile_.clear();
  gateShut();
}

void MediaHandlers::gateShut() {
  sessions_.gateShut();
  overlay_.gateShut();
}

void MediaHandlers::urlPlay(const cadence_audiod_v1_UrlPlay& request) {
  const auto mode = modeToken(request.mode);
  const auto refuse = [&](std::string_view code) {
    log_.info("media_command", {{"action", "play"}, {"mode", mode}, {"code", code}});
    answer(request.request_id, code);
  };
  const bool overlay = request.mode == cadence_audiod_v1_UrlMode_URL_MODE_OVERLAY;
  if (!overlay && request.mode != cadence_audiod_v1_UrlMode_URL_MODE_SESSION)
    return refuse(kMediaInvalid);
  MediaMetadata metadata;
  if (request.has_metadata)
    metadata = {textOf(request.metadata.title), textOf(request.metadata.artist), textOf(request.metadata.album),
                textOf(request.metadata.artwork_url), {}, {}};
  auto waiting = std::make_shared<Waiting>();
  waiting->requestId = textOf(request.request_id);
  UrlRequest url{textOf(request.url), std::move(metadata), request.start_ms, caFile_};
  auto done = [this, waiting, mode](std::string_view code) { settle(*waiting, mode, code, false); };
  if (!overlay && group_.play)
    group_.play(request.start_ms);
  const auto code = overlay ? overlay_.play(url, std::move(done)) : sessions_.play(url, std::move(done));
  if (!code.empty())
    return refuse(code);
  waiting->bound = loop_.after(playBound_, [this, waiting, mode] {
    waiting->bound.reset();
    settle(*waiting, mode, kMediaOk, true);
  });
}

// the first answer wins; ok at the bound only says the source is still on its way
void MediaHandlers::settle(Waiting& waiting, std::string_view mode, std::string_view code, bool atBound) {
  if (waiting.answered)
    return;
  waiting.answered = true;
  if (waiting.bound)
    loop_.cancel(*waiting.bound);
  waiting.bound.reset();
  if (atBound)
    log_.info("media_command", {{"action", "play"}, {"mode", mode}, {"code", "ok"}, {"answer", "bound"}});
  else
    log_.info("media_command", {{"action", "play"}, {"mode", mode}, {"code", codeToken(code)}});
  answer(waiting.requestId, code);
}

void MediaHandlers::command(const cadence_audiod_v1_MediaCommand& request) {
  const auto action = toAction(request.action);
  const auto finish = [this, action, requestId = textOf(request.request_id), connection = connection_](
                          std::string_view code) {
    log_.info("media_command", {{"action", actionToken(action)}, {"code", codeToken(code)}});
    if (connection == connection_)
      answer(requestId, code);
  };
  if (!action) {
    finish(kMediaUnsupported);
    return;
  }
  runCommand(*action, request.position_ms, finish);
}

void MediaHandlers::runCommand(MediaAction action, std::uint64_t positionMs, CommandDone done) {
  std::optional<std::uint64_t> heard;
  if (group_.command)
    heard = group_.command(action, positionMs);
  const auto code = sessions_.command(action, positionMs, done);
  // a paused group resumes from what it heard, not from what the decoder had read ahead
  if (action == MediaAction::Pause && code.empty() && heard) {
    if (auto* source = sessions_.ownerSource(); source != nullptr && source->actions().has(MediaAction::Seek))
      sessions_.command(MediaAction::Seek, *heard);
  }
  if (code != kMediaPending && done)
    done(code);
}

void MediaHandlers::answer(std::string_view requestId, std::string_view code) {
  Envelope reply = cadence_audiod_v1_Envelope_init_zero;
  reply.which_body = cadence_audiod_v1_Envelope_command_result_tag;
  auto& result = reply.body.command_result;
  copyText(result.request_id, requestId);
  result.ok = code.empty();
  copyText(result.code, code);
  ipc_.send(reply);
}

void MediaHandlers::send(const MediaSession& published) {
  if (group_.published && !group_.published(published))
    return;
  if (!announced_)
    return;
  auto session = published;
  if (group_.heardMs && session.state == PlayState::Playing) {
    if (const auto heard = group_.heardMs()) {
      session.positionMs = *heard;
      session.positionAtMs = unixNowMs();
    }
  }
  Envelope reply = cadence_audiod_v1_Envelope_init_zero;
  reply.which_body = cadence_audiod_v1_Envelope_media_state_tag;
  auto& state = reply.body.media_state;
  copyText(state.session_id, session.id);
  copyText(state.external_id, session.externalId);
  state.state = toWire(session.state);
  state.position_ms = session.positionMs;
  state.position_at_ms = session.positionAtMs;
  state.rate = session.rate();
  state.duration_ms = session.durationMs;
  if (!session.id.empty()) {
    state.source = toWire(session.kind);
    state.has_metadata = true;
    copyText(state.metadata.title, session.metadata.title);
    copyText(state.metadata.artist, session.metadata.artist);
    copyText(state.metadata.album, session.metadata.album);
    copyText(state.metadata.artwork_url, session.metadata.artworkUrl);
    copyText(state.metadata.artwork_id, session.metadata.artworkId);
    copyText(state.metadata.sender, session.metadata.sender);
    for (const auto action : kMediaActions) {
      if (session.actions.has(action))
        state.actions[state.actions_count++] = toWire(action);
    }
    state.audio_route = toWire(session.route);
    for (const auto control : kAudioControls) {
      if (session.controls.has(control))
        state.audio_controls[state.audio_controls_count++] = toWire(control);
    }
  }
  if (ipc_.send(reply)) {
    if (resend_)
      loop_.cancel(*resend_);
    resend_.reset();
    return;
  }
  if (resend_ || !ipc_.hasClient())
    return;
  // the client stopped reading and its socket is full: the latest state goes out once it takes frames again
  resend_ = loop_.after(kResendEvery, [this] {
    resend_.reset();
    send(sessions_.session());
  });
}

}  // namespace cadence::audiod
