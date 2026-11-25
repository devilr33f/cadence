#include "platform/platform.h"

#include "ipc/log.h"
#include "render/gl.h"

namespace cadence::screen::platform {

std::unique_ptr<Platform> Platform::create(const PlatformOptions& options, std::string* error) {
  std::unique_ptr<Platform> p(new Platform());
  p->vsync_ = options.vsync;
  if (!options.videoDriver.empty()) SDL_SetHint(SDL_HINT_VIDEO_DRIVER, options.videoDriver.c_str());
  // a tv app: the screen never blanks while it runs (sleep is display.screen's to decide)
  SDL_SetHint(SDL_HINT_VIDEO_ALLOW_SCREENSAVER, "0");
  // android: keep the event loop (and the link) alive in the background instead of blocking in SDL_PollEvent
  SDL_SetHint(SDL_HINT_ANDROID_BLOCK_ON_PAUSE, "0");
  if (!SDL_Init(SDL_INIT_VIDEO | SDL_INIT_EVENTS)) {
    if (error != nullptr) *error = "sdl_init";
    CADENCE_LOG(Error, "sdl_init_failed");
    return nullptr;
  }
  // sdl 3.4's android wait (SDL_WaitEventTimeoutNS) pumps with a poll sentinel each turn, and pushing it posts the
  // lifecycle wake its next Android_PumpEvents(timeout) waits on, so every timed wait spun a core until it expired.
  // without the sentinel the wait blocks on that semaphore until an event (any SDL_PushEvent posts the wake). the
  // main loop's poll loop ends on an empty queue, so it needs no sentinel
  SDL_SetEventEnabled(SDL_EVENT_POLL_SENTINEL, false);
  const char* driver = SDL_GetCurrentVideoDriver();
  p->driver_ = driver != nullptr ? driver : "";
  p->wakeEvent_ = SDL_RegisterEvents(1);

#if defined(__APPLE__)
  // macos (the screen-sim build) has no gles driver: desktop gl 4.1 core, the same calls (render/gl.h)
  SDL_GL_SetAttribute(SDL_GL_CONTEXT_PROFILE_MASK, SDL_GL_CONTEXT_PROFILE_CORE);
  SDL_GL_SetAttribute(SDL_GL_CONTEXT_MAJOR_VERSION, 3);
  SDL_GL_SetAttribute(SDL_GL_CONTEXT_MINOR_VERSION, 3);
#else
  SDL_GL_SetAttribute(SDL_GL_CONTEXT_PROFILE_MASK, SDL_GL_CONTEXT_PROFILE_ES);
  SDL_GL_SetAttribute(SDL_GL_CONTEXT_MAJOR_VERSION, 3);
  SDL_GL_SetAttribute(SDL_GL_CONTEXT_MINOR_VERSION, 0);
#endif
  SDL_GL_SetAttribute(SDL_GL_DOUBLEBUFFER, 1);
  SDL_GL_SetAttribute(SDL_GL_RED_SIZE, 8);
  SDL_GL_SetAttribute(SDL_GL_GREEN_SIZE, 8);
  SDL_GL_SetAttribute(SDL_GL_BLUE_SIZE, 8);
  SDL_GL_SetAttribute(SDL_GL_ALPHA_SIZE, options.translucent ? 8 : 0);
  SDL_GL_SetAttribute(SDL_GL_DEPTH_SIZE, 0);
  // rmlui's clip masks
  SDL_GL_SetAttribute(SDL_GL_STENCIL_SIZE, 8);
  // no msaa: rmlui's per-frame msaa layer was the stick's bottleneck (s1)
  SDL_GL_SetAttribute(SDL_GL_MULTISAMPLESAMPLES, 0);

  int width = options.width;
  int height = options.height;
  SDL_WindowFlags flags = SDL_WINDOW_OPENGL;
  if (width <= 0 || height <= 0) {
    const SDL_DisplayMode* mode = SDL_GetDesktopDisplayMode(SDL_GetPrimaryDisplay());
    width = mode != nullptr && mode->w > 0 ? mode->w : 1920;
    height = mode != nullptr && mode->h > 0 ? mode->h : 1080;
    if (p->driver_ != "offscreen") flags |= SDL_WINDOW_FULLSCREEN;
  }
  if (options.translucent) flags |= SDL_WINDOW_TRANSPARENT;
  if (options.hidden) flags |= SDL_WINDOW_HIDDEN;
#if defined(__APPLE__)
  // a retina mac: the drawable at the display's own density (the ui lays out by dp, so it is drawn twice as fine),
  // else macos scales a 1x surface up and rounded edges show their steps. headless runs stay at 1x (the screenshots'
  // size is the window's)
  if (!options.hidden) flags |= SDL_WINDOW_HIGH_PIXEL_DENSITY;
#endif
  p->window_ = SDL_CreateWindow("cadence", width, height, flags);
  if (p->window_ == nullptr) {
    if (error != nullptr) *error = "window";
    CADENCE_LOG(Error, "sdl_window_failed", {{"driver", ipc::Token(p->driver_)}});
    return nullptr;
  }
  if (!p->makeContext(error)) return nullptr;
  const auto size = p->drawableSize();
  CADENCE_LOG(Info, "platform_ready", {{"driver", ipc::Token(p->driver_)},
                                       {"width", size.width},
                                       {"height", size.height},
                                       {"vsync", p->vsync_},
                                       {"translucent", options.translucent}});
  return p;
}

bool Platform::makeContext(std::string* error) {
  gl_ = SDL_GL_CreateContext(window_);
  if (gl_ == nullptr) {
    if (error != nullptr) *error = "gl_context";
    CADENCE_LOG(Error, "gl_context_failed");
    return false;
  }
  SDL_GL_MakeCurrent(window_, gl_);
  // vsync paces the loop; adaptive first, as a late frame should tear rather than wait a whole period
  if (vsync_ && !SDL_GL_SetSwapInterval(-1)) SDL_GL_SetSwapInterval(1);
  if (!vsync_) SDL_GL_SetSwapInterval(0);
  return true;
}

Platform::~Platform() {
  if (gl_ != nullptr) SDL_GL_DestroyContext(gl_);
  if (window_ != nullptr) SDL_DestroyWindow(window_);
  SDL_Quit();
}

render::Size Platform::drawableSize() const {
  int w = 0;
  int h = 0;
  SDL_GetWindowSizeInPixels(window_, &w, &h);
  return render::Size{w, h};
}

DisplayInfo Platform::display() const {
  DisplayInfo info;
  info.pixels = drawableSize();
  const SDL_DisplayID id = SDL_GetDisplayForWindow(window_);
  if (const SDL_DisplayMode* mode = SDL_GetCurrentDisplayMode(id); mode != nullptr && mode->refresh_rate > 0) {
    info.refreshMhz = static_cast<std::uint32_t>(mode->refresh_rate * 1000.0f + 0.5f);
  }
  const float scale = SDL_GetWindowDisplayScale(window_);
  info.displayScale = scale > 0 ? scale : 1.0f;
  return info;
}

std::string Platform::glVersion() const {
  const auto* v = reinterpret_cast<const char*>(glGetString(GL_VERSION));
  return v != nullptr ? v : "";
}

std::string Platform::glRenderer() const {
  const auto* v = reinterpret_cast<const char*>(glGetString(GL_RENDERER));
  return v != nullptr ? v : "";
}

void Platform::swap() { SDL_GL_SwapWindow(window_); }

bool Platform::makeCurrent() { return gl_ != nullptr && SDL_GL_MakeCurrent(window_, gl_); }

bool Platform::recreateContext(std::string* error) {
  if (gl_ != nullptr) SDL_GL_DestroyContext(gl_);
  gl_ = nullptr;
  return makeContext(error);
}

void* Platform::getProcAddress(void*, const char* name) {
  return reinterpret_cast<void*>(SDL_GL_GetProcAddress(name));
}

void Platform::wake() {
  if (wakePending_.exchange(true)) return;
  SDL_Event e{};
  e.type = wakeEvent_;
  SDL_PushEvent(&e);
}

bool Platform::waitEvent(SDL_Event* event, int timeoutMs) {
  if (timeoutMs < 0) return SDL_WaitEvent(event);
  return SDL_WaitEventTimeout(event, timeoutMs);
}

}  // namespace cadence::screen::platform
