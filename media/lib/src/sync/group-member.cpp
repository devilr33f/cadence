#include "sync/group-member.h"

#include <algorithm>
#include <cmath>
#include <cstring>
#include <limits>
#include <sys/socket.h>

namespace cadence::sync {

namespace {

constexpr auto kPumpEvery = std::chrono::milliseconds(2);
constexpr std::int64_t kStatusEveryNs = 2'000'000'000;
// a rejoin asks for the frames from this far ahead of the leader's now: what a catch-up from the backlog takes
constexpr std::int64_t kJoinMarginNs = 300'000'000;
// a catch-up is the time from a join to playing again, and only one that came within this counts: a join in an idle
// group plays first at the next music or calibration, minutes later, and twice that taken as a margin put every
// frame of the next start before the join's from (all of it dropped, a calibration's chirps and a song's opening)
constexpr std::int64_t kMaxRefillNs = 3'000'000'000;
// the most a join asks ahead of now
constexpr std::int64_t kMaxJoinMarginNs = 2'000'000'000;

template <std::size_t N>
void copyText(char (&out)[N], std::string_view text) {
  const auto n = std::min(text.size(), N - 1);
  std::memcpy(out, text.data(), n);
  out[n] = '\0';
}

bool isError(std::string_view reason) {
  return reason == "unreachable" || reason == "refused" || reason == "auth_failed" || reason == "wrong_group" ||
         reason == "protocol";
}

// a link loss's token as a log may carry it: the leader's goodbye reason is a peer's text, so only known ones pass
std::string_view linkToken(std::string_view reason) {
  for (const std::string_view known : {"closed", "unreachable", "refused", "auth_failed", "wrong_group", "protocol",
                                       "leaving", "replaced", "overloaded"})
    if (reason == known)
      return known;
  return "other";
}

cadence_sync_v1_MemberState wireState(PlayoutState state, bool synced) {
  if (!synced)
    return cadence_sync_v1_MemberState_MEMBER_STATE_SYNCING;
  switch (state) {
    case PlayoutState::Playing:
      return cadence_sync_v1_MemberState_MEMBER_STATE_PLAYING;
    case PlayoutState::Buffering:
      return cadence_sync_v1_MemberState_MEMBER_STATE_BUFFERING;
    case PlayoutState::Rejoining:
      return cadence_sync_v1_MemberState_MEMBER_STATE_REJOINING;
    case PlayoutState::Syncing:
      return cadence_sync_v1_MemberState_MEMBER_STATE_SYNCING;
    case PlayoutState::Idle:
    case PlayoutState::Paused:
      break;
  }
  return cadence_sync_v1_MemberState_MEMBER_STATE_IDLE;
}

}  // namespace

GroupMember::GroupMember(PcmInput& out, GroupPlayout::PlayheadSource playhead, GroupMemberConfig config,
                         GroupMemberEvents events)
    : config_(std::move(config)), events_(std::move(events)),
      playout_(out, std::move(playhead), config_.playout) {
  active_ = config_.active;
}

GroupMember::~GroupMember() { stop(); }

bool GroupMember::start() {
  bindFailed_ = false;
  const auto clockPort = config_.clockPort == 0 ? config_.port : config_.clockPort;
  if (config_.serveClock) {
    // the master: its own clock is the group's, answered on the group's port number
    auto socket = ClockSocket::open(config_.clockBind, clockPort);
    if (!socket.ok()) {
      bindFailed_ = true;
      return false;
    }
    clockServer_ = std::make_unique<ClockServer>(std::move(socket.value()), config_.key,
                                                 [this] { threads_.clock.raise(kNiceAudioFeed); });
    clockServer_->setClock(config_.playout.local);
  } else {
    const auto master = ClockSocket::resolve(config_.clockHost.empty() ? config_.leaderHost : config_.clockHost,
                                             clockPort);
    if (!master)
      return false;
    auto socket = ClockSocket::open(master->addr.ss_family == AF_INET6 ? "::" : "", 0);
    if (!socket.ok())
      return false;
    auto clockConfig = config_.clock;
    clockConfig.smoother.syncedBelowNs = static_cast<double>(config_.budgetUs) * 1000.0;
    clockConfig.clock = config_.playout.local;
    clock_ = std::make_unique<ClockClient>(std::move(socket.value()), *master, config_.key, clockConfig,
                                           [this] { threads_.clock.raise(kNiceAudioFeed); });
  }
  MemberStreamConfig streamConfig;
  streamConfig.host = config_.leaderHost;
  streamConfig.port = config_.port;
  streamConfig.groupId = config_.groupId;
  streamConfig.memberId = config_.memberId;
  streamConfig.key = config_.key;
  streamConfig.backoffMinMs = config_.backoffMinMs;
  streamConfig.backoffMaxMs = config_.backoffMaxMs;
  streamConfig.onThread = [this] { threads_.stream.raise(kNiceAudioFeed); };
  MemberStreamEvents streamEvents;
  streamEvents.welcomed = [this](std::uint32_t latency) { welcomed(latency); };
  streamEvents.message = [this](const SyncEnvelope& e) { message(e); };
  streamEvents.lost = [this](std::string_view reason) { lost(reason); };
  stream_ = std::make_unique<MemberStream>(std::move(streamConfig), std::move(streamEvents));
  if (clock_)
    clock_->start();
  if (clockServer_)
    clockServer_->start();
  stream_->start();
  stop_ = false;
  {
    std::lock_guard lock(alive_->mutex);
    alive_->self = this;
  }
  thread_ = std::thread([this] { runPlayout(); });
  return true;
}

void GroupMember::stop() {
  if (stop_.exchange(true))
    return;
  cancelTracking();
  {
    std::lock_guard lock(alive_->mutex);
    alive_->self = nullptr;
  }
  worker_.join();
  wake_.notify_all();
  if (thread_.joinable())
    thread_.join();
  if (stream_)
    stream_->stop();
  if (clock_)
    clock_->stop();
  if (clockServer_)
    clockServer_->stop();
}

void GroupMember::setActive(bool active) {
  std::unique_lock lock(mutex_);
  active_ = active;
  released_ = false;
  wake_.notify_all();
  if (active || !thread_.joinable())
    return;
  // the input has one producer: the next one starts only once the playout let go of it
  wake_.wait_for(lock, std::chrono::milliseconds(500), [this] { return released_ || stop_.load(); });
}

bool GroupMember::control(cadence_sync_v1_ControlAction action, std::uint64_t positionMs) {
  if (!stream_)
    return false;
  SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
  e.which_body = cadence_sync_v1_Envelope_control_tag;
  e.body.control.action = action;
  e.body.control.position_ms = positionMs;
  return stream_->send(e);
}

void GroupMember::duckRequest(bool active) {
  ownDuck_ = active;
  if (active)
    cancelTracking();
  if (!stream_)
    return;
  SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
  e.which_body = cadence_sync_v1_Envelope_duck_request_tag;
  e.body.duck_request.active = active;
  stream_->send(e);
}

GroupTrimMode GroupMember::setTrim(std::int64_t ns) { return playout_.setTrim(ns); }

void GroupMember::setChannel(GroupChannelMap channel) { playout_.setChannel(channel); }

void GroupMember::setOutputKind(std::string kind) {
  {
    std::lock_guard lock(mutex_);
    config_.outputKind = std::move(kind);
  }
  sendStatus(report());
}

std::string GroupMember::outputKind() const {
  std::lock_guard lock(mutex_);
  return config_.outputKind;
}

std::optional<ClockMapping> GroupMember::mapping() const {
  if (clockServer_) {
    // the master plays by its own clock: the group's
    ClockMapping identity;
    identity.synced = true;
    return identity;
  }
  return clock_ ? clock_->mapping().load() : std::nullopt;
}

bool GroupMember::playing() const { return playout_.stats().state == PlayoutState::Playing; }

GroupMemberReport GroupMember::report() const {
  GroupMemberReport r;
  const auto stats = playout_.stats();
  const auto m = mapping();
  {
    std::lock_guard lock(mutex_);
    r.error = error_;
    r.connected = connected_;
    r.rejoins = rejoins_;
    r.latencyMs = latencyMs_;
  }
  r.synced = m && m->synced;
  r.clockErrorUs = m ? static_cast<std::uint32_t>(std::min(m->errorNs / 1000.0, 4e9)) : 0;
  r.queueMs = static_cast<std::uint32_t>(stats.queuedNs / 1'000'000);
  r.steerPpm = stats.steerPpm;
  r.underruns = static_cast<std::uint32_t>(stats.underruns);
  r.placementErrorUs = static_cast<std::int32_t>(stats.errorNs / 1000);
  if (!r.connected)
    r.state = GroupRunState::Connecting;
  else if (!r.synced)
    r.state = GroupRunState::Syncing;
  else
    switch (stats.state) {
      case PlayoutState::Playing:
        r.state = GroupRunState::Playing;
        break;
      case PlayoutState::Buffering:
        r.state = GroupRunState::Buffering;
        break;
      case PlayoutState::Rejoining:
        r.state = GroupRunState::Rejoining;
        break;
      case PlayoutState::Syncing:
        r.state = GroupRunState::Syncing;
        break;
      case PlayoutState::Idle:
      case PlayoutState::Paused:
        r.state = GroupRunState::Idle;
        break;
    }
  return r;
}

void GroupMember::changed() {
  if (events_.changed)
    events_.changed();
}

void GroupMember::welcomed(std::uint32_t latencyMs) {
  {
    std::lock_guard lock(mutex_);
    // a link that came back while the group played is a rejoin as much as one the playout asked for: the member
    // missed audio and catches up from the backlog (the status says so, the leader's latency raise counts it)
    if (welcomedBefore_ && active_) {
      ++rejoins_;
      linkRejoin_ = lastLost_;
    }
    welcomedBefore_ = true;
    connected_ = true;
    error_.clear();
    latencyMs_ = latencyMs;
    readyEpoch_ = 0;
  }
  joinPending_ = true;
  wake_.notify_all();
  // an idle member joins only when it plays, but the leader needs its calibration version before either can start
  sendStatus(report());
  changed();
}

void GroupMember::sendStatus(const GroupMemberReport& r) {
  if (!stream_)
    return;
  SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
  e.which_body = cadence_sync_v1_Envelope_status_tag;
  auto& s = e.body.status;
  s.state = wireState(playout_.stats().state, r.synced);
  s.synced = r.synced;
  s.clock_error_us = r.clockErrorUs;
  s.queue_ms = r.queueMs;
  s.steer_ppm = r.steerPpm;
  s.rejoins = r.rejoins;
  s.underruns = r.underruns;
  s.placement_error_us = r.placementErrorUs;
  s.calibration_version = config_.calibrationVersion;
  copyText(s.output_kind, outputKind());
  stream_->send(e);
}

void GroupMember::lost(std::string_view reason) {
  bool was = false;
  {
    std::lock_guard lock(mutex_);
    was = connected_;
    connected_ = false;
    lastLost_ = std::string(linkToken(reason));
    if (isError(reason))
      error_ = std::string(reason);
  }
  if (was || isError(reason))
    changed();
}

void GroupMember::message(const SyncEnvelope& e) {
  switch (e.which_body) {
    case cadence_sync_v1_Envelope_format_tag:
      playout_.format(e.body.format.epoch, e.body.format.codec == cadence_sync_v1_Codec_CODEC_PCM_S16
                                               ? MusicCodec::PcmS16
                                               : MusicCodec::Opus);
      break;
    case cadence_sync_v1_Envelope_frame_tag: {
      const auto& f = e.body.frame;
      playout_.frame(f.epoch, f.seq, f.t_ns, f.media_ms, std::vector<std::uint8_t>(f.data.bytes, f.data.bytes + f.data.size));
      break;
    }
    case cadence_sync_v1_Envelope_pause_tag:
      playout_.pause(e.body.pause.epoch, e.body.pause.t_ns);
      break;
    case cadence_sync_v1_Envelope_flush_tag:
      playout_.flush(e.body.flush.epoch);
      break;
    case cadence_sync_v1_Envelope_duck_tag:
      groupDuck_ = e.body.duck.active;
      if (e.body.duck.active)
        cancelTracking();
      if (events_.duck)
        events_.duck(e.body.duck.active);
      break;
    case cadence_sync_v1_Envelope_metadata_tag:
      if (events_.metadata)
        events_.metadata(e.body.metadata);
      break;
    case cadence_sync_v1_Envelope_calibrate_start_tag:
      calibrate(e.body.calibrate_start);
      break;
    case cadence_sync_v1_Envelope_track_cycle_tag:
      track(e.body.track_cycle);
      break;
    default:
      break;
  }
}

void GroupMember::answer(const cadence_sync_v1_TrackArrivals& arrivals) {
  if (!stream_)
    return;
  SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
  e.which_body = cadence_sync_v1_Envelope_track_arrivals_tag;
  e.body.track_arrivals = arrivals;
  stream_->send(e);
}

void GroupMember::answer(const cadence_sync_v1_CalibrateArrivals& arrivals) {
  if (!stream_)
    return;
  SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
  e.which_body = cadence_sync_v1_Envelope_calibrate_arrivals_tag;
  e.body.calibrate_arrivals = arrivals;
  stream_->send(e);
}

void GroupMember::progress(std::uint32_t id, std::uint32_t analysisBudgetMs) {
  if (!stream_)
    return;
  SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
  e.which_body = cadence_sync_v1_Envelope_calibrate_progress_tag;
  e.body.calibrate_progress.id = id;
  e.body.calibrate_progress.stage = cadence_sync_v1_CalibrateStage_CALIBRATE_STAGE_RECORDED;
  e.body.calibrate_progress.analysis_budget_ms = analysisBudgetMs;
  stream_->send(e);
}

bool GroupMember::playingOther(std::uint32_t epoch) const {
  // only audio it plays now keeps a chirp out. an epoch number alone says nothing: a restarted or moved leader numbers
  // its epochs from 1 again, while a member that reconnected keeps the old leader's (a pushed config restarts the
  // group, and the members may reach the old leader first), so a higher one refused every chirp until the new
  // leader's count caught up
  return playout_.stats().state == PlayoutState::Playing && playout_.epoch() != epoch;
}

void GroupMember::calibrate(const cadence_sync_v1_CalibrateStart& start) {
  cancelTracking();
  if (start.play_only) {
    // a leader before rounds: its listener calibration records nothing and wants no answer
    playChirp(start);
    return;
  }
  const auto m = mapping();
  if (!m || !m->synced) {
    answer(refusal(start.id, "unsynced"));
    return;
  }
  if (events_.calibration.gate) {
    if (const auto code = events_.calibration.gate(); !code.empty()) {
      answer(refusal(start.id, code));
      return;
    }
  }
  if (playingOther(start.epoch)) {
    answer(refusal(start.id, "epoch"));
    return;
  }
  auto plan = planOf(start);
  const auto local = config_.playout.local;
  auto toMono = [&](std::int64_t leaderNs) { return local.toMono(m->toLocal(leaderNs)); };
  // its chirps play whether or not it can record: a listener (or the others' microphones) hears them
  if (std::find(plan.members.begin(), plan.members.end(), config_.memberId) != plan.members.end()) {
    calibratingUntil_ = toMono(start.to_ns) + kChirpAfterNs;
    (void)placeProbes(playout_, start.epoch, plan, config_.memberId);
  }
  auto record = plan.hasUltrasonic() && events_.calibration.recordNative ? events_.calibration.recordNative
                                                                         : events_.calibration.record;
  if (!record) {
    answer(refusal(start.id, "no_capture"));
    return;
  }
  const auto analysed = events_.calibration.analysed;
  const auto budgetMs = events_.calibration.analysisBudgetMs;
  const bool acksProgress = start.accepts_progress;
  record(
      toMono(start.from_ns), toMono(start.to_ns),
      [alive = alive_, id = start.id, plan = std::move(plan), local, member = config_.memberId, analysed, budgetMs,
       acksProgress](
          MicResult mic) mutable {
        std::lock_guard guard(alive->mutex);
        auto* self = alive->self;
        if (self == nullptr)
          return;
        if (acksProgress)
          self->progress(id, budgetMs);
        self->worker_.run([self, id, plan = std::move(plan), local, member, analysed, mic = std::move(mic)]() mutable {
          // the mapping as it stands after the window: the latest estimate of the leader's clock
          const auto now = self->mapping();
          if (!now) {
            self->answer(refusal(id, "unsynced"));
            return;
          }
          const auto arrivals = analyseRecording(
              id, plan, member, mic,
              [&](double mono) { return static_cast<double>(now->toLeader(local.toSync(std::llround(mono)))); },
              analysed);
          // the recording goes as soon as it was analysed (rule 8)
          mic = {};
          self->answer(arrivals);
        });
      });
}

void GroupMember::cancelTracking() {
  trackingId_ = 0;
  playout_.clearUltrasonic();
  if (trackingRecording_.exchange(false) && events_.calibration.cancelNative)
    events_.calibration.cancelNative();
}

void GroupMember::track(const cadence_sync_v1_TrackCycle& cycle) {
  if (!config_.trackingEnabled)
    return;
  auto plan = planOf(cycle);
  const auto mine = std::find(plan.members.begin(), plan.members.end(), config_.memberId);
  if (mine == plan.members.end())
    return;
  cancelTracking();
  const auto index = static_cast<std::size_t>(mine - plan.members.begin());
  const auto refuse = [&](std::string_view code) {
    cadence_sync_v1_TrackArrivals out = cadence_sync_v1_TrackArrivals_init_zero;
    out.id = cycle.id;
    copyText(out.code, code);
    answer(out);
  };
  const auto m = mapping();
  if (ownDuck_ || groupDuck_ || !active_)
    return refuse("busy");
  if (!m || !m->synced)
    return refuse("unsynced");
  if (playingOther(cycle.epoch))
    return refuse("epoch");
  if (events_.calibration.gate) {
    if (const auto code = events_.calibration.gate(); !code.empty())
      return refuse(code);
  }
  if (outputKind() == "bluetooth")
    return refuse("unsupported");
  playout_.mixUltrasonic(cycle.epoch, plan.slot(0, index), plan.probeCode(index));
  ++trackingCycles_;
  if (!events_.calibration.recordNative)
    return refuse("unsupported");
  const auto local = config_.playout.local;
  auto toMono = [&](std::int64_t leaderNs) { return local.toMono(m->toLocal(leaderNs)); };
  trackingRecording_ = true;
  trackingId_ = cycle.id;
  events_.calibration.recordNative(
      toMono(cycle.from_ns), toMono(cycle.to_ns),
      [alive = alive_, id = cycle.id, plan = std::move(plan), local, member = config_.memberId](MicResult mic) mutable {
        std::lock_guard guard(alive->mutex);
        auto* self = alive->self;
        if (self == nullptr || self->trackingId_.load() != id)
          return;
        self->worker_.run([self, id, plan = std::move(plan), local, member, mic = std::move(mic)]() mutable {
          const auto now = self->mapping();
          self->trackingRecording_ = false;
          if (!now) {
            cadence_sync_v1_TrackArrivals out = cadence_sync_v1_TrackArrivals_init_zero;
            out.id = id;
            copyText(out.code, "unsynced");
            self->answer(out);
            return;
          }
          const auto arrivals = analyseRecording(
              id, plan, member, mic,
              [&](double mono) { return static_cast<double>(now->toLeader(local.toSync(std::llround(mono)))); });
          mic = {};
          self->answer(trackOf(arrivals));
        });
      });
}

void GroupMember::playChirp(const cadence_sync_v1_CalibrateStart& start) {
  // a listener calibration from a leader before rounds: the slots' chirps as mics mode plays them, nothing recorded
  // and nothing answered. a member that may not play now (unsynced, the gate, an epoch past the calibration's) stays
  // silent: the listener sees it missed
  const auto m = mapping();
  if (!m || !m->synced || playingOther(start.epoch))
    return;
  if (events_.calibration.gate && !events_.calibration.gate().empty())
    return;
  const auto plan = planOf(start);
  if (std::find(plan.members.begin(), plan.members.end(), config_.memberId) == plan.members.end())
    return;
  calibratingUntil_ = config_.playout.local.toMono(m->toLocal(start.to_ns)) + kChirpAfterNs;
  (void)placeProbes(playout_, start.epoch, plan, config_.memberId);
}

void GroupMember::join() {
  if (!stream_ || !stream_->connected())
    return;
  const auto m = mapping();
  std::int64_t from = std::numeric_limits<std::int64_t>::min();
  if (m) {
    const auto local = config_.playout.local.toSync(monoNs());
    from = m->toLeader(local) + std::clamp(2 * lastRefillNs_, kJoinMarginNs, kMaxJoinMarginNs);
  }
  SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
  e.which_body = cadence_sync_v1_Envelope_join_tag;
  e.body.join.from_ns = from;
  playout_.rejoinFrom(from);
  stream_->send(e);
  joinAtNs_ = monoNs();
}

void GroupMember::diagnose(std::int64_t now, const std::optional<ClockMapping>& m, RejoinReason reason,
                           bool active) {
  const auto clockStats = [this] { return clock_ ? clock_->stats() : ClockClientStats{}; };
  auto rejoined = [&](GroupCause cause, std::string link) {
    std::uint64_t suppressed = 0;
    if (!diagnostics_.admit(cause, now, suppressed) || !events_.rejoined)
      return;
    const auto drop = playout_.lastDrop();
    const auto r = report();
    GroupRejoinNote note;
    note.cause = cause;
    note.link = std::move(link);
    note.suppressed = suppressed;
    note.rejoins = r.rejoins;
    note.underruns = r.underruns;
    note.clockErrorUs = r.clockErrorUs;
    note.queueMs = r.queueMs;
    if (cause == GroupCause::Link) {
      note.delayFrames = playout_.stats().delayFrames;
    } else {
      note.errorUs = drop.errorNs / 1000;
      note.delayFrames = drop.delayFrames;
      note.delayMinFrames = drop.delayMinFrames;
      note.delayMaxFrames = drop.delayMaxFrames;
      note.queueMs = static_cast<std::uint32_t>(drop.queuedNs / 1'000'000);
      note.playedMs = drop.playedNs / 1'000'000;
    }
    events_.rejoined(note);
  };
  switch (reason) {
    case RejoinReason::None:
      break;
    case RejoinReason::Error:
      rejoined(GroupCause::Placement, {});
      break;
    case RejoinReason::Underrun:
      rejoined(GroupCause::Underrun, {});
      break;
    case RejoinReason::ClockLost:
      rejoined(GroupCause::ClockLost, {});
      break;
    case RejoinReason::ClockStep:
      rejoined(GroupCause::ClockStep, {});
      break;
    case RejoinReason::Gap:
      rejoined(GroupCause::Gap, {});
      break;
  }
  std::optional<std::string> link;
  {
    std::lock_guard lock(mutex_);
    link.swap(linkRejoin_);
  }
  if (link)
    rejoined(GroupCause::Link, std::move(*link));

  // the clock master plays by its own clock: nothing of a fit to tell
  if (clock_) {
    std::optional<GroupCause> event;
    if (!m && sawMapping_)
      event = GroupCause::Lost;
    else if (m && sawMapping_ && m->generation != sawGeneration_)
      event = GroupCause::Step;
    else if (m && m->synced && (!sawMapping_ || !sawSynced_))
      event = GroupCause::Synced;
    else if (m && !m->synced && sawMapping_ && sawSynced_)
      event = GroupCause::Unsynced;
    sawMapping_ = m.has_value();
    sawSynced_ = m && m->synced;
    sawGeneration_ = m ? m->generation : 0;
    std::uint64_t suppressed = 0;
    if (event && diagnostics_.admit(*event, now, suppressed) && events_.clock) {
      const auto stats = clockStats();
      GroupClockNote note;
      note.cause = *event;
      note.suppressed = suppressed;
      note.budgetUs = config_.budgetUs;
      if (m)
        note.errorUs = static_cast<std::uint32_t>(std::min(m->errorNs / 1000.0, 4e9));
      if (*event == GroupCause::Step)
        note.stepUs = stats.lastStepNs / 1000;
      if (stats.last) {
        note.rttMinUs = static_cast<std::uint32_t>(stats.last->rttMinNs / 1000);
        note.rttP50Us = static_cast<std::uint32_t>(stats.last->rttP50Ns / 1000);
        note.samples = static_cast<std::uint32_t>(stats.last->total);
        note.ppm = static_cast<std::int32_t>(std::lround(stats.last->skew * 1e6));
      }
      events_.clock(note);
    }
  }

  if (!active)
    return;
  if (diagnostics_.sampleDue(now)) {
    const auto stats = playout_.stats();
    GroupSample sample;
    sample.mapped = m.has_value();
    sample.clockErrorUs = m ? static_cast<std::uint32_t>(std::min(m->errorNs / 1000.0, 4e9)) : 0;
    if (const auto c = clockStats(); c.last) {
      sample.rttMinUs = static_cast<std::uint32_t>(c.last->rttMinNs / 1000);
      sample.rttP50Us = static_cast<std::uint32_t>(c.last->rttP50Ns / 1000);
    }
    sample.playing = stats.state == PlayoutState::Playing;
    sample.placementErrorUs = stats.errorNs / 1000;
    sample.delayFrames = stats.delayFrames;
    diagnostics_.sample(now, sample);
  }
  if (const auto summary = diagnostics_.summary(now); summary && events_.summary)
    events_.summary(*summary);
}

void GroupMember::runPlayout() {
  // it pushes into the music input a few tens of milliseconds ahead of the mixer
  threads_.playout.raise(kNiceAudio);
  bool wasActive = config_.active;
  bool wasCalibrating = false;
  auto statusAt = monoNs();
  auto lastState = report().state;
  while (!stop_) {
    {
      std::unique_lock lock(mutex_);
      wake_.wait_for(lock, kPumpEvery, [this] { return stop_.load(); });
    }
    if (stop_)
      break;
    const bool active = active_;
    const bool calibrating = monoNs() < calibratingUntil_.load();
    if (!active && !calibrating) {
      if (wasActive || wasCalibrating) {
        playout_.release();
      }
      wasActive = false;
      wasCalibrating = false;
      {
        std::lock_guard lock(mutex_);
        released_ = true;
      }
      wake_.notify_all();
      // the clock's events are told while it waits too
      diagnose(monoNs(), mapping(), RejoinReason::None, false);
      continue;
    }
    if (!active) {
      // a calibration's chirp in an idle group: the playout plays what was placed, the stream is not asked for more
      wasCalibrating = true;
      playout_.pump(monoNs(), mapping());
      continue;
    }
    if (!wasActive) {
      // what it missed while set aside is asked for again
      wasActive = true;
      joinPending_ = true;
      diagnostics_.restart(monoNs());
    }
    if (joinPending_.exchange(false))
      join();
    const auto m = mapping();
    const auto reason = playout_.pump(monoNs(), m);
    if (reason != RejoinReason::None) {
      {
        std::lock_guard lock(mutex_);
        ++rejoins_;
      }
      join();
    }
    diagnose(monoNs(), m, reason, active);
    const auto epoch = playout_.epoch();
    std::uint32_t ready = 0;
    {
      std::lock_guard lock(mutex_);
      ready = readyEpoch_;
    }
    if (epoch != 0 && epoch != ready && m && m->synced && stream_->connected()) {
      SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
      e.which_body = cadence_sync_v1_Envelope_ready_tag;
      e.body.ready.epoch = epoch;
      if (stream_->send(e)) {
        std::lock_guard lock(mutex_);
        readyEpoch_ = epoch;
      }
    }
    const auto now = monoNs();
    const auto r = report();
    if (now - statusAt >= kStatusEveryNs) {
      statusAt = now;
      sendStatus(r);
    }
    if (r.state != lastState) {
      lastState = r.state;
      // the time a catch-up took: the next join asks for at least twice that ahead
      if (r.state == GroupRunState::Playing && joinAtNs_ != 0) {
        if (now - joinAtNs_ <= kMaxRefillNs)
          lastRefillNs_ = now - joinAtNs_;
        joinAtNs_ = 0;
      }
      changed();
    }
  }
  playout_.release();
}

}  // namespace cadence::sync
