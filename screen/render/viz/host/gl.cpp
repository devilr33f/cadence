#include "gl.h"

#include <vector>

namespace cadence::render::viz::gl {

#if defined(__APPLE__)
// 4.1 core's glsl is the same language as es 3.00, its own version word alone (it takes es precision statements)
const char* const kPrelude = "#version 410 core\nprecision highp float;\n";
#else
const char* const kPrelude = "#version 300 es\nprecision highp float;\nprecision highp int;\n";
#endif

const char* const kFullscreenVertex = R"(
out vec2 vUv;
void main() {
  vec2 p = vec2(float((gl_VertexID << 1) & 2), float(gl_VertexID & 2));
  vUv = p;
  gl_Position = vec4(p * 2.0 - 1.0, 0.0, 1.0);
}
)";

namespace {

GLuint compile(GLenum type, std::string_view body, std::string* error) {
  const std::string src = std::string(kPrelude) + std::string(body);
  GLuint s = glCreateShader(type);
  const char* p = src.c_str();
  glShaderSource(s, 1, &p, nullptr);
  glCompileShader(s);
  GLint ok = 0;
  glGetShaderiv(s, GL_COMPILE_STATUS, &ok);
  if (!ok) {
    if (error) {
      GLint n = 0;
      glGetShaderiv(s, GL_INFO_LOG_LENGTH, &n);
      std::vector<char> log(static_cast<std::size_t>(n) + 1);
      glGetShaderInfoLog(s, n, nullptr, log.data());
      *error = log.data();
    }
    glDeleteShader(s);
    return 0;
  }
  return s;
}

GLuint empty_vao() {
  static GLuint vao = 0;
  if (!vao) glGenVertexArrays(1, &vao);
  return vao;
}

}  // namespace

GLuint program(std::string_view vertex, std::string_view fragment, std::string* error) {
  GLuint vs = compile(GL_VERTEX_SHADER, vertex, error);
  if (!vs) return 0;
  GLuint fs = compile(GL_FRAGMENT_SHADER, fragment, error);
  if (!fs) {
    glDeleteShader(vs);
    return 0;
  }
  GLuint p = glCreateProgram();
  glAttachShader(p, vs);
  glAttachShader(p, fs);
  glLinkProgram(p);
  glDeleteShader(vs);
  glDeleteShader(fs);
  GLint ok = 0;
  glGetProgramiv(p, GL_LINK_STATUS, &ok);
  if (!ok) {
    if (error) {
      GLint n = 0;
      glGetProgramiv(p, GL_INFO_LOG_LENGTH, &n);
      std::vector<char> log(static_cast<std::size_t>(n) + 1);
      glGetProgramInfoLog(p, n, nullptr, log.data());
      *error = log.data();
    }
    glDeleteProgram(p);
    return 0;
  }
  return p;
}

void fullscreen() {
  glBindVertexArray(empty_vao());
  glDrawArrays(GL_TRIANGLES, 0, 3);
}

bool Target::make(int w, int h) {
  width = w;
  height = h;
  glGenTextures(1, &texture);
  glBindTexture(GL_TEXTURE_2D, texture);
  glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA8, w, h, 0, GL_RGBA, GL_UNSIGNED_BYTE, nullptr);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_LINEAR);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_LINEAR);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_S, GL_CLAMP_TO_EDGE);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_T, GL_CLAMP_TO_EDGE);
  glGenFramebuffers(1, &fbo);
  glBindFramebuffer(GL_FRAMEBUFFER, fbo);
  glFramebufferTexture2D(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_TEXTURE_2D, texture, 0);
  const bool ok = glCheckFramebufferStatus(GL_FRAMEBUFFER) == GL_FRAMEBUFFER_COMPLETE;
  glClearColor(0, 0, 0, 1);
  glClear(GL_COLOR_BUFFER_BIT);
  glBindFramebuffer(GL_FRAMEBUFFER, 0);
  if (!ok) release();
  return ok;
}

void Target::release() {
  if (fbo) glDeleteFramebuffers(1, &fbo);
  if (texture) glDeleteTextures(1, &texture);
  fbo = texture = 0;
}

void Target::bind() const {
  glBindFramebuffer(GL_FRAMEBUFFER, fbo);
  glViewport(0, 0, width, height);
}

}  // namespace cadence::render::viz::gl
