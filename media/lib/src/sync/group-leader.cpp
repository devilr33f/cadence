#include "sync/group-leader.h"

#include <algorithm>
#include <cerrno>
#include <chrono>
#include <cmath>
#include <cstring>
#include <sys/socket.h>

namespace cadence::sync {

namespace {

constexpr auto kTick = std::chrono::milliseconds(2);
// a start whose barrier came late still gives the members this long to place the first frame
constexpr std::int64_t kBarrierLeadNs = 200'000'000;
constexpr std::int64_t kPauseLeadNs = 150'000'000;
constexpr std::int64_t kRejoinWindowNs = 60'000'000'000;
constexpr std::size_t kRejoinsToRaise = 3;
constexpr std::uint32_t kRaiseMs = 500;
constexpr std::uint32_t kMaxRaiseMs = 2000;
// a real-time source's lead past the latency is steered by this many ppm a millisecond, up to the bound
constexpr double kFeedPpmPerMs = 10;
constexpr std::int32_t kFeedMaxPpm = 500;
// a feed whose lead stays this far under its ceiling is a real-time source's (a file source keeps it at the ceiling)
constexpr std::int64_t kRealTimeBelowNs = 500'000'000;
constexpr std::int64_t kLocalJoinMarginNs = 100'000'000;
// how often the tuner looks at the members' status (each sends one every 2 s): every this many timeline ticks, ~250 ms
constexpr std::uint64_t kTuneEveryTicks = 125;

GroupTunerConfig tunerConfig(const GroupLeaderConfig& c) {
  GroupTunerConfig t;
  t.autoLatency = c.autoLatency;
  t.autoBitrate = c.autoBitrate && !c.pcm;
  t.autoWait = c.autoWait;
  t.latencyMs = c.latencyMs;
  t.bitrateKbps = c.bitrateKbps;
  t.pcm = c.pcm;
  t.waitMs = c.waitMs;
  return t;
}

GroupTrackerConfig trackerConfig(const GroupLeaderConfig& c) {
  GroupTrackerConfig out;
  out.enabled = c.trackingEnabled;
  out.memberId = c.memberId;
  out.outputKind = c.outputKind;
  out.members = c.trackingMembers;
  out.cyclePeriodNs = c.trackingCycleNs > 0 ? c.trackingCycleNs : 10'000'000'000;
  out.deadbandNs = c.trackingDeadbandNs > 0 ? c.trackingDeadbandNs : c.budgetUs <= 100 ? 250'000 : 1'000'000;
  return out;
}

GroupRunState runState(PlayoutState state) {
  switch (state) {
    case PlayoutState::Playing:
      return GroupRunState::Playing;
    case PlayoutState::Buffering:
      return GroupRunState::Buffering;
    case PlayoutState::Rejoining:
      return GroupRunState::Rejoining;
    case PlayoutState::Syncing:
      return GroupRunState::Syncing;
    case PlayoutState::Idle:
    case PlayoutState::Paused:
      break;
  }
  return GroupRunState::Idle;
}

GroupRunState fromWire(cadence_sync_v1_MemberState state) {
  switch (state) {
    case cadence_sync_v1_MemberState_MEMBER_STATE_SYNCING:
      return GroupRunState::Syncing;
    case cadence_sync_v1_MemberState_MEMBER_STATE_BUFFERING:
      return GroupRunState::Buffering;
    case cadence_sync_v1_MemberState_MEMBER_STATE_PLAYING:
      return GroupRunState::Playing;
    case cadence_sync_v1_MemberState_MEMBER_STATE_REJOINING:
      return GroupRunState::Rejoining;
    default:
      return GroupRunState::Idle;
  }
}

}  // namespace

Result<std::unique_ptr<GroupLeader>, SocketError> GroupLeader::start(PcmInput& feed, PcmInput& out,
                                                                    GroupPlayout::PlayheadSource playhead,
                                                                    GroupLeaderConfig config,
                                                                    GroupLeaderEvents events) {
  std::unique_ptr<GroupLeader> leader(new GroupLeader(feed, out, std::move(playhead), config, std::move(events)));
  auto* self = leader.get();
  if (!config.clockHost.empty()) {
    // another device serves the group clock: the leader fits it as a member does
    const auto master = ClockSocket::resolve(config.clockHost, config.clockPort == 0 ? config.port : config.clockPort);
    if (!master)
      return SocketError{EHOSTUNREACH};
    auto socket = ClockSocket::open(master->addr.ss_family == AF_INET6 ? "::" : "", 0);
    if (!socket.ok())
      return SocketError{socket.error()};
    auto clockConfig = config.clock;
    clockConfig.smoother.syncedBelowNs = static_cast<double>(config.budgetUs) * 1000.0;
    clockConfig.clock = config.playout.local;
    leader->clockClient_ = std::make_unique<ClockClient>(std::move(socket.value()), *master, config.key, clockConfig,
                                                         [self] { self->threads_.clock.raise(kNiceAudioFeed); });
  }
  LeaderStreamConfig streamConfig;
  streamConfig.groupId = config.groupId;
  streamConfig.key = config.key;
  streamConfig.members = config.members;
  streamConfig.latencyMs = config.latencyMs;
  streamConfig.clock = config.playout.local;
  if (leader->clockClient_)
    streamConfig.now = [self] { return self->groupNow(); };
  streamConfig.onThread = [self] { self->threads_.stream.raise(kNiceAudioFeed); };
  LeaderStreamEvents streamEvents;
  streamEvents.connected = [self](const std::string& member) {
    {
      std::lock_guard lock(self->mutex_);
      self->firstJoin_[member] = true;
    }
    if (self->events_.changed)
      self->events_.changed();
  };
  streamEvents.disconnected = [self](const std::string& member, std::string_view) {
    self->memberDuck(member, false);
    if (self->events_.changed)
      self->events_.changed();
  };
  streamEvents.ready = [self](const std::string& member, const cadence_sync_v1_Ready& ready) {
    std::lock_guard lock(self->mutex_);
    self->ready_[member] = ready.epoch;
    if (self->phase_ == Phase::Barrier && ready.epoch == self->epoch_) {
      const auto waited = std::max<std::int64_t>(self->groupNow() - self->startNs_, 0);
      self->tuner_.memberReady(member, static_cast<std::uint32_t>(std::min<std::int64_t>(waited / 1'000'000, 60'000)));
    }
  };
  streamEvents.control = [self](const std::string& member, const cadence_sync_v1_Control& control) {
    if (self->events_.control)
      self->events_.control(member, control.action, control.position_ms);
  };
  streamEvents.duck = [self](const std::string& member, bool active) { self->memberDuck(member, active); };
  streamEvents.joined = [self](const std::string& member, std::int64_t) { self->joined(member); };
  streamEvents.calibrated = [self](const std::string& member, const cadence_sync_v1_CalibrateArrivals& arrivals) {
    self->calibrated(member, arrivals);
  };
  streamEvents.calibrationProgress = [self](const std::string& member,
                                             const cadence_sync_v1_CalibrateProgress& progress) {
    self->calibrationProgress(member, progress);
  };
  streamEvents.tracked = [self](const std::string& member, const cadence_sync_v1_TrackArrivals& arrivals) {
    self->tracked(member, arrivals);
  };
  auto stream = LeaderStream::listen(config.host, config.port, std::move(streamConfig), std::move(streamEvents));
  if (!stream.ok())
    return stream.error();
  leader->stream_ = std::move(stream.value());
  if (leader->clockClient_) {
    leader->clockClient_->start();
  } else {
    // the clock answers on the stream's port number, over udp
    auto socket = ClockSocket::open(config.host, leader->stream_->port());
    if (!socket.ok())
      return SocketError{socket.error()};
    leader->clock_ = std::make_unique<ClockServer>(std::move(socket.value()), config.key,
                                                   [self] { self->threads_.clock.raise(kNiceAudioFeed); });
    leader->clock_->setClock(config.playout.local);
    leader->clock_->start();
  }
  feed.setAlwaysResample(true);
  leader->alive_->self = self;
  leader->timeline_ = std::thread([self] { self->runTimeline(); });
  leader->player_ = std::thread([self] { self->runPlayout(); });
  return leader;
}

GroupLeader::GroupLeader(PcmInput& feed, PcmInput& out, GroupPlayout::PlayheadSource playhead,
                         GroupLeaderConfig config, GroupLeaderEvents events)
    : feed_(feed),
      config_(std::move(config)),
      events_(std::move(events)),
      playout_(out, std::move(playhead), config_.playout),
      encoder_(config_.pcm ? MusicCodec::PcmS16 : MusicCodec::Opus, config_.bitrateKbps),
      latencyMs_(config_.latencyMs),
      tuner_(tunerConfig(config_)),
      tracker_(trackerConfig(config_)) {}

GroupLeader::~GroupLeader() { stop(); }

void GroupLeader::stop() {
  if (stop_.exchange(true))
    return;
  playout_.clearUltrasonic();
  if (events_.calibration.cancelNative)
    events_.calibration.cancelNative();
  {
    std::lock_guard lock(alive_->mutex);
    alive_->self = nullptr;
  }
  worker_.join();
  if (timeline_.joinable())
    timeline_.join();
  if (player_.joinable())
    player_.join();
  if (clock_)
    clock_->stop();
  if (clockClient_)
    clockClient_->stop();
  if (stream_)
    stream_->stop();
  feed_.requestSteer(0);
  feed_.setAlwaysResample(false);
}

std::int64_t GroupLeader::frameT(std::uint64_t seq) const {
  return t0_ + static_cast<std::int64_t>(seq) * kGroupFrameNs;
}

std::optional<ClockMapping> GroupLeader::mapping() const {
  if (!clockClient_) {
    ClockMapping identity;
    identity.synced = true;
    return identity;
  }
  return clockClient_->mapping().load();
}

std::int64_t GroupLeader::toGroup(std::int64_t local) const {
  if (!clockClient_)
    return local;
  if (const auto m = clockClient_->mapping().load()) {
    const auto group = m->toLeader(local);
    heldOffsetNs_.store(group - local, std::memory_order_relaxed);
    return group;
  }
  return local + heldOffsetNs_.load(std::memory_order_relaxed);
}

std::int64_t GroupLeader::fromGroup(std::int64_t group) const {
  if (!clockClient_)
    return group;
  if (const auto m = clockClient_->mapping().load())
    return m->toLocal(group);
  return group - heldOffsetNs_.load(std::memory_order_relaxed);
}

void GroupLeader::setMediaPosition(std::uint64_t ms) {
  std::lock_guard lock(mutex_);
  mediaBaseMs_ = ms;
  holdFeed_ = false;
}

void GroupLeader::setIdlePosition(std::uint64_t ms) {
  std::lock_guard lock(mutex_);
  if (phase_ == Phase::Idle)
    mediaBaseMs_ = ms;
}

void GroupLeader::hold() {
  holdFeed_ = true;
  holdClears_ = feed_.clears();
}

std::uint64_t GroupLeader::heardAt(std::int64_t t) const {
  const auto heard = mediaBaseMs_ + static_cast<std::uint64_t>(std::max<std::int64_t>(t - t0_, 0) / 1'000'000);
  return std::min(heard, mediaBaseMs_ + seq_ * 20);
}

std::uint64_t GroupLeader::pause() {
  std::lock_guard lock(mutex_);
  if (phase_ != Phase::Running) {
    if (phase_ == Phase::Barrier)
      stream_->flush(epoch_ + 1);
    hold();
    phase_ = Phase::Idle;
    return mediaBaseMs_;
  }
  const auto t = groupNow() + kPauseLeadNs;
  stream_->pause(epoch_, t);
  playout_.pause(epoch_, t);
  hold();
  mediaBaseMs_ = heardAt(t);
  endNs_ = std::min(endNs_, t);
  phase_ = Phase::Idle;
  return mediaBaseMs_;
}

void GroupLeader::flush(std::optional<std::uint64_t> positionMs) {
  std::lock_guard lock(mutex_);
  if (positionMs)
    mediaBaseMs_ = *positionMs;
  stream_->flush(epoch_ + 1);
  playout_.flush(epoch_ + 1);
  phase_ = Phase::Idle;
  endNs_ = 0;
  hold();
}

void GroupLeader::trackChanged(std::uint64_t positionMs) {
  std::lock_guard lock(mutex_);
  mediaBaseMs_ = positionMs;
  if (phase_ == Phase::Idle)
    return;
  stream_->flush(epoch_ + 1);
  playout_.flush(epoch_ + 1);
  phase_ = Phase::Idle;
  endNs_ = 0;
}

void GroupLeader::resume() {
  std::lock_guard lock(mutex_);
  holdFeed_ = false;
}

void GroupLeader::metadata(const cadence_sync_v1_Metadata& metadata) { stream_->metadata(metadata); }

void GroupLeader::duckRequest(bool active) {
  std::lock_guard lock(mutex_);
  ownDuck_ = active;
  applyDuck();
}

void GroupLeader::memberDuck(const std::string& member, bool active) {
  std::lock_guard lock(mutex_);
  ducks_[member] = active;
  applyDuck();
}

void GroupLeader::applyDuck() {
  const bool any =
      ownDuck_ || std::any_of(ducks_.begin(), ducks_.end(), [](const auto& entry) { return entry.second; });
  if (any == duck_)
    return;
  duck_ = any;
  stream_->duck(any);
  if (events_.duck)
    events_.duck(any);
}

void GroupLeader::joined(const std::string& member) {
  std::lock_guard lock(mutex_);
  if (auto& first = firstJoin_[member]; first) {
    first = false;
    return;
  }
  // a join while no epoch plays is a member coming back to the group (its session takes the speaker again for the
  // next track, after the last one ended): it missed nothing, so it is no rejoin, and the tuner never sees it
  if (phase_ != Phase::Running)
    return;
  ++rejoins_;
  tuner_.memberRejoined(member, groupNow(), true);
  // the tuner raises the latency itself: the fixed raise after repeated rejoins is auto latency's absence
  if (config_.autoLatency)
    return;
  const auto now = monoNs();
  rejoinTimes_.push_back(now);
  while (!rejoinTimes_.empty() && now - rejoinTimes_.front() > kRejoinWindowNs)
    rejoinTimes_.pop_front();
  if (rejoinTimes_.size() >= kRejoinsToRaise && latencyMs_ + kRaiseMs <= config_.latencyMs + kMaxRaiseMs) {
    raise_ = true;
    rejoinTimes_.clear();
  }
}

void GroupLeader::setActive(bool active) { active_ = active; }

void GroupLeader::setOutputKind(std::string kind) {
  std::lock_guard lock(mutex_);
  config_.outputKind = std::move(kind);
  tracker_.setOutputKind(config_.outputKind);
}

void GroupLeader::setReadAhead(std::optional<std::int64_t> aheadNs) {
  std::lock_guard lock(mutex_);
  readAheadNs_ = aheadNs;
}

std::optional<std::int64_t> GroupLeader::feedHeardNs(std::uint64_t pos) const {
  std::int64_t t = 0;
  {
    std::lock_guard lock(mutex_);
    if (phase_ != Phase::Running || !epochFeedKnown_ || pos < epochFeedPos_)
      return std::nullopt;
    t = t0_ + static_cast<std::int64_t>((pos - epochFeedPos_) * 1'000'000'000ULL / kGroupRate);
  }
  return config_.playout.local.toMono(fromGroup(t));
}

std::int64_t GroupLeader::feedLeadNs() const {
  std::int64_t latency = 0;
  {
    std::lock_guard lock(mutex_);
    latency = static_cast<std::int64_t>(latencyMs_) * 1'000'000;
  }
  if (const auto heard = feedHeardNs(feed_.readPosition()))
    return *heard - monoNs();
  return latency;
}

bool GroupLeader::sounding() const {
  std::lock_guard lock(mutex_);
  return groupNow() < endNs_;
}

calibration::Plan GroupLeader::planFor(std::int64_t startNs, std::int64_t spacingNs, const CalibrationBoost& boost,
                                       std::vector<std::string>* offline, std::uint32_t toleranceUs,
                                       const CalibrationPriors& priors, bool ultrasonic) const {
  std::vector<std::string> ids{config_.memberId};
  const auto views = stream_->members();
  // mixed groups keep the legacy plan: a member before version 2 cannot place or identify coded probes
  const bool coded = std::all_of(views.begin(), views.end(), [](const LeaderMemberView& view) {
    return !view.connected || view.calibrationVersion >= kCalibrationVersion;
  });
  for (const auto& view : views) {
    if (view.connected && ids.size() < calibration::kMaxMembers)
      ids.push_back(view.id);
    else if (offline != nullptr)
      offline->push_back(view.id);
  }
  std::vector<std::int64_t> priorNs(ids.size(), 0), priorSpanNs(ids.size(), 150'000'000);
  for (std::size_t k = 0; k < ids.size(); ++k) {
    if (const auto found = priors.find(ids[k]); found != priors.end()) {
      priorNs[k] = found->second.priorNs;
      priorSpanNs[k] = found->second.priorSpanNs;
    }
  }
  auto plan = coded ? calibration::makeProbePlan(ids, startNs, priorNs, priorSpanNs, spacingNs, config_.calibrationRounds)
                    : calibration::makePlan(ids, startNs, spacingNs, config_.calibrationRounds);
  plan.toleranceNs = static_cast<std::int64_t>(toleranceUs == 0 ? 2000 : toleranceUs) * 1000;
  plan.gainDb.assign(plan.slotNs.size(), 0);
  for (std::size_t k = 0; k < plan.slotNs.size(); ++k) {
    const auto found = boost.find(plan.members[k % plan.members.size()]);
    if (found != boost.end())
      plan.gainDb[k] = std::min(found->second, calibration::kMaxGainDb);
  }
  const bool capable = ultrasonic && std::all_of(views.begin(), views.end(), [](const LeaderMemberView& view) {
    return !view.connected || view.calibrationVersion >= kTrackingVersion;
  });
  if (capable) {
    std::vector<bool> skipped(ids.size(), false);
    skipped[0] = config_.outputKind == "bluetooth";
    for (const auto& view : views) {
      const auto found = std::find(ids.begin(), ids.end(), view.id);
      if (found != ids.end() && view.status && std::string_view(view.status->output_kind) == "bluetooth")
        skipped[static_cast<std::size_t>(found - ids.begin())] = true;
    }
    const auto spacing = plan.slotNs.size() > 1 ? plan.slotNs[1] - plan.slotNs[0] : spacingNs;
    calibration::addUltrasonicRound(plan, plan.slotNs.back() + spacing, spacing, std::move(skipped));
  }
  return plan;
}

void GroupLeader::recordOwn(const CalibrationHooks& hooks, std::uint32_t id, calibration::Plan plan) {
  auto record = plan.hasUltrasonic() && hooks.recordNative ? hooks.recordNative : hooks.record;
  if (!record) {
    calibrated(config_.memberId, refusal(id, "no_capture"));
    return;
  }
  // the window and the arrivals in group time: the leader's own clock through the master's fit when another device
  // serves it (the fit as it stands after the window: its latest estimate)
  const auto& local = config_.playout.local;
  const auto from = local.toMono(fromGroup(plan.windowFromNs()));
  const auto to = local.toMono(fromGroup(plan.windowToNs()));
  const auto analysed = hooks.analysed;
  const auto budgetMs = hooks.analysisBudgetMs;
  record(from, to,
               [alive = alive_, id, plan = std::move(plan), local, analysed, budgetMs](MicResult mic) mutable {
                 std::lock_guard guard(alive->mutex);
                 auto* self = alive->self;
                 if (self == nullptr)
                   return;
                 cadence_sync_v1_CalibrateProgress progress = cadence_sync_v1_CalibrateProgress_init_zero;
                 progress.id = id;
                 progress.stage = cadence_sync_v1_CalibrateStage_CALIBRATE_STAGE_RECORDED;
                 progress.analysis_budget_ms = budgetMs;
                 self->calibrationProgress(self->config_.memberId, progress);
                 self->worker_.run(
                     [self, id, plan = std::move(plan), local, analysed, mic = std::move(mic)]() mutable {
                       const auto arrivals = analyseRecording(
                           id, plan, self->config_.memberId, mic,
                           [self, local](double mono) {
                             return static_cast<double>(self->toGroup(local.toSync(std::llround(mono))));
                           },
                           analysed);
                       // the recording goes as soon as it was analysed (rule 8)
                       mic = {};
                       self->calibrated(self->config_.memberId, arrivals);
                     });
               });
}

std::vector<std::function<void(CalibrationOutcome)>> GroupLeader::clearListener() {
  std::vector<std::function<void(CalibrationOutcome)>> waiting;
  if (listener_)
    waiting = std::move(listener_->waiting);
  listener_.reset();
  return waiting;
}

void GroupLeader::calibrate(CalibrationHooks hooks, std::function<void(CalibrationOutcome)> done,
                            const CalibrationBoost& boost, std::uint32_t toleranceUs,
                            const CalibrationPriors& priors) {
  if (!hooks.record) {
    done(refused("no_capture"));
    return;
  }
  if (hooks.gate) {
    if (auto code = hooks.gate(); !code.empty()) {
      done(refused(code));
      return;
    }
  }
  std::unique_lock lock(mutex_);
  std::string_view refusalCode;
  const auto m = mapping();
  if (calibration_ || groupNow() < listenerUntilNs_)
    refusalCode = "busy";
  else if (phase_ != Phase::Idle || playout_.stats().state == PlayoutState::Playing)
    refusalCode = "playing";
  else if (!m || !m->synced)
    refusalCode = "unsynced";
  std::vector<std::string> offline;
  auto plan = planFor(groupNow() + kCalibrationLeadNs, calibration::kSlotSpacingNs, boost, &offline, toleranceUs, priors,
                      true);
  if (refusalCode.empty() && plan.members.size() < 2)
    refusalCode = "too_few";
  if (!refusalCode.empty()) {
    lock.unlock();
    done(refused(refusalCode));
    return;
  }
  // a mics calibration replaces the last listener run: its result is expired from now
  auto expired = clearListener();
  // the chirps play in an epoch of their own, and the next music's comes after it
  const auto epoch = ++epoch_;
  const auto id = ++calibrations_;
  calibration_.emplace(Calibration{CalibrationCollector(id, plan, offline), std::move(done)});
  stream_->calibrate(startOf(plan, id, epoch));
  (void)placeProbes(playout_, epoch, plan, config_.memberId);
  lock.unlock();
  for (auto& f : expired)
    f(refused("expired"));
  recordOwn(hooks, id, std::move(plan));
}

CalibrationOutcome GroupLeader::calibrateListener(const CalibrationHooks& hooks, const CalibrationBoost& boost,
                                                  std::uint32_t toleranceUs, const CalibrationPriors& priors) {
  if (hooks.gate) {
    if (auto code = hooks.gate(); !code.empty())
      return refused(code);
  }
  std::unique_lock lock(mutex_);
  const auto now = groupNow();
  if (calibration_ || now < listenerUntilNs_)
    return refused("busy");
  if (phase_ != Phase::Idle || playout_.stats().state == PlayoutState::Playing)
    return refused("playing");
  if (const auto m = mapping(); !m || !m->synced)
    return refused("unsynced");
  std::vector<std::string> offline;
  auto plan = planFor(now + kListenerStartNs, kListenerSlotNs, boost, &offline, toleranceUs, priors);
  if (plan.members.size() < 2)
    return refused("too_few");
  auto expired = clearListener();
  // the chirps play in an epoch of their own, and the next music's comes after it
  const auto epoch = ++epoch_;
  const auto id = ++calibrations_;
  // the members with a microphone record and answer as in mics mode (play_only stays false); a member before rounds
  // takes it as mics mode too, and one without a microphone still plays and answers no_capture
  stream_->calibrate(startOf(plan, id, epoch));
  (void)placeProbes(playout_, epoch, plan, config_.memberId);
  const auto first = plan.slotNs.front();
  const auto last = plan.slotNs.back();
  const auto durationNs = static_cast<std::int64_t>(std::llround(
      (plan.probe.kind == calibration::ProbeKind::Gold ? calibration::probeDuration(plan.probe) : plan.chirp.durationS) *
      1e9));
  listenerUntilNs_ = last + durationNs + kListenerTailNs;
  listener_.emplace(ListenerRun{CalibrationCollector(id, plan, offline), std::nullopt, {}});
  CalibrationOutcome out;
  out.code = "scheduled";
  out.startsInMs = static_cast<std::uint32_t>(kListenerStartNs / 1'000'000);
  out.runId = id;
  out.rounds = plan.rounds;
  out.chirp = plan.chirp;
  if (plan.probe.kind == calibration::ProbeKind::Gold)
    out.probe = plan.probe;
  for (std::size_t k = 0; k < plan.slotNs.size(); ++k)
    out.slots.push_back({plan.members[k % plan.members.size()],
                         static_cast<std::uint32_t>((plan.slotNs[k] - first) / 1'000'000),
                         static_cast<std::uint32_t>(k / plan.members.size()), plan.probeCode(k)});
  lock.unlock();
  for (auto& f : expired)
    f(refused("expired"));
  recordOwn(hooks, id, std::move(plan));
  return out;
}

void GroupLeader::listenerResult(std::uint32_t runId, std::function<void(CalibrationOutcome)> done) {
  std::unique_lock lock(mutex_);
  if (!listener_ || listener_->collector.id() != runId) {
    lock.unlock();
    done(refused("expired"));
    return;
  }
  if (listener_->outcome) {
    auto outcome = *listener_->outcome;
    lock.unlock();
    done(std::move(outcome));
    return;
  }
  listener_->waiting.push_back(std::move(done));
}

void GroupLeader::calibrated(const std::string& member, const cadence_sync_v1_CalibrateArrivals& arrivals) {
  std::lock_guard lock(mutex_);
  const auto now = groupNow();
  if (calibration_)
    calibration_->collector.add(member, arrivals, now);
  if (listener_ && !listener_->outcome)
    listener_->collector.add(member, arrivals, now);
}

void GroupLeader::calibrationProgress(const std::string& member,
                                      const cadence_sync_v1_CalibrateProgress& progress) {
  std::lock_guard lock(mutex_);
  if (calibration_)
    calibration_->collector.recorded(member, progress);
  if (listener_ && !listener_->outcome)
    listener_->collector.recorded(member, progress);
}

GroupTrackerEvents GroupLeader::trackingEvents() {
  GroupTrackerEvents events;
  events.start = [this](std::uint32_t id, std::uint32_t epoch, const calibration::Plan& plan) {
    stream_->track(cycleOf(plan, id, epoch));
    std::string gate;
    if (events_.calibration.gate)
      gate = events_.calibration.gate();
    if (!gate.empty()) {
      tracker_.arrivals(config_.memberId, trackOf(refusal(id, gate)), groupNow());
      return;
    }
    const auto self = std::find(plan.members.begin(), plan.members.end(), config_.memberId);
    if (self != plan.members.end()) {
      const auto index = static_cast<std::size_t>(self - plan.members.begin());
      playout_.mixUltrasonic(epoch, plan.slot(0, index), plan.probeCode(index));
    }
    recordOwnTracking(id, plan);
  };
  events.cancel = [this] {
    playout_.clearUltrasonic();
    if (events_.calibration.cancelNative)
      events_.calibration.cancelNative();
  };
  events.cycle = [this](std::uint32_t id, std::uint32_t slots, std::uint32_t answers) {
    if (events_.trackingCycle)
      events_.trackingCycle(id, slots, answers);
  };
  events.report = [this](const calibration::TrackingReport& report) {
    if (events_.tracked)
      events_.tracked(report);
  };
  return events;
}

void GroupLeader::recordOwnTracking(std::uint32_t id, calibration::Plan plan) {
  if (!events_.calibration.recordNative) {
    tracker_.arrivals(config_.memberId, trackOf(refusal(id, "unsupported")), groupNow());
    return;
  }
  const auto& local = config_.playout.local;
  const auto from = local.toMono(fromGroup(plan.windowFromNs()));
  const auto to = local.toMono(fromGroup(plan.windowToNs()));
  events_.calibration.recordNative(
      from, to, [alive = alive_, id, plan = std::move(plan), local](MicResult mic) mutable {
        std::lock_guard guard(alive->mutex);
        auto* self = alive->self;
        if (self == nullptr)
          return;
        self->worker_.run([self, id, plan = std::move(plan), local, mic = std::move(mic)]() mutable {
          const auto arrivals = analyseRecording(
              id, plan, self->config_.memberId, mic,
              [self, local](double mono) { return static_cast<double>(self->toGroup(local.toSync(std::llround(mono)))); });
          mic = {};
          self->tracked(self->config_.memberId, trackOf(arrivals));
        });
      });
}

void GroupLeader::tracked(const std::string& member, const cadence_sync_v1_TrackArrivals& arrivals) {
  std::lock_guard lock(mutex_);
  tracker_.arrivals(member, arrivals, groupNow());
}

std::optional<std::uint64_t> GroupLeader::heardMediaMs() const {
  std::lock_guard lock(mutex_);
  if (phase_ != Phase::Running)
    return std::nullopt;
  const auto now = groupNow();
  if (now < t0_)
    return mediaBaseMs_;
  const auto ms = static_cast<std::uint64_t>((now - t0_) / 1'000'000);
  return mediaBaseMs_ + std::min(ms, seq_ * 20);
}

GroupLeaderReport GroupLeader::report() const {
  GroupLeaderReport r;
  const auto stats = playout_.stats();
  r.steerPpm = stats.steerPpm;
  r.underruns = static_cast<std::uint32_t>(stats.underruns);
  r.placementErrorUs = static_cast<std::int32_t>(stats.errorNs / 1000);
  r.queueMs = static_cast<std::uint32_t>(stats.queuedNs / 1'000'000);
  const auto clock = mapping();
  r.synced = clock && clock->synced;
  r.clockErrorUs = clock ? static_cast<std::uint32_t>(std::min(clock->errorNs / 1000.0, 4e9)) : 0;
  {
    std::lock_guard lock(mutex_);
    r.latencyMs = latencyMs_;
    r.bitrateKbps = tuner_.bitrateKbps();
    r.waitMs = tuner_.waitMs();
    r.rejoins = rejoins_;
    r.state = phase_ == Phase::Barrier ? GroupRunState::Buffering
              : phase_ == Phase::Idle  ? GroupRunState::Idle
                                       : runState(stats.state);
    // a fit of the master's clock that is lost, or not synced before an epoch could start: the leader waits for it
    if (!clock || (!clock->synced && phase_ == Phase::Idle))
      r.state = GroupRunState::Syncing;
  }
  for (const auto& view : stream_->members()) {
    GroupLeaderMember m;
    m.id = view.id;
    m.connected = view.connected;
    if (view.status) {
      m.state = fromWire(view.status->state);
      m.synced = view.status->synced;
      m.clockErrorUs = view.status->clock_error_us;
      m.queueMs = view.status->queue_ms;
      m.rejoins = view.status->rejoins;
      m.placementErrorUs = view.status->placement_error_us;
    }
    if (!m.connected)
      m.state = GroupRunState::Connecting;
    r.members.push_back(std::move(m));
  }
  return r;
}

void GroupLeader::tune(std::int64_t now, std::uint64_t ownUnderruns, std::vector<GroupTuned>& tuned) {
  const bool playing = phase_ == Phase::Running;
  for (const auto& view : stream_->members()) {
    if (!view.connected) {
      tuner_.memberGone(view.id);
      continue;
    }
    // a member that plays no part (no GROUP session holds its speaker, an epoch it ran out of) says nothing of the
    // group's margin, and an underrun of its last epoch's run-out is none of this one's
    const bool in = view.status && (view.status->state == cadence_sync_v1_MemberState_MEMBER_STATE_PLAYING ||
                                    view.status->state == cadence_sync_v1_MemberState_MEMBER_STATE_REJOINING);
    if (view.status)
      tuner_.memberStatus(view.id, view.status->underruns, view.status->queue_ms, now, playing && in);
    tuner_.memberBacklog(view.id, view.unsentMs, now, playing);
  }
  tuner_.ownUnderruns(ownUnderruns, now, playing);
  if (auto change = tuner_.tick(now, playing)) {
    encoder_.setBitrate(change->to);
    tuned.push_back(*change);
  }
}

void GroupLeader::beginEpoch(std::int64_t now, std::vector<GroupTuned>& tuned) {
  if (auto change = tuner_.epochStarted(now)) {
    latencyMs_ = change->to;
    stream_->setLatency(latencyMs_);
    tuned.push_back(*change);
  }
  if (raise_) {
    raise_ = false;
    latencyMs_ += kRaiseMs;
    stream_->setLatency(latencyMs_);
  }
  ++epoch_;
  ready_.clear();
  cadence_sync_v1_Format format = cadence_sync_v1_Format_init_zero;
  format.epoch = epoch_;
  format.codec = config_.pcm ? cadence_sync_v1_Codec_CODEC_PCM_S16 : cadence_sync_v1_Codec_CODEC_OPUS;
  format.rate = kGroupRate;
  format.channels = static_cast<std::uint32_t>(kGroupChannels);
  format.frame_samples = static_cast<std::uint32_t>(kGroupFrameSamples);
  stream_->beginEpoch(format);
  playout_.format(epoch_, config_.pcm ? MusicCodec::PcmS16 : MusicCodec::Opus);
  startNs_ = now;
  epochClears_ = feed_.clears();
  seq_ = 0;
  epochFeedKnown_ = false;
  held_ = false;
  phase_ = Phase::Barrier;
  feedSteer_ = 0;
  feed_.requestSteer(0);
}

void GroupLeader::runTimeline() {
  threads_.timeline.raise(kNiceAudioFeed);
  std::vector<float> pcm(kGroupFrameSamples * kGroupChannels);
  std::vector<std::uint8_t> bytes;
  const auto liveTracking = trackingEvents();
  std::uint64_t ticks = 0;
  while (!stop_) {
    std::this_thread::sleep_for(kTick);
    // the own playout's counters, read without the group's lock (its pump holds the playout's): when the tuner looks
    std::optional<std::uint64_t> ownUnderruns;
    if (++ticks % kTuneEveryTicks == 0)
      ownUnderruns = playout_.stats().underruns;
    bool changed = false;
    std::optional<Calibration> finished;
    std::optional<CalibrationOutcome> listened;
    std::vector<std::function<void(CalibrationOutcome)>> listening;
    std::vector<GroupTuned> tuned;
    {
      std::lock_guard lock(mutex_);
      // the group's clock: none while the master's fit is lost, which holds the timeline (what was sent stays valid,
      // stamped in the master's time) and starts nothing; an epoch starts only on a synced one
      const auto clock = mapping();
      const auto now = groupNow();
      if (calibration_ && (calibration_->collector.complete() || now >= calibration_->collector.deadlineNs())) {
        finished = std::move(calibration_);
        calibration_.reset();
      }
      if (listener_ && !listener_->outcome &&
          (listener_->collector.complete() || now >= listener_->collector.deadlineNs())) {
        listener_->outcome = listener_->collector.finish();
        listened = listener_->outcome;
        listening = std::move(listener_->waiting);
        listener_->waiting.clear();
      }
      tracker_.tick(now, epoch_, phase_ == Phase::Running && active_ && clock && clock->synced, duck_,
                    stream_->members(), liveTracking);
      if (ownUnderruns)
        tune(now, *ownUnderruns, tuned);
      const auto latency = static_cast<std::int64_t>(latencyMs_) * 1'000'000;
      const auto ahead = readAheadNs_.value_or(config_.aheadNs);
      const auto queued = feed_.status().queued;
      const auto clears = feed_.clears();
      if (holdFeed_ && (queued == 0 || clears != holdClears_))
        holdFeed_ = false;
      if (phase_ != Phase::Idle && clears != epochClears_) {
        // a clear the group was not told of (a source's own skip or seek, a new session, the gate): the epoch stops
        // now on every device, and what the feed gives next starts the next one
        if (phase_ == Phase::Running)
          mediaBaseMs_ = heardAt(now);
        stream_->flush(epoch_ + 1);
        playout_.flush(epoch_ + 1);
        phase_ = Phase::Idle;
        endNs_ = 0;
        changed = true;
      }
      // a calibration's chirps play alone: what the feed holds waits for its end
      if (phase_ == Phase::Idle && !holdFeed_ && !calibration_ && now >= listenerUntilNs_ &&
          queued >= kGroupFrameSamples && clock && clock->synced) {
        beginEpoch(now, tuned);
        changed = true;
      }
      if (phase_ == Phase::Barrier && clock) {
        const bool all = std::all_of(config_.members.begin(), config_.members.end(), [&](const std::string& id) {
          const auto found = ready_.find(id);
          return found != ready_.end() && found->second == epoch_;
        });
        if (all || now - startNs_ >= static_cast<std::int64_t>(tuner_.waitMs()) * 1'000'000) {
          if (!all) {
            // a member connected but not ready in time counts the ceiling (one not connected says nothing)
            for (const auto& view : stream_->members()) {
              const auto found = ready_.find(view.id);
              if (view.connected && (found == ready_.end() || found->second != epoch_))
                tuner_.memberTimedOut(view.id);
            }
          }
          // the latency this epoch began with (a tuned one comes into force at its start)
          t0_ = std::max(startNs_ + static_cast<std::int64_t>(latencyMs_) * 1'000'000, now + kBarrierLeadNs);
          phase_ = Phase::Running;
          lastAudioNs_ = now;
          changed = true;
        }
      }
      if (phase_ == Phase::Running && !clock)
        held_ = true;
      if (phase_ == Phase::Running && clock && held_) {
        held_ = false;
        if (frameT(seq_) - now < latency - config_.stallNs) {
          // what it sent ran out (or nearly) while the clock was lost: the epoch ends where its last frame does, as a
          // stall's, and the feed's audio starts the next one from there, so nothing of it is skipped
          stream_->pause(epoch_, frameT(seq_));
          playout_.pause(epoch_, frameT(seq_));
          mediaBaseMs_ += seq_ * 20;
          phase_ = Phase::Idle;
          changed = true;
        }
      }
      if (phase_ == Phase::Running && clock) {
        while (frameT(seq_) < now + latency + ahead && feed_.status().queued >= kGroupFrameSamples) {
          const auto got = feed_.read(pcm);
          if (seq_ == 0) {
            // where the epoch starts in the feed: feedHeardNs counts from it
            epochFeedPos_ = feed_.readPosition() - got;
            epochFeedKnown_ = true;
          }
          if (!encoder_.encode(pcm, bytes))
            bytes.clear();
          StreamFrame frame{epoch_, seq_, frameT(seq_), mediaBaseMs_ + seq_ * 20, bytes};
          playout_.frame(epoch_, seq_, frame.tNs, frame.mediaMs, bytes);
          stream_->pushFrame(std::move(frame));
          ++seq_;
          endNs_ = frameT(seq_);
          lastAudioNs_ = now;
        }
        const auto lead = frameT(seq_) - now;
        if (lead < latency - config_.stallNs) {
          // a real-time source stalled, or the track ended: what was sent plays out, the next audio is a new epoch.
          // the epoch ends where its last frame does: a pause there fades every device out and leaves it paused, so
          // no playout takes the end for a stream that fell behind (a run-out, an underrun, a rejoin)
          stream_->pause(epoch_, frameT(seq_));
          playout_.pause(epoch_, frameT(seq_));
          mediaBaseMs_ += seq_ * 20;
          phase_ = Phase::Idle;
          changed = true;
        } else if (lead < latency + ahead - kRealTimeBelowNs) {
          const auto ppm = static_cast<std::int32_t>(std::clamp(
              std::lround(static_cast<double>(lead - latency) / 1e6 * kFeedPpmPerMs), -long{kFeedMaxPpm},
              long{kFeedMaxPpm}));
          if (std::abs(ppm - feedSteer_) >= 10) {
            feedSteer_ = ppm;
            feed_.requestSteer(ppm);
          }
        }
      }
    }
    if (finished)
      finished->done(finished->collector.finish());
    for (auto& f : listening)
      f(*listened);
    if (events_.tuned)
      for (const auto& t : tuned)
        events_.tuned(t);
    if (changed && events_.changed)
      events_.changed();
  }
}

void GroupLeader::runPlayout() {
  // it pushes into the music input a few tens of milliseconds ahead of the mixer
  threads_.playout.raise(kNiceAudio);
  bool wasActive = true;
  // a rejoin waits for the group's clock (the master's fit, lost a while)
  bool rejoin = false;
  while (!stop_) {
    std::this_thread::sleep_for(kTick);
    if (!active_) {
      if (wasActive)
        playout_.release();
      wasActive = false;
      continue;
    }
    if (!wasActive)
      rejoin = true;
    wasActive = true;
    // identity while the leader serves the clock; the master's fit otherwise, and none mutes the playout. a fit that
    // is not synced yet (the master's clock just came back) starts nothing: its first estimates are rough, and the
    // members play by the master's own time
    auto m = mapping();
    if (m && !m->synced && playout_.stats().state != PlayoutState::Playing)
      m.reset();
    if (playout_.pump(monoNs(), m) != RejoinReason::None)
      rejoin = true;
    if (rejoin && m) {
      rejoin = false;
      // the leader's own playout catches up from the backlog, as a member would
      const auto from = m->toLeader(config_.playout.local.now()) + kLocalJoinMarginNs;
      playout_.rejoinFrom(from);
      for (auto& frame : stream_->backlogFrom(from))
        playout_.frame(frame.epoch, frame.seq, frame.tNs, frame.mediaMs, std::move(frame.data));
      std::lock_guard lock(mutex_);
      ++rejoins_;
    }
  }
  playout_.release();
}

}  // namespace cadence::sync
