#include "render/frame-scheduler.h"
#include "render/frame-stats.h"
#include "render/gles-backend.h"

#include <catch2/catch_approx.hpp>
#include <catch2/catch_test_macros.hpp>

using namespace cadence::screen::render;

TEST_CASE("nothing is presented before the ui's first layout") {
  FrameScheduler s;
  FrameInputs in;
  in.exposed = true;
  CHECK_FALSE(s.decide(in).present);
  in.exposed = false;
  in.uiReady = true;
  CHECK(s.decide(in).present);
}

TEST_CASE("render when dirty: an unchanged frame is not presented again") {
  FrameScheduler s;
  FrameInputs in;
  in.uiReady = true;
  in.exposed = true;
  REQUIRE(s.decide(in).present);
  s.presented();
  in.exposed = false;
  CHECK_FALSE(s.decide(in).present);
  in.uiDirty = true;
  CHECK(s.decide(in).present);
}

TEST_CASE("a starting video session holds the screen until its first frame (s1's startup glitch)") {
  FrameScheduler s(3000);
  FrameInputs in;
  in.uiReady = true;
  in.exposed = true;
  REQUIRE(s.decide(in).present);
  s.presented();

  in.exposed = false;
  in.videoInGl = true;
  in.nowMs = 100;
  in.uiDirty = true;
  CHECK_FALSE(s.decide(in).present);
  CHECK(s.holding());
  in.nowMs = 200;
  CHECK_FALSE(s.decide(in).present);

  // the first frame is due: rendered, and with it the screen is shown
  in.nowMs = 300;
  in.videoFrameDue = true;
  const auto d = s.decide(in);
  CHECK(d.renderVideo);
  CHECK(d.present);
  CHECK_FALSE(s.holding());
}

TEST_CASE("the startup hold gives up after its timeout") {
  FrameScheduler s(1000);
  FrameInputs in;
  in.uiReady = true;
  in.videoInGl = true;
  in.uiDirty = true;
  in.nowMs = 0;
  CHECK_FALSE(s.decide(in).present);
  in.nowMs = 999;
  CHECK_FALSE(s.decide(in).present);
  in.nowMs = 1000;
  CHECK(s.decide(in).present);
}

TEST_CASE("the hold waits for the ui too") {
  FrameScheduler s;
  FrameInputs in;
  in.videoInGl = true;
  in.videoFrameDue = true;
  in.uiReady = false;
  CHECK(s.decide(in).renderVideo);
  CHECK_FALSE(s.decide(in).present);
  in.uiReady = true;
  in.firstVideoFrame = true;
  CHECK(s.decide(in).present);
}

TEST_CASE("frame stats: percentiles, fps and late frames over the window") {
  FrameStats stats(10, 50.0);
  // an animation: 20 ms apart, 5 ms of work; one frame 60 ms late
  std::int64_t t = 0;
  for (int i = 0; i < 10; i++) {
    t += i == 5 ? 60'000'000 : 20'000'000;
    stats.add(t - 5'000'000, t, true);
  }
  const auto s = stats.summary();
  CHECK(s.frames == 10);
  CHECK(s.workP50Ms == Catch::Approx(5));
  CHECK(s.late == 1);
  CHECK(s.chained == 9);
  CHECK(s.fps == Catch::Approx(9.0 / 0.22));
  CHECK(s.chainedFps == Catch::Approx(9.0 / 0.22));
  // the window rolls
  for (int i = 0; i < 10; i++) {
    t += 20'000'000;
    stats.add(t - 2'000'000, t, true);
  }
  CHECK(stats.summary().late == 0);
  CHECK(stats.summary().workMaxMs == Catch::Approx(2));
}

TEST_CASE("frame stats: render-on-demand's idle gaps are not late frames") {
  // regression: the stick logged late=297 of 300 for a screen that drew a frame now and then
  FrameStats stats(300, 59.94);
  std::int64_t t = 0;
  for (int i = 0; i < 20; i++) {
    // a key every 2 s, then a 150 ms transition at the display's rate
    t += 2'000'000'000;
    stats.add(t - 3'000'000, t, false);
    for (int f = 0; f < 9; f++) {
      t += 16'683'350;
      stats.add(t - 3'000'000, t, true);
    }
  }
  const auto s = stats.summary();
  CHECK(s.late == 0);
  CHECK(s.chained == 180);
  CHECK(s.chainedFps == Catch::Approx(59.94).epsilon(0.01));
  CHECK(s.fps < 6);
}

TEST_CASE("the loop sleeps when idle and looks again at once while the ui animates") {
  // regression: the wait came before the ui's update, so a transition got its first frame and then the cap's 250 ms
  WaitInputs idle;
  idle.maxWaitMs = 250;
  CHECK(loopWaitMs(idle) == 250);
  // a timer, a spinner's next frame or a caret: that long, not the cap
  WaitInputs due = idle;
  due.uiDueMs = 40;
  CHECK(loopWaitMs(due) == 40);
  due.keysDueMs = 12;
  CHECK(loopWaitMs(due) == 12);
  // an animation's frame was just presented: the next turn at once, the swap's vsync pacing it
  WaitInputs anim = idle;
  anim.animating = true;
  CHECK(loopWaitMs(anim) == 0);
  WaitInputs owed = idle;
  owed.frameOwed = true;
  CHECK(loopWaitMs(owed) == 0);
  // in the background or with the display off nothing is drawn: a dirty ui must not spin the loop
  WaitInputs bg = owed;
  bg.animating = true;
  bg.uiDueMs = 0;
  bg.hidden = true;
  CHECK(loopWaitMs(bg) == 250);
  bg.keysDueMs = 30;
  CHECK(loopWaitMs(bg) == 30);
  // the startup hold presents nothing: a frame owed polls instead of spinning
  WaitInputs held = owed;
  held.holding = true;
  CHECK(loopWaitMs(held) == kHoldPollMs);
  held.maxWaitMs = 5;
  CHECK(loopWaitMs(held) == 5);
}

TEST_CASE("scissor rects flip to gl's bottom-left origin and clamp") {
  CHECK(toGlScissor({10, 20, 100, 50}, {1920, 1080}) == Rect{10, 1010, 100, 50});
  CHECK(toGlScissor({-10, -10, 30, 30}, {100, 100}) == Rect{0, 80, 20, 20});
  CHECK(toGlScissor({90, 90, 50, 50}, {100, 100}) == Rect{90, 0, 10, 10});
  CHECK(toGlScissor({0, 0, -5, 10}, {100, 100}) == Rect{0, 90, 0, 10});
}
