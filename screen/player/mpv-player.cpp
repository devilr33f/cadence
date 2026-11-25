#include "player/mpv-player.h"

#include "ipc/log.h"
#include "player/eq-filter.h"
#include "player/mpv-tracks.h"

#include <algorithm>
#include <chrono>
#include <cmath>
#include <cstring>

namespace cadence::screen::player {

namespace {

// observed properties, by reply id
enum Prop : std::uint64_t {
  kPause = 1,
  kPausedForCache,
  kSeeking,
  kTimePos,
  kDuration,
  kCacheDuration,
  kSpeed,
  kTrackList,
  kIdleActive,
  kVolume,
  kMute,
  kSubText,
  kBuffering,
  kVideoParams,
  kCurrentAo,
  kAudioParams,
  kAudioOutParams,
};

ipc::Level levelOf(mpv_log_level level) {
  if (level <= MPV_LOG_LEVEL_ERROR) return ipc::Level::Error;
  if (level <= MPV_LOG_LEVEL_WARN) return ipc::Level::Warn;
  if (level <= MPV_LOG_LEVEL_INFO) return ipc::Level::Info;
  return ipc::Level::Debug;
}

const char* endErrorCode(int error) {
  switch (error) {
    case MPV_ERROR_LOADING_FAILED: return "network";
    case MPV_ERROR_UNKNOWN_FORMAT: return "unsupported";
    case MPV_ERROR_NOTHING_TO_PLAY: return "decode";
    case MPV_ERROR_AO_INIT_FAILED: return "failed";
    case MPV_ERROR_VO_INIT_FAILED: return "failed";
    default: return "failed";
  }
}

std::uint64_t toMs(double seconds) {
  if (!std::isfinite(seconds) || seconds <= 0) return 0;
  return static_cast<std::uint64_t>(std::llround(seconds * 1000.0));
}

// the stats' async reads (never a synchronous one, as with the commands): cache-speed each second, then a round of
// them each statsEveryMs_, which ends at hwdec-current's reply
enum StatReply : std::uint64_t {
  kStatSpeed = 300,
  kStatsSpeed,
  kStatsCache,
  kStatsDrops,
  kStatsDecoderDrops,
  kStatsDelayed,
  kStatsVfFps,
  kStatsDisplayFps,
  kStatsAvsync,
  kStatsHwdec,
};

std::uint64_t countOf(std::int64_t v) { return v > 0 ? static_cast<std::uint64_t>(v) : 0; }

double finite(double v) { return std::isfinite(v) ? v : 0; }

// demuxer-cache-state's figures: fw-bytes, total-bytes, raw-input-rate, underrun and the source's seeks
void readCacheState(const mpv_node& node, PlayerStats& out) {
  if (node.format != MPV_FORMAT_NODE_MAP || node.u.list == nullptr) return;
  const auto* list = node.u.list;
  for (int i = 0; i < list->num; i++) {
    const std::string_view key = list->keys[i];
    const mpv_node& v = list->values[i];
    if (v.format == MPV_FORMAT_INT64) {
      const auto n = countOf(v.u.int64);
      if (key == "fw-bytes") out.cacheAheadBytes = n;
      else if (key == "total-bytes") out.cacheTotalBytes = n;
      else if (key == "raw-input-rate") out.inputRateBps = n;
      else if (key == "debug-low-level-seeks") out.lowLevelSeeks = n;
      else if (key == "debug-byte-level-seeks") out.byteLevelSeeks = n;
    } else if (v.format == MPV_FORMAT_FLAG && key == "underrun") {
      out.underrun = v.u.flag != 0;
    }
  }
}

// the commands' reply ids: a failure comes back as MPV_EVENT_COMMAND_REPLY
enum Reply : std::uint64_t { kLoad = 100, kStop, kSeek, kAfRemove, kAfAdd, kAudioAdd, kSubAdd, kSet, kAoReload };

// media.mpv's live option sets answer at 1000 and up, one reply id per set (the ids above and the stats' stay below)
constexpr std::uint64_t kSettingReplyBase = 1000;

const char* replyName(std::uint64_t id) {
  switch (id) {
    case kLoad: return "load";
    case kStop: return "stop";
    case kSeek: return "seek";
    case kAfRemove: return "af_remove";
    case kAfAdd: return "af_add";
    case kAudioAdd: return "audio_add";
    case kSubAdd: return "sub_add";
    case kAoReload: return "ao_reload";
    default: return "set";
  }
}

// every call that changes mpv is async: with the render api's advanced control the core may wait on the render
// thread, which is this one, so a synchronous call from here can deadlock (seen under load in the app test)
int runCommand(mpv_handle* mpv, std::uint64_t reply, const std::vector<std::string>& args) {
  std::vector<const char*> argv;
  argv.reserve(args.size() + 1);
  for (const auto& a : args) argv.push_back(a.c_str());
  argv.push_back(nullptr);
  return mpv_command_async(mpv, reply, argv.data());
}

void setFlag(mpv_handle* mpv, const char* name, bool value) {
  int v = value ? 1 : 0;
  mpv_set_property_async(mpv, kSet, name, MPV_FORMAT_FLAG, &v);
}

void setDouble(mpv_handle* mpv, const char* name, double value) {
  mpv_set_property_async(mpv, kSet, name, MPV_FORMAT_DOUBLE, &value);
}

void setString(mpv_handle* mpv, const char* name, const std::string& value) {
  const char* v = value.c_str();
  mpv_set_property_async(mpv, kSet, name, MPV_FORMAT_STRING, &v);
}

}  // namespace

std::unique_ptr<MpvPlayer> MpvPlayer::create(MpvPlayerConfig config) {
  std::unique_ptr<MpvPlayer> player(new MpvPlayer(std::move(config)));
  if (!player->init()) return nullptr;
  return player;
}

MpvPlayer::MpvPlayer(MpvPlayerConfig config)
    : mpvConfig_(std::move(config.mpv)),
      output_(std::move(config.output)),
      wake_(std::move(config.wake)),
      unixNowMs_(std::move(config.unixNowMs)),
      monoNowNs_(std::move(config.monoNowNs)),
      statsEveryMs_(config.statsEveryMs > 0 ? config.statsEveryMs : 10000) {
  if (!unixNowMs_) {
    unixNowMs_ = [] {
      return std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::system_clock::now().time_since_epoch())
          .count();
    };
  }
  if (!monoNowNs_) {
    // CLOCK_MONOTONIC on linux and android, as the group clock's mono_ns
    monoNowNs_ = [] {
      return std::chrono::duration_cast<std::chrono::nanoseconds>(std::chrono::steady_clock::now().time_since_epoch())
          .count();
    };
  }
  state_.audioRoute = cadence_v2_AudioRoute_AUDIO_ROUTE_UNSPECIFIED;
  reported_ = state_;
}

MpvPlayer::~MpvPlayer() {
  // the render context goes first, with the gl context current; then mpv
  if (output_) output_->detach();
  if (mpv_ != nullptr) {
    mpv_set_wakeup_callback(mpv_, nullptr, nullptr);
    mpv_terminate_destroy(mpv_);
  }
}

bool MpvPlayer::init() {
  mpv_ = mpv_create();
  if (mpv_ == nullptr) {
    CADENCE_LOG(Error, "mpv_create_failed");
    return false;
  }
  for (const auto& [key, value] : mpvInitOptions(mpvConfig_)) {
    const int err = mpv_set_option_string(mpv_, key.c_str(), value.c_str());
    if (err < 0) {
      CADENCE_LOG(Error, "mpv_option_refused", {{"option", ipc::Token(key)}, {"code", err}});
      return false;
    }
  }
  mpv_request_log_messages(mpv_, mpvConfig_.logLevel.c_str());
  if (const int err = mpv_initialize(mpv_); err < 0) {
    CADENCE_LOG(Error, "mpv_initialize_failed", {{"code", err}});
    return false;
  }
  // a board's display rate, which mpv cannot measure through the render api: its vo judges a frame late against
  // it. video-sync stays mpv's default (audio): a display-sync mode times frames by our swap reports, and every
  // turn of the main loop that slipped past a vblank read as a slow, jittery display, so the pi's vdrop settled at
  // ~20 fps of 60 with a/v drifting, where audio sync drops evenly and holds a/v. 0.40 renamed the knob
  // display-fps-override (display-fps is the measured value, with no runtime property), so it goes through the
  // options/ property here
  if (mpvConfig_.displayFpsCap > 0 && mpvConfig_.videoPath == VideoPath::RenderApi)
    setString(mpv_, "options/display-fps-override", std::to_string(mpvConfig_.displayFpsCap));
  mpv_observe_property(mpv_, kPause, "pause", MPV_FORMAT_FLAG);
  mpv_observe_property(mpv_, kPausedForCache, "paused-for-cache", MPV_FORMAT_FLAG);
  mpv_observe_property(mpv_, kSeeking, "seeking", MPV_FORMAT_FLAG);
  mpv_observe_property(mpv_, kTimePos, "time-pos", MPV_FORMAT_DOUBLE);
  mpv_observe_property(mpv_, kDuration, "duration", MPV_FORMAT_DOUBLE);
  mpv_observe_property(mpv_, kCacheDuration, "demuxer-cache-duration", MPV_FORMAT_DOUBLE);
  mpv_observe_property(mpv_, kSpeed, "speed", MPV_FORMAT_DOUBLE);
  mpv_observe_property(mpv_, kTrackList, "track-list", MPV_FORMAT_NODE);
  mpv_observe_property(mpv_, kIdleActive, "idle-active", MPV_FORMAT_FLAG);
  mpv_observe_property(mpv_, kVolume, "volume", MPV_FORMAT_DOUBLE);
  mpv_observe_property(mpv_, kMute, "mute", MPV_FORMAT_FLAG);
  mpv_observe_property(mpv_, kSubText, "sub-text", MPV_FORMAT_STRING);
  mpv_observe_property(mpv_, kBuffering, "cache-buffering-state", MPV_FORMAT_DOUBLE);
  mpv_observe_property(mpv_, kVideoParams, "video-params", MPV_FORMAT_NODE);
  mpv_observe_property(mpv_, kCurrentAo, "current-ao", MPV_FORMAT_STRING);
  mpv_observe_property(mpv_, kAudioParams, "audio-params", MPV_FORMAT_NODE);
  mpv_observe_property(mpv_, kAudioOutParams, "audio-out-params", MPV_FORMAT_NODE);
  if (wake_) {
    mpv_set_wakeup_callback(
        mpv_, [](void* self) {
          auto* p = static_cast<MpvPlayer*>(self);
          if (p->wake_) p->wake_();
        },
        this);
  }
  if (output_ && !output_->attach(mpv_)) return false;
  CADENCE_LOG(Info, "mpv_ready", {{"client_api", static_cast<std::uint64_t>(mpv_client_api_version())}});
  return true;
}

std::int64_t MpvPlayer::nowMs() const { return unixNowMs_(); }

bool MpvPlayer::gateHolds() const {
  return state_.sessionId.empty() ? false : (!route_.silent && !audio_.gateOpen());
}

void MpvPlayer::applyPause() {
  if (mpv_ == nullptr || state_.sessionId.empty()) return;
  setFlag(mpv_, "pause", userPaused_ || gateHolds() || (following_ && followPaused_));
}

void MpvPlayer::applyVolume(std::int64_t now) {
  if (mpv_ == nullptr || !route_.playerControls) return;
  const double v = audio_.volume(now);
  if (std::fabs(v - lastVolume_) < 0.01) return;
  setDouble(mpv_, "volume", v);
  lastVolume_ = v;
}

Result MpvPlayer::play(const PlayRequest& request) {
  if (request.url.empty() || request.sessionId.empty()) return "invalid";
  if (request.purpose != cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT &&
      request.purpose != cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_AMBIENT) {
    // boot movies are v2's
    return "unsupported";
  }
  request_ = request;
  // a follower plays no audio: the group's session carries its sound (multiroom P4)
  following_ = !request.followGroupId.empty() && request.purpose == cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT;
  route_ = audioRouteFor(mpvConfig_.ao, following_ ? cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_AMBIENT : request.purpose);
  followPaused_ = following_;
  follower_.reset();
  followLoggedMs_ = nowMs();
  // a follower's steering is the last item's alone
  if (speed_ != 1) setDouble(mpv_, "speed", 1.0);
  userPaused_ = false;
  firstFrame_ = false;
  ignoreEndUpTo_ = maxEntry_;
  flags_ = MpvFlags{true, false, false, false, false, false};
  timePos_ = static_cast<double>(request.startMs) / 1000.0;
  timePosAtMs_ = nowMs();
  // a seek of the file before is nobody's now; its answers may still come, and are counted off to none
  seekPending_ = false;
  seekReplies_ = 0;
  duration_ = 0;
  cached_ = 0;
  mpvTracks_.clear();
  cacheSpeed_.reset();
  speedAskedMs_ = 0;
  statsAskedMs_ = nowMs();

  state_ = PlayerState{};
  state_.sessionId = request.sessionId;
  state_.purpose = request.purpose;
  state_.audioRoute = route_.route;
  state_.audioControls = route_.controls;
  state_.controlLatencyMs = route_.controlLatencyMs;
  state_.positionMs = request.startMs;
  state_.positionAtMs = nowMs();

  // the volume first (0 while gated), then the file, paused if the gate holds it (rule 7)
  lastVolume_ = -1;
  applyVolume(nowMs());
  // a follower waits paused for its controller to place it
  const bool paused = gateHolds() || following_;
  const int err = runCommand(mpv_, kLoad, loadfileCommand(request, paused, route_, settingsLoad_));
  if (err < 0) {
    CADENCE_LOG(Warn, "player_load_failed", {{"code", err}});
    state_.state = cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE;
    state_.errorCode = "failed";
    update(nowMs());
    return "failed";
  }
  CADENCE_LOG(Info, "player_load", {{"session", ipc::Token(request.sessionId)},
                                    {"purpose", static_cast<int>(request.purpose)},
                                    {"route", static_cast<int>(route_.route)},
                                    {"gated", paused},
                                    {"start_ms", request.startMs},
                                    {"headers", request.headers.size()},
                                    {"subtitles", request.subtitles.size()},
                                    {"split_audio", !request.audioUrl.empty()},
                                    {"follow", following_}});
  update(nowMs());
  return "";
}

Result MpvPlayer::command(const Command& c) {
  if (state_.sessionId.empty() || !flags_.hasSession) return "invalid";
  switch (c.action) {
    case CommandAction::Pause:
      userPaused_ = true;
      applyPause();
      break;
    case CommandAction::Resume:
      userPaused_ = false;
      applyPause();
      if (gateHolds()) {
        update(nowMs());
        return "gated";
      }
      break;
    case CommandAction::Stop: {
      runCommand(mpv_, kStop, {"stop"});
      flags_.ended = true;
      break;
    }
    case CommandAction::Seek: {
      // from where playback is (or the target of the seek still under way, so rapid presses add up), never from a
      // position the ui read earlier; exact, so the bar's target is where the picture lands
      double target = c.relative ? timePos_ + static_cast<double>(c.deltaMs) / 1000.0 : static_cast<double>(c.positionMs) / 1000.0;
      if (duration_ > 0) target = std::min(target, duration_);
      target = std::max(0.0, target);
      const std::vector<std::string> args =
          c.relative ? std::vector<std::string>{"seek", std::to_string(target - timePos_), "relative+exact"}
                     : std::vector<std::string>{"seek", std::to_string(target), "absolute+exact"};
      if (runCommand(mpv_, kSeek, args) < 0) return "failed";
      ++seekReplies_;
      ++seekSeq_;
      seekPending_ = true;
      timePos_ = target;
      timePosAtMs_ = nowMs();
      break;
    }
    case CommandAction::SelectTrack: {
      const auto ref = trackSelection(c.trackId, c.trackKind);
      if (!ref) return "invalid";
      setString(mpv_, ref->property, ref->value);
      break;
    }
  }
  update(nowMs());
  return "";
}

void MpvPlayer::setGate(bool open) {
  if (audio_.gateOpen() == open) return;
  audio_.setGate(open);
  CADENCE_LOG(Info, "player_gate", {{"open", open}});
  // shut: silent at once, then paused; open: the level first, then the pause the user chose
  if (!open) {
    applyVolume(nowMs());
    applyPause();
  } else {
    applyPause();
    applyVolume(nowMs());
  }
  if (!state_.sessionId.empty()) update(nowMs());
}

Result MpvPlayer::setLevel(std::uint32_t level, std::uint32_t rampMs) {
  if (mpvConfig_.ao == AoMode::Cadence) return "unsupported";
  if (level > 100) return "invalid";
  audio_.setLevel(level, rampMs, nowMs());
  applyVolume(nowMs());
  return "";
}

Result MpvPlayer::setDuck(bool active, float level, std::uint32_t rampMs) {
  if (mpvConfig_.ao == AoMode::Cadence) return "unsupported";
  audio_.setDuck(active, level, rampMs, nowMs());
  applyVolume(nowMs());
  return "";
}

Result MpvPlayer::setMute(bool muted) {
  if (mpvConfig_.ao == AoMode::Cadence) return "unsupported";
  audio_.setMute(muted);
  applyVolume(nowMs());
  return "";
}

Result MpvPlayer::setEq(const EqRequest& eq) {
  if (mpvConfig_.ao == AoMode::Cadence) return "unsupported";
  const auto filter = eqFilter(eq);
  if (!filter) return "invalid";
  // a missing label is fine: there was no eq before
  runCommand(mpv_, kAfRemove, {"af", "remove", std::string("@") + kEqLabel});
  if (!filter->empty() && runCommand(mpv_, kAfAdd, {"af", "add", *filter}) < 0) return "failed";
  return "";
}

bool MpvPlayer::applyMpvConfig(std::string_view json) {
  if (mpv_ == nullptr) return false;
  const auto settings = parseMpvSettings(json, mpvConfig_.videoPath);
  // the config before it goes whole: its options answered, its load options replaced. the reply counter runs on, so
  // a set of the config before cannot answer for one of this one
  settingKeys_.clear();
  settingsLoad_ = nextLoadOptions(settings);
  for (const auto& key : settings.refused) {
    if (key.empty()) continue;
    SettingKey k;
    k.key = key;
    k.refusedKey = true;
    settingKeys_.push_back(std::move(k));
  }
  for (const auto& e : settings.entries) {
    SettingKey k;
    k.key = e.key;
    for (const auto& o : e.options) {
      SettingOpt opt;
      opt.option = o.option;
      opt.nextLoad = o.nextLoad;
      if (!o.nextLoad) {
        // setString's own reply id is kSet; a setting's answer must name its option, so the set goes out here
        opt.reply = kSettingReplyBase + settingsReplies_++;
        const std::string property = "options/" + o.option;
        const char* value = o.value.c_str();
        mpv_set_property_async(mpv_, opt.reply, property.c_str(), MPV_FORMAT_STRING, &value);
      }
      k.opts.push_back(std::move(opt));
    }
    settingKeys_.push_back(std::move(k));
  }
  CADENCE_LOG(Info, "mpv_settings", {{"keys", settingKeys_.size()},
                                     {"next_load", settingsLoad_.size()},
                                     {"live", settingsReplies_}});
  reportSettings(true);
  return true;
}

// the answer to one live option set: applied, or refused with mpv's code (a key the state then names)
void MpvPlayer::settingReply(std::uint64_t reply, int error) {
  for (auto& k : settingKeys_) {
    for (auto& o : k.opts) {
      if (o.reply != reply || o.reply < kSettingReplyBase) continue;
      o.status = error >= 0 ? 1 : 2;
      if (error < 0) {
        CADENCE_LOG(Warn, "mpv_setting_refused", {{"option", ipc::Token(o.option)}, {"code", error}});
      }
      reportSettings();
      return;
    }
  }
}

// the keys by their options' worst status: refused over pending over applied (a key without options is done, as
// "profile":"default" is). sent when one moved
void MpvPlayer::reportSettings(bool force) {
  MpvKeyState st;
  for (const auto& k : settingKeys_) {
    if (k.refusedKey) {
      st.refused.push_back(k.key);
      continue;
    }
    int status = 1;
    for (const auto& o : k.opts) {
      if (o.status == 2) {
        status = 2;
        break;
      }
      if (o.status == 0) status = 0;
    }
    if (status == 0) st.pending.push_back(k.key);
    else if (status == 2) st.refused.push_back(k.key);
    else st.applied.push_back(k.key);
  }
  if (!force && st.applied == settingsReport_.applied && st.pending == settingsReport_.pending &&
      st.refused == settingsReport_.refused) {
    return;
  }
  settingsReport_ = std::move(st);
  if (settingsListener_) settingsListener_(settingsReport_);
}

void MpvPlayer::pump(std::int64_t now) {
  lastPumpMs_ = now;
  if (mpv_ == nullptr) return;
  for (;;) {
    mpv_event* event = mpv_wait_event(mpv_, 0);
    if (event == nullptr || event->event_id == MPV_EVENT_NONE) break;
    handle(*event);
  }
  if (audio_.ramping(now) || lastVolume_ < 0) applyVolume(now);
  if (following()) follow(nowMs());
  update(nowMs());
  askStats(now);
}

void MpvPlayer::follow(std::int64_t now) {
  if (mpv_ == nullptr || !flags_.hasSession || flags_.ended) return;
  FollowReading reading;
  reading.seeking = seekPending_ || flags_.seeking;
  if (flags_.loaded) {
    const auto sample = positionSample();
    const auto since = std::clamp<std::int64_t>(now - sample.atMs, 0, 1000);
    reading.positionMs = static_cast<double>(sample.positionMs) + static_cast<double>(since) * sample.rate;
  }
  const auto action = follower_.step(monoNowNs_(), reading);
  if (action.pause && *action.pause != followPaused_) {
    followPaused_ = *action.pause;
    applyPause();
  }
  if (action.speed) setDouble(mpv_, "speed", *action.speed);
  if (action.seekMs) {
    Command seek;
    seek.action = CommandAction::Seek;
    seek.positionMs = static_cast<std::uint64_t>(std::llround(*action.seekMs));
    command(seek);
  }
  if (now - followLoggedMs_ >= 10000) {
    followLoggedMs_ = now;
    const auto stats = follower_.stats();
    CADENCE_LOG(Info, "video_follow", {{"error_ms", static_cast<std::int64_t>(std::llround(stats.errorMs))},
                                       {"speed", stats.speed},
                                       {"seeks", static_cast<std::int64_t>(stats.seeks)}});
  }
}

void MpvPlayer::askStats(std::int64_t now) {
  if (mpv_ == nullptr || state_.sessionId.empty() || !flags_.hasSession ||
      state_.purpose != cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT) {
    cacheSpeed_.reset();
    return;
  }
  if (now - speedAskedMs_ >= 1000) {
    speedAskedMs_ = now;
    mpv_get_property_async(mpv_, kStatSpeed, "cache-speed", MPV_FORMAT_INT64);
  }
  const bool running = state_.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING ||
                       state_.state == cadence_v2_PlaybackState_PLAYBACK_STATE_BUFFERING;
  if (!running || statsPending_ || now - statsAskedMs_ < statsEveryMs_) return;
  statsAskedMs_ = now;
  statsPending_ = true;
  pending_ = PlayerStats{};
  pending_.sessionId = state_.sessionId;
  pending_.bufferedMs = toMs(cached_);
  mpv_get_property_async(mpv_, kStatsSpeed, "cache-speed", MPV_FORMAT_INT64);
  mpv_get_property_async(mpv_, kStatsCache, "demuxer-cache-state", MPV_FORMAT_NODE);
  mpv_get_property_async(mpv_, kStatsDrops, "frame-drop-count", MPV_FORMAT_INT64);
  mpv_get_property_async(mpv_, kStatsDecoderDrops, "decoder-frame-drop-count", MPV_FORMAT_INT64);
  mpv_get_property_async(mpv_, kStatsDelayed, "vo-delayed-frame-count", MPV_FORMAT_INT64);
  mpv_get_property_async(mpv_, kStatsVfFps, "estimated-vf-fps", MPV_FORMAT_DOUBLE);
  mpv_get_property_async(mpv_, kStatsDisplayFps, "display-fps", MPV_FORMAT_DOUBLE);
  mpv_get_property_async(mpv_, kStatsAvsync, "avsync", MPV_FORMAT_DOUBLE);
  mpv_get_property_async(mpv_, kStatsHwdec, "hwdec-current", MPV_FORMAT_STRING);
}

// a property mpv lacks now (no video, no display fps on the embed path) answers an error: its figure stays 0
void MpvPlayer::statReply(const mpv_event& e) {
  const auto* p = static_cast<const mpv_event_property*>(e.data);
  const bool ok = e.error >= 0 && p != nullptr && p->data != nullptr;
  const auto asInt = [&]() -> std::int64_t {
    return ok && p->format == MPV_FORMAT_INT64 ? *static_cast<std::int64_t*>(p->data) : 0;
  };
  const auto asDouble = [&]() -> double {
    return ok && p->format == MPV_FORMAT_DOUBLE ? finite(*static_cast<double*>(p->data)) : 0.0;
  };
  switch (e.reply_userdata) {
    case kStatSpeed:
      if (ok && p->format == MPV_FORMAT_INT64 && !state_.sessionId.empty()) cacheSpeed_ = countOf(asInt());
      return;
    case kStatsSpeed: pending_.cacheSpeedBps = countOf(asInt()); return;
    case kStatsCache:
      if (ok && p->format == MPV_FORMAT_NODE) readCacheState(*static_cast<mpv_node*>(p->data), pending_);
      return;
    case kStatsDrops: pending_.frameDrops = countOf(asInt()); return;
    case kStatsDecoderDrops: pending_.decoderDrops = countOf(asInt()); return;
    case kStatsDelayed: pending_.voDelayedFrames = countOf(asInt()); return;
    case kStatsVfFps: pending_.estimatedVfFps = asDouble(); return;
    case kStatsDisplayFps: pending_.displayFps = asDouble(); return;
    case kStatsAvsync: pending_.avsyncMs = asDouble() * 1000.0; return;
    case kStatsHwdec: {
      const char* name = ok && p->format == MPV_FORMAT_STRING ? *static_cast<char**>(p->data) : nullptr;
      pending_.hwdec = name != nullptr ? name : "";
      statsPending_ = false;
      const auto& st = pending_;
      CADENCE_LOG(Info, "player_stats", {{"session", ipc::Token(st.sessionId)},
                                         {"buffered_ms", st.bufferedMs},
                                         {"speed_bps", st.cacheSpeedBps},
                                         {"input_bps", st.inputRateBps},
                                         {"ahead_bytes", st.cacheAheadBytes},
                                         {"cache_bytes", st.cacheTotalBytes},
                                         {"underrun", st.underrun},
                                         {"low_seeks", st.lowLevelSeeks},
                                         {"byte_seeks", st.byteLevelSeeks},
                                         {"drops", st.frameDrops},
                                         {"decoder_drops", st.decoderDrops},
                                         {"delayed", st.voDelayedFrames},
                                         {"vf_fps", st.estimatedVfFps},
                                         {"display_fps", st.displayFps},
                                         {"avsync_ms", st.avsyncMs},
                                         {"hwdec", ipc::Token(st.hwdec.empty() ? "unknown" : st.hwdec)}});
      if (statsListener_) statsListener_(st);
      return;
    }
    default: return;
  }
}

void MpvPlayer::handle(const mpv_event& e) {
  switch (e.event_id) {
    case MPV_EVENT_LOG_MESSAGE: {
      const auto* m = static_cast<const mpv_event_log_message*>(e.data);
      const std::string line = std::string(m->prefix) + ": " + m->text;
      ipc::Log::get().thirdParty("mpv", levelOf(m->log_level), line);
      break;
    }
    case MPV_EVENT_START_FILE: {
      const auto* s = static_cast<const mpv_event_start_file*>(e.data);
      if (s->playlist_entry_id > maxEntry_) maxEntry_ = s->playlist_entry_id;
      break;
    }
    case MPV_EVENT_FILE_LOADED:
      onFileLoaded();
      break;
    case MPV_EVENT_PLAYBACK_RESTART:
      flags_.loaded = true;
      // the newest seek landed: mpv answered every seek command, and this restart follows the last answer
      if (seekPending_ && seekReplies_ == 0) seekPending_ = false;
      break;
    case MPV_EVENT_END_FILE:
      onEndFile(*static_cast<const mpv_event_end_file*>(e.data));
      break;
    case MPV_EVENT_GET_PROPERTY_REPLY:
      statReply(e);
      break;
    case MPV_EVENT_COMMAND_REPLY:
    case MPV_EVENT_SET_PROPERTY_REPLY:
      if (e.reply_userdata == kSeek && e.event_id == MPV_EVENT_COMMAND_REPLY && seekReplies_ > 0) {
        --seekReplies_;
        // a refused seek lands nowhere: what mpv reads next is where playback is
        if (e.error < 0 && seekReplies_ == 0) seekPending_ = false;
      }
      if (e.reply_userdata >= kSettingReplyBase && e.event_id == MPV_EVENT_SET_PROPERTY_REPLY) {
        settingReply(e.reply_userdata, e.error);
        break;
      }
      if (e.error < 0) {
        // a refused remove is an eq that was never set; anything else is worth a line
        if (e.reply_userdata != kAfRemove) {
          CADENCE_LOG(Warn, "player_command_failed", {{"command", replyName(e.reply_userdata)}, {"code", e.error}});
        }
        if (e.reply_userdata == kLoad && !state_.sessionId.empty()) {
          state_.errorCode = "failed";
          flags_.ended = true;
        }
      }
      break;
    case MPV_EVENT_PROPERTY_CHANGE: {
      const auto* p = static_cast<const mpv_event_property*>(e.data);
      const bool has = p->format != MPV_FORMAT_NONE && p->data != nullptr;
      if (has && p->format == MPV_FORMAT_DOUBLE) props_[p->name] = *static_cast<double*>(p->data);
      if (has && p->format == MPV_FORMAT_FLAG) props_[p->name] = *static_cast<int*>(p->data) != 0 ? 1.0 : 0.0;
      if (!has) props_.erase(p->name);
      const auto asFlag = [&] { return has && *static_cast<int*>(p->data) != 0; };
      const auto asDouble = [&] { return has ? *static_cast<double*>(p->data) : 0.0; };
      switch (e.reply_userdata) {
        case kPause: flags_.paused = asFlag(); break;
        case kPausedForCache: flags_.pausedForCache = asFlag(); break;
        case kSeeking: flags_.seeking = asFlag(); break;
        case kTimePos:
          // read before the seek under way landed: stale
          if (has && !seekPending_) {
            timePos_ = asDouble();
            timePosAtMs_ = nowMs();
          }
          break;
        case kDuration: duration_ = asDouble(); break;
        case kCacheDuration: cached_ = asDouble(); break;
        case kSpeed: if (has) speed_ = asDouble(); break;
        case kTrackList:
          if (has) mpvTracks_ = parseTrackList(*static_cast<mpv_node*>(p->data));
          break;
        case kVideoParams: {
          std::optional<VideoContent> content;
          if (has && p->format == MPV_FORMAT_NODE) content = parseVideoParams(*static_cast<mpv_node*>(p->data));
          if (output_) output_->contentChanged(content);
          break;
        }
        case kSubText: {
          // third-party text: shown, never logged
          const char* text = has && p->format == MPV_FORMAT_STRING ? *static_cast<char**>(p->data) : nullptr;
          std::string next = text != nullptr ? text : "";
          if (next != subText_) {
            subText_ = std::move(next);
            if (subListener_) subListener_(subText_);
          }
          break;
        }
        case kCurrentAo: {
          const char* ao = has && p->format == MPV_FORMAT_STRING ? *static_cast<char**>(p->data) : nullptr;
          std::string next = ao != nullptr ? ao : "";
          if (next != currentAo_) {
            currentAo_ = std::move(next);
            // an ao name is mpv's own token; "none" when it has none open
            CADENCE_LOG(Info, "player_ao", {{"ao", ipc::Token(currentAo_.empty() ? "none" : currentAo_)},
                                            {"ao_cadence", currentAo_ == "cadence"}});
          }
          break;
        }
        case kAudioParams:
        case kAudioOutParams: {
          const auto params =
              has && p->format == MPV_FORMAT_NODE ? parseAudioParams(*static_cast<mpv_node*>(p->data)) : MpvAudioParams{};
          auto& slot = e.reply_userdata == kAudioParams ? audioParams_ : audioOutParams_;
          if (params == slot) break;
          slot = params;
          if (audioParams_.rate == 0 && audioOutParams_.rate == 0) break;
          CADENCE_LOG(Info, "player_audio_params",
                      {{"rate", audioParams_.rate},
                       {"channels", audioParams_.channels},
                       {"format", ipc::Token(audioParams_.format.empty() ? "none" : audioParams_.format)},
                       {"out_rate", audioOutParams_.rate},
                       {"out_channels", audioOutParams_.channels},
                       {"out_format", ipc::Token(audioOutParams_.format.empty() ? "none" : audioOutParams_.format)},
                       {"ao", ipc::Token(currentAo_.empty() ? "none" : currentAo_)}});
          break;
        }
        default: break;
      }
      break;
    }
    default:
      break;
  }
}

bool MpvPlayer::useRing(PcmRing ring) {
  if (mpv_ == nullptr || mpvConfig_.ao != AoMode::Cadence || !mpvConfig_.aoOverride.empty()) return false;
  // the newest two stay open: the one the ao reads until its reload, and the one it reopens on
  rings_.push_back(std::move(ring));
  while (rings_.size() > 2) rings_.erase(rings_.begin());
  const auto& adopted = rings_.back();
  const auto capacity = adopted.header()->capacity;
  for (const auto& [key, value] : ringOptions(adopted.ringFd(), adopted.eventFd(), capacity)) {
    setString(mpv_, ("options/" + key).c_str(), value);
  }
  runCommand(mpv_, kAoReload, {"ao-reload"});
  // ao-reload does nothing without an ao: one that failed to open before this ring came (no fds yet) deselected
  // the audio track, which is selected again so ao_cadence opens on the new fds
  bool retried = false;
  if (!state_.sessionId.empty() && !route_.silent && currentAo_.empty()) {
    if (const auto track = audioTrackToRetry(mpvTracks_)) {
      setString(mpv_, "aid", std::to_string(*track));
      retried = true;
    }
  }
  CADENCE_LOG(Info, "player_ring_adopted", {{"capacity", static_cast<std::uint64_t>(capacity)},
                                            {"ao", ipc::Token(currentAo_.empty() ? "none" : currentAo_)},
                                            {"audio_retried", retried}});
  return true;
}

bool MpvPlayer::waiting() const {
  if (state_.sessionId.empty() || !flags_.hasSession || flags_.ended) return false;
  return !flags_.loaded || flags_.seeking || flags_.pausedForCache;
}

std::optional<std::uint64_t> MpvPlayer::cacheSpeed() const {
  if (state_.sessionId.empty()) return std::nullopt;
  return cacheSpeed_;
}

std::optional<int> MpvPlayer::bufferingPercent() const {
  if (!flags_.pausedForCache) return std::nullopt;
  const auto it = props_.find("cache-buffering-state");
  if (it == props_.end()) return std::nullopt;
  return static_cast<int>(std::lround(std::clamp(it->second, 0.0, 100.0)));
}

void MpvPlayer::onFileLoaded() {
  if (state_.sessionId.empty()) return;
  // the split audio and the server's subtitles join once the file is open; their urls never reach a log
  if (!request_.audioUrl.empty() && !route_.silent) {
    if (runCommand(mpv_, kAudioAdd, {"audio-add", request_.audioUrl, "select"}) < 0) CADENCE_LOG(Warn, "player_audio_add_failed");
  }
  for (const auto& s : request_.subtitles) {
    const std::vector<std::string> args = {"sub-add", s.url, s.isDefault ? "select" : "auto", s.label, s.language};
    if (runCommand(mpv_, kSubAdd, args) < 0) CADENCE_LOG(Warn, "player_sub_add_failed");
  }
  // the file opened over the config's next-load options: mpv took them with it
  bool moved = false;
  for (auto& k : settingKeys_) {
    for (auto& o : k.opts) {
      if (o.nextLoad && o.status == 0) {
        o.status = 1;
        moved = true;
      }
    }
  }
  if (moved) reportSettings();
}

void MpvPlayer::onEndFile(const mpv_event_end_file& end) {
  if (end.playlist_entry_id <= ignoreEndUpTo_ || state_.sessionId.empty()) return;
  flags_.ended = true;
  if (end.reason == MPV_END_FILE_REASON_ERROR) {
    state_.errorCode = endErrorCode(end.error);
    CADENCE_LOG(Warn, "player_failed", {{"session", ipc::Token(state_.sessionId)},
                                        {"code", ipc::Token(state_.errorCode)},
                                        {"mpv_error", end.error}});
  } else {
    CADENCE_LOG(Info, "player_ended", {{"session", ipc::Token(state_.sessionId)}, {"reason", end.reason}});
  }
}

void MpvPlayer::update(std::int64_t now) {
  PlayerState next = state_;
  if (!state_.sessionId.empty()) {
    next.state = playbackStateFor(flags_);
    const bool playing = next.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING;
    next.rate = playing ? static_cast<float>(speed_) : 0.0f;
    if (next.state != cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE) {
      const auto sample = positionSample();
      const auto since = std::clamp<std::int64_t>(now - sample.atMs, 0, 1000);
      next.positionMs = sample.positionMs + static_cast<std::uint64_t>(static_cast<double>(since) * sample.rate);
      next.positionAtMs = now;
      next.durationMs = request_.live ? 0 : toMs(duration_);
      next.bufferedMs = toMs(cached_);
      const auto percent = bufferingPercent();
      next.bufferingPercent = percent ? std::optional<std::uint32_t>(static_cast<std::uint32_t>(*percent)) : std::nullopt;
    } else {
      next.rate = 0;
    }
    next.tracks = toTracks(mpvTracks_);
    next.gated = gateHolds() && next.state != cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE;
    next.ended = flags_.ended;
    next.seekSeq = seekSeq_;
    next.seekPending = seekPending_;
  }
  state_ = next;
  if (worthReporting(reported_, state_)) {
    reported_ = state_;
    if (listener_) listener_(state_);
  }
  // an ended session is reported once (with its error, if any), then there is none
  if (flags_.ended && !state_.sessionId.empty()) {
    flags_ = MpvFlags{};
    firstFrame_ = false;
    userPaused_ = false;
    route_ = AudioRouteInfo{};
    state_ = PlayerState{};
    reported_ = state_;
    if (listener_) listener_(state_);
  }
}

PositionSample MpvPlayer::positionSample() const {
  PositionSample p;
  p.positionMs = toMs(timePos_);
  p.atMs = timePosAtMs_;
  // it runs on only while it plays: not paused, buffering or seeking; the extrapolation is never longer than a
  // second, as mpv sends time-pos every frame
  const bool running = !seekPending_ && playbackStateFor(flags_) == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING;
  p.rate = running ? speed_ : 0.0;
  p.seekSeq = seekSeq_;
  p.seekPending = seekPending_;
  return p;
}

bool MpvPlayer::frameDue() { return output_ && output_->frameDue(); }

bool MpvPlayer::renderFrame() {
  if (!output_ || !output_->render()) return false;
  // a frame before playback restarted can be mpv's black placeholder: the startup hold must not count it
  if (!state_.sessionId.empty() && flags_.loaded) firstFrame_ = true;
  return true;
}

std::optional<render::Texture> MpvPlayer::videoTexture() const {
  if (!output_ || !flags_.hasSession) return std::nullopt;
  return output_->texture();
}

bool MpvPlayer::videoInGl() const {
  if (!output_ || !output_->inGl() || !flags_.hasSession) return false;
  // any real video track: mpv's selection can arrive after the first frame
  for (const auto& t : mpvTracks_) {
    if (t.type == "video" && !t.albumart) return true;
  }
  // before the track list arrives, a session is assumed to have video
  return mpvTracks_.empty();
}

void MpvPlayer::reportSwap() {
  if (output_) output_->reportSwap();
}

std::optional<double> MpvPlayer::doubleProperty(const char* name) const {
  const auto it = props_.find(name);
  if (it == props_.end()) return std::nullopt;
  return it->second;
}

std::optional<bool> MpvPlayer::flagProperty(const char* name) const {
  const auto v = doubleProperty(name);
  if (!v) return std::nullopt;
  return *v != 0;
}

}  // namespace cadence::screen::player
