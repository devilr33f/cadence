#pragma once

#if defined(__APPLE__)
// macos (the screen-sim build): desktop gl 4.1 core, the same calls (screen/render/gl.h tells the rest)
#define GL_SILENCE_DEPRECATION 1
#include <OpenGL/gl3.h>
#else
#include <GLES3/gl3.h>
#endif

#include <string>
#include <string_view>

// small gles 3 helpers shared by the host's stages: a program from sources, a fullscreen triangle, an fbo target
namespace cadence::render::viz::gl {

// "#version 300 es" and default precision; prepended to every shader
extern const char* const kPrelude;

// a linked program, 0 on failure with the log in *error
GLuint program(std::string_view vertex, std::string_view fragment, std::string* error = nullptr);
// the vertex shader of a fullscreen triangle with uv in 0..1 (out vec2 vUv)
extern const char* const kFullscreenVertex;
// draws the triangle (one draw call, no buffers: gl_VertexID)
void fullscreen();

struct Target {
  GLuint texture = 0;
  GLuint fbo = 0;
  int width = 0, height = 0;

  bool make(int w, int h);
  void release();
  // binds its fbo and sets the viewport
  void bind() const;
};

}  // namespace cadence::render::viz::gl
