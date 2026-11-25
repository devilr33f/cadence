#pragma once

#include "backend/pcm-input.h"
#include "sync/calibration-run.h"
#include "sync/clock-client.h"
#include "sync/clock-server.h"
#include "sync/group-diagnostics.h"
#include "sync/group-playout.h"
#include "sync/group-threads.h"
#include "sync/stream-member.h"
#include "sync/sync-clock.h"

#include <atomic>
#include <condition_variable>
#include <cstdint>
#include <deque>
#include <functional>
#include <memory>
#include <mutex>
#include <optional>
#include <string>
#include <thread>
#include <vector>

namespace cadence::sync {

// a group device's state as GroupStatus carries it (cadence.audiod.v1.GroupState's values)
enum class GroupRunState : std::uint8_t { Off, Idle, Connecting, Syncing, Buffering, Playing, Rejoining, Failed };

struct GroupMemberConfig {
  std::string groupId;
  std::string memberId;
  std::string leaderHost;
  std::uint16_t port = 7701;
  // the clock master when it is not the leader (empty: the leader serves the clock), on clockPort (0: port)
  std::string clockHost;
  std::uint16_t clockPort = 0;
  // this member is the clock master: it serves the group clock over udp on clockHost's port number (clockPort, else
  // port; 0 picks one) on every address of clockBind's family, and its own clock is the group's (identity)
  bool serveClock = false;
  std::string clockBind;
  std::vector<std::uint8_t> key;
  // the clock error under which it counts as synced
  std::uint32_t budgetUs = 1000;
  GroupPlayoutConfig playout;
  ClockClientConfig clock;
  int backoffMinMs = 250;
  int backoffMaxMs = 5000;
  // whether it plays from the start, or waits for setActive (audiod: until its GROUP session holds the speaker)
  bool active = true;
  // advertised in the initial and periodic Status; tests can set 1 to stand in for a legacy member
  std::uint32_t calibrationVersion = kTrackingVersion;
  // this member's active output kind, sent with Status so the leader skips bluetooth's ultrasonic slot
  std::string outputKind;
  // accept and answer opt-in ultrasonic cycles; the leader never sends them to a mixed-version group
  bool trackingEnabled = false;
};

struct GroupMemberReport {
  GroupRunState state = GroupRunState::Connecting;
  // a token: unreachable, refused, auth_failed, wrong_group, protocol; empty while all is well
  std::string error;
  bool connected = false;
  bool synced = false;
  std::uint32_t clockErrorUs = 0;
  std::uint32_t queueMs = 0;
  std::int32_t steerPpm = 0;
  std::uint32_t rejoins = 0;
  std::uint32_t underruns = 0;
  std::int32_t placementErrorUs = 0;
  std::uint32_t latencyMs = 0;
};

// what the leader says that is not audio, on the member's own threads (audiod posts it to its loop)
struct GroupMemberEvents {
  std::function<void(const cadence_sync_v1_Metadata& metadata)> metadata;
  std::function<void(bool active)> duck;
  // the report moved on (a state, an error)
  std::function<void()> changed;
  // what a calibration the leader starts records and asks first; none: every calibration is refused (no_capture)
  CalibrationHooks calibration;
  // why its audio or clock dropped (group-diagnostics.h), rate-limited, and a summary every 30 s while it plays; on
  // the playout's thread
  std::function<void(const GroupRejoinNote& note)> rejoined;
  std::function<void(const GroupClockNote& note)> clock;
  std::function<void(const GroupSummary& summary)> summary;
};

// a member of a group: the stream from the leader, the group clock fitted to the master (the leader, or the device
// clockHost names; a member that is the master serves it instead and plays by its own clock), and the playout that places the
// leader's frames into the music input. it plays while active (audiod's GROUP session holds the speaker); inactive it
// lets go of the input and asks for the stream again when it is active once more. three threads: the stream's, the
// clock's and the playout's; every public call is safe from any thread
class GroupMember {
 public:
  GroupMember(PcmInput& out, GroupPlayout::PlayheadSource playhead, GroupMemberConfig config,
              GroupMemberEvents events);
  ~GroupMember();
  GroupMember(const GroupMember&) = delete;
  GroupMember& operator=(const GroupMember&) = delete;

  // false when its clock socket could not be opened or bound (bindFailed: the master's port is still held), or the
  // leader's or the master's address does not resolve
  bool start();
  [[nodiscard]] bool bindFailed() const { return bindFailed_; }
  // the port it serves the clock on (serveClock), 0 otherwise
  [[nodiscard]] std::uint16_t clockPort() const { return clockServer_ ? clockServer_->port() : 0; }
  // the audio fades and the stream says goodbye
  void stop();

  void setActive(bool active);
  // a transport command for the leader's session
  bool control(cadence_sync_v1_ControlAction action, std::uint64_t positionMs);
  // this device's own duck, which the leader ors into the group's
  void duckRequest(bool active);
  GroupTrimMode setTrim(std::int64_t ns);
  void setChannel(GroupChannelMap channel);
  void setOutputKind(std::string kind);

  [[nodiscard]] GroupMemberReport report() const;
  [[nodiscard]] const GroupPlayout& playout() const { return playout_; }
  // what raising its threads' priorities came to
  [[nodiscard]] const GroupThreads& threads() const { return threads_; }
  [[nodiscard]] bool playing() const;
  // accepted ultrasonic cycles, for the loopback tests and numbers-only diagnostics
  [[nodiscard]] std::uint64_t trackingCycles() const { return trackingCycles_.load(); }

 private:
  void message(const SyncEnvelope& envelope);
  // the leader's CalibrateStart: its chirps placed (every round's), the window recorded, the analysis answered off the
  // stream's thread; with no microphone the chirps still play and it answers no_capture
  void calibrate(const cadence_sync_v1_CalibrateStart& start);
  void track(const cadence_sync_v1_TrackCycle& cycle);
  void cancelTracking();
  void answer(const cadence_sync_v1_TrackArrivals& arrivals);
  void answer(const cadence_sync_v1_CalibrateArrivals& arrivals);
  void progress(std::uint32_t id, std::uint32_t analysisBudgetMs);
  // a listener calibration's CalibrateStart from a leader before rounds (play_only): the chirps alone
  // whether the playout plays audio of another epoch now (music the calibration must not talk over)
  [[nodiscard]] bool playingOther(std::uint32_t epoch) const;
  void playChirp(const cadence_sync_v1_CalibrateStart& start);
  void welcomed(std::uint32_t latencyMs);
  void sendStatus(const GroupMemberReport& report);
  void lost(std::string_view reason);
  void runPlayout();
  // the playout's thread: tells events_ of a rejoin, a clock event, a sample and the summary due
  void diagnose(std::int64_t now, const std::optional<ClockMapping>& m, RejoinReason reason, bool active);
  void join();
  [[nodiscard]] std::optional<ClockMapping> mapping() const;
  void changed();
  [[nodiscard]] std::string outputKind() const;

  GroupMemberConfig config_;
  GroupMemberEvents events_;
  GroupPlayout playout_;
  GroupThreads threads_;
  std::unique_ptr<ClockClient> clock_;
  // the group clock it serves as the master
  std::unique_ptr<ClockServer> clockServer_;
  bool bindFailed_ = false;
  std::unique_ptr<MemberStream> stream_;
  std::thread thread_;
  std::atomic<bool> stop_{false};
  std::atomic<bool> active_{true};
  std::atomic<bool> joinPending_{false};

  mutable std::mutex mutex_;
  std::condition_variable wake_;
  std::string error_;
  bool connected_ = false;
  std::uint32_t latencyMs_ = 0;
  std::uint32_t readyEpoch_ = 0;
  std::uint32_t rejoins_ = 0;
  // a welcome came before: the next one is a reconnect
  bool welcomedBefore_ = false;
  // the link's last loss token, and a reconnect counted as a rejoin that diagnose has not told of yet
  std::string lastLost_;
  std::optional<std::string> linkRejoin_;
  // the playout let go of the input after the last setActive(false)
  bool released_ = false;
  // when the last join went out, and how long the last one took to play again: the margin of the next
  std::int64_t joinAtNs_ = 0;
  std::int64_t lastRefillNs_ = 0;
  // the playout pumps until then (local mono) even while inactive: a calibration's chirp plays in an idle group
  std::atomic<std::int64_t> calibratingUntil_{0};
  // what a recording's completion reaches the member through: cleared on stop, so a late one does nothing
  struct Alive {
    std::mutex mutex;
    GroupMember* self = nullptr;
  };
  std::shared_ptr<Alive> alive_ = std::make_shared<Alive>();
  CalibrationWorker worker_;
  std::atomic<bool> ownDuck_{false};
  std::atomic<bool> groupDuck_{false};
  std::atomic<bool> trackingRecording_{false};
  std::atomic<std::uint32_t> trackingId_{0};
  std::atomic<std::uint64_t> trackingCycles_{0};
  // the playout's thread: the diagnostics and the mapping it saw last
  GroupDiagnostics diagnostics_;
  bool sawMapping_ = false;
  bool sawSynced_ = false;
  std::uint64_t sawGeneration_ = 0;
};

}  // namespace cadence::sync
