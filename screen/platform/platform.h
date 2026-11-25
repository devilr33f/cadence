#pragma once

#include "render/render-backend.h"

#include <SDL3/SDL.h>

#include <atomic>
#include <cstdint>
#include <memory>
#include <string>

namespace cadence::screen::platform {

struct PlatformOptions {
  // sdl's video driver: kmsdrm or wayland on the pi, android, offscreen in tests; empty = SDL_VIDEO_DRIVER or sdl's
  // own choice
  std::string videoDriver;
  // 0 = the display's mode
  int width = 0;
  int height = 0;
  bool vsync = true;
  // a window that is never shown (the sim's headless runs): the context and readback work as on screen
  bool hidden = false;
  // android's surface embed: the sdl surface is translucent over mpv's own SurfaceView, so the gl surface needs alpha
  bool translucent = false;
};

struct DisplayInfo {
  render::Size pixels;
  // millihertz (59940), 0 when unknown
  std::uint32_t refreshMhz = 0;
  float displayScale = 1.0f;
};

// sdl3 with one window and a gles 3.0 context made current on the calling thread, which is the main loop's from
// then on. destroying it takes the context, the window and sdl down: everything holding gl objects goes first
class Platform {
 public:
  static std::unique_ptr<Platform> create(const PlatformOptions& options, std::string* error);
  ~Platform();
  Platform(const Platform&) = delete;
  Platform& operator=(const Platform&) = delete;

  [[nodiscard]] render::Size drawableSize() const;
  [[nodiscard]] DisplayInfo display() const;
  [[nodiscard]] const std::string& driver() const { return driver_; }
  [[nodiscard]] std::string glVersion() const;
  [[nodiscard]] std::string glRenderer() const;

  void swap();
  // false once the context is lost (android, after a long background): the app tears gl down and calls recreate
  bool makeCurrent();
  bool recreateContext(std::string* error);

  // mpv's render api resolves gl through this
  static void* getProcAddress(void* ctx, const char* name);

  // safe from any thread: wakes a main loop blocked in waitEvent; wakes coalesce until the main loop sees one
  void wake();
  [[nodiscard]] std::uint32_t wakeEventType() const { return wakeEvent_; }
  // the main loop saw the wake event
  void woken() { wakePending_ = false; }
  // waits up to timeoutMs (-1 forever) for an event
  bool waitEvent(SDL_Event* event, int timeoutMs);

  [[nodiscard]] SDL_Window* window() const { return window_; }

 private:
  Platform() = default;
  bool makeContext(std::string* error);

  SDL_Window* window_ = nullptr;
  SDL_GLContext gl_ = nullptr;
  std::string driver_;
  bool vsync_ = true;
  std::uint32_t wakeEvent_ = 0;
  std::atomic<bool> wakePending_{false};
};

}  // namespace cadence::screen::platform
