#pragma once

// an offscreen gles 3 context for the benchmark and the gl tests: mesa's surfaceless platform where it exists (the
// test image, llvmpipe), else the default display with a 1×1 pbuffer (android, from adb shell)
#include <EGL/egl.h>
#include <EGL/eglext.h>

namespace cadence::render::viz {

inline bool egl_offscreen() {
  EGLDisplay d = EGL_NO_DISPLAY;
  auto get_platform = reinterpret_cast<PFNEGLGETPLATFORMDISPLAYEXTPROC>(eglGetProcAddress("eglGetPlatformDisplayEXT"));
#ifdef EGL_PLATFORM_SURFACELESS_MESA
  if (get_platform) d = get_platform(EGL_PLATFORM_SURFACELESS_MESA, EGL_DEFAULT_DISPLAY, nullptr);
#else
  (void)get_platform;
#endif
  EGLint maj = 0, min = 0;
  if (d == EGL_NO_DISPLAY || !eglInitialize(d, &maj, &min)) {
    d = eglGetDisplay(EGL_DEFAULT_DISPLAY);
    if (d == EGL_NO_DISPLAY || !eglInitialize(d, &maj, &min)) return false;
  }
  eglBindAPI(EGL_OPENGL_ES_API);
  const EGLint cfg_attr[] = {EGL_SURFACE_TYPE, EGL_PBUFFER_BIT, EGL_RENDERABLE_TYPE, EGL_OPENGL_ES3_BIT, EGL_RED_SIZE, 8, EGL_GREEN_SIZE, 8,
                             EGL_BLUE_SIZE, 8, EGL_ALPHA_SIZE, 8, EGL_NONE};
  EGLConfig cfg;
  EGLint n = 0;
  if (!eglChooseConfig(d, cfg_attr, &cfg, 1, &n) || n == 0) return false;
  const EGLint ctx_attr[] = {EGL_CONTEXT_MAJOR_VERSION, 3, EGL_NONE};
  EGLContext c = eglCreateContext(d, cfg, EGL_NO_CONTEXT, ctx_attr);
  if (c == EGL_NO_CONTEXT) return false;
  if (eglMakeCurrent(d, EGL_NO_SURFACE, EGL_NO_SURFACE, c)) return true;
  const EGLint pb_attr[] = {EGL_WIDTH, 1, EGL_HEIGHT, 1, EGL_NONE};
  EGLSurface s = eglCreatePbufferSurface(d, cfg, pb_attr);
  return s != EGL_NO_SURFACE && eglMakeCurrent(d, s, s, c);
}

}  // namespace cadence::render::viz
