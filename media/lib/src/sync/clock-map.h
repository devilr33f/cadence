#pragma once

#include "sync/clock-estimator.h"

#include <atomic>
#include <cstdint>
#include <optional>

namespace cadence::sync {

// the leader's clock as a line through the member's: leader = local + offsetNs + skew * (local - atNs)
struct ClockMapping {
  double offsetNs = 0;
  double skew = 0;
  std::int64_t atNs = 0;
  // the estimate's error figure (its split) when this was published
  double errorNs = 0;
  // moves on whenever the mapping stepped instead of slewing: audio placed by an older generation is off by the step
  std::uint64_t generation = 0;
  // the error was under the group's budget when published
  bool synced = false;

  [[nodiscard]] std::int64_t toLeader(std::int64_t localNs) const;
  [[nodiscard]] std::int64_t toLocal(std::int64_t leaderNs) const;
};

struct ClockSmootherConfig {
  // how fast the published offset may move toward a new estimate, as a rate (50 ppm: 50 µs a second). the spike's
  // estimates moved ~20-30 µs between fits; a slewed mapping turns that into a rate the playout's steering follows
  // instead of a jump it would chase
  double maxSlew = 50e-6;
  // a disagreement past this is a step: the mapping jumps to the estimate and its generation moves on
  double stepNs = 5'000'000;
  // the error under which a member is synced: 100 µs for a stereo pair, 1 ms in one room, 5 ms across rooms
  double syncedBelowNs = 1'000'000;
  // a window shorter than this is never synced, however small its split: a few lucky samples say little
  std::int64_t minSpanNs = 3'000'000'000;
};

// turns successive estimates into the published mapping: the first one as it is, then each one slewed toward, and
// past stepNs, stepped to. one thread
class ClockSmoother {
 public:
  explicit ClockSmoother(ClockSmootherConfig config = {});

  // the mapping after an estimate made at its own at
  ClockMapping update(const ClockEstimate& estimate);
  void reset();
  [[nodiscard]] const std::optional<ClockMapping>& current() const { return current_; }
  // how far the last step jumped (estimate minus the published line), 0 before one
  [[nodiscard]] double lastStepNs() const { return lastStepNs_; }

 private:
  ClockSmootherConfig config_;
  std::optional<ClockMapping> current_;
  std::uint64_t generation_ = 0;
  double lastStepNs_ = 0;
};

// the mapping one thread publishes and any other reads without waiting (the playback thread): a sequence lock over
// its fields, as PresentationClock's
class SharedClockMapping {
 public:
  void store(const ClockMapping& mapping);
  void clear();
  // none until a store, and after a clear
  [[nodiscard]] std::optional<ClockMapping> load() const;

 private:
  std::atomic<std::uint32_t> lock_{0};
  std::atomic<bool> valid_{false};
  std::atomic<double> offsetNs_{0};
  std::atomic<double> skew_{0};
  std::atomic<std::int64_t> atNs_{0};
  std::atomic<double> errorNs_{0};
  std::atomic<std::uint64_t> generation_{0};
  std::atomic<bool> synced_{false};
};

}  // namespace cadence::sync
