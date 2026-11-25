#include "sync/clock-estimator.h"

#include <algorithm>
#include <cmath>
#include <span>

namespace cadence::sync {

namespace {

struct Line {
  double offset = 0;
  double slope = 0;
  double rms = 0;
};

// least squares of offset against member time, centred on at. under a second of spread (rms) the slope is noise
// and the fit is a constant
Line fit(std::span<const ClockSample> ss, std::int64_t at) {
  const auto n = static_cast<double>(ss.size());
  double sx = 0;
  double sy = 0;
  for (const auto& s : ss) {
    sx += static_cast<double>(s.mid() - at);
    sy += static_cast<double>(s.offset());
  }
  const double mx = sx / n;
  const double my = sy / n;
  double sxx = 0;
  double sxy = 0;
  for (const auto& s : ss) {
    const double dx = static_cast<double>(s.mid() - at) - mx;
    sxx += dx * dx;
    sxy += dx * (static_cast<double>(s.offset()) - my);
  }
  Line line;
  if (sxx > n * 1e18)
    line.slope = sxy / sxx;
  line.offset = my - line.slope * mx;
  double se = 0;
  for (const auto& s : ss) {
    const double r = static_cast<double>(s.offset()) - (line.offset + line.slope * static_cast<double>(s.mid() - at));
    se += r * r;
  }
  line.rms = std::sqrt(se / n);
  return line;
}

}  // namespace

ClockEstimator::ClockEstimator(ClockEstimatorConfig config) : config_(config) {}

void ClockEstimator::add(const ClockSample& sample) {
  if (sample.rtt() < 0 || sample.t4 < sample.t1)
    return;
  samples_.push_back(sample);
  const auto cut = sample.t4 - config_.windowNs;
  while (!samples_.empty() && samples_.front().t4 < cut)
    samples_.pop_front();
}

void ClockEstimator::reset() { samples_.clear(); }

std::optional<ClockEstimate> ClockEstimator::estimate(std::int64_t at) const {
  const auto n = samples_.size();
  if (n < config_.minSamples || n == 0)
    return std::nullopt;
  scratch_.assign(samples_.begin(), samples_.end());
  std::sort(scratch_.begin(), scratch_.end(), [](const auto& a, const auto& b) { return a.rtt() < b.rtt(); });
  auto k = static_cast<std::size_t>(std::ceil(static_cast<double>(n) * config_.keep));
  k = std::clamp(k, config_.minSamples, n);
  const std::span<const ClockSample> used(scratch_.data(), k);
  const auto line = fit(used, at);

  ClockEstimate e;
  e.at = at;
  e.offsetNs = line.offset;
  e.skew = line.slope;
  e.residualNs = line.rms;
  e.used = k;
  e.total = n;
  e.rttMinNs = used.front().rtt();
  e.rttMaxUsedNs = used.back().rtt();
  e.rttP50Ns = scratch_[n / 2].rtt();
  e.spanNs = samples_.back().mid() - samples_.front().mid();

  if (k >= 6) {
    half_.clear();
    for (std::size_t i = 0; i < k; i += 2)
      half_.push_back(used[i]);
    const auto even = fit(half_, at);
    half_.clear();
    for (std::size_t i = 1; i < k; i += 2)
      half_.push_back(used[i]);
    const auto odd = fit(half_, at);
    e.splitNs = std::abs(even.offset - odd.offset) / 2;
  }
  return e;
}

}  // namespace cadence::sync
