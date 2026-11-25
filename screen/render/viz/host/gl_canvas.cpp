#include "gl_canvas.h"

#include "gl.h"

namespace cadence::render::viz {

namespace {

const char* const kVertex = R"(
layout(location = 0) in vec2 aPos;
uniform vec2 uSize;
void main() {
  // pixels, origin top left, to clip space (the fbo's rows bottom up: y flips)
  vec2 p = aPos / uSize * 2.0 - 1.0;
  gl_Position = vec4(p.x, -p.y, 0.0, 1.0);
}
)";

const char* const kFragment = R"(
uniform vec4 uColor;
out vec4 cadenceOut;
void main() { cadenceOut = uColor; }
)";

}  // namespace

bool GlCanvas::init(int width, int height) {
  w_ = width;
  h_ = height;
  program_ = gl::program(kVertex, kFragment);
  if (!program_) return false;
  u_color_ = glGetUniformLocation(program_, "uColor");
  u_size_ = glGetUniformLocation(program_, "uSize");
  glGenVertexArrays(1, &vao_);
  glGenBuffers(1, &vbo_);
  glBindVertexArray(vao_);
  glBindBuffer(GL_ARRAY_BUFFER, vbo_);
  glEnableVertexAttribArray(0);
  glVertexAttribPointer(0, 2, GL_FLOAT, GL_FALSE, 0, nullptr);
  glBindVertexArray(0);
  return true;
}

void GlCanvas::release() {
  if (vbo_) glDeleteBuffers(1, &vbo_);
  if (vao_) glDeleteVertexArrays(1, &vao_);
  if (program_) glDeleteProgram(program_);
  vbo_ = vao_ = program_ = 0;
}

void GlCanvas::clear(Color c) {
  glDisable(GL_BLEND);
  glClearColor(c.r, c.g, c.b, c.a);
  glClear(GL_COLOR_BUFFER_BIT);
}

void GlCanvas::fill_rects(std::span<const Rect> rects, Color c) {
  if (rects.empty()) return;
  verts_.clear();
  for (const Rect& r : rects) {
    const float x0 = r.x, y0 = r.y, x1 = r.x + r.w, y1 = r.y + r.h;
    verts_.insert(verts_.end(), {x0, y0, x1, y0, x1, y1, x0, y0, x1, y1, x0, y1});
  }
  glEnable(GL_BLEND);
  glBlendFuncSeparate(GL_SRC_ALPHA, GL_ONE_MINUS_SRC_ALPHA, GL_ONE, GL_ONE_MINUS_SRC_ALPHA);
  glUseProgram(program_);
  glUniform2f(u_size_, static_cast<float>(w_), static_cast<float>(h_));
  glUniform4f(u_color_, c.r, c.g, c.b, c.a);
  glBindVertexArray(vao_);
  glBindBuffer(GL_ARRAY_BUFFER, vbo_);
  glBufferData(GL_ARRAY_BUFFER, static_cast<GLsizeiptr>(verts_.size() * sizeof(float)), verts_.data(), GL_STREAM_DRAW);
  glDrawArrays(GL_TRIANGLES, 0, static_cast<GLsizei>(verts_.size() / 2));
  glBindVertexArray(0);
  glDisable(GL_BLEND);
}

}  // namespace cadence::render::viz
