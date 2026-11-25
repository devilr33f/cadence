#pragma once

#include "player/audio-control.h"
#include "player/audio-route.h"
#include "player/mpv-tracks.h"
#include "player/mpv-options.h"
#include "player/mpv-settings.h"
#include "player/pcm-ring.h"
#include "player/player.h"
#include "player/state-report.h"
#include "player/video-output.h"

#include <mpv/client.h>

#include <functional>
#include <map>
#include <memory>
#include <string>

namespace cadence::screen::player {

struct MpvPlayerConfig {
  MpvConfig mpv;
  // where frames go: RenderApiOutput on linux, SurfaceEmbedOutput on android; null = no video at all
  std::unique_ptr<VideoOutput> output;
  // mpv's thread calls this when events wait (the app pushes an sdl event)
  std::function<void()> wake;
  // unix ms, for position_at_ms
  std::function<std::int64_t()> unixNowMs;
  // CLOCK_MONOTONIC ns, the group clock's time (a follower's); steady_clock's by default
  std::function<std::int64_t()> monoNowNs;
  // how often a playing content session's figures are read (onStats); a test's shorter
  std::int64_t statsEveryMs = 10000;
};

// Player on libmpv. made and destroyed on the main thread with the gl context current: the destructor frees the
// render context first, then mpv (s1's teardown order)
class MpvPlayer final : public Player {
 public:
  // nullptr when mpv does not start (an option refused, no render context)
  static std::unique_ptr<MpvPlayer> create(MpvPlayerConfig config);
  ~MpvPlayer() override;
  MpvPlayer(const MpvPlayer&) = delete;
  MpvPlayer& operator=(const MpvPlayer&) = delete;

  Result play(const PlayRequest& request) override;
  Result command(const Command& command) override;
  void setGate(bool open) override;
  Result setLevel(std::uint32_t level, std::uint32_t rampMs) override;
  Result setDuck(bool active, float level, std::uint32_t rampMs) override;
  Result setMute(bool muted) override;
  Result setEq(const EqRequest& eq) override;
  void pump(std::int64_t nowMs) override;
  [[nodiscard]] bool frameDue() override;
  bool renderFrame() override;
  [[nodiscard]] std::optional<render::Texture> videoTexture() const override;
  [[nodiscard]] bool videoInGl() const override;
  [[nodiscard]] bool firstFrameShown() const override { return firstFrame_; }
  void reportSwap() override;
  [[nodiscard]] const PlayerState& state() const override { return state_; }
  [[nodiscard]] PositionSample positionSample() const override;
  void onStateChanged(std::function<void(const PlayerState&)> listener) override { listener_ = std::move(listener); }
  void groupClock(const FollowClock& clock) override { follower_.clock(clock); }
  [[nodiscard]] bool following() const override { return following_ && !state_.sessionId.empty(); }

  // a MIXER session's ring offered by audiod's tap (an out-of-process audiod: android, where the screen and audiod
  // are separate processes): ao_cadence reopens on its fds (async option sets and ao-reload). the ring before it
  // stays mapped until the next one replaces it, so the ao never reads a closed fd. false on the stock ao
  bool useRing(PcmRing ring);
  [[nodiscard]] const PcmRing* ring() const { return rings_.empty() ? nullptr : &rings_.back(); }

  // media.mpv's config, already validated by cadenced and here again: each key's options are applied at once where
  // mpv takes a runtime set and stored for the next file otherwise; where each key landed comes back through
  // onMpvState. replaces the config before it whole
  bool applyMpvConfig(std::string_view json);
  void onMpvState(std::function<void(const MpvKeyState&)> listener) { settingsListener_ = std::move(listener); }
  // the next-load options of the config in force, for tests
  [[nodiscard]] const MpvOptions& mpvLoadOptions() const { return settingsLoad_; }

  // the subtitle line now on screen (mpv's sub-text, formatting stripped): the screen draws subtitles itself, as
  // mediacodec_embed draws none, so mpv's own are off on every path. empty between lines
  [[nodiscard]] const std::string& subtitleText() const { return subText_; }
  void onSubtitle(std::function<void(const std::string&)> listener) override { subListener_ = std::move(listener); }
  // mpv's cache-buffering-state while paused for the cache, 0-100; nullopt when it reports none
  [[nodiscard]] std::optional<int> bufferingPercent() const override;
  // the session waits for its source: loading before its first frame, seeking, or paused for the cache (whatever
  // the user's pause)
  [[nodiscard]] bool waiting() const override;
  // what the cache reads now, bytes/s (mpv's cache-speed, asked once a second while a session plays); nullopt before
  // mpv answered or without a session
  [[nodiscard]] std::optional<std::uint64_t> cacheSpeed() const override;
  // every statsEveryMs (10 s) while a content session plays or waits for its cache, the player's figures
  void onStats(std::function<void(const PlayerStats&)> listener) override { statsListener_ = std::move(listener); }

  // the output, for android's surface callbacks
  [[nodiscard]] VideoOutput* output() const { return output_.get(); }
  // an observed property as mpv last reported it (pause, volume, mute, time-pos, ...), for tests and stats: never a
  // synchronous read, which could deadlock on the render thread; nullopt when mpv has none
  std::optional<double> doubleProperty(const char* name) const;
  std::optional<bool> flagProperty(const char* name) const;

 private:
  explicit MpvPlayer(MpvPlayerConfig config);
  bool init();
  void handle(const mpv_event& event);
  void onFileLoaded();
  void onEndFile(const mpv_event_end_file& end);
  void refreshTracks();
  // the pause mpv should hold: the user's, or the gate's for a session that has sound
  void applyPause();
  void applyVolume(std::int64_t nowMs);
  void update(std::int64_t nowMs);
  // the async reads of cache-speed (each second) and the stats (each statsEveryMs_)
  void askStats(std::int64_t nowMs);
  void statReply(const mpv_event& event);
  // media.mpv: the reply to one live option set, and the report when a key's status moved
  void settingReply(std::uint64_t reply, int error);
  void reportSettings(bool force = false);
  [[nodiscard]] bool gateHolds() const;
  // a follower's step: the controller's reading of mpv, and what it asks carried out; its line every 10 s
  void follow(std::int64_t nowMs);
  [[nodiscard]] std::int64_t nowMs() const;

  MpvConfig mpvConfig_;
  std::unique_ptr<VideoOutput> output_;
  std::function<void()> wake_;
  std::function<std::int64_t()> unixNowMs_;
  std::function<std::int64_t()> monoNowNs_;
  std::function<void(const PlayerState&)> listener_;
  mpv_handle* mpv_ = nullptr;

  AudioControl audio_;
  AudioRouteInfo route_;
  PlayRequest request_;
  bool userPaused_ = false;
  // a follower's play (multiroom P4): its controller, the pause it holds, its last line
  bool following_ = false;
  bool followPaused_ = false;
  VideoFollower follower_;
  std::int64_t followLoggedMs_ = 0;
  bool firstFrame_ = false;
  double lastVolume_ = -1;
  std::int64_t lastPumpMs_ = 0;
  // end-file events of entries up to this one belong to files a play replaced
  std::int64_t ignoreEndUpTo_ = 0;
  std::int64_t maxEntry_ = 0;
  MpvFlags flags_;
  // mpv's time-pos and when it came (unix ms); while a seek is under way, that seek's target, and the time-pos events
  // mpv sends before it lands (read before the seek) are dropped
  double timePos_ = 0;
  std::int64_t timePosAtMs_ = 0;
  // the newest seek asked for, whether it is still under way, and the seek commands mpv has not answered yet: a
  // playback-restart lands the newest only once every seek before it was answered
  std::uint32_t seekSeq_ = 0;
  bool seekPending_ = false;
  int seekReplies_ = 0;
  double duration_ = 0;
  double cached_ = 0;
  double speed_ = 1;
  std::vector<MpvTrack> mpvTracks_;
  std::map<std::string, double> props_;
  PlayerState state_;
  PlayerState reported_;
  std::vector<PcmRing> rings_;
  // mpv's current-ao ("cadence" once ao_cadence opened), and the audio's decoded and output params, for the logs
  std::string currentAo_;
  MpvAudioParams audioParams_;
  MpvAudioParams audioOutParams_;
  std::string subText_;
  std::function<void(const std::string&)> subListener_;
  std::function<void(const PlayerStats&)> statsListener_;
  std::optional<std::uint64_t> cacheSpeed_;
  std::int64_t speedAskedMs_ = 0;
  std::int64_t statsAskedMs_ = 0;
  std::int64_t statsEveryMs_ = 10000;
  // the figures being read; sent when the last reply of the round comes
  PlayerStats pending_;
  bool statsPending_ = false;

  // media.mpv: the config's keys, each option's status (0 pending, 1 applied, 2 refused) and the reply id of its
  // live set; the next-load options ride every loadfile; the last report keeps the listener quiet until one moves
  struct SettingOpt {
    std::string option;
    bool nextLoad = false;
    int status = 0;
    std::uint64_t reply = 0;
  };
  struct SettingKey {
    std::string key;
    std::vector<SettingOpt> opts;
    // the device's own check refused the key (no option was ever set)
    bool refusedKey = false;
  };
  std::vector<SettingKey> settingKeys_;
  MpvOptions settingsLoad_;
  // one past the last live set's reply id, kept across configs so no id answers twice
  std::uint64_t settingsReplies_ = 0;
  MpvKeyState settingsReport_;
  std::function<void(const MpvKeyState&)> settingsListener_;
};

}  // namespace cadence::screen::player
