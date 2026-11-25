#include "platform/lifecycle.h"
#include "platform/overscan.h"

#include <catch2/catch_approx.hpp>
#include <catch2/catch_test_macros.hpp>

using namespace cadence::screen;

TEST_CASE("the safe area insets each side by the overscan") {
  CHECK(platform::safeArea({1920, 1080}, 0) == render::Rect{0, 0, 1920, 1080});
  CHECK(platform::safeArea({1920, 1080}, 5) == render::Rect{96, 54, 1728, 972});
  CHECK(platform::safeArea({1920, 1080}, 2.5) == render::Rect{48, 27, 1824, 1026});
  CHECK(platform::safeArea({1920, 1080}, -3) == render::Rect{0, 0, 1920, 1080});
}

TEST_CASE("dp ratio: the design's 1080p is 1") {
  CHECK(platform::dpRatio({1920, 1080}) == Catch::Approx(1.0));
  CHECK(platform::dpRatio({3840, 2160}) == Catch::Approx(2.0));
  CHECK(platform::dpRatio({1280, 720}) == Catch::Approx(2.0 / 3.0));
  CHECK(platform::dpRatio({0, 0}) == Catch::Approx(1.0));
}

TEST_CASE("sdl's app events are the lifecycle") {
  SDL_Event e{};
  e.type = SDL_EVENT_DID_ENTER_BACKGROUND;
  CHECK(platform::lifecycleOf(e) == platform::Lifecycle::Pause);
  e.type = SDL_EVENT_DID_ENTER_FOREGROUND;
  CHECK(platform::lifecycleOf(e) == platform::Lifecycle::Resume);
  e.type = SDL_EVENT_TERMINATING;
  CHECK(platform::lifecycleOf(e) == platform::Lifecycle::Quit);
  e.type = SDL_EVENT_WINDOW_PIXEL_SIZE_CHANGED;
  CHECK(platform::lifecycleOf(e) == platform::Lifecycle::Resize);
  e.type = SDL_EVENT_KEY_DOWN;
  CHECK(platform::lifecycleOf(e) == platform::Lifecycle::None);
}
