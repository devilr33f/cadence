#pragma once

#include <algorithm>
#include <cstdint>

// eased motion the ui drives itself (not rmlui's transitions): the page cross-fade's timing. pure, so the unit tests
// pin it without a gl context
namespace cadence::screen::ui::motion {

// cubic ease-in-out on [0, 1]: slow out of the start, slow into the end (clamped)
inline double easeInOut(double t) {
  t = std::clamp(t, 0.0, 1.0);
  return t < 0.5 ? 4 * t * t * t : 1 - (-2 * t + 2) * (-2 * t + 2) * (-2 * t + 2) / 2;
}

// cubic ease-out on [0, 1]
inline double easeOut(double t) {
  t = 1 - std::clamp(t, 0.0, 1.0);
  return 1 - t * t * t;
}

// the outgoing frame of a cross-fade: drawn over the incoming one at opacity(now), which eases 1 -> 0 over ms from
// startMs. idle (startMs < 0) or past its end it is 0, and the snapshot needs no drawing
struct Fade {
  std::int64_t startMs = -1;
  int ms = 0;

  void start(std::int64_t nowMs, int durationMs) {
    startMs = nowMs;
    ms = std::max(1, durationMs);
  }
  void stop() { startMs = -1; }
  [[nodiscard]] bool running(std::int64_t nowMs) const { return startMs >= 0 && nowMs < startMs + ms; }
  [[nodiscard]] double opacity(std::int64_t nowMs) const {
    if (startMs < 0) return 0;
    return 1 - easeInOut(static_cast<double>(nowMs - startMs) / ms);
  }
};

}  // namespace cadence::screen::ui::motion
