#pragma once

#include "sync/calibration/solve.h"

#include <cstddef>
#include <cstdint>
#include <deque>
#include <map>
#include <string>
#include <vector>

namespace cadence::sync::calibration {

struct TrackingConfig {
  std::size_t windowCycles = 6;
  std::int64_t deadbandNs = 1'000'000;
  std::int64_t maxCorrectionNs = 5'000'000;
  std::uint32_t requiredWindows = 3;
  std::int64_t correctionCooldownNs = 60'000'000'000;
  // a 2 ms step is 93.6 % through the filter on its third window; between-cycle spread counts only when stable
  double smoothingAlpha = 0.6;
};

struct TrackingMember {
  std::string id;
  std::int32_t offsetUs = 0;
  std::uint32_t ci95Us = 0;
  std::uint32_t cycles = 0;
  std::int32_t correctionUs = 0;
  std::uint32_t correctionId = 0;
};

struct TrackingReport {
  // tracking or needs_calibration; the leader supplies off, idle and unsupported around this reducer
  std::string state = "tracking";
  std::vector<TrackingMember> members;
};

// completed one-round cycles in, a six-cycle robust solve and bounded correction suggestions out. no audio, clock or
// thread lives here; corrections are suggestions only, because the server owns the per-output trim store
class TrackingWindow {
 public:
  explicit TrackingWindow(TrackingConfig config = {}) : config_(config) {}

  TrackingReport add(const Plan& plan, const std::vector<std::vector<HeardV2>>& heard, std::int64_t nowNs);
  void reset();

 private:
  struct Cycle {
    Plan plan;
    std::vector<std::vector<HeardV2>> heard;
    Solution solution;
  };

 private:
  struct Smoothed {
    double offsetNs = 0;
    double ci95Ns = 0;
    bool set = false;
    std::uint32_t qualifying = 0;
    std::int64_t lastCorrectionNs = 0;
    double selfNs = 0;
    bool selfSet = false;
  };

  TrackingConfig config_;
  std::deque<Cycle> cycles_;
  std::map<std::string, Smoothed> smoothed_;
  // pausing resets the measurements, not these ids: the server must not take a resumed correction for a replay
  std::map<std::string, std::uint32_t> correctionIds_;
};

}  // namespace cadence::sync::calibration
