#include "render/gles-backend.h"

#include "render/gl.h"

#include <algorithm>
#include <string>
#include <vector>

namespace cadence::screen::render {

namespace {

constexpr std::string_view kQuadVertex = R"(#version 300 es
uniform vec4 u_dst;
uniform mat3 u_uv;
uniform bool u_flip;
out vec2 v_uv;
void main() {
  vec2 c = vec2(float(gl_VertexID & 1), float((gl_VertexID >> 1) & 1));
  v_uv = (u_uv * vec3(c, 1.0)).xy;
  if (u_flip) v_uv.y = 1.0 - v_uv.y;
  vec2 p = u_dst.xy + c * u_dst.zw;
  gl_Position = vec4(p.x * 2.0 - 1.0, 1.0 - p.y * 2.0, 0.0, 1.0);
}
)";

// R8 and PREMUL are fixed per program (quadSource): v3d's compiler made each test of a uniform bool a per-fragment
// branch, and the two of them more than doubled a full-screen quad's gpu time on the pi (3.8 ms against 1.8)
constexpr std::string_view kQuadFragment = R"(#version 300 es
precision mediump float;
uniform sampler2D u_tex0;
uniform float u_opacity;
in vec2 v_uv;
out vec4 color;
void main() {
  vec4 t = texture(u_tex0, v_uv);
#if R8
  t = vec4(t.r, t.r, t.r, t.r);
#endif
#if PREMUL
  color = t * u_opacity;
#else
  color = vec4(t.rgb, t.a * u_opacity);
#endif
}
)";

// the quad's fragment shader with its two switches defined, after the version line
std::string quadSource(bool r8, bool premul) {
  std::string text(kQuadFragment);
  const auto line = text.find('\n') + 1;
  text.insert(line, std::string("#define R8 ") + (r8 ? "1" : "0") + "\n#define PREMUL " + (premul ? "1" : "0") + "\n");
  return text;
}

GLuint compileShader(GLenum type, std::string_view source, std::string* error) {
  std::string text(source);
#if defined(__APPLE__)
  // desktop gl 4.1 core's glsl is the same language, its own version word alone
  if (text.rfind("#version 300 es", 0) == 0) text.replace(0, 15, "#version 410 core");
#endif
  const char* line = text.c_str();
  const auto length = static_cast<GLint>(text.size());
  GLuint shader = glCreateShader(type);
  glShaderSource(shader, 1, &line, &length);
  glCompileShader(shader);
  GLint ok = 0;
  glGetShaderiv(shader, GL_COMPILE_STATUS, &ok);
  if (ok == 0) {
    if (error != nullptr) {
      char log[1024] = {};
      glGetShaderInfoLog(shader, sizeof log, nullptr, log);
      *error = log;
    }
    glDeleteShader(shader);
    return 0;
  }
  return shader;
}

GLenum glFormat(PixelFormat format) { return format == PixelFormat::R8 ? GL_RED : GL_RGBA; }
GLenum glInternal(PixelFormat format) { return format == PixelFormat::R8 ? GL_R8 : GL_RGBA8; }
int bytesPerPixel(PixelFormat format) { return format == PixelFormat::R8 ? 1 : 4; }

void applyBlend(Blend blend) {
  switch (blend) {
    case Blend::None:
      glDisable(GL_BLEND);
      break;
    case Blend::Premultiplied:
      glEnable(GL_BLEND);
      glBlendEquation(GL_FUNC_ADD);
      glBlendFunc(GL_ONE, GL_ONE_MINUS_SRC_ALPHA);
      break;
    case Blend::Alpha:
      glEnable(GL_BLEND);
      glBlendEquation(GL_FUNC_ADD);
      glBlendFuncSeparate(GL_SRC_ALPHA, GL_ONE_MINUS_SRC_ALPHA, GL_ONE, GL_ONE_MINUS_SRC_ALPHA);
      break;
  }
}

void setUnpack(int stride, PixelFormat format) {
  const int bpp = bytesPerPixel(format);
  glPixelStorei(GL_UNPACK_ALIGNMENT, 1);
  glPixelStorei(GL_UNPACK_ROW_LENGTH, stride > 0 ? stride / bpp : 0);
}

}  // namespace

Rect toGlScissor(Rect rect, Size target) {
  const int x0 = std::clamp(rect.x, 0, target.width);
  const int y0 = std::clamp(rect.y, 0, target.height);
  const int x1 = std::clamp(rect.x + std::max(rect.width, 0), 0, target.width);
  const int y1 = std::clamp(rect.y + std::max(rect.height, 0), 0, target.height);
  return Rect{x0, target.height - y1, x1 - x0, y1 - y0};
}

GlesBackend::GlesBackend() {
  std::string error;
  for (int i = 0; i < 4; i++) quad_[i] = compileProgram(kQuadVertex, quadSource((i & 2) != 0, (i & 1) != 0), &error);
  // gles 3 draws nothing without a bound vertex array; the quads take their corners from gl_VertexID
  glGenVertexArrays(1, &fullscreenVao_);
}

GlesBackend::~GlesBackend() {
  for (auto& q : quad_) destroyProgram(q);
  if (fullscreenVao_ != 0) glDeleteVertexArrays(1, &fullscreenVao_);
}

void GlesBackend::beginFrame(Size size) {
  frame_ = size;
  bindRenderTarget(nullptr);
  resetState();
  // colour and stencil at once, first in the pass: a tiler's fast clear, and the ui's clip masks (drawn straight into
  // this framebuffer) start from a clean stencil
  glClearColor(0, 0, 0, 1);
  glClearStencil(0);
  glClear(GL_COLOR_BUFFER_BIT | GL_STENCIL_BUFFER_BIT);
}

void GlesBackend::resetState() {
  glBindFramebuffer(GL_FRAMEBUFFER, 0);
  bound_ = frame_;
  glViewport(0, 0, frame_.width, frame_.height);
  glDisable(GL_SCISSOR_TEST);
  glDisable(GL_STENCIL_TEST);
  glDisable(GL_DEPTH_TEST);
  glDisable(GL_CULL_FACE);
  glColorMask(GL_TRUE, GL_TRUE, GL_TRUE, GL_TRUE);
  glPixelStorei(GL_UNPACK_ALIGNMENT, 4);
  glPixelStorei(GL_UNPACK_ROW_LENGTH, 0);
  glPixelStorei(GL_PACK_ALIGNMENT, 4);
  glActiveTexture(GL_TEXTURE0);
  glUseProgram(0);
  glBindVertexArray(0);
  scissor_.reset();
  applyBlend(Blend::Premultiplied);
}

Texture GlesBackend::createTexture(Size size, PixelFormat format, const void* pixels, int stride) {
  if (size.width <= 0 || size.height <= 0) return {};
  GLuint id = 0;
  glGenTextures(1, &id);
  glBindTexture(GL_TEXTURE_2D, id);
  glTexStorage2D(GL_TEXTURE_2D, 1, glInternal(format), size.width, size.height);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_LINEAR);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_LINEAR);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_S, GL_CLAMP_TO_EDGE);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_T, GL_CLAMP_TO_EDGE);
  if (pixels != nullptr) {
    setUnpack(stride, format);
    glTexSubImage2D(GL_TEXTURE_2D, 0, 0, 0, size.width, size.height, glFormat(format), GL_UNSIGNED_BYTE, pixels);
    glPixelStorei(GL_UNPACK_ROW_LENGTH, 0);
  }
  return Texture{id, size.width, size.height, format, false};
}

bool GlesBackend::updateTexture(const Texture& texture, Rect region, const void* pixels, int stride) {
  if (!texture || pixels == nullptr) return false;
  if (region.x < 0 || region.y < 0 || region.width <= 0 || region.height <= 0 ||
      region.x + region.width > texture.width || region.y + region.height > texture.height) {
    return false;
  }
  glBindTexture(GL_TEXTURE_2D, texture.id);
  setUnpack(stride, texture.format);
  glTexSubImage2D(GL_TEXTURE_2D, 0, region.x, region.y, region.width, region.height, glFormat(texture.format),
                  GL_UNSIGNED_BYTE, pixels);
  glPixelStorei(GL_UNPACK_ROW_LENGTH, 0);
  return true;
}

void GlesBackend::destroyTexture(Texture& texture) {
  if (texture.id != 0) {
    GLuint id = texture.id;
    glDeleteTextures(1, &id);
  }
  texture = {};
}

RenderTarget GlesBackend::createRenderTarget(Size size, bool stencil) {
  RenderTarget target;
  target.color = createTexture(size, PixelFormat::Rgba8, nullptr, 0);
  if (!target.color) return {};
  target.color.flipped = true;
  GLuint fbo = 0;
  glGenFramebuffers(1, &fbo);
  target.fbo = fbo;
  glBindFramebuffer(GL_FRAMEBUFFER, fbo);
  glFramebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, target.color.id, 0);
  if (stencil) {
    GLuint rb = 0;
    glGenRenderbuffers(1, &rb);
    glBindRenderbuffer(GL_RENDERBUFFER, rb);
    glRenderbufferStorage(GL_RENDERBUFFER, GL_STENCIL_INDEX8, size.width, size.height);
    glFramebufferRenderbuffer(GL_FRAMEBUFFER, GL_STENCIL_ATTACHMENT, GL_RENDERBUFFER, rb);
    target.stencil = rb;
  }
  const bool complete = glCheckFramebufferStatus(GL_FRAMEBUFFER) == GL_FRAMEBUFFER_COMPLETE;
  if (complete) {
    glClearColor(0, 0, 0, 0);
    glClear(GL_COLOR_BUFFER_BIT | (stencil ? GL_STENCIL_BUFFER_BIT : 0u));
  }
  glBindFramebuffer(GL_FRAMEBUFFER, 0);
  bound_ = frame_;
  glViewport(0, 0, frame_.width, frame_.height);
  if (!complete) {
    destroyRenderTarget(target);
    return {};
  }
  return target;
}

void GlesBackend::destroyRenderTarget(RenderTarget& target) {
  if (target.fbo != 0) {
    GLuint fbo = target.fbo;
    glDeleteFramebuffers(1, &fbo);
  }
  if (target.stencil != 0) {
    GLuint rb = target.stencil;
    glDeleteRenderbuffers(1, &rb);
  }
  destroyTexture(target.color);
  target = {};
}

void GlesBackend::bindRenderTarget(const RenderTarget* target) {
  if (target == nullptr || !*target) {
    glBindFramebuffer(GL_FRAMEBUFFER, 0);
    bound_ = frame_;
  } else {
    glBindFramebuffer(GL_FRAMEBUFFER, target->fbo);
    bound_ = Size{target->color.width, target->color.height};
  }
  glViewport(0, 0, bound_.width, bound_.height);
  if (scissor_) setScissor(scissor_);
}

void GlesBackend::clear(float r, float g, float b, float a) {
  const bool scissored = scissor_.has_value();
  if (scissored) glDisable(GL_SCISSOR_TEST);
  glClearColor(r, g, b, a);
  glClear(GL_COLOR_BUFFER_BIT);
  if (scissored) glEnable(GL_SCISSOR_TEST);
}

void GlesBackend::setScissor(std::optional<Rect> rect) {
  scissor_ = rect;
  if (!rect) {
    glDisable(GL_SCISSOR_TEST);
    return;
  }
  const Rect gl = toGlScissor(*rect, bound_);
  glEnable(GL_SCISSOR_TEST);
  glScissor(gl.x, gl.y, gl.width, gl.height);
}

void GlesBackend::setBlend(Blend blend) { applyBlend(blend); }

Program GlesBackend::compileProgram(std::string_view vertex, std::string_view fragment, std::string* error) {
  GLuint vs = compileShader(GL_VERTEX_SHADER, vertex, error);
  if (vs == 0) return {};
  GLuint fs = compileShader(GL_FRAGMENT_SHADER, fragment, error);
  if (fs == 0) {
    glDeleteShader(vs);
    return {};
  }
  GLuint program = glCreateProgram();
  glAttachShader(program, vs);
  glAttachShader(program, fs);
  glLinkProgram(program);
  glDeleteShader(vs);
  glDeleteShader(fs);
  GLint ok = 0;
  glGetProgramiv(program, GL_LINK_STATUS, &ok);
  if (ok == 0) {
    if (error != nullptr) {
      char log[1024] = {};
      glGetProgramInfoLog(program, sizeof log, nullptr, log);
      *error = log;
    }
    glDeleteProgram(program);
    return {};
  }
  return Program{program};
}

void GlesBackend::destroyProgram(Program& program) {
  if (program.id != 0) glDeleteProgram(program.id);
  program = {};
}

int GlesBackend::uniform(const Program& program, const char* name) {
  if (!program) return -1;
  glUseProgram(program.id);
  return glGetUniformLocation(program.id, name);
}

void GlesBackend::setUniform(const Program& program, const char* name, float value) {
  if (int at = uniform(program, name); at >= 0) glUniform1f(at, value);
}

void GlesBackend::setUniform(const Program& program, const char* name, float x, float y) {
  if (int at = uniform(program, name); at >= 0) glUniform2f(at, x, y);
}

void GlesBackend::setUniform(const Program& program, const char* name, float x, float y, float z, float w) {
  if (int at = uniform(program, name); at >= 0) glUniform4f(at, x, y, z, w);
}

void GlesBackend::setUniform(const Program& program, const char* name, const Mat3& value) {
  if (int at = uniform(program, name); at >= 0) glUniformMatrix3fv(at, 1, GL_FALSE, value.m);
}

void GlesBackend::drawTexturedQuad(const Texture& texture, const QuadParams& params) {
  const bool r8 = texture.format == PixelFormat::R8;
  const bool premul = params.blend != Blend::Alpha;
  const Program& quad = quad_[(r8 ? 2 : 0) | (premul ? 1 : 0)];
  if (!texture || !quad || bound_.width <= 0 || bound_.height <= 0) return;
  const auto w = static_cast<float>(bound_.width);
  const auto h = static_cast<float>(bound_.height);
  glUseProgram(quad.id);
  glUniform4f(glGetUniformLocation(quad.id, "u_dst"), static_cast<float>(params.dst.x) / w,
              static_cast<float>(params.dst.y) / h, static_cast<float>(params.dst.width) / w,
              static_cast<float>(params.dst.height) / h);
  glUniformMatrix3fv(glGetUniformLocation(quad.id, "u_uv"), 1, GL_FALSE, params.uv.m);
  glUniform1i(glGetUniformLocation(quad.id, "u_flip"), texture.flipped ? 1 : 0);
  glUniform1f(glGetUniformLocation(quad.id, "u_opacity"), params.opacity);
  glUniform1i(glGetUniformLocation(quad.id, "u_tex0"), 0);
  glActiveTexture(GL_TEXTURE0);
  glBindTexture(GL_TEXTURE_2D, texture.id);
  applyBlend(params.blend);
  glBindVertexArray(fullscreenVao_);
  glDrawArrays(GL_TRIANGLE_STRIP, 0, 4);
  glBindVertexArray(0);
}

void GlesBackend::drawFullscreen(const Program& program, std::span<const Texture> textures, const Mat3& uv,
                                 float opacity, Blend blend) {
  if (!program) return;
  glUseProgram(program.id);
  glUniformMatrix3fv(glGetUniformLocation(program.id, "u_uv"), 1, GL_FALSE, uv.m);
  glUniform1i(glGetUniformLocation(program.id, "u_flip"), !textures.empty() && textures[0].flipped ? 1 : 0);
  glUniform1f(glGetUniformLocation(program.id, "u_opacity"), opacity);
  for (std::size_t i = 0; i < textures.size() && i < 8; i++) {
    const std::string name = "u_tex" + std::to_string(i);
    glUniform1i(glGetUniformLocation(program.id, name.c_str()), static_cast<GLint>(i));
    glActiveTexture(GL_TEXTURE0 + static_cast<GLenum>(i));
    glBindTexture(GL_TEXTURE_2D, textures[i].id);
  }
  glActiveTexture(GL_TEXTURE0);
  applyBlend(blend);
  glBindVertexArray(fullscreenVao_);
  glDrawArrays(GL_TRIANGLES, 0, 3);
  glBindVertexArray(0);
}

}  // namespace cadence::screen::render
