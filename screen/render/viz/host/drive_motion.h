#pragma once

#include "features.h"

#include <algorithm>
#include <cmath>

namespace cadence::render::viz {

// how fast the particles and the tunnel move, per second. driven by the average loudness and lifted through a drop
// section (drop_level), so quiet parts drift, build-ups stay restrained, and drops accelerate into sustained warp
// speed. the shader advances particles by 0.12 of this a second. a quiet interlude with the music still on keeps a
// slow drift — a hard stop mid-song reads as a stall, not as calm (ghost!'s 132-150 s froze near solid)
inline double drive_travel_rate(const FrameInputs& in) {
  const double e = std::max(0.0, static_cast<double>(in.energy_slow));
  const double d = std::max(0.0, static_cast<double>(in.drop_level));
  return std::max(0.85, 0.15 + (9.5 + 8.5 * d) * e * e);
}

}  // namespace cadence::render::viz
