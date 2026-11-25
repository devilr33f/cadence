#pragma once

#include "platform/platform.h"
#include "render/capture.h"
#include "render/gles-backend.h"

#include <catch2/catch_test_macros.hpp>

#include <memory>
#include <ostream>
#include <string>

namespace cadence::screen::test {

// sdl's offscreen driver on mesa (egl surfaceless, llvmpipe) with a gles 3 context and a backend
struct GlEnv {
  explicit GlEnv(int width = 64, int height = 48) {
    platform::PlatformOptions o;
    o.videoDriver = "offscreen";
    o.width = width;
    o.height = height;
    o.vsync = false;
    std::string error;
    platform = platform::Platform::create(o, &error);
    INFO("platform: " << error);
    REQUIRE(platform);
    backend = std::make_unique<render::GlesBackend>();
    REQUIRE(backend->ok());
    backend->beginFrame(platform->drawableSize());
  }
  ~GlEnv() {
    backend.reset();
    platform.reset();
  }

  std::unique_ptr<platform::Platform> platform;
  std::unique_ptr<render::GlesBackend> backend;
};

struct Rgba {
  int r, g, b, a;
};

inline std::ostream& operator<<(std::ostream& os, Rgba c) {
  return os << "rgba(" << c.r << "," << c.g << "," << c.b << "," << c.a << ")";
}

inline Rgba pixel(const render::Image& image, int x, int y) {
  const auto* p = image.at(x, y);
  return {p[0], p[1], p[2], p[3]};
}

inline bool near(Rgba got, Rgba want, int tolerance = 8) {
  const auto close = [&](int a, int b) { return a - b <= tolerance && b - a <= tolerance; };
  return close(got.r, want.r) && close(got.g, want.g) && close(got.b, want.b) && close(got.a, want.a);
}

}  // namespace cadence::screen::test
