#pragma once

#include "cadence/screen/v1/media.pb.h"
#include "cadence/v2/media.pb.h"
#include "cadence/v2/screen.pb.h"
#include "player/video-follow.h"
#include "render/render-backend.h"

#include <cstdint>
#include <functional>
#include <map>
#include <optional>
#include <string>
#include <vector>

namespace cadence::screen::player {

struct Subtitle {
  std::string url;
  std::string language;
  std::string label;
  bool isDefault = false;
};

// a quality the server holds for the item, by name ("1080p", "720p60"); picking one is select_track of a video
// track with its name, which cadenced sends to the server
struct Rendition {
  std::string name;
  std::uint32_t height = 0;
  std::uint32_t fps = 0;
  std::vector<std::string> codecs;
  bool selected = false;
  // its dynamic range when not SDR (HDR10, HLG, DV)
  std::string hdr;
};

// media.video/play as the player takes it; urls, headers and labels are never logged
struct PlayRequest {
  std::string sessionId;
  cadence_screen_v1_MediaPurpose purpose = cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT;
  std::string url;
  std::string audioUrl;
  std::map<std::string, std::string> headers;
  std::vector<Subtitle> subtitles;
  std::uint64_t startMs = 0;
  bool live = false;
  // highest first; empty when there is no choice
  std::vector<Rendition> renditions;
  // a follower's play (multiroom P4): the group whose stream its picture follows. it plays no audio (the group's
  // session carries the sound), starts paused, and its picture is steered onto the group's clock (groupClock)
  std::string followGroupId;
};

struct Track {
  // "a2", "s1", "v1": the kind's letter and mpv's id, stable while the item plays
  std::string id;
  cadence_v2_MediaTrackKind kind = cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_UNSPECIFIED;
  std::string language;
  std::string label;
  bool selected = false;
  bool external = false;
  // the decoder's codec name as mpv reports it (h264, hevc, aac); the osd's source chip shows it, never sent
  std::string codec;

  bool operator==(const Track&) const = default;
};

// screen/v1's MediaState, as the player knows it
struct PlayerState {
  std::string sessionId;
  cadence_screen_v1_MediaPurpose purpose = cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_UNSPECIFIED;
  cadence_v2_PlaybackState state = cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE;
  std::uint64_t positionMs = 0;
  // unix ms on the device clock when positionMs held
  std::int64_t positionAtMs = 0;
  float rate = 0;
  std::uint64_t durationMs = 0;
  std::uint64_t bufferedMs = 0;
  // mpv's cache-buffering-state (0-100) while it waits for its cache; none otherwise
  std::optional<std::uint32_t> bufferingPercent;
  std::vector<Track> tracks;
  cadence_v2_AudioRoute audioRoute = cadence_v2_AudioRoute_AUDIO_ROUTE_UNSPECIFIED;
  std::vector<cadence_v2_AudioControl> audioControls;
  std::uint32_t controlLatencyMs = 0;
  // a failure token (decode, network, unsupported); never "gated", which is gated's
  std::string errorCode;
  // the playback gate holds the session (rule 7)
  bool gated = false;
  // the session's last state: it finished, state IDLE (with errorCode when it failed)
  bool ended = false;
  // the newest seek the player was asked for (counted from 1 over the player's life) and whether it is still under
  // way: while it is, positionMs is that seek's target, never a position mpv read before it; the ui holds its own
  // target until a state of its seek or later that is no longer pending. never sent (screen-only)
  std::uint32_t seekSeq = 0;
  bool seekPending = false;

  bool operator==(const PlayerState&) const = default;
};

// where playback is, as the player last read it: positionMs at atMs (unix ms), running on at rate (0 while paused,
// buffering or seeking), and the seek it is of (PlayerState's seekSeq, seekPending)
struct PositionSample {
  std::uint64_t positionMs = 0;
  std::int64_t atMs = 0;
  double rate = 0;
  std::uint32_t seekSeq = 0;
  bool seekPending = false;
};

// the player's i/o, rendering and sync figures (screen/v1's PlayerStats): numbers and a decoder token only (rule 8).
// the counts are the item's, from 0 at each new item
struct PlayerStats {
  std::string sessionId;
  std::uint64_t cacheSpeedBps = 0;
  std::uint64_t inputRateBps = 0;
  std::uint64_t cacheAheadBytes = 0;
  std::uint64_t cacheTotalBytes = 0;
  std::uint64_t bufferedMs = 0;
  std::uint64_t lowLevelSeeks = 0;
  std::uint64_t byteLevelSeeks = 0;
  bool underrun = false;
  std::uint64_t frameDrops = 0;
  std::uint64_t decoderDrops = 0;
  std::uint64_t voDelayedFrames = 0;
  double estimatedVfFps = 0;
  double displayFps = 0;
  double avsyncMs = 0;
  // mpv's hwdec-current: mediacodec, vaapi, ..., "no" for software; empty when unknown
  std::string hwdec;
};

enum class CommandAction { Pause, Resume, Stop, Seek, SelectTrack };

struct Command {
  CommandAction action = CommandAction::Pause;
  std::uint64_t positionMs = 0;
  // a seek by deltaMs from where playback is (or from the target of a seek still under way, so rapid presses add up)
  // instead of to positionMs
  bool relative = false;
  std::int64_t deltaMs = 0;
  std::string trackId;
  // the kind select_track turns off when trackId is empty
  cadence_v2_MediaTrackKind trackKind = cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_UNSPECIFIED;
};

struct EqBand {
  cadence_screen_v1_EqType type = cadence_screen_v1_EqType_EQ_TYPE_PEAKING;
  float freqHz = 0;
  float gainDb = 0;
  float q = 0;
};

struct EqRequest {
  std::string preset;
  float preampDb = 0;
  std::vector<EqBand> bands;
};

// a command's answer: empty when ok, else screen/v1's token (unsupported, invalid, gated, busy, failed)
using Result = std::string;

// one media player (mpv) for content and ambient video. the main thread only, the one the gl context is current on.
// rule 7: nothing is audible before the playback gate opens; a content session waits paused until it does
class Player {
 public:
  virtual ~Player() = default;

  // replaces whatever plays
  virtual Result play(const PlayRequest& request) = 0;
  virtual Result command(const Command& command) = 0;

  virtual void setGate(bool open) = 0;
  // a DIRECT route's level (0-100) and its ramp; a MIXER session answers unsupported, as audiod levels it
  virtual Result setLevel(std::uint32_t level, std::uint32_t rampMs) = 0;
  // a duck never raises the level (rule 7): level is clamped to 0..1 of it
  virtual Result setDuck(bool active, float level, std::uint32_t rampMs) = 0;
  virtual Result setMute(bool muted) = 0;
  virtual Result setEq(const EqRequest& eq) = 0;

  // drains mpv's events and steps level ramps; call on every loop turn and on a wake
  virtual void pump(std::int64_t nowMs) = 0;
  // the render api path: a new frame is due (from mpv's update callback)
  [[nodiscard]] virtual bool frameDue() = 0;
  // renders the due frame into the player's texture; false when there was none
  virtual bool renderFrame() = 0;
  // the last rendered frame (flipped); none on android's surface embed, where mpv draws below the ui
  [[nodiscard]] virtual std::optional<render::Texture> videoTexture() const = 0;
  // the session draws through our gl and has video
  [[nodiscard]] virtual bool videoInGl() const = 0;
  [[nodiscard]] virtual bool firstFrameShown() const = 0;
  // after the swap that showed a rendered frame
  virtual void reportSwap() = 0;

  [[nodiscard]] virtual const PlayerState& state() const = 0;
  // called from pump when the state a MediaState carries changed
  virtual void onStateChanged(std::function<void(const PlayerState&)> listener) = 0;

  // the transport's figures while a session waits for its cache (mpv's numbers): a player without one answers none
  // of them, and never waits
  [[nodiscard]] virtual std::optional<int> bufferingPercent() const { return std::nullopt; }
  [[nodiscard]] virtual bool waiting() const { return false; }
  [[nodiscard]] virtual std::optional<std::uint64_t> cacheSpeed() const { return std::nullopt; }
  // the position as the player reads it now (mpv's time-pos as it last came, not the debounced state): its state's by
  // default
  [[nodiscard]] virtual PositionSample positionSample() const {
    const auto& s = state();
    return PositionSample{s.positionMs, s.positionAtMs, s.rate, s.seekSeq, s.seekPending};
  }
  // the subtitle line now on screen (empty between lines): the ui draws subtitles itself
  virtual void onSubtitle(std::function<void(const std::string&)> listener) { (void)listener; }
  // the player's figures (PlayerStats), as they change while a session plays or waits
  virtual void onStats(std::function<void(const PlayerStats&)> listener) { (void)listener; }
  // the group's media clock from audiod's tap (multiroom P4), which a follower's play follows; main thread
  virtual void groupClock(const FollowClock& clock) { (void)clock; }
  // the session playing now is a follower's
  [[nodiscard]] virtual bool following() const { return false; }
};

}  // namespace cadence::screen::player
