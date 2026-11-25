#include "render/capture.h"
#include "render/gl.h"
#include "support/gl-env.h"

#include <catch2/catch_test_macros.hpp>

#include <cstdint>

using namespace cadence::screen;
using test::GlEnv;
using test::near;
using test::pixel;
using test::Rgba;

namespace {

// 2 x 2, top row first: red, green / blue, white
const std::uint8_t kQuad[] = {255, 0, 0, 255, 0, 255, 0, 255, 0, 0, 255, 255, 255, 255, 255, 255};

}  // namespace

TEST_CASE("the offscreen context is gles 3") {
  GlEnv env;
  INFO(env.platform->glVersion() << " / " << env.platform->glRenderer());
  CHECK(env.platform->glVersion().rfind("OpenGL ES 3", 0) == 0);
  CHECK(env.platform->drawableSize() == render::Size{64, 48});
  CHECK(env.platform->driver() == "offscreen");
}

TEST_CASE("the platform's waits block: no poll sentinel, and a wake ends a timed wait") {
  // regression: sdl 3.4's android wait spun on its own sentinel's wake (SDLThread at 100% cpu on the stick)
  GlEnv env;
  CHECK_FALSE(SDL_EventEnabled(SDL_EVENT_POLL_SENTINEL));
  SDL_Event e;
  while (SDL_PollEvent(&e)) {
  }
  env.platform->wake();
  REQUIRE(env.platform->waitEvent(&e, 1000));
  CHECK(e.type == env.platform->wakeEventType());
  env.platform->woken();
  // nothing pending: the wait times out rather than returning a sentinel or an empty event
  CHECK_FALSE(env.platform->waitEvent(&e, 20));
}

TEST_CASE("a textured quad lands top-left first, in a target and on the screen") {
  GlEnv env;
  auto& b = *env.backend;
  auto tex = b.createTexture({2, 2}, render::PixelFormat::Rgba8, kQuad, 0);
  REQUIRE(tex);
  auto target = b.createRenderTarget({64, 48}, false);
  REQUIRE(target);
  CHECK(target.color.flipped);

  b.bindRenderTarget(&target);
  CHECK(b.boundSize() == render::Size{64, 48});
  b.clear(0, 0, 0, 1);
  render::QuadParams q;
  q.dst = {0, 0, 64, 48};
  q.blend = render::Blend::None;
  b.drawTexturedQuad(tex, q);
  auto img = render::readTarget(target);
  INFO("target corners " << pixel(img, 0, 0) << pixel(img, 63, 0) << pixel(img, 0, 47) << pixel(img, 63, 47));
  CHECK(near(pixel(img, 0, 0), {255, 0, 0, 255}));
  CHECK(near(pixel(img, 63, 0), {0, 255, 0, 255}));
  CHECK(near(pixel(img, 0, 47), {0, 0, 255, 255}));
  CHECK(near(pixel(img, 63, 47), {255, 255, 255, 255}));

  // the target's (flipped) colour drawn to the screen keeps the orientation
  b.bindRenderTarget(nullptr);
  b.clear(0, 0, 0, 1);
  b.drawTexturedQuad(target.color, q);
  auto screen = render::readPixels(b.frameSize());
  CHECK(near(pixel(screen, 0, 0), {255, 0, 0, 255}));
  CHECK(near(pixel(screen, 63, 47), {255, 255, 255, 255}));

  // a quad into part of the screen
  b.clear(0, 0, 0, 1);
  q.dst = {32, 24, 32, 24};
  b.drawTexturedQuad(tex, q);
  screen = render::readPixels(b.frameSize());
  CHECK(near(pixel(screen, 10, 10), {0, 0, 0, 255}));
  CHECK(near(pixel(screen, 33, 25), {255, 0, 0, 255}));
  CHECK(near(pixel(screen, 63, 47), {255, 255, 255, 255}));

  b.destroyRenderTarget(target);
  b.destroyTexture(tex);
  CHECK_FALSE(target);
  CHECK_FALSE(tex);
  b.destroyTexture(tex);
}

TEST_CASE("scissor clips drawing, top-left origin") {
  GlEnv env;
  auto& b = *env.backend;
  const std::uint8_t white[] = {255, 255, 255, 255};
  auto tex = b.createTexture({1, 1}, render::PixelFormat::Rgba8, white, 0);
  b.clear(1, 0, 0, 1);
  b.setScissor(render::Rect{8, 4, 16, 10});
  render::QuadParams q;
  q.dst = {0, 0, 64, 48};
  q.blend = render::Blend::None;
  b.drawTexturedQuad(tex, q);
  b.setScissor(std::nullopt);
  auto img = render::readPixels(b.frameSize());
  CHECK(near(pixel(img, 8, 4), {255, 255, 255, 255}));
  CHECK(near(pixel(img, 23, 13), {255, 255, 255, 255}));
  CHECK(near(pixel(img, 24, 13), {255, 0, 0, 255}));
  CHECK(near(pixel(img, 8, 14), {255, 0, 0, 255}));
  CHECK(near(pixel(img, 7, 4), {255, 0, 0, 255}));
  b.destroyTexture(tex);
}

TEST_CASE("opacity and blending: premultiplied, straight alpha and r8 masks") {
  GlEnv env;
  auto& b = *env.backend;
  const std::uint8_t white[] = {255, 255, 255, 255};
  auto tex = b.createTexture({1, 1}, render::PixelFormat::Rgba8, white, 0);
  b.clear(0, 0, 0, 1);
  render::QuadParams q;
  q.dst = {0, 0, 32, 48};
  q.opacity = 0.5f;
  b.drawTexturedQuad(tex, q);
  q.dst = {32, 0, 32, 48};
  q.blend = render::Blend::Alpha;
  b.drawTexturedQuad(tex, q);
  auto img = render::readPixels(b.frameSize());
  CHECK(near(pixel(img, 4, 4), {128, 128, 128, 255}, 3));
  CHECK(near(pixel(img, 40, 4), {128, 128, 128, 255}, 3));

  const std::uint8_t mask[] = {64};
  auto r8 = b.createTexture({1, 1}, render::PixelFormat::R8, mask, 0);
  REQUIRE(r8);
  b.clear(0, 0, 0, 1);
  q = {};
  q.dst = {0, 0, 64, 48};
  b.drawTexturedQuad(r8, q);
  img = render::readPixels(b.frameSize());
  CHECK(near(pixel(img, 10, 10), {64, 64, 64, 255}, 3));

  // a texture updated in place
  const std::uint8_t green[] = {0, 255, 0, 255};
  CHECK(b.updateTexture(tex, {0, 0, 1, 1}, green, 0));
  CHECK_FALSE(b.updateTexture(tex, {0, 0, 2, 1}, green, 0));
  q.blend = render::Blend::None;
  b.drawTexturedQuad(tex, q);
  img = render::readPixels(b.frameSize());
  CHECK(near(pixel(img, 10, 10), {0, 255, 0, 255}));
  b.destroyTexture(tex);
  b.destroyTexture(r8);
}

TEST_CASE("a fullscreen program with a uv transform (ken burns' shape)") {
  GlEnv env;
  auto& b = *env.backend;
  std::string error;
  auto program = b.compileProgram(render::kFullscreenVertexShader, R"(#version 300 es
precision mediump float;
uniform sampler2D u_tex0;
uniform float u_opacity;
uniform vec4 u_tint;
in vec2 v_uv;
out vec4 color;
void main() { color = texture(u_tex0, v_uv) * u_tint * u_opacity; }
)",
                                  &error);
  INFO(error);
  REQUIRE(program);
  b.setUniform(program, "u_tint", 1, 1, 1, 1);
  b.setUniform(program, "u_absent", 1.0f);
  auto tex = b.createTexture({2, 2}, render::PixelFormat::Rgba8, kQuad, 0);
  const render::Texture textures[] = {tex};

  b.clear(0, 0, 0, 1);
  b.drawFullscreen(program, textures, render::Mat3::identity(), 1.0f, render::Blend::None);
  auto img = render::readPixels(b.frameSize());
  CHECK(near(pixel(img, 0, 0), {255, 0, 0, 255}));
  CHECK(near(pixel(img, 63, 47), {255, 255, 255, 255}));

  // the left half only, zoomed: the right edge is half way between red and green
  b.clear(0, 0, 0, 1);
  b.drawFullscreen(program, textures, render::Mat3::scaleOffset(0.5f, 0.5f, 0, 0), 1.0f, render::Blend::None);
  img = render::readPixels(b.frameSize());
  CHECK(near(pixel(img, 0, 0), {255, 0, 0, 255}));
  CHECK(near(pixel(img, 63, 0), {128, 128, 0, 255}, 12));

  // a render target's texture through the same program comes out upright too
  auto target = b.createRenderTarget({64, 48}, true);
  REQUIRE(target);
  REQUIRE(target.stencil != 0);
  b.bindRenderTarget(&target);
  render::QuadParams q;
  q.dst = {0, 0, 64, 48};
  q.blend = render::Blend::None;
  b.drawTexturedQuad(tex, q);
  b.bindRenderTarget(nullptr);
  const render::Texture flipped[] = {target.color};
  b.drawFullscreen(program, flipped, render::Mat3::identity(), 1.0f, render::Blend::None);
  img = render::readPixels(b.frameSize());
  CHECK(near(pixel(img, 0, 0), {255, 0, 0, 255}));
  CHECK(near(pixel(img, 0, 47), {0, 0, 255, 255}));

  b.destroyRenderTarget(target);
  b.destroyTexture(tex);
  b.destroyProgram(program);
}

TEST_CASE("a shader that does not compile says why") {
  GlEnv env;
  std::string error;
  auto p = env.backend->compileProgram(render::kFullscreenVertexShader, "#version 300 es\nnot glsl", &error);
  CHECK_FALSE(p);
  CHECK_FALSE(error.empty());
}

TEST_CASE("the same scene hashes the same") {
  GlEnv env;
  auto& b = *env.backend;
  auto tex = b.createTexture({2, 2}, render::PixelFormat::Rgba8, kQuad, 0);
  const auto draw = [&] {
    b.beginFrame(env.platform->drawableSize());
    render::QuadParams q;
    q.dst = {3, 5, 50, 30};
    q.opacity = 0.7f;
    b.drawTexturedQuad(tex, q);
    return render::hashImage(render::readPixels(b.frameSize()));
  };
  const auto first = draw();
  CHECK(draw() == first);
  env.platform->swap();
  CHECK(draw() == first);
  b.destroyTexture(tex);
}
