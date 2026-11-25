#pragma once

#include "app/routes/audio/bars.h"
#include "render/render-backend.h"

#include <array>

namespace cadence::screen::routes {

// the classic visualizer as one background draw: the fragment shader finds the bar at a pixel and evaluates its
// capsule, so there are no rml transforms, per-bar draws, clip masks or stencil work. made and released with gl current
class BarRenderer {
 public:
  void draw(render::RenderBackend& backend, render::Rect screen, const std::array<double, BarSmoother::kBars>& heights,
            std::uint32_t colorA, std::uint32_t colorB);
  void release();
  void abandon();

 private:
  render::RenderBackend* backend_ = nullptr;
  render::Program program_{};
  bool failed_ = false;
};

}  // namespace cadence::screen::routes
