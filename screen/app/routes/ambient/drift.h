#pragma once

#include <cmath>
#include <cstdint>

namespace cadence::screen::routes {

// burn-in drift: a point that walks a box's diagonals at a fixed speed, bouncing at the edges (a triangle wave per
// axis with different periods, so it covers the box instead of retracing a line). positions are whole dp, so the
// layer redraws only when it has moved a whole dp: ambient's overlay at 8 dp a minute redraws every 7.5 s, audio
// mode's group at 1 dp every 2 s
struct DriftOffset {
  int x = 0;
  int y = 0;
  bool operator==(const DriftOffset&) const = default;
};

inline double triangle(double travelled, double span) {
  if (span <= 0) return 0;
  const double period = 2 * span;
  const double m = std::fmod(travelled, period);
  return m <= span ? m : period - m;
}

// the offset after tS seconds at speedDpPerS inside a w × h dp box (0..w, 0..h)
inline DriftOffset drift(double tS, double speedDpPerS, double w, double h) {
  // the path's direction: 3 across for 2 down, so x and y bounce out of step
  const double d = tS * speedDpPerS;
  const double ux = 3.0 / std::sqrt(13.0), uy = 2.0 / std::sqrt(13.0);
  return DriftOffset{static_cast<int>(std::floor(triangle(d * ux, w))), static_cast<int>(std::floor(triangle(d * uy, h)))};
}

}  // namespace cadence::screen::routes
