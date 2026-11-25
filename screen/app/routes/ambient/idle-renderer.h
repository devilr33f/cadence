#pragma once

#include "render/render-backend.h"

#include <cstdint>

namespace cadence::screen::routes {

// idle's own backdrop while no aerial clip or image shows (the owner: "make the idle screen look better"): slow
// aurora curtains over a deep gradient with a few twinkling stars, coloured by the time of day and dimmer at night,
// always moving so nothing burns in. one full-screen draw, at most ~20 frames a second (the page asks no faster)
class IdleRenderer {
 public:
  void draw(render::RenderBackend& backend, render::Rect screen, double nowS, int localHour, bool reducedMotion);
  void release();
  void abandon();

 private:
  render::RenderBackend* backend_ = nullptr;
  render::Program program_{};
  bool failed_ = false;
};

// the palette of an hour 0-23: two colours (0xRRGGBB) and a brightness 0..1
struct IdlePalette {
  std::uint32_t a, b;
  float brightness;
};
IdlePalette idlePalette(int localHour);

}  // namespace cadence::screen::routes
