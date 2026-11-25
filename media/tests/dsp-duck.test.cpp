#include "dsp/duck.h"
#include "support/signals.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/matchers/catch_matchers_floating_point.hpp>

#include <algorithm>
#include <cmath>
#include <cstddef>
#include <limits>
#include <vector>

using namespace cadence::dsp;
using namespace cadence::test_support;
using Catch::Matchers::WithinAbs;

namespace {

constexpr std::size_t kBlock = 480;
// the steepest ramp: 1 down to 0.15 in 200 ms
constexpr double kSteepest = 0.85 / 9600;

// the gain on each of the next frames, read off dc, with setActive before every block
void run(DuckEnvelope& duck, bool active, std::size_t frames, std::vector<double>& gains) {
  std::vector<float> dc(frames * 2, 1.0f);
  inBlocks(dc, 2, kBlock, [&](std::span<float> block) {
    duck.setActive(active);
    duck.apply(block);
  });
  for (std::size_t i = 0; i < frames; ++i) {
    REQUIRE(dc[2 * i] == dc[2 * i + 1]);
    gains.push_back(dc[2 * i]);
  }
}

// a sine's gain through the duck, in dB, over 200 ms after the first settle frames
double sineDb(DuckEnvelope& duck, bool active, double hz, std::size_t settle) {
  const std::size_t measure = framesFor(200);
  auto signal = sine(hz, 0.5, settle + measure, 2);
  const auto input = signal;
  inBlocks(signal, 2, kBlock, [&](std::span<float> block) {
    duck.setActive(active);
    duck.apply(block);
  });
  return db(rms(signal, 2, 0, settle, measure) / rms(input, 2, 0, settle, measure));
}

// the highest gain over frames [from, to)
double highest(const std::vector<double>& gains, std::size_t from, std::size_t to) {
  return *std::max_element(gains.begin() + static_cast<std::ptrdiff_t>(from),
                           gains.begin() + static_cast<std::ptrdiff_t>(to));
}

double largestStep(const std::vector<double>& gains) {
  double step = 0;
  for (std::size_t i = 1; i < gains.size(); ++i)
    step = std::max(step, std::abs(gains[i] - gains[i - 1]));
  return step;
}

}  // namespace

TEST_CASE("the duck ramps down over 200 ms, holds 0.5 s after it ends and comes back over 500 ms") {
  DuckEnvelope duck(DuckMode::Duck);
  std::vector<double> gains;
  run(duck, true, framesFor(1000), gains);
  CHECK_THAT(gains[0], WithinAbs(1, 1e-3));
  CHECK_THAT(gains[framesFor(100)], WithinAbs(0.575, 1e-3));
  CHECK_THAT(gains[framesFor(200)], WithinAbs(0.15, 1e-3));
  CHECK_THAT(gains.back(), WithinAbs(0.15, 1e-6));

  gains.clear();
  run(duck, false, framesFor(1500), gains);
  CHECK(highest(gains, 0, framesFor(500)) <= 0.15 + 1e-6);
  CHECK_THAT(gains[framesFor(750)], WithinAbs(0.575, 1e-3));
  CHECK_THAT(gains[framesFor(1000)], WithinAbs(1, 1e-3));
  CHECK(gains.back() == 1);
}

TEST_CASE("an activation during the ramp up turns around from the current gain") {
  DuckEnvelope duck(DuckMode::Duck);
  std::vector<double> gains;
  run(duck, true, framesFor(1000), gains);
  run(duck, false, framesFor(750), gains);
  const std::size_t back = gains.size();
  run(duck, true, framesFor(400), gains);
  CHECK_THAT(gains[back], WithinAbs(0.575, 1e-3));
  CHECK_THAT(gains[back + framesFor(200)], WithinAbs(0.15, 1e-3));
  CHECK(largestStep(gains) <= kSteepest + 1e-7);
}

TEST_CASE("an activation during the hold keeps the duck and a new end holds 0.5 s again") {
  DuckEnvelope duck(DuckMode::Duck);
  std::vector<double> gains;
  run(duck, true, framesFor(300), gains);
  run(duck, false, framesFor(300), gains);
  run(duck, true, framesFor(300), gains);
  const std::size_t end = gains.size();
  run(duck, false, framesFor(1000), gains);
  CHECK(highest(gains, framesFor(200), end + framesFor(500)) <= 0.15 + 1e-6);
  CHECK_THAT(gains[end + framesFor(750)], WithinAbs(0.575, 1e-3));
}

TEST_CASE("muffle low-passes the ducked music and leaves it alone at full gain") {
  DuckEnvelope idleLow(DuckMode::Muffle);
  DuckEnvelope idleHigh(DuckMode::Muffle);
  CHECK_THAT(sineDb(idleLow, false, 300, framesFor(50)), WithinAbs(0, 0.1));
  CHECK_THAT(sineDb(idleHigh, false, 5000, framesFor(50)), WithinAbs(0, 0.1));

  DuckEnvelope low(DuckMode::Muffle);
  DuckEnvelope high(DuckMode::Muffle);
  const double lowDb = sineDb(low, true, 300, framesFor(400));
  const double highDb = sineDb(high, true, 5000, framesFor(400));
  CHECK_THAT(lowDb, WithinAbs(db(0.15), 0.5));
  CHECK(lowDb - highDb >= 20);
}

TEST_CASE("the plain duck lowers every frequency alike") {
  DuckEnvelope low(DuckMode::Duck);
  DuckEnvelope high(DuckMode::Duck);
  CHECK_THAT(sineDb(low, true, 300, framesFor(400)), WithinAbs(db(0.15), 0.01));
  CHECK_THAT(sineDb(high, true, 5000, framesFor(400)), WithinAbs(db(0.15), 0.01));
}

TEST_CASE("a nan or inf burst counts as silence and leaves the muffle filter working") {
  const float nan = std::numeric_limits<float>::quiet_NaN();
  const float inf = std::numeric_limits<float>::infinity();
  auto clean = sine(300, 0.5, framesFor(600), 2);
  auto dirty = clean;
  for (std::size_t i = 2 * framesFor(300) + 5; i < 2 * framesFor(300) + 37; ++i) {
    clean[i] = 0;
    dirty[i] = i % 3 == 0 ? nan : i % 3 == 1 ? inf : -inf;
  }
  DuckEnvelope reference(DuckMode::Muffle);
  DuckEnvelope duck(DuckMode::Muffle);
  const auto ducked = [](DuckEnvelope& envelope) {
    return [&envelope](std::span<float> block) {
      envelope.setActive(true);
      envelope.apply(block);
    };
  };
  inBlocks(clean, 2, kBlock, ducked(reference));
  inBlocks(dirty, 2, kBlock, ducked(duck));
  CHECK(dirty == clean);
}
