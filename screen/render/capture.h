#pragma once

#include "render/render-backend.h"

#include <cstdint>
#include <vector>

namespace cadence::screen::render {

// rgba8 pixels, top row first
struct Image {
  int width = 0;
  int height = 0;
  std::vector<std::uint8_t> rgba;

  [[nodiscard]] const std::uint8_t* at(int x, int y) const {
    return rgba.data() + (static_cast<std::size_t>(y) * static_cast<std::size_t>(width) + static_cast<std::size_t>(x)) * 4;
  }
};

// reads what is bound (the screen or a render target) back, top row first; tests and debugging only, it stalls the
// gpu. a target whose colour is flipped (every render target) reads in the same orientation as the screen
Image readPixels(Size size);
// reads a render target, top row first
Image readTarget(const RenderTarget& target);

// fnv-1a 64 of the pixels, for stable-frame checks
std::uint64_t hashImage(const Image& image);

}  // namespace cadence::screen::render
