#include "sync/group-placer.h"

#include <algorithm>
#include <cmath>
#include <cstdlib>

namespace cadence::sync {

void GroupPlacer::reset() {
  integral_ = 0;
  smoothed_ = 0;
  steer_ = 0;
}

GroupPlacement GroupPlacer::place(std::int64_t errorNs, std::int64_t dtNs) {
  const auto magnitude = std::llabs(errorNs);
  if (magnitude > config_.rejoinNs)
    return {.action = GroupPlacement::Action::Rejoin, .steerPpm = steer_, .cutNs = errorNs};
  if (magnitude > config_.cutNs) {
    ++cuts_;
    // the cut takes the error out; the drift the integral holds is still the device's
    smoothed_ = 0;
    return {.action = GroupPlacement::Action::Cut, .steerPpm = steer_, .cutNs = errorNs};
  }
  const double us = static_cast<double>(errorNs) / 1000.0;
  smoothed_ += config_.smoothing * (static_cast<double>(errorNs) - smoothed_);
  const double max = config_.maxPpm;
  integral_ = std::clamp(integral_ + config_.integralPpmPerUsS * us * static_cast<double>(dtNs) / 1e9, -max, max);
  const double proportional = magnitude < config_.deadBandNs ? 0.0 : config_.ppmPerUs * us;
  steer_ = static_cast<std::int32_t>(std::lround(std::clamp(proportional + integral_, -max, max)));
  return {.action = GroupPlacement::Action::Steer, .steerPpm = steer_, .cutNs = 0};
}

}  // namespace cadence::sync
