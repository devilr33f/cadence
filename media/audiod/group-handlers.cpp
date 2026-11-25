#include "audiod/group-handlers.h"

#include <algorithm>
#include <cerrno>
#include <cmath>
#include <chrono>
#include <cstdlib>
#include <cstring>
#include <string_view>
#include <utility>

namespace cadence::audiod {

namespace {

constexpr std::uint16_t kDefaultPort = 7701;
constexpr std::uint32_t kDefaultWaitMs = 3000;
constexpr std::uint32_t kMaxWaitMs = 10000;
constexpr auto kTickEvery = std::chrono::milliseconds(5);
constexpr auto kBindRetryFirst = std::chrono::milliseconds(250);
constexpr auto kBindRetryMax = std::chrono::milliseconds(5000);
constexpr auto kStatusEvery = std::chrono::seconds(5);
// every this many ticks the report is compared with the last one sent
constexpr std::uint64_t kStatesEvery = 20;

template <std::size_t N>
void copyText(char (&out)[N], std::string_view text) {
  const auto n = std::min(text.size(), N - 1);
  std::memcpy(out, text.data(), n);
  out[n] = '\0';
}

template <std::size_t N>
std::string textOf(const char (&field)[N]) {
  return {field, strnlen(field, N)};
}

std::string_view roleToken(cadence_audiod_v1_GroupRole role) {
  switch (role) {
    case cadence_audiod_v1_GroupRole_GROUP_ROLE_LEADER:
      return "leader";
    case cadence_audiod_v1_GroupRole_GROUP_ROLE_MEMBER:
      return "member";
    default:
      return "none";
  }
}

sync::GroupChannelMap toChannel(cadence_audiod_v1_GroupChannel channel) {
  switch (channel) {
    case cadence_audiod_v1_GroupChannel_GROUP_CHANNEL_LEFT:
      return sync::GroupChannelMap::Left;
    case cadence_audiod_v1_GroupChannel_GROUP_CHANNEL_RIGHT:
      return sync::GroupChannelMap::Right;
    case cadence_audiod_v1_GroupChannel_GROUP_CHANNEL_MONO:
      return sync::GroupChannelMap::Mono;
    default:
      return sync::GroupChannelMap::Both;
  }
}

cadence_audiod_v1_GroupState toWire(sync::GroupRunState state) {
  switch (state) {
    case sync::GroupRunState::Off:
      return cadence_audiod_v1_GroupState_GROUP_STATE_OFF;
    case sync::GroupRunState::Idle:
      return cadence_audiod_v1_GroupState_GROUP_STATE_IDLE;
    case sync::GroupRunState::Connecting:
      return cadence_audiod_v1_GroupState_GROUP_STATE_CONNECTING;
    case sync::GroupRunState::Syncing:
      return cadence_audiod_v1_GroupState_GROUP_STATE_SYNCING;
    case sync::GroupRunState::Buffering:
      return cadence_audiod_v1_GroupState_GROUP_STATE_BUFFERING;
    case sync::GroupRunState::Playing:
      return cadence_audiod_v1_GroupState_GROUP_STATE_PLAYING;
    case sync::GroupRunState::Rejoining:
      return cadence_audiod_v1_GroupState_GROUP_STATE_REJOINING;
    case sync::GroupRunState::Failed:
      return cadence_audiod_v1_GroupState_GROUP_STATE_FAILED;
  }
  return cadence_audiod_v1_GroupState_GROUP_STATE_UNSPECIFIED;
}

std::optional<MediaAction> toAction(cadence_sync_v1_ControlAction action) {
  switch (action) {
    case cadence_sync_v1_ControlAction_CONTROL_ACTION_PAUSE:
      return MediaAction::Pause;
    case cadence_sync_v1_ControlAction_CONTROL_ACTION_RESUME:
      return MediaAction::Resume;
    case cadence_sync_v1_ControlAction_CONTROL_ACTION_STOP:
      return MediaAction::Stop;
    case cadence_sync_v1_ControlAction_CONTROL_ACTION_NEXT:
      return MediaAction::Next;
    case cadence_sync_v1_ControlAction_CONTROL_ACTION_PREVIOUS:
      return MediaAction::Previous;
    case cadence_sync_v1_ControlAction_CONTROL_ACTION_SEEK:
      return MediaAction::Seek;
    default:
      return std::nullopt;
  }
}

bool sameConfig(const cadence_audiod_v1_GroupConfig& a, const cadence_audiod_v1_GroupConfig& b) {
  if (a.enabled != b.enabled || textOf(a.group_id) != textOf(b.group_id) || a.role != b.role ||
      textOf(a.member_id) != textOf(b.member_id) || textOf(a.leader_host) != textOf(b.leader_host) ||
      a.port != b.port || a.key.size != b.key.size || std::memcmp(a.key.bytes, b.key.bytes, a.key.size) != 0 ||
      a.channel != b.channel || a.latency_ms != b.latency_ms || a.budget_us != b.budget_us ||
      a.bitrate_kbps != b.bitrate_kbps || a.codec_pcm != b.codec_pcm || a.members_count != b.members_count ||
      a.wait_ms != b.wait_ms || a.auto_latency != b.auto_latency || a.auto_bitrate != b.auto_bitrate ||
      a.auto_wait != b.auto_wait || textOf(a.clock_host) != textOf(b.clock_host) || a.clock_master != b.clock_master ||
      a.tracking_enabled != b.tracking_enabled || a.tracking_members_count != b.tracking_members_count)
    return false;
  for (pb_size_t i = 0; i < a.tracking_members_count; ++i)
    if (textOf(a.tracking_members[i]) != textOf(b.tracking_members[i]))
      return false;
  for (pb_size_t i = 0; i < a.members_count; ++i)
    if (textOf(a.members[i]) != textOf(b.members[i]))
      return false;
  return true;
}

// how this device takes part in the group's clock: it serves it (a leader with no clock_host, a member that is the
// master) or dials the master
std::string_view clockToken(const cadence_audiod_v1_GroupConfig& config) {
  if (config.clock_master)
    return "serves";
  if (config.role == cadence_audiod_v1_GroupRole_GROUP_ROLE_LEADER && textOf(config.clock_host).empty())
    return "serves";
  return "dials";
}

// trim_us when set (a calibration's, to the microsecond), else trim_ms
std::int64_t trimNs(const cadence_audiod_v1_GroupConfig& config) {
  if (config.trim_us != 0)
    return static_cast<std::int64_t>(std::clamp(config.trim_us, -500'000, 500'000)) * 1000;
  return static_cast<std::int64_t>(config.trim_ms) * 1'000'000;
}

}  // namespace

sync::SyncClock groupClock() {
  static const sync::SyncClock clock = [] {
    sync::SyncClock c;
#ifdef CADENCE_MEDIA_SIM
    // the sim alone: every container shares one kernel clock, and a test needs devices whose clocks differ
    if (const char* offset = std::getenv("CADENCE_SIM_CLOCK_OFFSET_MS"))
      c.offsetNs = std::strtoll(offset, nullptr, 10) * 1'000'000;
    if (const char* ppm = std::getenv("CADENCE_SIM_CLOCK_PPM"))
      c.ppm = std::strtod(ppm, nullptr);
    c.startNs = sync::monoNs();
#endif
    return c;
  }();
  return clock;
}

void GroupHandlers::SharedPlayhead::store(std::optional<Playhead> playhead) {
  std::lock_guard lock(mutex_);
  playhead_ = playhead;
}

std::optional<Playhead> GroupHandlers::SharedPlayhead::load() const {
  std::lock_guard lock(mutex_);
  return playhead_;
}

GroupHandlers::GroupHandlers(Loop& loop, IpcServer& ipc, const Log& log, Playback& playback, MediaHandlers& media)
    : loop_(loop), ipc_(ipc), log_(log), playback_(playback), media_(media), clock_(groupClock()) {
  ipc_.on(cadence_audiod_v1_Envelope_group_config_tag,
          [this](const Envelope& envelope) { apply(envelope.body.group_config); });
  ipc_.on(cadence_audiod_v1_Envelope_group_calibrate_tag,
          [this](const Envelope& envelope) { calibrate(envelope.body.group_calibrate); });
  // this device's own duck (its wake) reaches the leader, which ors it into the group's
  playback_.onDuck([this](bool active) {
    if (leader_)
      leader_->duckRequest(active);
    if (member_)
      member_->duckRequest(active);
  });
}

GroupHandlers::~GroupHandlers() { teardown("stopping"); }

void GroupHandlers::configure(const Configure& /*configuration*/) { announced_ = false; }

void GroupHandlers::ready() {
  announced_ = true;
  if (config_ || !failed_.empty())
    sendStatus();
}

void GroupHandlers::reset() {
  announced_ = false;
  // cadenced is gone: nothing keeps playing for a group it no longer runs (rule 7)
  teardown("client_gone");
  config_.reset();
  failed_.clear();
}

std::function<void(std::uint32_t, std::uint64_t, std::int64_t)> GroupHandlers::heardProbe() const {
#ifdef CADENCE_MEDIA_SIM
  // the sim alone: when a frame every 5 s was heard, in leader time, so the e2e compares the devices of a group
  return [log = &log_](std::uint32_t epoch, std::uint64_t seq, std::int64_t leaderNs) {
    log->info("group_heard", {{"epoch", epoch}, {"seq", seq}, {"t_ns", leaderNs}});
  };
#else
  return {};
#endif
}

sync::GroupPlayout::PlayheadSource GroupHandlers::playheadSource() {
  return [shared = playhead_] { return shared->load(); };
}

void GroupHandlers::apply(const cadence_audiod_v1_GroupConfig& config) {
  if (config_ && sameConfig(*config_, config)) {
    const auto from = trimNs(*config_);
    const auto to = trimNs(config);
    config_ = config;
    if (leader_)
      leader_->setOutputKind(textOf(config.output_kind));
    else if (member_)
      member_->setOutputKind(textOf(config.output_kind));
    if (from == to)
      return;
    // a trim is where already playing audio lands, not a property of the stream or clock links
    auto mode = sync::GroupTrimMode::Jump;
    if (leader_)
      mode = leader_->setTrim(to);
    else if (member_)
      mode = member_->setTrim(to);
    log_.info("group_trim", {{"from_us", from / 1000},
                             {"to_us", to / 1000},
                             {"mode", mode == sync::GroupTrimMode::Slew ? std::string_view("slew")
                                                                        : std::string_view("jump")}});
    return;
  }
  teardown("reconfigured");
  failed_.clear();
  if (!config.enabled) {
    config_.reset();
    log_.info("group_off");
    sendStatus();
    return;
  }
  config_ = config;
  if (config.key.size < sync::kClockMinKeyBytes || textOf(config.group_id).empty() ||
      textOf(config.member_id).empty()) {
    fail("bad_config");
    return;
  }
  const bool started = config.role == cadence_audiod_v1_GroupRole_GROUP_ROLE_LEADER  ? startLeader(config)
                       : config.role == cadence_audiod_v1_GroupRole_GROUP_ROLE_MEMBER ? startMember(config)
                                                                                      : (fail("bad_config"), false);
  if (!started) {
    if (failed_ == "bind_failed")
      retryBind();
    return;
  }
  announceStarted(config);
}

// the port a leader (or a member that is the clock master) binds can still be held for a moment by the one just stopped (its threads let it go a little
// after stop, a reconfigure starts the next at once): a bind that failed is tried again, 250 ms then doubling to 5 s,
// for as long as the same config stands
void GroupHandlers::retryBind() {
  const auto delay = std::min(kBindRetryFirst * (1U << std::min(bindRetries_, 5U)), kBindRetryMax);
  ++bindRetries_;
  const auto generation = generation_;
  bindRetry_ = loop_.after(delay, [this, generation] {
    bindRetry_.reset();
    if (generation != generation_ || !config_ || failed_ != "bind_failed")
      return;
    failed_.clear();
    const bool leader = config_->role == cadence_audiod_v1_GroupRole_GROUP_ROLE_LEADER;
    if (!(leader ? startLeader(*config_) : startMember(*config_))) {
      if (failed_ == "bind_failed")
        retryBind();
      return;
    }
    log_.info("group_bind_retried", {{"attempts", static_cast<std::uint64_t>(bindRetries_)}});
    announceStarted(*config_);
  });
}

void GroupHandlers::announceStarted(const cadence_audiod_v1_GroupConfig& config) {
  bindRetries_ = 0;
  log_.info("group_started", {{"role", roleToken(config.role)},
                              {"port", config.port == 0 ? kDefaultPort : config.port},
                              {"latency_ms", config.latency_ms},
                              {"members", static_cast<std::uint64_t>(config.members_count)},
                              {"clock", clockToken(config)},
                              {"auto_latency", config.auto_latency ? 1U : 0U},
                              {"auto_bitrate", config.auto_bitrate ? 1U : 0U},
                              {"auto_wait", config.auto_wait ? 1U : 0U},
                              {"tracking", config.tracking_enabled ? 1U : 0U}});
  tick();
  armStatus();
  sendStatus();
}

void GroupHandlers::fail(std::string_view error) {
  failed_ = std::string(error);
  log_.warn("group_failed", {{"error", error}});
  sendStatus();
}

bool GroupHandlers::startLeader(const cadence_audiod_v1_GroupConfig& config) {
  sync::GroupLeaderConfig c;
  c.groupId = textOf(config.group_id);
  c.key.assign(config.key.bytes, config.key.bytes + config.key.size);
  for (pb_size_t i = 0; i < config.members_count; ++i)
    c.members.push_back(textOf(config.members[i]));
  c.port = static_cast<std::uint16_t>(config.port == 0 ? kDefaultPort : config.port);
  c.latencyMs = std::clamp<std::uint32_t>(config.latency_ms, 200, 5000);
  c.waitMs = std::min(config.wait_ms == 0 ? kDefaultWaitMs : config.wait_ms, kMaxWaitMs);
  c.bitrateKbps = config.bitrate_kbps;
  c.pcm = config.codec_pcm;
  c.autoLatency = config.auto_latency;
  c.autoBitrate = config.auto_bitrate;
  c.autoWait = config.auto_wait;
  c.playout.channel = toChannel(config.channel);
  c.playout.trimNs = trimNs(config);
  c.playout.local = clock_;
  c.playout.heard = heardProbe();
  c.memberId = textOf(config.member_id);
  c.outputKind = textOf(config.output_kind);
  c.trackingEnabled = config.tracking_enabled;
  for (pb_size_t i = 0; i < config.tracking_members_count; ++i)
    c.trackingMembers.push_back(textOf(config.tracking_members[i]));
  // another device serves the group clock: the leader dials it, on the group's port (clock_master wins: it serves)
  if (!config.clock_master)
    c.clockHost = textOf(config.clock_host);
  c.budgetUs = config.budget_us == 0 ? 1000 : config.budget_us;
  const auto generation = ++generation_;
  sync::GroupLeaderEvents events;
  events.control = [this, generation](const std::string&, cadence_sync_v1_ControlAction action,
                                      std::uint64_t positionMs) {
    loop_.post([this, generation, action, positionMs] {
      if (generation != generation_ || !leader_)
        return;
      if (const auto a = toAction(action))
        media_.runCommand(*a, positionMs, [this](std::string_view code) {
          log_.info("group_control", {{"code", code.empty() ? std::string_view("ok") : code}});
        });
    });
  };
  events.duck = [this, generation](bool active) {
    loop_.post([this, generation, active] {
      if (generation == generation_)
        playback_.setGroupDuck(active);
    });
  };
  events.changed = [this, generation] {
    loop_.post([this, generation] {
      if (generation == generation_)
        sendStatus();
    });
  };
  events.tuned = [this, generation](const sync::GroupTuned& tuned) {
    loop_.post([this, generation, tuned] {
      if (generation != generation_)
        return;
      log_.info("group_tuned", {{"what", tuned.what}, {"from", tuned.from}, {"to", tuned.to}, {"reason", tuned.reason}});
      sendStatus();
    });
  };
  events.calibration = calibrationHooks(false);
  events.tracked = [this, generation](const sync::calibration::TrackingReport& report) {
    loop_.post([this, generation, report] {
      if (generation == generation_)
        tracked(report);
    });
  };
  events.trackingCycle = [this, generation](std::uint32_t id, std::uint32_t slots, std::uint32_t answers) {
    loop_.post([this, generation, id, slots, answers] {
      if (generation == generation_)
        log_.info("group_tracking", {{"id", id}, {"slots", slots}, {"answers", answers}});
    });
  };
  auto made = sync::GroupLeader::start(feed_, playback_.music(), playheadSource(), c, std::move(events));
  if (!made.ok()) {
    // a clock master's address that does not resolve is no port held: nothing to try again
    if (made.error().code == EHOSTUNREACH) {
      fail("unreachable");
      return false;
    }
    log_.warn("group_bind_failed", {{"errno", made.error().code}});
    fail("bind_failed");
    return false;
  }
  leader_ = std::move(made.value());
  LeaderFeed::Hooks feedHooks;
  feedHooks.pumping = [this](bool on) {
    log_.info("group_ring_pump", {{"state", on ? "on" : "off"}});
  };
  feedHooks.flushed = [this, generation] {
    loop_.post([this, generation] {
      if (generation == generation_)
        log_.info("group_ring_flushed");
    });
  };
  feedHooks.started = [this, generation](std::int64_t delayNs) {
    loop_.post([this, generation, delayNs] {
      if (generation == generation_)
        log_.info("group_ring_started", {{"delay_ms", delayNs / 1'000'000}});
    });
  };
  leaderFeed_ = std::make_unique<LeaderFeed>(*leader_, feed_, std::move(feedHooks));
  leaderFeed_->setSessionPosition(media_.sessions().session().positionMs);
  // a screen's ring is pumped into the feed once its output opens there
  media_.externals().useGroupFeed(leaderFeed_.get());
  // whatever source plays now feeds the group
  media_.sessions().retarget(feed_);
  MediaHandlers::GroupHooks hooks;
  hooks.command = [this](MediaAction action, std::uint64_t positionMs) -> std::optional<std::uint64_t> {
    if (!leader_)
      return std::nullopt;
    // an external session's player keeps its own position: the group's media position is no seek target for it
    const bool external = !media_.sessions().session().externalId.empty();
    switch (action) {
      case MediaAction::Pause: {
        const auto heard = leader_->pause();
        return external ? std::nullopt : std::optional(heard);
      }
      case MediaAction::Seek:
        leader_->flush(positionMs);
        // the player's ring flush that follows goes there too
        if (external)
          leaderFeed_->setSeek(positionMs);
        return std::nullopt;
      case MediaAction::Stop:
      case MediaAction::Next:
      case MediaAction::Previous:
        leader_->flush();
        return std::nullopt;
      case MediaAction::Resume:
        leader_->resume();
        return std::nullopt;
    }
    return std::nullopt;
  };
  hooks.play = [this](std::uint64_t startMs) {
    if (!leader_)
      return;
    leader_->flush(startMs);
    // the new session's track is this play's, not a skip of the source's own
    track_.reset();
  };
  hooks.heardMs = [this]() -> std::optional<std::uint64_t> {
    // an external session's position is its player's own, which the group's counts nothing of
    if (!leader_ || !media_.sessions().session().externalId.empty())
      return std::nullopt;
    return leader_->heardMediaMs();
  };
  hooks.published = [this](const MediaSession& session) { return leaderPublished(session); };
  media_.setGroupHooks(std::move(hooks));
  leaderPublished(media_.sessions().session());
  return true;
}

std::optional<GroupClockSample> GroupHandlers::heardClock(std::int64_t nowMono) const {
  if (leader_)
    return groupClockAt(leader_->playout().heardAt(nowMono), nowMono);
  if (member_)
    return groupClockAt(member_->playout().heardAt(nowMono), nowMono);
  return std::nullopt;
}

bool GroupHandlers::leaderPublished(const MediaSession& session) {
  if (!leader_)
    return true;
  // a file source reads ahead of what is heard by the group's latency and lead: its end, the next play cadenced
  // starts at it and the members' sessions all wait until the last of it was heard
  if (session.id.empty() && leaderPlaying_ && leader_->sounding()) {
    endHeld_ = true;
    return false;
  }
  endHeld_ = false;
  leaderPlaying_ = session.state == PlayState::Playing;
  if (leaderFeed_)
    leaderFeed_->setSessionPosition(session.positionMs);
  // a screen's player says its own position after its ring flushed (a seek of its own) or paused: the next epoch starts
  // there, so a follower screen's picture seeks to where this one plays (multiroom P4)
  if (!session.externalId.empty())
    leader_->setIdlePosition(session.positionMs);
  // a track the source moved to on its own (soloist's next, a session that took over after the last one ended):
  // the group drops the old track's epoch and starts the new one from its position. a play through audiod's command
  // path flushed already, and only names the track it brought
  if (session.state == PlayState::Playing && !session.id.empty()) {
    // a live stream's title (icy) changes as it plays on: only a track with a length is known by its tags
    Track track{session.id, {}, {}, {}, session.durationMs};
    if (session.durationMs > 0) {
      track.title = session.metadata.title;
      track.artist = session.metadata.artist;
      track.album = session.metadata.album;
    }
    if (track_ && *track_ != track) {
      log_.info("group_track_changed");
      leader_->trackChanged(session.positionMs);
    }
    track_ = std::move(track);
  } else if (session.id.empty()) {
    track_.reset();
  }
  cadence_sync_v1_Metadata m = cadence_sync_v1_Metadata_init_zero;
  copyText(m.title, session.metadata.title);
  copyText(m.artist, session.metadata.artist);
  copyText(m.album, session.metadata.album);
  copyText(m.artwork_url, session.metadata.artworkUrl);
  m.duration_ms = session.durationMs;
  m.playing = session.state == PlayState::Playing;
  if (lastMetadata_ && std::memcmp(&*lastMetadata_, &m, sizeof m) == 0)
    return true;
  lastMetadata_ = m;
  leader_->metadata(m);
  return true;
}

bool GroupHandlers::startMember(const cadence_audiod_v1_GroupConfig& config) {
  sync::GroupMemberConfig c;
  c.groupId = textOf(config.group_id);
  c.memberId = textOf(config.member_id);
  c.outputKind = textOf(config.output_kind);
  c.trackingEnabled = config.tracking_enabled;
  c.leaderHost = textOf(config.leader_host);
  c.port = static_cast<std::uint16_t>(config.port == 0 ? kDefaultPort : config.port);
  // the clock master: this member serves the group clock, or dials the device clock_host names
  c.serveClock = config.clock_master;
  if (!config.clock_master)
    c.clockHost = textOf(config.clock_host);
  c.key.assign(config.key.bytes, config.key.bytes + config.key.size);
  c.budgetUs = config.budget_us == 0 ? 1000 : config.budget_us;
  c.playout.channel = toChannel(config.channel);
  c.playout.trimNs = trimNs(config);
  c.playout.local = clock_;
  c.playout.heard = heardProbe();
  // it plays once its GROUP session holds the speaker
  c.active = false;
  if (c.leaderHost.empty()) {
    fail("bad_config");
    return false;
  }
  const auto generation = ++generation_;
  sync::GroupMemberEvents events;
  events.metadata = [this, generation](const cadence_sync_v1_Metadata& metadata) {
    loop_.post([this, generation, metadata] {
      if (generation == generation_)
        memberMetadata(metadata);
    });
  };
  events.duck = [this, generation](bool active) {
    loop_.post([this, generation, active] {
      if (generation == generation_)
        playback_.setGroupDuck(active);
    });
  };
  events.changed = [this, generation] {
    loop_.post([this, generation] {
      if (generation == generation_)
        sendStatus();
    });
  };
  events.calibration = calibrationHooks(true);
  // why the audio or the clock dropped: posted to the loop, which logs it (the playout's thread never writes a line)
  events.rejoined = [this, generation](const sync::GroupRejoinNote& note) {
    loop_.post([this, generation, note] {
      if (generation == generation_)
        logRejoin(note);
    });
  };
  events.clock = [this, generation](const sync::GroupClockNote& note) {
    loop_.post([this, generation, note] {
      if (generation == generation_)
        logClock(note);
    });
  };
  events.summary = [this, generation](const sync::GroupSummary& summary) {
    loop_.post([this, generation, summary] {
      if (generation == generation_)
        logSummary(summary);
    });
  };
  member_ = std::make_unique<sync::GroupMember>(playback_.music(), playheadSource(), std::move(c), std::move(events));
  if (!member_->start()) {
    const bool bind = member_->bindFailed();
    member_.reset();
    if (bind) {
      log_.warn("group_bind_failed", {{"what", "clock"}});
      fail("bind_failed");
    } else {
      fail("unreachable");
    }
    return false;
  }
  return true;
}

void GroupHandlers::logRejoin(const sync::GroupRejoinNote& n) const {
  log_.warn("group_rejoin", {{"reason", sync::causeToken(n.cause)},
                             {"link", n.link.empty() ? std::string_view("none") : std::string_view(n.link)},
                             {"error_us", n.errorUs},
                             {"delay_frames", n.delayFrames},
                             {"delay_min_frames", n.delayMinFrames},
                             {"delay_max_frames", n.delayMaxFrames},
                             {"queue_ms", n.queueMs},
                             {"played_ms", n.playedMs},
                             {"clock_error_us", n.clockErrorUs},
                             {"rejoins", n.rejoins},
                             {"underruns", n.underruns},
                             {"suppressed", n.suppressed}});
}

void GroupHandlers::logClock(const sync::GroupClockNote& n) const {
  const std::initializer_list<LogField> fields{{"reason", sync::causeToken(n.cause)},
                                               {"step_us", n.stepUs},
                                               {"error_us", n.errorUs},
                                               {"budget_us", n.budgetUs},
                                               {"rtt_min_us", n.rttMinUs},
                                               {"rtt_p50_us", n.rttP50Us},
                                               {"samples", n.samples},
                                               {"ppm", n.ppm},
                                               {"suppressed", n.suppressed}};
  if (n.cause == sync::GroupCause::Synced)
    log_.info("group_clock", fields);
  else
    log_.warn("group_clock", fields);
}

void GroupHandlers::logSummary(const sync::GroupSummary& s) const {
  const auto count = [&](sync::GroupCause cause) { return s.counts[static_cast<std::size_t>(cause)]; };
  log_.info("group_summary", {{"period_ms", s.periodMs},
                              {"placement", count(sync::GroupCause::Placement)},
                              {"underrun", count(sync::GroupCause::Underrun)},
                              {"clock_lost", count(sync::GroupCause::ClockLost)},
                              {"clock_step", count(sync::GroupCause::ClockStep)},
                              {"gap", count(sync::GroupCause::Gap)},
                              {"link", count(sync::GroupCause::Link)},
                              {"lost", count(sync::GroupCause::Lost)},
                              {"step", count(sync::GroupCause::Step)},
                              {"unsynced", count(sync::GroupCause::Unsynced)},
                              {"synced", count(sync::GroupCause::Synced)},
                              {"clock_error_p50_us", s.clockErrorP50Us},
                              {"clock_error_max_us", s.clockErrorMaxUs},
                              {"rtt_min_us", s.rttMinUs},
                              {"rtt_p50_us", s.rttP50Us},
                              {"placement_error_max_us", s.placementErrorMaxUs},
                              {"delay_min_frames", s.delayMinFrames},
                              {"delay_max_frames", s.delayMaxFrames}});
}

void GroupHandlers::ensureSource() {
  if (sourceSerial_ || !member_)
    return;
  sourceSerial_ = media_.sessions().add([this](std::shared_ptr<MediaSourceEvents> events) {
    auto source = std::make_unique<GroupSource>(
        std::move(events),
        [this](cadence_sync_v1_ControlAction action, std::uint64_t positionMs) {
          return member_ && member_->control(action, positionMs);
        },
        [this](bool active) {
          if (member_)
            member_->setActive(active);
        },
        [this] {
          // the sessions let it go (its session ended): the next play makes another
          source_ = nullptr;
          sourceSerial_.reset();
        });
    source_ = source.get();
    return source;
  });
}

void GroupHandlers::memberMetadata(const cadence_sync_v1_Metadata& metadata) {
  if (!member_)
    return;
  if (metadata.playing)
    ensureSource();
  if (source_ != nullptr)
    source_->leaderState(metadata);
}

void GroupHandlers::teardown(std::string_view reason) {
  ++generation_;
  if (bindRetry_)
    loop_.cancel(*bindRetry_);
  bindRetry_.reset();
  bindRetries_ = 0;
  if (calibrating_) {
    // the leader goes, and its calibration with it
    const auto id = *calibrating_;
    calibrated(id, sync::refused("failed"));
  }
  for (const auto& id : std::exchange(listenerWaits_, {}))
    calibrated(id, sync::refused("failed"));
  const bool running = leader_ || member_;
  if (tick_)
    loop_.cancel(*tick_);
  tick_.reset();
  if (status_)
    loop_.cancel(*status_);
  status_.reset();
  if (sourceSerial_) {
    // the GROUP session ends; its source lets the member's playout go as it is destroyed
    const auto serial = *sourceSerial_;
    sourceSerial_.reset();
    media_.sessions().remove(serial);
  }
  source_ = nullptr;
  if (member_) {
    member_->stop();
    member_.reset();
  }
  if (leader_) {
    // a ring pump stops reading into the feed before the leader goes; the retarget below opens it on the mixer again
    media_.externals().useGroupFeed(nullptr);
    media_.setGroupHooks({});
    // the leader's playout lets go of the music input before the sources push into it again
    leader_->stop();
    leaderFeed_.reset();
    leader_.reset();
    media_.sessions().retarget(playback_.music());
  }
  lastMetadata_.reset();
  track_.reset();
  threadsLogged_ = false;
  endHeld_ = false;
  leaderPlaying_ = false;
  lastReport_.reset();
  playback_.setGroupDuck(false);
  if (running)
    log_.info("group_stopped", {{"reason", reason}});
}

void GroupHandlers::tick() {
  auto* clock = playback_.clock();
  playhead_->store(clock != nullptr ? clock->playhead() : std::nullopt);
  if (!threadsLogged_)
    logThreads();
  if (endHeld_ && leader_ && !leader_->sounding()) {
    endHeld_ = false;
    media_.republish();
  }
  // a state the threads changed without telling (the leader's own playout started) goes out within 100 ms
  if (++ticks_ % kStatesEvery == 0) {
    if (reportKey() != lastReport_)
      sendStatus();
    // a member's session runs at the position it hears the group at (its screen's progress)
    if (member_ && source_ != nullptr) {
      const auto now =
          std::chrono::duration_cast<std::chrono::nanoseconds>(Loop::Clock::now().time_since_epoch()).count();
      if (const auto heard = heardClock(static_cast<std::int64_t>(now)); heard && heard->playing)
        source_->heard(heard->epoch, static_cast<std::uint64_t>(heard->mediaNs / 1'000'000), heard->monoNs);
    }
  }
  const auto& session = media_.sessions().session();
  gate_->open = playback_.gateOpen();
  gate_->busy = session.state == PlayState::Playing && session.kind != MediaKind::Group;
  tick_ = loop_.after(kTickEvery, [this] {
    tick_.reset();
    tick();
  });
}

void GroupHandlers::logThreads() {
  const sync::GroupThreads* threads = leader_ ? &leader_->threads() : member_ ? &member_->threads() : nullptr;
  if (threads == nullptr)
    return;
  const auto playout = threads->playout.get();
  const auto stream = threads->stream.get();
  const auto clock = threads->clock.get();
  const auto timeline = threads->timeline.get();
  if (!playout || !stream || !clock || (leader_ && !timeline))
    return;
  threadsLogged_ = true;
  // what the scheduler gives the group's threads (android alone raises them, as airplay's rtp_nice and player_nice)
  if (timeline)
    log_.info("group_threads", {{"role", "leader"},
                                {"attempted", playout->attempted ? 1U : 0U},
                                {"playout_nice", playout->nice},
                                {"playout_nice_errno", playout->error},
                                {"timeline_nice", timeline->nice},
                                {"stream_nice", stream->nice},
                                {"clock_nice", clock->nice}});
  else
    log_.info("group_threads", {{"role", "member"},
                                {"attempted", playout->attempted ? 1U : 0U},
                                {"playout_nice", playout->nice},
                                {"playout_nice_errno", playout->error},
                                {"stream_nice", stream->nice},
                                {"clock_nice", clock->nice}});
}

sync::CalibrationHooks GroupHandlers::calibrationHooks(bool member) {
  sync::CalibrationHooks hooks;
  if (record_)
    hooks.record = record_;
  if (recordNative_)
    hooks.recordNative = recordNative_;
  if (cancelNative_)
    hooks.cancelNative = cancelNative_;
  hooks.analysed = [log = &log_](const sync::CalibrationAnalysis& note) {
    log->info("calibration_analysed", {{"id", note.id},
                                       {"ms", note.ms},
                                       {"frames", note.frames},
                                       {"rate", note.rate},
                                       {"channels", note.channels},
                                       {"code", note.code}});
  };
  // on the stream's thread (a member's) or the loop's (the leader's): what the loop saw last
  hooks.gate = [gate = gate_, member]() -> std::string {
    if (!gate->open)
      return "gated";
    if (member && gate->busy)
      return "busy";
    return {};
  };
  return hooks;
}

void GroupHandlers::calibrate(const cadence_audiod_v1_GroupCalibrate& request) {
  const auto id = textOf(request.request_id);
  if (!leader_) {
    calibrated(id, sync::refused("not_leader"));
    return;
  }
  if (request.mode == cadence_audiod_v1_GroupCalibrateMode_GROUP_CALIBRATE_MODE_LISTENER_RESULT) {
    // the speakers' own measure of a listener run: at once when it is known, else once it is
    listenerWaits_.push_back(id);
    leader_->listenerResult(request.run_id, [this, generation = generation_, id](sync::CalibrationOutcome outcome) {
      loop_.post([this, generation, id, outcome = std::move(outcome)] {
        if (generation != generation_)
          return;
        const auto found = std::find(listenerWaits_.begin(), listenerWaits_.end(), id);
        if (found == listenerWaits_.end())
          return;
        listenerWaits_.erase(found);
        calibrated(id, outcome);
      });
    });
    return;
  }
  if (calibrating_) {
    calibrated(id, sync::refused("busy"));
    return;
  }
  sync::CalibrationBoost boost;
  for (pb_size_t i = 0; i < request.boost_count; ++i)
    boost[textOf(request.boost[i].member_id)] =
        std::min<std::uint32_t>(request.boost[i].gain_db, sync::calibration::kMaxGainDb);
  sync::CalibrationPriors priors;
  for (pb_size_t i = 0; i < request.priors_count; ++i) {
    const auto spanUs = request.priors[i].prior_span_us == 0 ? 150'000 : request.priors[i].prior_span_us;
    priors[textOf(request.priors[i].member_id)] = {
        static_cast<std::int64_t>(request.priors[i].prior_us) * 1000,
        static_cast<std::int64_t>(spanUs) * 1000,
    };
  }
  const auto toleranceUs = request.tolerance_us == 0 ? 2000 : request.tolerance_us;
  // the gate as it is now, not as the last tick saw it
  gate_->open = playback_.gateOpen();
  if (request.mode == cadence_audiod_v1_GroupCalibrateMode_GROUP_CALIBRATE_MODE_LISTENER) {
    // the leader answers at once with the schedule, or its refusal; the speakers' own measure comes by listener_result
    log_.info("group_calibrating", {{"mode", "listener"}, {"boosted", static_cast<std::uint64_t>(boost.size())}});
    calibrated(id, leader_->calibrateListener(calibrationHooks(false), boost, toleranceUs, priors));
    return;
  }
  calibrating_ = id;
  log_.info("group_calibrating", {{"mode", "mics"}, {"boosted", static_cast<std::uint64_t>(boost.size())}});
  leader_->calibrate(
      calibrationHooks(false),
      [this, generation = generation_, id](sync::CalibrationOutcome outcome) {
        loop_.post([this, generation, id, outcome = std::move(outcome)] {
          if (generation == generation_ && calibrating_ == id)
            calibrated(id, outcome);
        });
      },
      boost, toleranceUs, priors);
}

void GroupHandlers::calibrated(const std::string& requestId, const sync::CalibrationOutcome& outcome) {
  if (calibrating_ == requestId)
    calibrating_.reset();
  Envelope out = cadence_audiod_v1_Envelope_init_zero;
  out.which_body = cadence_audiod_v1_Envelope_group_calibrated_tag;
  auto& c = out.body.group_calibrated;
  copyText(c.request_id, requestId);
  copyText(c.code, outcome.code);
  c.residual_us = outcome.residualUs;
  for (const auto& m : outcome.members) {
    log_.info("calibration_member",
              {{"member", m.id}, {"code", m.code}, {"answered_ms", m.answeredMs}});
    if (c.members_count >= sizeof c.members / sizeof c.members[0])
      break;
    auto& w = c.members[c.members_count++];
    copyText(w.member_id, m.id);
    copyText(w.code, m.code);
    w.offset_us = m.offsetUs;
    w.confidence = m.confidence;
    w.spread_us = m.spreadUs;
    w.rounds = m.rounds;
    w.mic_channels = m.micChannels;
    w.ci95_us = m.ci95Us;
    copyText(w.ultrasonic, m.ultrasonic);
  }
  c.starts_in_ms = outcome.startsInMs;
  for (const auto& slot : outcome.slots) {
    if (c.slots_count >= sizeof c.slots / sizeof c.slots[0])
      break;
    auto& w = c.slots[c.slots_count++];
    copyText(w.member_id, slot.id);
    w.offset_ms = slot.offsetMs;
    w.round = slot.round;
    w.code = slot.code;
  }
  c.run_id = outcome.runId;
  c.rounds = outcome.rounds;
  if (outcome.chirp) {
    c.has_chirp = true;
    c.chirp.f0_hz = static_cast<std::uint32_t>(std::lround(outcome.chirp->f0Hz));
    c.chirp.f1_hz = static_cast<std::uint32_t>(std::lround(outcome.chirp->f1Hz));
    c.chirp.duration_us = static_cast<std::uint32_t>(std::lround(outcome.chirp->durationS * 1e6));
    c.chirp.fade_us = static_cast<std::uint32_t>(std::lround(outcome.chirp->fadeS * 1e6));
  }
  if (outcome.probe) {
    c.has_probe = true;
    c.probe.kind = outcome.probe->kind == sync::calibration::ProbeKind::Gold
                       ? cadence_audiod_v1_GroupProbeKind_GROUP_PROBE_KIND_GOLD
                       : cadence_audiod_v1_GroupProbeKind_GROUP_PROBE_KIND_UNSPECIFIED;
    c.probe.band_lo_hz = static_cast<std::uint32_t>(std::lround(outcome.probe->bandLoHz));
    c.probe.band_hi_hz = static_cast<std::uint32_t>(std::lround(outcome.probe->bandHiHz));
    c.probe.fade_us = static_cast<std::uint32_t>(std::lround(outcome.probe->fadeS * 1e6));
  }
  log_.info("group_calibrated", {{"code", outcome.code},
                                 {"residual_us", outcome.residualUs},
                                 {"members", static_cast<std::uint64_t>(outcome.members.size())},
                                 {"rounds", outcome.rounds},
                                 {"run_id", outcome.runId}});
  ipc_.send(out);
}

void GroupHandlers::tracked(const sync::calibration::TrackingReport& report) {
  Envelope out = cadence_audiod_v1_Envelope_init_zero;
  out.which_body = cadence_audiod_v1_Envelope_group_tracked_tag;
  if (config_)
    copyText(out.body.group_tracked.group_id, textOf(config_->group_id));
  copyText(out.body.group_tracked.state, report.state);
  std::uint64_t corrections = 0;
  for (const auto& member : report.members) {
    if (out.body.group_tracked.members_count >=
        sizeof out.body.group_tracked.members / sizeof out.body.group_tracked.members[0])
      break;
    auto& wire = out.body.group_tracked.members[out.body.group_tracked.members_count++];
    copyText(wire.member_id, member.id);
    wire.offset_us = member.offsetUs;
    wire.ci95_us = member.ci95Us;
    wire.cycles = member.cycles;
    wire.correction_us = member.correctionUs;
    wire.correction_id = member.correctionId;
    corrections += member.correctionUs != 0 ? 1 : 0;
  }
  log_.info("group_tracked", {{"state", report.state},
                              {"members", static_cast<std::uint64_t>(report.members.size())},
                              {"corrections", corrections}});
  ipc_.send(out);
}

void GroupHandlers::armStatus() {
  status_ = loop_.after(kStatusEvery, [this] {
    status_.reset();
    sendStatus();
    armStatus();
  });
}

std::optional<std::vector<std::uint32_t>> GroupHandlers::reportKey() const {
  std::vector<std::uint32_t> key;
  if (leader_) {
    const auto r = leader_->report();
    key.push_back(static_cast<std::uint32_t>(r.state));
    for (const auto& m : r.members) {
      key.push_back(static_cast<std::uint32_t>(m.state));
      key.push_back(m.connected ? 1U : 0U);
      key.push_back(m.synced ? 1U : 0U);
    }
  } else if (member_) {
    const auto r = member_->report();
    key.push_back(static_cast<std::uint32_t>(r.state));
    key.push_back(r.synced ? 1U : 0U);
    key.push_back(r.connected ? 1U : 0U);
  } else {
    return std::nullopt;
  }
  return key;
}

void GroupHandlers::sendStatus() {
  if (!announced_)
    return;
  lastReport_ = reportKey();
  Envelope out = cadence_audiod_v1_Envelope_init_zero;
  out.which_body = cadence_audiod_v1_Envelope_group_status_tag;
  auto& s = out.body.group_status;
  if (config_) {
    copyText(s.group_id, textOf(config_->group_id));
    s.role = config_->role;
  }
  if (!failed_.empty()) {
    s.state = cadence_audiod_v1_GroupState_GROUP_STATE_FAILED;
    copyText(s.error, failed_);
  } else if (leader_) {
    const auto r = leader_->report();
    s.state = toWire(r.state);
    // its own clock: synced, error 0, while it serves it; the master's fit when another device does
    s.synced = r.synced;
    s.clock_error_us = r.clockErrorUs;
    s.steer_ppm = r.steerPpm;
    s.rejoins = r.rejoins;
    s.underruns = r.underruns;
    s.placement_error_us = r.placementErrorUs;
    s.queue_ms = r.queueMs;
    s.latency_ms = r.latencyMs;
    s.bitrate_kbps = r.bitrateKbps;
    s.wait_ms = r.waitMs;
    for (const auto& m : r.members) {
      if (s.members_count >= sizeof s.members / sizeof s.members[0])
        break;
      auto& w = s.members[s.members_count++];
      copyText(w.member_id, m.id);
      w.state = toWire(m.state);
      w.connected = m.connected;
      w.synced = m.synced;
      w.clock_error_us = m.clockErrorUs;
      w.queue_ms = m.queueMs;
      w.rejoins = m.rejoins;
      w.placement_error_us = m.placementErrorUs;
    }
  } else if (member_) {
    const auto r = member_->report();
    s.state = toWire(r.state);
    copyText(s.error, r.error);
    s.synced = r.synced;
    s.clock_error_us = r.clockErrorUs;
    s.queue_ms = r.queueMs;
    s.steer_ppm = r.steerPpm;
    s.rejoins = r.rejoins;
    s.underruns = r.underruns;
    s.placement_error_us = r.placementErrorUs;
    s.latency_ms = r.latencyMs;
  } else {
    s.state = cadence_audiod_v1_GroupState_GROUP_STATE_OFF;
  }
  ipc_.send(out);
}

}  // namespace cadence::audiod
