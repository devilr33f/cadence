#include "sim/fake-player.h"

#include <algorithm>
#include <cmath>
#include <cstring>

namespace cadence::screen::sim {

namespace {

// the placeholder plane's size: enough that a screenshot tells playing from idle, cheap enough to repaint often
constexpr int kPlaneW = 192;
constexpr int kPlaneH = 108;
// a made-up item's length (24 min, as the fake catalog's episodes)
constexpr std::uint64_t kDurationMs = 24u * 60 * 1000;
// the cache's first fill before the first frame
constexpr std::int64_t kFillMs = 1800;
// a seek lands this long after it was asked for
constexpr std::int64_t kSeekLandMs = 600;

}  // namespace

FakePlayer::FakePlayer(render::RenderBackend& backend) : backend_(backend) {
  pixels_.assign(static_cast<std::size_t>(kPlaneW * kPlaneH * 4), 0);
  texture_ = backend_.createTexture(render::Size{kPlaneW, kPlaneH}, render::PixelFormat::Rgba8, nullptr, 0);
  state_.audioRoute = cadence_v2_AudioRoute_AUDIO_ROUTE_DIRECT;
}

std::int64_t FakePlayer::unixMs() {
  return std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::system_clock::now().time_since_epoch())
      .count();
}

player::Track FakePlayer::track(const char* id, cadence_v2_MediaTrackKind kind, const char* language,
                                const char* label, bool selected) const {
  player::Track t;
  t.id = id;
  t.kind = kind;
  t.language = language;
  t.label = label;
  t.selected = selected;
  return t;
}

void FakePlayer::changed() {
  // the state a MediaState carries is told from pump, never inside the call that changed it (mpv's contract; the
  // session the ui builds of a MediaPlay must land before the player's report of it)
  notify_ = true;
}

player::Result FakePlayer::play(const player::PlayRequest& request) {
  state_ = player::PlayerState{};
  state_.sessionId = request.sessionId;
  state_.purpose = request.purpose;
  state_.durationMs = kDurationMs;
  state_.audioRoute = cadence_v2_AudioRoute_AUDIO_ROUTE_DIRECT;
  // codecs as mpv names them, so the osd's source chip has its line
  state_.tracks = {track("v1", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_VIDEO, "", "1080p", true),
                   track("a1", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_AUDIO, "ru", "AniLibria", true),
                   track("a2", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_AUDIO, "en", "Original", false),
                   track("s1", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_SUBTITLE, "ru", "Русские", true)};
  state_.tracks[0].codec = "hevc";
  state_.tracks[1].codec = "aac";
  state_.tracks[2].codec = "aac";
  position_ = static_cast<double>(request.startMs) / 1000.0;
  lastPumpMs_ = 0;
  firstFrame_ = false;
  fillFromMs_ = -1;
  seekPending_ = false;
  // rule 7: a session with sound waits paused until the gate opens
  if (!gate_ && request.purpose == cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT) {
    state_.state = cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED;
    state_.gated = true;
  } else {
    state_.state = cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING;
    state_.rate = 1;
  }
  state_.positionMs = request.startMs;
  state_.positionAtMs = unixMs();
  changed();
  return "";
}

player::Result FakePlayer::command(const player::Command& command) {
  switch (command.action) {
    case player::CommandAction::Pause:
      if (state_.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING) {
        state_.state = cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED;
        state_.rate = 0;
        changed();
      }
      return "";
    case player::CommandAction::Resume:
      if (state_.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED) {
        state_.state = cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING;
        state_.rate = 1;
        changed();
      }
      return "";
    case player::CommandAction::Stop:
      stop(nullptr);
      return "";
    case player::CommandAction::Seek: {
      if (state_.sessionId.empty()) return "invalid";
      // as mpv: a seek lands kSeekLandMs later, from the target of one under way when there is one. until it lands
      // this player keeps reporting the position it reads, which is from before the seek (worse than the real
      // player, which reports the target): the ui must hold its own target over those readings
      const double base = seekPending_ ? seekTarget_ : position_;
      double target = command.relative ? base + static_cast<double>(command.deltaMs) / 1000.0
                                       : static_cast<double>(command.positionMs) / 1000.0;
      target = std::clamp(target, 0.0, static_cast<double>(kDurationMs) / 1000.0 - 1.0);
      seekTarget_ = target;
      seekPending_ = true;
      seekLandsAtMs_ = nowMs_ + kSeekLandMs;
      state_.seekSeq++;
      state_.seekPending = true;
      changed();
      return "";
    }
    case player::CommandAction::SelectTrack: {
      bool found = false;
      for (auto& t : state_.tracks) {
        if (command.trackId.empty()) {
          if (t.kind == command.trackKind) t.selected = false;
        } else if (t.id == command.trackId) {
          t.selected = true;
          found = true;
        }
      }
      if (!found && !command.trackId.empty()) return "invalid";
      changed();
      return "";
    }
  }
  return "unsupported";
}

void FakePlayer::stop(const char* error) {
  const bool was = state_.state != cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE;
  state_ = player::PlayerState{};
  state_.ended = true;
  if (error != nullptr) state_.errorCode = error;
  firstFrame_ = false;
  if (was) changed();
}

void FakePlayer::setGate(bool open) {
  gate_ = open;
  if (open && state_.gated && state_.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED) {
    state_.state = cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING;
    state_.rate = 1;
    state_.gated = false;
    changed();
    return;
  }
  if (!open && state_.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING &&
      state_.purpose == cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT) {
    state_.state = cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED;
    state_.rate = 0;
    state_.gated = true;
    changed();
  }
}

player::Result FakePlayer::setLevel(std::uint32_t level, std::uint32_t rampMs) {
  (void)rampMs;
  level_ = level;
  return "";
}

player::Result FakePlayer::setDuck(bool active, float level, std::uint32_t rampMs) {
  (void)active;
  (void)level;
  (void)rampMs;
  return "";
}

player::Result FakePlayer::setMute(bool muted) {
  (void)muted;
  return "";
}

player::Result FakePlayer::setEq(const player::EqRequest& eq) {
  (void)eq;
  return "";
}

void FakePlayer::pump(std::int64_t nowMs) {
  nowMs_ = nowMs;
  if (!state_.sessionId.empty() && fillFromMs_ < 0) fillFromMs_ = nowMs;
  if (notify_) {
    notify_ = false;
    if (listener_) listener_(state_);
  }
  if (lastPumpMs_ == 0) lastPumpMs_ = nowMs;
  const double dt = static_cast<double>(std::max<std::int64_t>(nowMs - lastPumpMs_, 0)) / 1000.0;
  lastPumpMs_ = nowMs;
  if (seekPending_ && nowMs >= seekLandsAtMs_) {
    seekPending_ = false;
    position_ = seekTarget_;
    state_.seekPending = false;
    state_.positionMs = static_cast<std::uint64_t>(position_ * 1000.0);
    state_.positionAtMs = unixMs();
    changed();
  } else if (seekPending_) {
    // a reading from before the seek, every turn it is under way
    changed();
  }
  if (state_.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING && !waiting()) {
    position_ = std::min(position_ + dt, kDurationMs / 1000.0);
    state_.positionMs = static_cast<std::uint64_t>(position_ * 1000.0);
    state_.positionAtMs = unixMs();
    state_.bufferedMs = kDurationMs;
    if (position_ >= static_cast<double>(kDurationMs) / 1000.0) stop(nullptr);
  }
  // a frame of the placeholder plane every 66 ms while something plays, once the cache's first fill is done
  if (state_.state != cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE && !waiting() && nowMs - lastFrameMs_ >= 66) {
    lastFrameMs_ = nowMs;
    frameDue_ = true;
  }
}

bool FakePlayer::frameDue() { return frameDue_; }

bool FakePlayer::renderFrame() {
  if (!frameDue_ || !texture_) return false;
  frameDue_ = false;
  frame_++;
  // a drifting pair of colour bands over a dark base: anything a screenshot compares can tell apart
  const float t = static_cast<float>(frame_) * 0.06f;
  for (int y = 0; y < kPlaneH; y++) {
    for (int x = 0; x < kPlaneW; x++) {
      auto* p = pixels_.data() + (static_cast<std::size_t>(y) * kPlaneW + static_cast<std::size_t>(x)) * 4;
      const float fx = static_cast<float>(x) / kPlaneW;
      const float fy = static_cast<float>(y) / kPlaneH;
      const float a = 0.5f + 0.5f * std::sin(fx * 6.0f + t);
      const float b = 0.5f + 0.5f * std::sin(fy * 5.0f - t * 0.7f);
      p[0] = static_cast<std::uint8_t>(30 + 120 * a);
      p[1] = static_cast<std::uint8_t>(24 + 90 * b);
      p[2] = static_cast<std::uint8_t>(40 + 110 * (1.0f - a * 0.5f - b * 0.5f) * (0.5f + 0.5f * a));
      p[3] = 255;
    }
  }
  backend_.updateTexture(texture_, render::Rect{0, 0, kPlaneW, kPlaneH}, pixels_.data(), kPlaneW * 4);
  firstFrame_ = true;
  return true;
}

std::optional<render::Texture> FakePlayer::videoTexture() const {
  if (!texture_ || state_.state == cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE) return std::nullopt;
  return texture_;
}

bool FakePlayer::videoInGl() const { return true; }

bool FakePlayer::waiting() const {
  return !state_.sessionId.empty() && (fillFromMs_ < 0 || nowMs_ - fillFromMs_ < kFillMs);
}

std::optional<int> FakePlayer::bufferingPercent() const {
  if (!waiting() || fillFromMs_ < 0) return std::nullopt;
  return static_cast<int>(std::clamp<std::int64_t>((nowMs_ - fillFromMs_) * 100 / kFillMs, 0, 99));
}

std::optional<std::uint64_t> FakePlayer::cacheSpeed() const {
  if (state_.sessionId.empty()) return std::nullopt;
  return 3'400'000;
}

bool FakePlayer::firstFrameShown() const { return firstFrame_; }

const player::PlayerState& FakePlayer::state() const { return state_; }

void FakePlayer::onStateChanged(std::function<void(const player::PlayerState&)> listener) {
  listener_ = std::move(listener);
}

}  // namespace cadence::screen::sim
