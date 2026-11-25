#pragma once

// gles 3.0 everywhere: mesa's libGLESv2 on linux, the ndk's GLESv3 on android. macos (the screen-sim build) has no
// gles: desktop gl 4.1 core instead, the same calls, with the two differences hidden here: no glTexStorage2D (its
// immutable textures are an upload-only allocation here) and shaders transpiled from "#version 300 es" at compile
// time (4.1 core's glsl takes es precision statements)
#if defined(__APPLE__)
#define GL_SILENCE_DEPRECATION 1
#include <OpenGL/gl3.h>
#else
#include <GLES3/gl3.h>
#endif

#if defined(__APPLE__)

#include <cstddef>

inline void glTexStorage2D(GLenum target, GLsizei levels, GLenum internalformat, GLsizei width, GLsizei height) {
  (void)levels;
  glTexImage2D(target, 0, static_cast<GLint>(internalformat), width, height, 0,
               static_cast<GLenum>(internalformat == GL_R8 ? GL_RED : GL_RGBA), GL_UNSIGNED_BYTE, nullptr);
}

#endif
