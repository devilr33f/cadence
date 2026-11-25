#pragma once

#include <cstddef>
#include <span>

namespace cadence::render::viz {

struct StrobeResult {
  // the most flashes found in any one second
  std::size_t max_flashes_per_s = 0;
  // more than 3 flashes in some second (photosensitivity, tv plan item 16)
  bool strobing = false;
};

// flashes in a series of frames' mean relative luminance (0..1) at fps: a flash is a pair of opposing swings of at
// least 0.1 where the darker side is below 0.8 (the wcag general flash's thresholds over the whole frame, which
// is how a milkdrop preset flashes); a preset with more than 3 in any second is dropped from the curated list
StrobeResult check_strobe(std::span<const double> luma, double fps);

}  // namespace cadence::render::viz
