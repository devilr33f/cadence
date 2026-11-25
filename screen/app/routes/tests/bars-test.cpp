// the classic background bars' model: plain c++ so the same test runs in the native simulator and the screen suite
#include "app/routes/audio/bars.h"

#include <array>
#include <cmath>
#include <cstdio>
#include <string>

using cadence::screen::render::Rect;
using cadence::screen::routes::BarSmoother;
using cadence::screen::routes::barLayout;
using cadence::screen::routes::barRect;

namespace {

int failures = 0;

void check(bool ok, const std::string& what) {
  if (ok) return;
  ++failures;
  std::printf("fail: %s\n", what.c_str());
}

bool near(double a, double b, double epsilon = 0.05) { return std::fabs(a - b) <= epsilon; }

std::array<std::uint8_t, 16> filled(std::uint8_t v) {
  std::array<std::uint8_t, 16> b{};
  b.fill(v);
  return b;
}

void attackIsImmediate() {
  BarSmoother b;
  b.bands(filled(0), 0);
  b.step(0);
  b.bands(filled(255), 33);
  b.step(33);
  b.step(49);
  // the owner's "slow bars": a loud frame shows within a display frame or two, no interpolation behind it
  check(b.heights()[8] > BarSmoother::kMaxDp * 0.9, "attack: up within a frame");
  for (int t = 65; t <= 600; t += 16) b.step(t);
  check(near(b.heights()[8], BarSmoother::kMaxDp), "attack: reaches the loud target");
  check(!b.active(600), "attack: sleeps at a constant target");
}

void releaseFallsUnderGravity() {
  BarSmoother b;
  b.bands(filled(255), 0);
  for (int t = 0; t <= 300; t += 16) b.step(t);
  b.step(304);
  b.bands(filled(60), 304);
  b.step(320);
  const double first = BarSmoother::kMaxDp - b.heights()[8];
  b.step(336);
  const double second = BarSmoother::kMaxDp - b.heights()[8] - first;
  check(first > 0 && second > first, "release: falls faster as it goes");
  check(b.heights()[8] > BarSmoother::kMaxDp * 0.8, "release: no cut, a fall");
  for (int t = 352; t <= 1200; t += 16) b.step(t);
  check(b.heights()[8] < BarSmoother::kMaxDp * 0.5, "release: reaches the quieter frame");
}

void quietTrackFillsTheRange() {
  BarSmoother b;
  // a quiet master: its loudest bands at -33 dbfs
  std::array<std::uint8_t, 16> quiet{};
  for (std::size_t i = 0; i < 16; ++i) quiet[i] = static_cast<std::uint8_t>(i < 4 ? 115 : i < 12 ? 80 : 40);
  for (int t = 0; t <= 4000; t += 33) {
    b.bands(quiet, t);
    b.step(t);
  }
  check(b.heights()[0] > BarSmoother::kMaxDp * 0.85, "agc: a quiet track's loud band reaches near the top");
  check(near(b.heights()[14], BarSmoother::kMinDp), "agc: a band in the noise (under -45 dbfs) stays down");
}

void kickBounces() {
  BarSmoother b;
  std::array<std::uint8_t, 16> bed{};
  for (std::size_t i = 0; i < 16; ++i) bed[i] = static_cast<std::uint8_t>(150 - 4 * i);
  for (int t = 0; t <= 2000; t += 33) {
    b.bands(bed, t);
    b.step(t);
  }
  check(b.bounce() == 0, "kick: a steady bed never bounces");
  auto kick = bed;
  for (std::size_t i = 0; i < 4; ++i) kick[i] = 235;
  b.bands(kick, 2033);
  check(b.bounce() > 0.9, "kick: the low bands' jump bounces the row");
  check(b.active(2033), "kick: the bounce owes frames");
  for (int t = 2049; t <= 2600; t += 16) b.step(t);
  check(b.bounce() == 0, "kick: the bounce decays");
}

void pausedDecaysThenSleeps() {
  BarSmoother b;
  b.bands(filled(255), 0);
  for (int t = 0; t <= 500; t += 16) b.step(t);
  b.setPaused(true);
  b.step(516);
  const double before = b.heights()[5];
  b.step(532);
  check(b.heights()[5] < before && b.heights()[5] > BarSmoother::kMinDp, "idle: falls smoothly, not a cut");
  check(b.active(532), "idle: owes frames while falling");
  for (int t = 548; t <= 1800; t += 16) b.step(t);
  check(near(b.heights()[5], BarSmoother::kMinDp), "idle: reaches the floor");
  check(!b.active(1800), "idle: asleep at the floor");
  check(!b.step(1816), "idle: no more redraws");
}

void reducedMotion() {
  BarSmoother b;
  b.setReducedMotion(true);
  b.bands(filled(255), 0);
  b.step(0);
  check(near(b.heights()[0], BarSmoother::kMaxDp), "reduced: target directly, no tween");
  check(!b.active(0), "reduced: no display-rate animation");
  b.bands(filled(160), 50);
  check(!b.step(50), "reduced: at most ten updates a second");
  check(b.step(100), "reduced: the next tick applies the frame");
  check(b.bounce() == 0, "reduced: no bounce");
  b.setPaused(true);
  check(b.active(150), "reduced: pause keeps the next settling frame owed");
  check(!b.step(150), "reduced: the settling frame waits for the 10 hz tick");
  b.step(200);
  check(near(b.heights()[0], BarSmoother::kMinDp), "reduced: pause settles at the next update");
  check(!b.active(200), "reduced: asleep once paused bars settle");
}

void layoutIsIntegralAndSymmetric() {
  for (const auto& screen : {Rect{0, 0, 1920, 1080}, Rect{0, 0, 1919, 1079}, Rect{0, 0, 1280, 720}}) {
    const auto layout = barLayout(screen);
    check(layout.valid(), "layout: valid at tv sizes");
    check(layout.band.y >= screen.height * 2 / 3, "layout: band stays in the bottom third");
    const auto first = barRect(layout, 0, BarSmoother::kMaxDp);
    const auto last = barRect(layout, BarSmoother::kBars - 1, BarSmoother::kMaxDp);
    check(first.x - screen.x == screen.x + screen.width - last.x - last.width, "layout: equal outer margins");
    for (int i = 0; i < BarSmoother::kBars; ++i) {
      const auto rect = barRect(layout, i, 63.71);
      check(rect.width == layout.barWidth, "layout: equal bar widths");
      check(rect.y + rect.height == layout.baseline, "layout: one baseline");
      check(rect.height == barRect(layout, i, 63.71).height, "layout: snapped height is stable");
      if (i > 0) {
        const auto previous = barRect(layout, i - 1, 63.71);
        check(rect.x - previous.x - previous.width == layout.gap, "layout: equal integral gaps");
      }
    }
    check(barRect(layout, 0, BarSmoother::kMaxDp).height == layout.maxHeight, "layout: full-scale height");
    check(barRect(layout, 0, BarSmoother::kMinDp).height > 0, "layout: idle floor remains visible");
  }
  check(!barLayout({0, 0, 0, 0}).valid(), "layout: empty screen draws nothing");
}

}  // namespace

int main() {
  attackIsImmediate();
  releaseFallsUnderGravity();
  quietTrackFillsTheRange();
  kickBounces();
  pausedDecaysThenSleeps();
  reducedMotion();
  layoutIsIntegralAndSymmetric();
  if (failures == 0) {
    std::printf("audio-bars: ok\n");
    return 0;
  }
  std::printf("audio-bars: %d failure(s)\n", failures);
  return 1;
}
