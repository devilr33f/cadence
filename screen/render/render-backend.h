#pragma once

#include <cstdint>
#include <optional>
#include <span>
#include <string>
#include <string_view>

namespace cadence::screen::render {

// pixels, top-left origin, on whatever target is bound
struct Rect {
  int x = 0;
  int y = 0;
  int width = 0;
  int height = 0;

  bool operator==(const Rect&) const = default;
};

struct Size {
  int width = 0;
  int height = 0;

  bool operator==(const Size&) const = default;
};

enum class PixelFormat {
  // 4 bytes a pixel, r g b a in memory
  Rgba8,
  // 1 byte a pixel (glyph atlases, masks), sampled as (1, 1, 1, r)
  R8,
};

// a gpu texture; flipped means its rows are stored bottom-up (a render target's colour, mpv's frame), which the
// quad helpers undo, so callers never flip by hand
struct Texture {
  std::uint32_t id = 0;
  int width = 0;
  int height = 0;
  PixelFormat format = PixelFormat::Rgba8;
  bool flipped = false;

  explicit operator bool() const { return id != 0; }
};

// an fbo with its colour texture (rgba8) and, if asked, a stencil buffer (rmlui's clip masks)
struct RenderTarget {
  std::uint32_t fbo = 0;
  std::uint32_t stencil = 0;
  Texture color;

  explicit operator bool() const { return fbo != 0; }
};

struct Program {
  std::uint32_t id = 0;

  explicit operator bool() const { return id != 0; }
};

enum class Blend {
  None,
  // src + (1 - src.a) * dst: rmlui's output, the ui layer over video
  Premultiplied,
  // src.a * src + (1 - src.a) * dst
  Alpha,
};

// a 2d affine transform as a column-major 3x3 (gl's mat3 layout); applied to uv in [0, 1]^2, top-left origin
struct Mat3 {
  float m[9] = {1, 0, 0, 0, 1, 0, 0, 0, 1};

  static Mat3 identity() { return {}; }
  // uv' = uv * scale + offset: a ken burns frame is a scale below 1 and an offset that drifts
  static Mat3 scaleOffset(float sx, float sy, float ox, float oy) {
    return Mat3{{sx, 0, 0, 0, sy, 0, ox, oy, 1}};
  }
};

struct QuadParams {
  // where, in the bound target's pixels
  Rect dst;
  // which part of the texture, uv top-left origin
  Mat3 uv = Mat3::identity();
  float opacity = 1.0f;
  Blend blend = Blend::Premultiplied;
};

// the uniforms and inputs a fullscreen program gets from drawFullscreen: a vertex shader of its own is not needed,
// as kFullscreenVertexShader passes v_uv (top-left origin, through u_uv) to the fragment shader; textures are bound
// to units 0..n-1 as u_tex0..u_texN, u_opacity is set, and u_time_s carries the caller's time
inline constexpr std::string_view kFullscreenVertexShader = R"(#version 300 es
uniform mat3 u_uv;
uniform bool u_flip;
out vec2 v_uv;
void main() {
  vec2 p = vec2(float((gl_VertexID << 1) & 2), float(gl_VertexID & 2));
  vec2 uv = vec2(p.x, 1.0 - p.y);
  v_uv = (u_uv * vec3(uv, 1.0)).xy;
  if (u_flip) v_uv.y = 1.0 - v_uv.y;
  gl_Position = vec4(p * 2.0 - 1.0, 0.0, 1.0);
}
)";

// the gles 3 drawing surface every layer of the screen shares: the ui (stream b's rmlui render interface is
// implemented on it), ken burns and the visualizers (stream p), the player's video. one thread, the one the gl context
// is current on. handles are plain values; destroying one twice, or a zero one, does nothing
class RenderBackend {
 public:
  virtual ~RenderBackend() = default;

  // starts a frame on the screen: binds it, sets the viewport to size, clears to opaque black, resets state
  virtual void beginFrame(Size size) = 0;
  // the size beginFrame was given
  [[nodiscard]] virtual Size frameSize() const = 0;
  // puts back the state this backend relies on, after mpv or anything else drew with raw gl
  virtual void resetState() = 0;

  // pixels may be null (undefined contents); stride in bytes, 0 = tightly packed
  virtual Texture createTexture(Size size, PixelFormat format, const void* pixels, int stride) = 0;
  virtual bool updateTexture(const Texture& texture, Rect region, const void* pixels, int stride) = 0;
  virtual void destroyTexture(Texture& texture) = 0;

  virtual RenderTarget createRenderTarget(Size size, bool stencil) = 0;
  virtual void destroyRenderTarget(RenderTarget& target) = 0;
  // null binds the screen; sets the viewport to the target's size
  virtual void bindRenderTarget(const RenderTarget* target) = 0;
  // the size of what is bound
  [[nodiscard]] virtual Size boundSize() const = 0;
  virtual void clear(float r, float g, float b, float a) = 0;

  // nullopt turns it off; the rect is clamped to the bound target
  virtual void setScissor(std::optional<Rect> rect) = 0;
  virtual void setBlend(Blend blend) = 0;

  // gles 3.0 sources (#version 300 es); on failure a zero program and the compiler's log in error
  virtual Program compileProgram(std::string_view vertex, std::string_view fragment, std::string* error) = 0;
  virtual void destroyProgram(Program& program) = 0;
  // uniforms of a program, by name; a name the program lacks is ignored
  virtual void setUniform(const Program& program, const char* name, float value) = 0;
  virtual void setUniform(const Program& program, const char* name, float x, float y) = 0;
  virtual void setUniform(const Program& program, const char* name, float x, float y, float z, float w) = 0;
  virtual void setUniform(const Program& program, const char* name, const Mat3& value) = 0;

  // the textured quad: texture into dst, its uv through params.uv, times opacity
  virtual void drawTexturedQuad(const Texture& texture, const QuadParams& params) = 0;
  // the whole bound target through a program made with kFullscreenVertexShader (ken burns, visualizers); the first
  // texture's flip is undone through u_flip
  virtual void drawFullscreen(const Program& program, std::span<const Texture> textures, const Mat3& uv,
                              float opacity, Blend blend) = 0;
};

}  // namespace cadence::screen::render
