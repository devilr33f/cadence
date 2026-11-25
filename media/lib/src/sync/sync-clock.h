#pragma once

#include "sync/clock-socket.h"

#include <cmath>
#include <cstdint>

namespace cadence::sync {

// the clock the sync plane stamps by: CLOCK_MONOTONIC itself on a device, a skewed and offset one in the sim, where
// every container shares one kernel clock and a test needs two that differ. sync = mono + offset + ppm * 1e-6 *
// (mono - start); the playout turns a due time back into mono with toMono before it places audio
struct SyncClock {
  std::int64_t offsetNs = 0;
  double ppm = 0;
  std::int64_t startNs = 0;

  [[nodiscard]] bool identity() const { return offsetNs == 0 && ppm == 0; }
  [[nodiscard]] std::int64_t toSync(std::int64_t mono) const {
    if (identity())
      return mono;
    return mono + offsetNs + static_cast<std::int64_t>(std::llround(ppm * 1e-6 * static_cast<double>(mono - startNs)));
  }
  // the inverse, written around start so no product of two clock readings is formed
  [[nodiscard]] std::int64_t toMono(std::int64_t sync) const {
    if (identity())
      return sync;
    const double d = static_cast<double>(sync - offsetNs - startNs);
    return startNs + static_cast<std::int64_t>(std::llround(d / (1.0 + ppm * 1e-6)));
  }
  [[nodiscard]] std::int64_t now() const { return toSync(monoNs()); }
};

}  // namespace cadence::sync
