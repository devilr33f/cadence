#pragma once

#include <cstddef>
#include <cstdint>
#include <deque>
#include <optional>
#include <vector>

namespace cadence::sync {

// one exchange: t1 member send, t2 leader receive, t3 leader send, t4 member receive; t1 and t4 on the member's
// CLOCK_MONOTONIC, t2 and t3 on the leader's, ns
struct ClockSample {
  std::int64_t t1 = 0;
  std::int64_t t2 = 0;
  std::int64_t t3 = 0;
  std::int64_t t4 = 0;

  // time on the wire both ways, the leader's turnaround taken out
  [[nodiscard]] std::int64_t rtt() const { return (t4 - t1) - (t3 - t2); }
  // leader minus member, assuming both legs took as long
  [[nodiscard]] std::int64_t offset() const { return ((t2 - t1) + (t3 - t4)) / 2; }
  // the member's time halfway through, what the offset is measured at
  [[nodiscard]] std::int64_t mid() const { return t1 + (t4 - t1) / 2; }
};

struct ClockEstimatorConfig {
  // how far back samples are kept
  std::int64_t windowNs = 30'000'000'000;
  // the share of the window, fastest exchanges first, that the fit uses: wi-fi queueing and power save only ever add
  // delay, so the fastest exchanges are the least asymmetric (the spike's 10 % held midi to ~25 µs)
  double keep = 0.1;
  // what a fit needs
  std::size_t minSamples = 8;
};

// the leader's clock seen from the member at member time at
struct ClockEstimate {
  std::int64_t at = 0;
  // leader minus member at at
  double offsetNs = 0;
  // how much faster the leader's clock runs, as a fraction (1e-6 = 1 ppm)
  double skew = 0;
  // rms distance of the fitted samples from the line
  double residualNs = 0;
  // half the disagreement at at between fits of the even and the odd fitted samples: how much the estimate moves
  // with the samples it happened to get, the error figure that matters for playback (it tracked the true error on
  // the spike's simulated networks)
  double splitNs = 0;
  std::size_t used = 0;
  std::size_t total = 0;
  std::int64_t rttMinNs = 0;
  std::int64_t rttMaxUsedNs = 0;
  // the median of the whole window's: how much slower than the fastest a typical exchange is (wi-fi queueing)
  std::int64_t rttP50Ns = 0;
  // the member-time span the window covers
  std::int64_t spanNs = 0;
};

// a window of exchanges, fitted on demand: the fastest share, a least-squares line of offset against member time.
// one thread
class ClockEstimator {
 public:
  explicit ClockEstimator(ClockEstimatorConfig config = {});

  // an exchange with a negative rtt (a broken stamp) is dropped; samples older than the window are let go
  void add(const ClockSample& sample);
  // none until minSamples
  [[nodiscard]] std::optional<ClockEstimate> estimate(std::int64_t at) const;
  void reset();
  [[nodiscard]] std::size_t size() const { return samples_.size(); }

 private:
  ClockEstimatorConfig config_;
  std::deque<ClockSample> samples_;
  mutable std::vector<ClockSample> scratch_;
  mutable std::vector<ClockSample> half_;
};

}  // namespace cadence::sync
