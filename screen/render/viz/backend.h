#pragma once

// what the visualizer and the ken burns quad draw through. inside screen/'s project, screen_backend.h implements it
// on stream A's RenderBackend (BackendCanvas) and the host's SharedContext on egl; alone (make screen-p-test) the
// cpu raster and the gl canvas stand in. nothing here touches gl
#include <cstdint>
#include <span>

namespace cadence::render {

// pixels of the target, origin top left
struct Rect {
  float x = 0, y = 0, w = 0, h = 0;
};

// straight (not premultiplied) rgba, 0..1
struct Color {
  float r = 0, g = 0, b = 0, a = 1;
};

// an image the backend has uploaded; 0 is none
using TextureId = std::uint32_t;

// a quad of a texture: uv is the part of the texture (0..1) drawn into dst
struct TexturedQuad {
  TextureId texture = 0;
  Rect dst;
  Rect uv{0, 0, 1, 1};
  float opacity = 1;
};

class Canvas {
 public:
  virtual ~Canvas() = default;

  [[nodiscard]] virtual int width() const = 0;
  [[nodiscard]] virtual int height() const = 0;
  // fill the target (alpha replaces)
  virtual void clear(Color c) = 0;
  // solid rects blended over the target, one draw call
  virtual void fill_rects(std::span<const Rect> rects, Color c) = 0;
  // a textured quad blended over the target; a canvas without textures ignores it
  virtual void draw_quad(const TexturedQuad& q) = 0;
};

}  // namespace cadence::render
