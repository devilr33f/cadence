#pragma once

#include <array>

namespace cadence {

// short-lived perceptual energy, finite 0..1; bands are bass, mid and treble
struct MusicLevels {
  float level = 0;
  std::array<float, 3> bands{};
};

}  // namespace cadence
