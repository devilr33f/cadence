#pragma once

#include "render/perf.h"
#include "render/render-backend.h"

#include <cstdint>
#include <string>
#include <vector>

namespace cadence::screen::render::perf {

// the on-screen frame graph (CADENCE_SCREEN_HUD=1): a bar a presented frame (cpu work over the swap's wait, a red
// cap on a late one), the refresh period's line, and the window's numbers. drawn last, over everything, only in
// frames the loop presents anyway: it never asks for a frame of its own
class Hud {
 public:
  static constexpr int kWidth = 360;
  static constexpr int kHeight = 132;

  ~Hud();
  void draw(RenderBackend& backend, Size screen, const Recorder& recorder, const Summary& window);
  void release(RenderBackend& backend);

 private:
  Texture texture_{};
  std::vector<std::uint8_t> pixels_;
};

// the hud's pixels (premultiplied rgba, kWidth x kHeight): pure, for tests
void rasterHud(std::vector<std::uint8_t>& out, const std::vector<Recorder::Bar>& bars, const Summary& window,
               double periodMs);
// the text lines the hud prints
std::vector<std::string> hudLines(const Summary& window);

}  // namespace cadence::screen::render::perf
