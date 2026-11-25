#include "app/options.h"
#include "render/perf-hud.h"
#include "render/perf.h"

#include <catch2/catch_approx.hpp>
#include <catch2/catch_test_macros.hpp>

#include <cstdint>

using namespace cadence::screen::render::perf;
using Catch::Approx;

namespace {

constexpr std::int64_t kMs = 1000000;

// a presented frame ending at endMs, with update and draw work and a swap
Turn frame(std::int64_t endMs, double updateMs, double drawMs, double swapMs, bool chained = true) {
  Turn t;
  t.presented = true;
  t.chained = chained;
  t.uiDirty = true;
  t.endNs = endMs * kMs;
  t.ns[static_cast<std::size_t>(Phase::Update)] = static_cast<std::int64_t>(updateMs * kMs);
  t.ns[static_cast<std::size_t>(Phase::Draw)] = static_cast<std::int64_t>(drawMs * kMs);
  t.ns[static_cast<std::size_t>(Phase::Swap)] = static_cast<std::int64_t>(swapMs * kMs);
  return t;
}

}  // namespace

TEST_CASE("perf: the cpu work leaves the swap's wait out, the frame keeps it") {
  Recorder r(600, 60);
  r.reset(1);
  for (int i = 1; i <= 100; ++i) r.add(frame(i * 16, 1.0, 2.0, 10.0));
  const auto s = r.summary(100 * 16 * kMs);
  CHECK(s.frames == 100);
  CHECK(s.cpuMs.p50 == Approx(3.0));
  CHECK(s.frameMs.p50 == Approx(13.0));
  CHECK(s.phaseMs[static_cast<std::size_t>(Phase::Swap)].p95 == Approx(10.0));
  CHECK(s.late == 0);
}

TEST_CASE("perf: percentiles see the spike, and the worst frames name it") {
  Recorder r(600, 60);
  r.reset(1);
  for (int i = 1; i <= 99; ++i) r.add(frame(i * 16, 1.0, 1.0, 1.0));
  r.add(frame(100 * 16, 1.0, 40.0, 1.0));
  const auto s = r.summary(100 * 16 * kMs);
  CHECK(s.cpuMs.p50 == Approx(2.0));
  CHECK(s.cpuMs.max == Approx(41.0));
  const auto worst = r.worst(2);
  REQUIRE(worst.size() == 2);
  CHECK(worst[0].ns[static_cast<std::size_t>(Phase::Draw)] == 40 * kMs);
}

TEST_CASE("perf: an animation's frame later than 1.5 refresh periods is late; an idle gap is not") {
  Recorder r(600, 60);
  r.reset(1);
  r.add(frame(1000, 1, 1, 1, false));
  r.add(frame(1016, 1, 1, 1));
  r.add(frame(1050, 1, 1, 1));         // 34 ms after the one before: a missed vsync
  r.add(frame(3000, 1, 1, 1, false));  // after an idle wait: not an animation's frame
  const auto s = r.summary(3000 * kMs);
  CHECK(s.chained == 2);
  CHECK(s.late == 1);
  const auto bars = r.recent(10);
  REQUIRE(bars.size() == 4);
  CHECK(bars[2].late);
  CHECK_FALSE(bars[3].late);
}

TEST_CASE("perf: turns that present nothing count as wakeups, not frames") {
  Recorder r(600, 60);
  r.reset(1);
  for (int i = 0; i < 8; ++i) {
    Turn t;
    t.endNs = (i + 1) * 250 * kMs;
    t.ns[static_cast<std::size_t>(Phase::Update)] = kMs / 10;
    r.add(t);
  }
  const auto s = r.summary(2000 * kMs);
  CHECK(s.frames == 0);
  CHECK(s.turns == 8);
  CHECK(s.turnsPerS == Approx(4.0).epsilon(0.01));
  CHECK(s.idleTurns == 8);
}

TEST_CASE("perf: spans and counters a turn took are its own, and the ring keeps the newest frames") {
  add(Span::Upload, 3 * kMs, 2);
  count(Counter::Draws, 40);
  Turn t = frame(16, 1, 1, 1);
  t.spans = take();
  t.counters = takeCounters();
  CHECK(t.spans[static_cast<std::size_t>(Span::Upload)].count == 2);
  CHECK(t.counters[static_cast<std::size_t>(Counter::Draws)] == 40);
  CHECK(take()[static_cast<std::size_t>(Span::Upload)].count == 0);

  Recorder r(4, 60);
  r.reset(1);
  r.add(t);
  for (int i = 2; i <= 6; ++i) r.add(frame(i * 16, static_cast<double>(i), 0, 0));
  const auto s = r.summary(6 * 16 * kMs);
  CHECK(s.frames == 6);
  CHECK(s.spanCount[static_cast<std::size_t>(Span::Upload)] == 2);
  // the ring holds 4: frames 3 to 6
  CHECK(s.cpuMs.max == Approx(6.0));
  CHECK(s.phaseMs[static_cast<std::size_t>(Phase::Update)].p50 >= 3.0);
}

TEST_CASE("perf: the hud is a panel with a bar a frame and the window's numbers") {
  Recorder r(600, 60);
  r.reset(1);
  for (int i = 1; i <= 10; ++i) r.add(frame(i * 16, 2, 2, 8));
  std::vector<std::uint8_t> px;
  rasterHud(px, r.recent(Hud::kWidth / 2), r.summary(160 * kMs), r.periodMs());
  REQUIRE(px.size() == static_cast<std::size_t>(Hud::kWidth) * Hud::kHeight * 4);
  // every pixel is covered (premultiplied, at least the panel's alpha)
  for (std::size_t i = 3; i < px.size(); i += 4) REQUIRE(px[i] >= 204);
  const auto lines = hudLines(r.summary(160 * kMs));
  REQUIRE(lines.size() == 4);
  CHECK(lines[0].rfind("FPS ", 0) == 0);
}

TEST_CASE("perf: the env switches the profile's line and the hud") {
  cadence::screen::app::AppOptions o;
  cadence::screen::app::applyPerfEnv(o, nullptr, nullptr);
  CHECK(o.perfLogMs == 60000);
  CHECK_FALSE(o.perfGpu);
  CHECK_FALSE(o.hud);
  cadence::screen::app::applyPerfEnv(o, "0", "0");
  CHECK(o.perfLogMs == 60000);
  CHECK_FALSE(o.hud);
  cadence::screen::app::applyPerfEnv(o, "gpu", "1");
  CHECK(o.perfLogMs == 5000);
  CHECK(o.perfGpu);
  CHECK(o.hud);
}
