#pragma once

#include "render/render-backend.h"

namespace cadence::screen::render {

// RenderBackend on gles 3.0. made and destroyed with the gl context current; nothing here survives a context loss,
// so the app destroys it before the context goes and makes a new one after
class GlesBackend final : public RenderBackend {
 public:
  GlesBackend();
  ~GlesBackend() override;
  GlesBackend(const GlesBackend&) = delete;
  GlesBackend& operator=(const GlesBackend&) = delete;

  // false when its own programs did not build (a broken driver): nothing can be drawn
  [[nodiscard]] bool ok() const { return quad_[0].id != 0 && fullscreenVao_ != 0; }

  void beginFrame(Size size) override;
  [[nodiscard]] Size frameSize() const override { return frame_; }
  void resetState() override;

  Texture createTexture(Size size, PixelFormat format, const void* pixels, int stride) override;
  bool updateTexture(const Texture& texture, Rect region, const void* pixels, int stride) override;
  void destroyTexture(Texture& texture) override;

  RenderTarget createRenderTarget(Size size, bool stencil) override;
  void destroyRenderTarget(RenderTarget& target) override;
  void bindRenderTarget(const RenderTarget* target) override;
  [[nodiscard]] Size boundSize() const override { return bound_; }
  void clear(float r, float g, float b, float a) override;

  void setScissor(std::optional<Rect> rect) override;
  void setBlend(Blend blend) override;

  Program compileProgram(std::string_view vertex, std::string_view fragment, std::string* error) override;
  void destroyProgram(Program& program) override;
  void setUniform(const Program& program, const char* name, float value) override;
  void setUniform(const Program& program, const char* name, float x, float y) override;
  void setUniform(const Program& program, const char* name, float x, float y, float z, float w) override;
  void setUniform(const Program& program, const char* name, const Mat3& value) override;

  void drawTexturedQuad(const Texture& texture, const QuadParams& params) override;
  void drawFullscreen(const Program& program, std::span<const Texture> textures, const Mat3& uv, float opacity,
                      Blend blend) override;

 private:
  int uniform(const Program& program, const char* name);

  // by (r8 ? 2 : 0) | (premul ? 1 : 0): a fragment shader each, so none branches on a uniform
  Program quad_[4];
  std::uint32_t fullscreenVao_ = 0;
  Size frame_;
  Size bound_;
  std::optional<Rect> scissor_;
};

// a scissor rect, top-left origin, as gl's bottom-left one inside a target of that size; clamped, never negative
Rect toGlScissor(Rect rect, Size target);

}  // namespace cadence::screen::render
