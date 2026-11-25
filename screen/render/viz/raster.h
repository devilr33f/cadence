#pragma once

#include "backend.h"

#include <cstdint>
#include <string>
#include <vector>

namespace cadence::render::viz {

// a Canvas in memory (rgba8, straight alpha), for golden images and the preview harness; it draws no textures
class RasterCanvas final : public Canvas {
 public:
  RasterCanvas(int width, int height);

  [[nodiscard]] int width() const override { return w_; }
  [[nodiscard]] int height() const override { return h_; }
  void clear(Color c) override;
  void fill_rects(std::span<const Rect> rects, Color c) override;
  void draw_quad(const TexturedQuad&) override {}

  [[nodiscard]] const std::vector<std::uint8_t>& pixels() const { return px_; }
  // the image as a png (rgba8): the same pixels make the same bytes
  [[nodiscard]] std::vector<std::uint8_t> png() const;

 private:
  int w_, h_;
  std::vector<std::uint8_t> px_;
};

// a png of rgba8 rows; deflate with fixed huffman codes and run matches, deterministic
std::vector<std::uint8_t> encode_png(int width, int height, const std::vector<std::uint8_t>& rgba);

}  // namespace cadence::render::viz
