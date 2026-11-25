#include "render/capture.h"

#include "render/gl.h"

#include <cstring>

namespace cadence::screen::render {

Image readPixels(Size size) {
  Image image;
  if (size.width <= 0 || size.height <= 0) return image;
  image.width = size.width;
  image.height = size.height;
  const auto row = static_cast<std::size_t>(size.width) * 4;
  std::vector<std::uint8_t> raw(row * static_cast<std::size_t>(size.height));
  glPixelStorei(GL_PACK_ALIGNMENT, 1);
  glReadPixels(0, 0, size.width, size.height, GL_RGBA, GL_UNSIGNED_BYTE, raw.data());
  glPixelStorei(GL_PACK_ALIGNMENT, 4);
  // gl reads bottom row first
  image.rgba.resize(raw.size());
  for (int y = 0; y < size.height; y++) {
    std::memcpy(image.rgba.data() + static_cast<std::size_t>(y) * row,
                raw.data() + static_cast<std::size_t>(size.height - 1 - y) * row, row);
  }
  return image;
}

Image readTarget(const RenderTarget& target) {
  if (!target) return {};
  GLint previous = 0;
  glGetIntegerv(GL_FRAMEBUFFER_BINDING, &previous);
  glBindFramebuffer(GL_FRAMEBUFFER, target.fbo);
  Image image = readPixels(Size{target.color.width, target.color.height});
  glBindFramebuffer(GL_FRAMEBUFFER, static_cast<GLuint>(previous));
  return image;
}

std::uint64_t hashImage(const Image& image) {
  std::uint64_t h = 14695981039346656037ull;
  for (std::uint8_t b : image.rgba) {
    h ^= b;
    h *= 1099511628211ull;
  }
  return h;
}

}  // namespace cadence::screen::render
