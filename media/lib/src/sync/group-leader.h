#pragma once

#include "audio/music-codec.h"
#include "backend/pcm-input.h"
#include "result.h"
#include "sync/calibration-run.h"
#include "sync/clock-client.h"
#include "sync/clock-server.h"
#include "sync/group-member.h"
#include "sync/group-playout.h"
#include "sync/group-threads.h"
#include "sync/group-tracking.h"
#include "sync/group-tuner.h"
#include "sync/stream-leader.h"
#include "sync/sync-clock.h"

#include <atomic>
#include <cstdint>
#include <deque>
#include <functional>
#include <map>
#include <memory>
#include <mutex>
#include <optional>
#include <string>
#include <thread>
#include <vector>

namespace cadence::sync {

struct GroupLeaderConfig {
  std::string groupId;
  // the leader's own device id: its slot in a calibration
  std::string memberId;
  std::vector<std::uint8_t> key;
  std::vector<std::string> members;
  // empty: every v4 address
  std::string host;
  std::uint16_t port = 7701;
  std::uint32_t latencyMs = 1000;
  std::uint32_t waitMs = 3000;
  std::uint32_t bitrateKbps = kGroupDefaultKbps;
  bool pcm = false;
  // auto-tune (group-tuner.h): latencyMs is where the latency starts, bitrateKbps and waitMs the ceilings
  bool autoLatency = false;
  bool autoBitrate = false;
  bool autoWait = false;
  // the leader's own playout: its channel, trim and clock (the leader's sync clock, whose identity mapping it plays by
  // while it serves the group clock itself)
  GroupPlayoutConfig playout;
  // the clock master when another device serves the group clock (empty: the leader serves it on port, over udp). the
  // leader then binds the stream alone, fits the master's clock as a member does, and its timeline, its frames and
  // its own playout speak the master's time
  std::string clockHost;
  // the master's port; 0: port
  std::uint16_t clockPort = 0;
  // the fit of the master's clock (its local side is playout.local), synced under budgetUs
  ClockClientConfig clock;
  std::uint32_t budgetUs = 1000;
  // how far ahead of real time, past the latency, a file source is encoded
  std::int64_t aheadNs = 2'000'000'000;
  // a real-time source that gives nothing for this long ends the epoch; its next audio starts a new one
  std::int64_t stallNs = 500'000'000;
  // how many times every member chirps in a calibration (either mode)
  std::uint32_t calibrationRounds = calibration::kCalibrationRounds;
  // this leader's active output kind; bluetooth cannot carry the ultrasonic band
  std::string outputKind;
  // opt-in live tracking: the server's capable-member allow-list, a cycle every 10 s, and a 250 us deadband for a
  // stereo pair (budget_us <= 100) or 1 ms otherwise. zero overrides select those defaults
  bool trackingEnabled = false;
  std::vector<std::string> trackingMembers;
  std::int64_t trackingCycleNs = 10'000'000'000;
  std::int64_t trackingDeadbandNs = 0;
};

// the owner's boost of members heard faintly: member id -> dB over the chirp's level (at most kMaxGainDb)
using CalibrationBoost = std::map<std::string, std::uint32_t>;

struct CalibrationPrior {
  std::int64_t priorNs = 0;
  std::int64_t priorSpanNs = 150'000'000;
};
using CalibrationPriors = std::map<std::string, CalibrationPrior>;

struct GroupLeaderMember {
  std::string id;
  GroupRunState state = GroupRunState::Connecting;
  bool connected = false;
  bool synced = false;
  std::uint32_t clockErrorUs = 0;
  std::uint32_t queueMs = 0;
  std::uint32_t rejoins = 0;
  std::int32_t placementErrorUs = 0;
};

struct GroupLeaderReport {
  GroupRunState state = GroupRunState::Idle;
  // its own clock against the group's: always synced, error 0, while it serves the clock itself
  bool synced = true;
  std::uint32_t clockErrorUs = 0;
  std::uint32_t latencyMs = 0;
  std::int32_t steerPpm = 0;
  std::uint32_t rejoins = 0;
  std::uint32_t underruns = 0;
  std::int32_t placementErrorUs = 0;
  std::uint32_t queueMs = 0;
  // in force: the opus bitrate (0 with pcm) and the start wait
  std::uint32_t bitrateKbps = 0;
  std::uint32_t waitMs = 0;
  std::vector<GroupLeaderMember> members;
};

// told on the leader's threads (audiod posts them to its loop)
struct GroupLeaderEvents {
  // a member's transport command for the group's session
  std::function<void(const std::string& member, cadence_sync_v1_ControlAction action, std::uint64_t positionMs)>
      control;
  // the group's duck changed: every device applies it to the group's audio
  std::function<void(bool active)> duck;
  std::function<void()> changed;
  // the auto-tune changed the latency (applied at this epoch's start) or the bitrate (from the next frame)
  std::function<void(const GroupTuned& tuned)> tuned;
  // native microphone hooks for the leader's own live cycles (the manual calibrate call has its own)
  CalibrationHooks calibration;
  std::function<void(const calibration::TrackingReport& report)> tracked;
  std::function<void(std::uint32_t id, std::uint32_t slots, std::uint32_t answers)> trackingCycle;
};

// a group's leader: the timeline thread reads the group feed (the music target of the leader's sources), stamps each
// 20 ms frame with when it is heard, encodes it and hands it to the stream and to the leader's own playout, which
// plays it as any member does (identity clock; the master's mapping when another device serves the group clock, and
// then no epoch starts until that clock is synced, and a lost one holds the timeline). an epoch starts at the feed's first audio after it was idle: Format
// goes out, the start waits for every member's Ready up to wait_ms, then T0 = max(start + latency, barrier + lead).
// a file source is encoded up to aheadNs past the latency; a real-time one fills at its pace and is steered (the
// feed's resampler) to keep its lead near the latency. every public call is safe from any thread
class GroupLeader {
 public:
  static Result<std::unique_ptr<GroupLeader>, SocketError> start(PcmInput& feed, PcmInput& out,
                                                                GroupPlayout::PlayheadSource playhead,
                                                                GroupLeaderConfig config, GroupLeaderEvents events);
  ~GroupLeader();
  GroupLeader(const GroupLeader&) = delete;
  GroupLeader& operator=(const GroupLeader&) = delete;

  void stop();
  [[nodiscard]] std::uint16_t port() const { return stream_->port(); }

  // the media position the source's next audio starts at (a play, a seek, a resume): what frames carry from then on
  void setMediaPosition(std::uint64_t ms);
  // the media position the next epoch starts at, while none runs (an external player's own position, which audiod
  // hears of after its ring was flushed or paused): unlike setMediaPosition, a feed held for a pause stays held
  void setIdlePosition(std::uint64_t ms);
  // the group pauses 150 ms from now; the media position heard there, which a seekable source resumes from
  std::uint64_t pause();
  // a seek, a new track, a stop: every member drops the epoch now; positionMs is where the next audio starts, which a
  // stop leaves out
  void flush(std::optional<std::uint64_t> positionMs = std::nullopt);
  // a paused source resumes: what the feed holds now is what the next epoch plays (nothing old, once its pause cleared
  // it; a real-time source that never clears nor runs dry would otherwise hold the group idle for good)
  void resume();
  // the source moved to another track on its own, its audio already in the feed: the epoch in force ends on every
  // device and the feed's audio starts the next one, from positionMs
  void trackChanged(std::uint64_t positionMs);
  void metadata(const cadence_sync_v1_Metadata& metadata);
  // the leader's own duck (its wake), or-ed with its members'
  void duckRequest(bool active);
  // the leader's own playout plays while active (audiod's gate and session)
  void setActive(bool active);
  // an acoustic calibration of the group (the owner's: never a voice turn, rule 7): every connected member and the
  // leader play their chirps at their slots (config's rounds, round-major, kSlotSpacingNs apart; boost's members
  // louder) and record their microphones (hooks: this device's), the leader solves what they heard. refused unless
  // the group is idle or paused (playing), while one runs (busy), or when no member is connected (too_few). done runs
  // once, on a leader thread (or the caller's for a refusal); a stop drops it unanswered. it clears the last listener
  // run (its result is expired from then)
  void calibrate(CalibrationHooks hooks, std::function<void(CalibrationOutcome)> done,
                 const CalibrationBoost& boost = {}, std::uint32_t toleranceUs = 2000,
                 const CalibrationPriors& priors = {});
  // a listener calibration (the owner's, rule 7): the leader and every connected member, in config order, play their
  // chirps at their slots (round-major; the leader's first kListenerStartNs from now, the next ones kListenerSlotNs
  // apart, each trim left out) while a listener's microphone measures the arrivals; the members with a microphone (and
  // the leader, with hooks.record) record and answer as in calibrate, which listenerResult reads. answered at once:
  // code scheduled with the slots, the run id, rounds and chirp, or a refusal (the gate's code, busy, playing,
  // too_few). busy until 500 ms past the last chirp
  [[nodiscard]] CalibrationOutcome calibrateListener(const CalibrationHooks& hooks, const CalibrationBoost& boost = {},
                                                     std::uint32_t toleranceUs = 2000,
                                                     const CalibrationPriors& priors = {});
  // the speakers' own measure of the listener run runId (as calibrate's outcome), once every member answered or the
  // answer wait passed: at once when it is known, else later on a leader thread. code expired for a run that is not
  // the last listener run, or was cleared since. a stop drops it unanswered
  void listenerResult(std::uint32_t runId, std::function<void(CalibrationOutcome)> done);

  // how far past the latency the feed is read while a source wants it closer than config's aheadNs (a screen's ring:
  // its player's picture follows what was read, and a pause skips what was sent past it); none: aheadNs again
  void setReadAhead(std::optional<std::int64_t> aheadNs);
  // the CLOCK_MONOTONIC time at which the group hears the feed's frame at pos (a PcmInput position, as its
  // readPosition and writePos count): T0 plus the frame's offset in the running epoch, through the group clock's
  // mapping (identity while the leader serves it). none before the epoch's T0 and its first read, and for a frame
  // before the epoch's first
  [[nodiscard]] std::optional<std::int64_t> feedHeardNs(std::uint64_t pos) const;
  // how long from now until the group hears the next frame the feed gives: the latency in force while no epoch runs
  [[nodiscard]] std::int64_t feedLeadNs() const;

  // the group still has audio of its last epoch to play (a source that ended is heard out, a pause or flush ends it)
  [[nodiscard]] bool sounding() const;
  // what is heard now: none while nothing is
  [[nodiscard]] std::optional<std::uint64_t> heardMediaMs() const;
  [[nodiscard]] GroupLeaderReport report() const;
  // the epoch in force and its T0 (leader sync time), for the tests
  [[nodiscard]] std::pair<std::uint32_t, std::int64_t> timeline() const {
    std::lock_guard lock(mutex_);
    return {epoch_, t0_};
  }
  // the frames the running epoch sent, for the tests
  [[nodiscard]] std::uint64_t framesSent() const {
    std::lock_guard lock(mutex_);
    return seq_;
  }
  GroupTrimMode setTrim(std::int64_t ns) { return playout_.setTrim(ns); }
  void setOutputKind(std::string kind);
  [[nodiscard]] std::uint64_t trackingCycles() const { return tracker_.cycles(); }
  [[nodiscard]] const GroupPlayout& playout() const { return playout_; }
  // what raising its threads' priorities came to
  [[nodiscard]] const GroupThreads& threads() const { return threads_; }

 private:
  enum class Phase : std::uint8_t { Idle, Barrier, Running };

  GroupLeader(PcmInput& feed, PcmInput& out, GroupPlayout::PlayheadSource playhead, GroupLeaderConfig config,
              GroupLeaderEvents events);
  void runTimeline();
  void runPlayout();
  // the tuner's changes at the epoch's start go into tuned, told once the mutex is let go
  void beginEpoch(std::int64_t now, std::vector<GroupTuned>& tuned);
  // what the feed holds is ignored until it is cleared or read empty; mutex held
  void hold();
  // the media position heard at leader time t of the running epoch; mutex held
  [[nodiscard]] std::uint64_t heardAt(std::int64_t t) const;
  void memberDuck(const std::string& member, bool active);
  void joined(const std::string& member);
  void applyDuck();
  [[nodiscard]] std::int64_t frameT(std::uint64_t seq) const;
  // the group clock on the leader's: identity while it serves the clock, the master's fit otherwise (none while that
  // is lost). lock-free
  [[nodiscard]] std::optional<ClockMapping> mapping() const;
  // the group time at the leader's sync time local, and back: by the fit, or while it is lost by the offset it last
  // had. lock-free
  [[nodiscard]] std::int64_t toGroup(std::int64_t local) const;
  [[nodiscard]] std::int64_t fromGroup(std::int64_t group) const;
  [[nodiscard]] std::int64_t groupNow() const { return toGroup(config_.playout.local.now()); }
  void calibrated(const std::string& member, const cadence_sync_v1_CalibrateArrivals& arrivals);
  void tracked(const std::string& member, const cadence_sync_v1_TrackArrivals& arrivals);
  void recordOwnTracking(std::uint32_t id, calibration::Plan plan);
  [[nodiscard]] GroupTrackerEvents trackingEvents();
  void calibrationProgress(const std::string& member, const cadence_sync_v1_CalibrateProgress& progress);
  // the tuner's look at the members' status and the own playout's underruns, a few times a second on the timeline;
  // mutex held
  void tune(std::int64_t now, std::uint64_t ownUnderruns, std::vector<GroupTuned>& tuned);

  struct Calibration {
    CalibrationCollector collector;
    std::function<void(CalibrationOutcome)> done;
  };
  // the last listener run: its arrivals as they come, its outcome once complete, who waits for it
  struct ListenerRun {
    CalibrationCollector collector;
    std::optional<CalibrationOutcome> outcome;
    std::vector<std::function<void(CalibrationOutcome)>> waiting;
  };
  // the plan of a calibration: the leader and its connected members (offline: the rest), rounds, boosts; mutex held
  [[nodiscard]] calibration::Plan planFor(std::int64_t startNs, std::int64_t spacingNs, const CalibrationBoost& boost,
                                          std::vector<std::string>* offline, std::uint32_t toleranceUs,
                                          const CalibrationPriors& priors, bool ultrasonic = false) const;
  // the leader records its own microphone over the plan's window and answers its own arrivals; mutex not held
  void recordOwn(const CalibrationHooks& hooks, std::uint32_t id, calibration::Plan plan);
  // the listener run's waiting callbacks, told expired; mutex held
  [[nodiscard]] std::vector<std::function<void(CalibrationOutcome)>> clearListener();
  // what a recording's completion reaches the leader through: cleared on stop, so a late one does nothing
  struct Alive {
    std::mutex mutex;
    GroupLeader* self = nullptr;
  };

  PcmInput& feed_;
  GroupLeaderConfig config_;
  GroupLeaderEvents events_;
  GroupPlayout playout_;
  GroupThreads threads_;
  MusicEncoder encoder_;
  std::unique_ptr<LeaderStream> stream_;
  std::unique_ptr<ClockServer> clock_;
  // the master's clock, when another device serves it
  std::unique_ptr<ClockClient> clockClient_;
  // group time less the leader's sync time, as the fit last said: what a lost clock still goes by
  mutable std::atomic<std::int64_t> heldOffsetNs_{0};
  std::thread timeline_;
  std::thread player_;
  std::atomic<bool> stop_{false};
  std::atomic<bool> active_{true};

  mutable std::mutex mutex_;
  Phase phase_ = Phase::Idle;
  std::uint32_t epoch_ = 0;
  std::int64_t startNs_ = 0;
  std::int64_t t0_ = 0;
  std::uint64_t seq_ = 0;
  std::uint64_t mediaBaseMs_ = 0;
  std::int64_t lastAudioNs_ = 0;
  // when the last frame sent ends (leader time), 0 once nothing more is to be heard
  std::int64_t endNs_ = 0;
  std::int32_t feedSteer_ = 0;
  // the feed's position of the running epoch's first frame, once its first read took it
  std::uint64_t epochFeedPos_ = 0;
  bool epochFeedKnown_ = false;
  std::optional<std::int64_t> readAheadNs_;
  // the running epoch was held while the group's clock was lost (the master's fit): checked once it is back
  bool held_ = false;
  std::uint32_t latencyMs_ = 0;
  std::map<std::string, std::uint32_t> ready_;
  std::map<std::string, bool> ducks_;
  bool ownDuck_ = false;
  bool duck_ = false;
  // a member's first join after it connected is no rejoin
  std::map<std::string, bool> firstJoin_;
  std::deque<std::int64_t> rejoinTimes_;
  std::uint32_t rejoins_ = 0;
  // the latency raised after repeated rejoins applies from the next epoch
  bool raise_ = false;
  // after a pause or a flush, what the feed still holds is the old audio: ignored until the feed was cleared since
  // (holdClears_ is the feed's clear count then), read empty, or the source said where its next audio starts. an
  // empty feed alone is not enough: a new source pushes within a millisecond of the old one's clear, between ticks
  bool holdFeed_ = false;
  std::uint64_t holdClears_ = 0;
  // the feed's clear count when the epoch began: a clear nobody asked the group for (a source's own seek or skip, a
  // new session, the gate) ends the epoch
  std::uint64_t epochClears_ = 0;
  std::optional<Calibration> calibration_;
  std::optional<ListenerRun> listener_;
  std::uint32_t calibrations_ = 0;
  // a listener calibration's chirps play until then (leader time): no epoch, no other calibration
  std::int64_t listenerUntilNs_ = 0;
  GroupTuner tuner_;
  GroupTracker tracker_;
  std::shared_ptr<Alive> alive_ = std::make_shared<Alive>();
  CalibrationWorker worker_;
};

}  // namespace cadence::sync
