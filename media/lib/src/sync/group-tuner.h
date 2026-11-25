#pragma once

#include <cstdint>
#include <deque>
#include <map>
#include <optional>
#include <string>
#include <string_view>
#include <vector>

// a group leader's auto-tune (multiroom tune A): the stream's latency, the opus bitrate and the start wait, decided from
// what the leader sees (its members' status, their rejoins and ready times, its own playout's underruns). pure: no
// clock, no thread; the leader feeds it events with their time (leader sync ns) and applies what it decides. one thread
// at a time
namespace cadence::sync {

struct GroupTunerConfig {
  bool autoLatency = false;
  bool autoBitrate = false;
  bool autoWait = false;
  // where the latency starts, 200..5000
  std::uint32_t latencyMs = 1000;
  // the bitrate's ceiling, 0: 256
  std::uint32_t bitrateKbps = 0;
  // pcm has no bitrate: auto bitrate is off with it
  bool pcm = false;
  // the start wait's ceiling, 0: 3000
  std::uint32_t waitMs = 0;
};

// one decision, as group_tuned logs it: what (latency, bitrate), from, to and why (underrun, rejoin, margin, backlog,
// clean)
struct GroupTuned {
  std::string_view what;
  std::uint32_t from = 0;
  std::uint32_t to = 0;
  std::string_view reason;
  bool operator==(const GroupTuned&) const = default;
};

inline constexpr std::uint32_t kTuneLatencyMinMs = 200;
inline constexpr std::uint32_t kTuneLatencyMaxMs = 5000;
// at most one raise this often
inline constexpr std::int64_t kTuneRaiseEveryNs = 10'000'000'000;
// this long playing with no raise trigger and a wide margin lowers it
inline constexpr std::int64_t kTuneLowerAfterNs = 300'000'000'000;
// queue samples this soon after an epoch's start say nothing of the margin (the members still fill)
inline constexpr std::int64_t kTuneSettleNs = 10'000'000'000;
// a member whose frames the leader has not sent yet past this is behind
inline constexpr std::uint32_t kTuneBacklogMs = 500;
inline constexpr std::int64_t kTuneRejoinWindowNs = 60'000'000'000;
inline constexpr std::int64_t kTuneStepDownEveryNs = 15'000'000'000;
inline constexpr std::int64_t kTuneStepUpAfterNs = 120'000'000'000;
inline constexpr std::size_t kTuneReadySamples = 8;
inline constexpr std::uint32_t kTuneWaitMinMs = 500;

class GroupTuner {
 public:
  explicit GroupTuner(GroupTunerConfig config);

  [[nodiscard]] const GroupTunerConfig& config() const { return config_; }
  // in force now
  [[nodiscard]] std::uint32_t latencyMs() const { return latencyMs_; }
  // 0 with pcm
  [[nodiscard]] std::uint32_t bitrateKbps() const;
  [[nodiscard]] std::uint32_t waitMs() const;
  // the latency the next epoch starts with
  [[nodiscard]] std::uint32_t pendingLatencyMs() const { return pendingMs_; }

  // time passes; playing: the group's epoch runs. returns a bitrate change to apply now
  std::optional<GroupTuned> tick(std::int64_t now, bool playing);
  // an epoch starts: the pending latency comes into force (its change returned), the queue's settling begins
  std::optional<GroupTuned> epochStarted(std::int64_t now);

  // a connected member's status: its cumulative underruns and its queue
  void memberStatus(const std::string& id, std::uint32_t underruns, std::uint32_t queueMs, std::int64_t now,
                    bool playing);
  // a member's frames the leader holds unsent, in ms of audio
  void memberBacklog(const std::string& id, std::uint32_t unsentMs, std::int64_t now, bool playing);
  // a member joined again (not its first join after it connected)
  void memberRejoined(const std::string& id, std::int64_t now, bool playing);
  // the leader's own playout's cumulative underruns
  void ownUnderruns(std::uint64_t count, std::int64_t now, bool playing);
  // a member went: its counters start over when it is back
  void memberGone(const std::string& id);
  // how long from an epoch's start a member took to be ready; a member that timed out is ready()'s ceiling
  void memberReady(const std::string& id, std::uint32_t delayMs);
  void memberTimedOut(const std::string& id);

 private:
  void raise(std::int64_t now, std::string_view reason);
  void lowerWindowRestart();

  GroupTunerConfig config_;
  std::uint32_t latencyMs_;
  std::uint32_t pendingMs_;
  std::string_view pendingReason_;
  std::optional<std::int64_t> lastRaiseNs_;
  // playing time since the last raise trigger (or the window's start), and the least queue a member had over it
  std::int64_t playedNs_ = 0;
  std::optional<std::uint32_t> minQueueMs_;
  std::optional<std::int64_t> lastTickNs_;
  std::optional<std::int64_t> epochNs_;
  std::map<std::string, std::uint32_t> underruns_;
  std::optional<std::uint64_t> ownUnderruns_;

  std::vector<std::uint32_t> ladder_;
  std::size_t rung_ = 0;
  std::optional<std::int64_t> lastStepDownNs_;
  // playing time with no step-down trigger
  std::int64_t cleanNs_ = 0;
  bool stepDownWanted_ = false;
  std::string_view stepDownReason_;
  std::map<std::string, std::deque<std::int64_t>> rejoins_;

  std::map<std::string, std::deque<std::uint32_t>> ready_;
};

}  // namespace cadence::sync
