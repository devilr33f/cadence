#pragma once

#include "sync/calibration-run.h"
#include "sync/calibration/track.h"
#include "sync/stream-leader.h"

#include <atomic>
#include <cstdint>
#include <functional>
#include <optional>
#include <string>
#include <utility>
#include <vector>

namespace cadence::sync {

struct GroupTrackerConfig {
  bool enabled = false;
  std::string memberId;
  std::string outputKind;
  // the server-allowed, ultrasonically capable member ids; only the leader reads this list
  std::vector<std::string> members;
  std::int64_t cyclePeriodNs = 10'000'000'000;
  std::int64_t deadbandNs = 1'000'000;
};

struct GroupTrackerEvents {
  // start runs on the leader's timeline: announce the cycle, mix this member's slot and record its native mic
  std::function<void(std::uint32_t id, std::uint32_t epoch, const calibration::Plan& plan)> start;
  std::function<void()> cancel;
  std::function<void(std::uint32_t id, std::uint32_t slots, std::uint32_t answers)> cycle;
  std::function<void(const calibration::TrackingReport& report)> report;
};

// the leader's opt-in live scheduler. a mixed-version group never sees a TrackCycle; a voice duck cancels the cycle
// and its native recording. the leader serializes tick and arrivals with its mutex
class GroupTracker {
 public:
  explicit GroupTracker(GroupTrackerConfig config);

  void tick(std::int64_t now, std::uint32_t epoch, bool playing, bool voice, const std::vector<LeaderMemberView>& views,
            const GroupTrackerEvents& events);
  void arrivals(const std::string& member, const cadence_sync_v1_TrackArrivals& arrivals, std::int64_t now);
  void cancel(const GroupTrackerEvents& events);
  void setOutputKind(std::string kind) { config_.outputKind = std::move(kind); }
  [[nodiscard]] std::uint64_t cycles() const { return cycles_.load(); }

 private:
  struct Active {
    CalibrationCollector collector;
    std::int64_t deadlineNs = 0;
  };

  void publish(const calibration::TrackingReport& report, bool force, const GroupTrackerEvents& events);

  GroupTrackerConfig config_;
  calibration::TrackingWindow window_;
  std::optional<Active> active_;
  std::int64_t nextStartNs_ = 0;
  std::atomic<std::uint32_t> cycles_{0};
  std::string published_;
};

}  // namespace cadence::sync
