#include "render/frame-stats.h"

#include <algorithm>

namespace cadence::screen::render {

FrameStats::FrameStats(std::size_t window, double refreshHz)
    : window_(window == 0 ? 1 : window), periodNs_(refreshHz > 0 ? 1e9 / refreshHz : 1e9 / 60.0) {
  samples_.reserve(window_);
}

void FrameStats::add(std::int64_t startNs, std::int64_t endNs, bool chained) {
  if (samples_.size() < window_) {
    samples_.push_back({startNs, endNs, chained});
  } else {
    samples_[next_] = {startNs, endNs, chained};
    next_ = (next_ + 1) % window_;
  }
}

void FrameStats::reset() {
  samples_.clear();
  next_ = 0;
}

FrameStatsSummary FrameStats::summary() const {
  FrameStatsSummary out;
  out.frames = samples_.size();
  if (samples_.empty()) return out;
  // oldest first
  std::vector<Sample> ordered;
  ordered.reserve(samples_.size());
  for (std::size_t i = 0; i < samples_.size(); i++) ordered.push_back(samples_[(next_ + i) % samples_.size()]);

  std::vector<double> work;
  work.reserve(ordered.size());
  for (const auto& s : ordered) work.push_back(static_cast<double>(s.endNs - s.startNs) / 1e6);
  std::sort(work.begin(), work.end());
  const auto at = [&](double q) {
    const auto i = static_cast<std::size_t>(q * static_cast<double>(work.size() - 1) + 0.5);
    return work[std::min(i, work.size() - 1)];
  };
  out.workP50Ms = at(0.5);
  out.workP95Ms = at(0.95);
  out.workMaxMs = work.back();

  double chainedNs = 0;
  for (std::size_t i = 1; i < ordered.size(); i++) {
    if (!ordered[i].chained) continue;
    const auto interval = static_cast<double>(ordered[i].endNs - ordered[i - 1].endNs);
    out.chained++;
    chainedNs += interval;
    if (interval > 1.5 * periodNs_) out.late++;
  }
  if (chainedNs > 0) out.chainedFps = static_cast<double>(out.chained) / (chainedNs / 1e9);
  if (ordered.size() > 1) {
    const double spanS = static_cast<double>(ordered.back().endNs - ordered.front().endNs) / 1e9;
    if (spanS > 0) out.fps = static_cast<double>(ordered.size() - 1) / spanS;
  }
  return out;
}

}  // namespace cadence::screen::render
