#include "screen_backend.h"

#if defined(__APPLE__)
// macos (the screen-sim build) has no egl: a cgl context sharing objects with the screen's (sdl's), no drawable (the
// host draws into fbos, as the surfaceless egl path does)
#define GL_SILENCE_DEPRECATION 1
#include <OpenGL/CGLCurrent.h>
#include <OpenGL/OpenGL.h>
#else
#include <EGL/egl.h>
#endif

#include <cmath>
#include <string>

namespace cadence::render {

namespace {

constexpr const char* kSolid = R"(#version 300 es
precision mediump float;
uniform vec4 u_color;
uniform float u_opacity;
out vec4 frag;
void main() { frag = vec4(u_color.rgb * u_color.a, u_color.a) * u_opacity; }
)";

screen::render::Rect pixels(const Rect& r) {
  const int x = static_cast<int>(std::floor(r.x));
  const int y = static_cast<int>(std::floor(r.y));
  return {x, y, static_cast<int>(std::ceil(r.x + r.w)) - x, static_cast<int>(std::ceil(r.y + r.h)) - y};
}

#if defined(__APPLE__)

class CglShared final : public viz::SharedContext {
 public:
  explicit CglShared(CGLContextObj context) : context_(context) {}
  ~CglShared() override {
    // only its own: a host torn down from the ui thread must not unbind the ui's context
    if (CGLGetCurrentContext() == context_) release();
    CGLDestroyContext(context_);
  }
  bool make_current() override { return CGLSetCurrentContext(context_) == kCGLNoError; }
  void release() override { CGLSetCurrentContext(nullptr); }

 private:
  CGLContextObj context_;
};

#else

class EglShared final : public viz::SharedContext {
 public:
  EglShared(EGLDisplay d, EGLContext c, EGLSurface s) : display_(d), context_(c), surface_(s) {}
  ~EglShared() override {
    // only its own: a host torn down from the ui thread (a quality change) unbound the ui's context, and the screen
    // froze
    if (eglGetCurrentContext() == context_) release();
    if (surface_ != EGL_NO_SURFACE) eglDestroySurface(display_, surface_);
    eglDestroyContext(display_, context_);
  }
  bool make_current() override { return eglMakeCurrent(display_, surface_, surface_, context_) == EGL_TRUE; }
  void release() override { eglMakeCurrent(display_, EGL_NO_SURFACE, EGL_NO_SURFACE, EGL_NO_CONTEXT); }

 private:
  EGLDisplay display_;
  EGLContext context_;
  EGLSurface surface_;
};

#endif

}  // namespace

BackendCanvas::BackendCanvas(screen::render::RenderBackend& backend) : backend_(backend) {
  std::string error;
  program_ = backend_.compileProgram(screen::render::kFullscreenVertexShader, kSolid, &error);
}

BackendCanvas::~BackendCanvas() { backend_.destroyProgram(program_); }

void BackendCanvas::clear(Color c) { backend_.clear(c.r * c.a, c.g * c.a, c.b * c.a, c.a); }

void BackendCanvas::fill_rects(std::span<const Rect> rects, Color c) {
  if (!ok()) return;
  backend_.setUniform(program_, "u_color", c.r, c.g, c.b, c.a);
  for (const auto& r : rects) {
    backend_.setScissor(pixels(r));
    backend_.drawFullscreen(program_, {}, screen::render::Mat3::identity(), 1.0f, screen::render::Blend::Premultiplied);
  }
  backend_.setScissor(std::nullopt);
}

void BackendCanvas::draw_quad(const TexturedQuad& q) {
  if (q.texture == 0) return;
  screen::render::Texture t;
  t.id = q.texture;
  t.width = 1;
  t.height = 1;
  t.flipped = flipped_;
  screen::render::QuadParams p;
  p.dst = pixels(q.dst);
  p.uv = screen::render::Mat3::scaleOffset(q.uv.w, q.uv.h, q.uv.x, q.uv.y);
  p.opacity = q.opacity;
  p.blend = screen::render::Blend::Premultiplied;
  backend_.drawTexturedQuad(t, p);
}

std::unique_ptr<viz::SharedContext> make_shared_context() {
#if defined(__APPLE__)
  // called with the screen's context current (the ui thread): a sibling sharing its objects, for the host's thread
  CGLContextObj share = CGLGetCurrentContext();
  if (share == nullptr) return nullptr;
  CGLPixelFormatObj format = CGLGetPixelFormat(share);
  CGLContextObj context = nullptr;
  if (format == nullptr || CGLCreateContext(format, share, &context) != kCGLNoError || context == nullptr) {
    return nullptr;
  }
  return std::make_unique<CglShared>(context);
#else
  const EGLDisplay display = eglGetCurrentDisplay();
  const EGLContext share = eglGetCurrentContext();
  if (display == EGL_NO_DISPLAY || share == EGL_NO_CONTEXT) return nullptr;
  EGLint configId = 0;
  eglQueryContext(display, share, EGL_CONFIG_ID, &configId);
  const EGLint pick[] = {EGL_CONFIG_ID, configId, EGL_NONE};
  EGLConfig config = nullptr;
  EGLint n = 0;
  if (eglChooseConfig(display, pick, &config, 1, &n) != EGL_TRUE || n != 1) return nullptr;
  const EGLint attrs[] = {EGL_CONTEXT_CLIENT_VERSION, 3, EGL_NONE};
  const EGLContext context = eglCreateContext(display, config, share, attrs);
  if (context == EGL_NO_CONTEXT) return nullptr;
  // surfaceless where the driver has it (mesa, most android), else a 1x1 pbuffer: the host draws into fbos
  EGLSurface surface = EGL_NO_SURFACE;
  const char* ext = eglQueryString(display, EGL_EXTENSIONS);
  if (ext == nullptr || std::string(ext).find("EGL_KHR_surfaceless_context") == std::string::npos) {
    const EGLint pb[] = {EGL_WIDTH, 1, EGL_HEIGHT, 1, EGL_NONE};
    surface = eglCreatePbufferSurface(display, config, pb);
    if (surface == EGL_NO_SURFACE) {
      eglDestroyContext(display, context);
      return nullptr;
    }
  }
  return std::make_unique<EglShared>(display, context, surface);
#endif
}

}  // namespace cadence::render
