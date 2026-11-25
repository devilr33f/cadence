#pragma once

#include "../backend.h"

#include <vector>

namespace cadence::render::viz {

// the Canvas the classic styles draw through, into the bound fbo: rects in one draw per colour (two triangles each,
// one small vertex buffer), no textures. stream A's RenderBackend may replace it once it lands
class GlCanvas final : public Canvas {
 public:
  bool init(int width, int height);
  void release();

  [[nodiscard]] int width() const override { return w_; }
  [[nodiscard]] int height() const override { return h_; }
  void clear(Color c) override;
  void fill_rects(std::span<const Rect> rects, Color c) override;
  void draw_quad(const TexturedQuad&) override {}

 private:
  int w_ = 0, h_ = 0;
  unsigned program_ = 0, vao_ = 0, vbo_ = 0;
  int u_color_ = -1, u_size_ = -1;
  std::vector<float> verts_;
};

}  // namespace cadence::render::viz
