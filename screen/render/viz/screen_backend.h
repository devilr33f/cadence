#pragma once

// the reconciliation with stream A (screen/render): the visualizer's and ken burns' Canvas drawn through A's
// RenderBackend, and the host's SharedContext as an egl context sharing objects with the screen's (surfaceless,
// else a 1x1 pbuffer). built only inside screen/'s project, where A's backend exists
#include "backend.h"
#include "host/host.h"
#include "render/render-backend.h"

#include <memory>

namespace cadence::render {

// Canvas over the RenderBackend, into whatever it has bound: solid rects through one small program (a scissored
// fullscreen draw each), textured quads through drawTexturedQuad. a texture id the visualizer hands out is a render
// target's colour (stored bottom-up), so quads are flipped back by default; ken burns' uploaded images are not
class BackendCanvas final : public Canvas {
 public:
  explicit BackendCanvas(screen::render::RenderBackend& backend);
  ~BackendCanvas() override;
  BackendCanvas(const BackendCanvas&) = delete;
  BackendCanvas& operator=(const BackendCanvas&) = delete;

  [[nodiscard]] bool ok() const { return program_.id != 0; }
  // textures drawn from now on are stored bottom-up (render targets) or top-down (uploads)
  void set_flipped(bool flipped) { flipped_ = flipped; }

  [[nodiscard]] int width() const override { return backend_.boundSize().width; }
  [[nodiscard]] int height() const override { return backend_.boundSize().height; }
  void clear(Color c) override;
  void fill_rects(std::span<const Rect> rects, Color c) override;
  void draw_quad(const TexturedQuad& q) override;

 private:
  screen::render::RenderBackend& backend_;
  screen::render::Program program_;
  bool flipped_ = true;
};

// the visualizer thread's context: made on the ui thread with the screen's context current, sharing its objects
std::unique_ptr<viz::SharedContext> make_shared_context();

}  // namespace cadence::render
