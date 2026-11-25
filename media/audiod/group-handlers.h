#pragma once

#include "audiod/daemon.h"
#include "audiod/group-clock.h"
#include "audiod/group-feed.h"
#include "audiod/group-source.h"
#include "audiod/ipc.h"
#include "audiod/log.h"
#include "audiod/loop.h"
#include "audiod/media-handlers.h"
#include "audiod/playback.h"
#include "backend/pcm-input.h"
#include "backend/presentation-clock.h"
#include "sync/clock-wire.h"
#include "sync/group-leader.h"
#include "sync/group-member.h"
#include "sync/sync-clock.h"

#include "cadence/audiod/v1/audiod.pb.h"

#include <atomic>
#include <cstdint>
#include <functional>
#include <memory>
#include <mutex>
#include <optional>
#include <string>
#include <vector>

namespace cadence::audiod {

// the sync plane's clock: CLOCK_MONOTONIC, or in a sim build the skewed one CADENCE_SIM_CLOCK_OFFSET_MS and
// CADENCE_SIM_CLOCK_PPM describe (containers share one kernel clock); started at the first call
sync::SyncClock groupClock();

// multiroom playback (GroupConfig from cadenced, GroupStatus back). a leader binds the group's port, makes its group
// feed the music target of the media sessions (so whatever source plays feeds the group) and plays the group's
// timeline itself through the same playout as its members; a pause stops the group on one sample and the paused
// source resumes from what was heard, a seek, a new track or a stop flushes it, and its MediaState carries the heard
// position. a member dials its leader and plays the stream as a GROUP session (GroupSource) that displaces its own
// sources as any newer session does; its transport commands go to the leader. each device's duck is sent to the
// leader, which ors them into the group's. GroupStatus goes out on every change and every 5 s while enabled; logs
// carry ids, tokens and numbers alone (rule 8). a new GroupConfig that differs from the one in force starts over;
// enabled false and cadenced going away leave the group (the audio fades)
class GroupHandlers final : public Part {
 public:
  GroupHandlers(Loop& loop, IpcServer& ipc, const Log& log, Playback& playback, MediaHandlers& media);
  ~GroupHandlers() override;
  GroupHandlers(const GroupHandlers&) = delete;
  GroupHandlers& operator=(const GroupHandlers&) = delete;

  void configure(const Configure& configuration) override;
  void ready() override;
  void reset() override;

  // the microphone a calibration records (Capture::record); none: every calibration is refused no_capture
  using RecordMic =
      std::function<void(std::int64_t fromMono, std::int64_t toMono, std::function<void(sync::MicResult)> done)>;
  void useMic(RecordMic record, RecordMic native = {}, std::function<void()> cancelNative = {}) {
    record_ = std::move(record);
    recordNative_ = std::move(native);
    cancelNative_ = std::move(cancelNative);
  }

  // the group's media timeline as this device hears it at nowMono (CLOCK_MONOTONIC): a member's playout, or a leader's
  // own; none outside a group and before anything was heard (the tap's GroupClock, multiroom P4). on the loop thread
  [[nodiscard]] std::optional<GroupClockSample> heardClock(std::int64_t nowMono) const;

 private:
  // the playhead the loop reads off the mixer, for the playout threads
  class SharedPlayhead {
   public:
    void store(std::optional<Playhead> playhead);
    [[nodiscard]] std::optional<Playhead> load() const;

   private:
    mutable std::mutex mutex_;
    std::optional<Playhead> playhead_;
  };

  void apply(const cadence_audiod_v1_GroupConfig& config);
  // GroupCalibrate: a leader runs the group's acoustic calibration, answered by GroupCalibrated
  void calibrate(const cadence_audiod_v1_GroupCalibrate& request);
  void calibrated(const std::string& requestId, const sync::CalibrationOutcome& outcome);
  void tracked(const sync::calibration::TrackingReport& report);
  [[nodiscard]] sync::CalibrationHooks calibrationHooks(bool member);
  void teardown(std::string_view reason);
  bool startLeader(const cadence_audiod_v1_GroupConfig& config);
  bool startMember(const cadence_audiod_v1_GroupConfig& config);
  void fail(std::string_view error);
  void retryBind();
  void announceStarted(const cadence_audiod_v1_GroupConfig& config);
  void memberMetadata(const cadence_sync_v1_Metadata& metadata);
  // a member's group_rejoin, group_clock and group_summary lines
  void logRejoin(const sync::GroupRejoinNote& note) const;
  void logClock(const sync::GroupClockNote& note) const;
  void logSummary(const sync::GroupSummary& summary) const;
  // false: the state waits (see MediaHandlers::GroupHooks::published)
  bool leaderPublished(const MediaSession& session);
  void ensureSource();
  void tick();
  // once each of the group's threads raised its priority: what it came to
  void logThreads();
  void sendStatus();
  // what a GroupStatus says of the states, to tell a change apart; none without a group
  [[nodiscard]] std::optional<std::vector<std::uint32_t>> reportKey() const;
  void armStatus();
  [[nodiscard]] sync::GroupPlayout::PlayheadSource playheadSource();
  // the sim's alignment probe (group_heard lines), none in a device's build
  [[nodiscard]] std::function<void(std::uint32_t, std::uint64_t, std::int64_t)> heardProbe() const;

  Loop& loop_;
  IpcServer& ipc_;
  const Log& log_;
  Playback& playback_;
  MediaHandlers& media_;
  sync::SyncClock clock_;
  std::shared_ptr<SharedPlayhead> playhead_ = std::make_shared<SharedPlayhead>();
  // counts groups, so a report a stopped one posted is told apart
  std::uint64_t generation_ = 0;

  std::optional<cadence_audiod_v1_GroupConfig> config_;
  std::string failed_;
  PcmInput feed_;
  std::unique_ptr<sync::GroupLeader> leader_;
  // the leader's feed as a ring pump and the tap see it, while leading
  std::unique_ptr<LeaderFeed> leaderFeed_;
  std::unique_ptr<sync::GroupMember> member_;
  // the member's GROUP session source while the sessions hold it
  std::optional<std::uint64_t> sourceSerial_;
  GroupSource* source_ = nullptr;
  std::optional<cadence_sync_v1_Metadata> lastMetadata_;
  // the leader's playing track as its session names it: a change the sessions made alone is a skip
  struct Track {
    std::string session;
    std::string title;
    std::string artist;
    std::string album;
    std::uint64_t durationMs = 0;
    bool operator==(const Track&) const = default;
  };
  std::optional<Track> track_;
  // the leader's session ended while its group still plays what it sent: the end goes out once that was heard
  bool endHeld_ = false;
  bool threadsLogged_ = false;
  bool leaderPlaying_ = false;
  std::optional<std::vector<std::uint32_t>> lastReport_;
  std::uint64_t ticks_ = 0;
  bool announced_ = false;
  std::optional<Loop::TimerId> tick_;
  std::optional<Loop::TimerId> status_;
  // a leader's bind that failed, tried again (retryBind)
  std::optional<Loop::TimerId> bindRetry_;
  unsigned bindRetries_ = 0;

  RecordMic record_;
  RecordMic recordNative_;
  std::function<void()> cancelNative_;
  // the request a running calibration answers
  std::optional<std::string> calibrating_;
  // the listener_result requests waiting for their run's outcome
  std::vector<std::string> listenerWaits_;
  // what the loop saw last, for a calibration's gate on the stream's thread: the volume holds, and another source plays
  struct GateState {
    std::atomic<bool> open{false};
    std::atomic<bool> busy{false};
  };
  std::shared_ptr<GateState> gate_ = std::make_shared<GateState>();
};

}  // namespace cadence::audiod
