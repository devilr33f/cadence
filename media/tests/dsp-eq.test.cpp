#include "dsp/eq.h"
#include "support/signals.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/matchers/catch_matchers_floating_point.hpp>

#include <array>
#include <atomic>
#include <cmath>
#include <complex>
#include <cstddef>
#include <limits>
#include <numbers>
#include <random>
#include <thread>
#include <utility>
#include <vector>

using namespace cadence::dsp;
using namespace cadence::test_support;
using Catch::Matchers::WithinAbs;

namespace {

// 100 ms: the 20 ms crossfade and every filter's transient
constexpr std::size_t kSettle = 4800;
// 200 ms: whole cycles of every test frequency, all multiples of 5 Hz
constexpr std::size_t kMeasure = 9600;
constexpr std::array<double, 5> kFrequencies{50, 200, 1000, 5000, 15000};

// the cookbook's analog prototype at the frequency the bilinear transform maps hz to, prewarped at the band's
// frequency: the digital filter's exact magnitude there
double cookbookDb(const EqBand& band, double hz) {
  const double w = std::tan(std::numbers::pi * hz / 48000) / std::tan(std::numbers::pi * band.freqHz / 48000);
  const std::complex<double> s(0, w);
  const double a = std::pow(10.0, band.gainDb / 40);
  const double q = band.q;
  const double root = std::sqrt(a);
  std::complex<double> h;
  switch (band.type) {
    case FilterType::Peaking:
      h = (s * s + s * (a / q) + 1.0) / (s * s + s / (a * q) + 1.0);
      break;
    case FilterType::LowShelf:
      h = a * (s * s + (root / q) * s + a) / (a * s * s + (root / q) * s + 1.0);
      break;
    case FilterType::HighShelf:
      h = a * (a * s * s + (root / q) * s + 1.0) / (s * s + (root / q) * s + a);
      break;
    case FilterType::LowPass:
      h = 1.0 / (s * s + s / q + 1.0);
      break;
    case FilterType::HighPass:
      h = s * s / (s * s + s / q + 1.0);
      break;
    case FilterType::Unspecified:
      h = 1;
      break;
  }
  return db(std::abs(h));
}

// a sine's steady-state gain through a fresh eq, in dB; both channels must agree
double responseDb(const EqSettings& profile, const EqSettings& user, double hz) {
  Eq eq(2);
  eq.set(profile, user);
  auto signal = sine(hz, 0.25, kSettle + kMeasure, 2);
  const auto input = signal;
  inBlocks(signal, 2, 480, [&](std::span<float> block) { eq.process(block); });
  const double left = db(rms(signal, 2, 0, kSettle, kMeasure) / rms(input, 2, 0, kSettle, kMeasure));
  const double right = db(rms(signal, 2, 1, kSettle, kMeasure) / rms(input, 2, 1, kSettle, kMeasure));
  CHECK_THAT(right, WithinAbs(left, 1e-9));
  return left;
}

// 100 ms of stereo noise through a fresh eq
std::vector<float> through(const EqSettings& profile, const EqSettings& user) {
  std::mt19937 random(7);
  std::uniform_real_distribution<float> uniform(-0.25f, 0.25f);
  std::vector<float> signal(kSettle * 2);
  for (auto& sample : signal)
    sample = uniform(random);
  Eq eq(2);
  eq.set(profile, user);
  inBlocks(signal, 2, 480, [&](std::span<float> block) { eq.process(block); });
  return signal;
}

EqSettings user(std::vector<EqBand> bands, float preampDb = 0) { return {.preampDb = preampDb, .bands = bands}; }

}  // namespace

TEST_CASE("each filter type follows the cookbook's response within 0.5 dB") {
  const std::array bands{
      EqBand{FilterType::Peaking, 1000, 6, 1.4f},
      EqBand{FilterType::LowShelf, 200, 6, 0.707f},
      EqBand{FilterType::HighShelf, 5000, -6, 0.707f},
      // q is the shelf's own q, up to slope s = 1
      EqBand{FilterType::HighShelf, 3000, 9, 0.5f},
      EqBand{FilterType::LowPass, 2000, 0, 0.707f},
      EqBand{FilterType::HighPass, 500, 0, 0.707f},
  };
  for (const auto& band : bands) {
    for (const double hz : kFrequencies) {
      CAPTURE(static_cast<int>(band.type), band.freqHz, hz);
      CHECK_THAT(responseDb({}, user({band}), hz), WithinAbs(cookbookDb(band, hz), 0.5));
    }
  }
}

TEST_CASE("the profile and the user eq cascade into the product of their bands and preamps") {
  const EqSettings profile{.preampDb = -3,
                           .bands = {{FilterType::Peaking, 1000, 6, 1}, {FilterType::LowShelf, 100, 4, 0.707f}}};
  const EqSettings mine{.preampDb = 2,
                        .bands = {{FilterType::HighShelf, 8000, -5, 0.707f}, {FilterType::Peaking, 3000, -4, 2}}};
  for (const double hz : kFrequencies) {
    double expected = profile.preampDb + mine.preampDb;
    for (const auto& band : profile.bands)
      expected += cookbookDb(band, hz);
    for (const auto& band : mine.bands)
      expected += cookbookDb(band, hz);
    CAPTURE(hz);
    CHECK_THAT(responseDb(profile, mine, hz), WithinAbs(expected, 0.5));
  }
}

TEST_CASE("values out of range are clamped and bands past the limits or of no type are skipped") {
  const EqBand band{FilterType::Peaking, 1000, 6, 1};
  const auto withBand = [&](float freqHz, float gainDb, float q) {
    return user({{FilterType::Peaking, freqHz, gainDb, q}});
  };
  const float nan = std::numeric_limits<float>::quiet_NaN();

  CHECK(through({}, user({band}, 20)) == through({}, user({band}, 12)));
  CHECK(through({}, user({band}, -20)) == through({}, user({band}, -12)));
  CHECK(through({}, user({band}, nan)) == through({}, user({band})));
  CHECK(through({}, withBand(5, 6, 1)) == through({}, withBand(20, 6, 1)));
  CHECK(through({}, withBand(30000, 6, 1)) == through({}, withBand(20000, 6, 1)));
  CHECK(through({}, withBand(1000, 30, 1)) == through({}, withBand(1000, 12, 1)));
  CHECK(through({}, withBand(1000, -30, 1)) == through({}, withBand(1000, -12, 1)));
  CHECK(through({}, withBand(1000, 6, 0)) == through({}, withBand(1000, 6, 0.1f)));
  CHECK(through({}, withBand(1000, 6, 50)) == through({}, withBand(1000, 6, 10)));
  // a shelf stops at slope s = 1, past which it would overshoot its gain
  const auto slopeOne = static_cast<float>(std::numbers::sqrt2 / 2);
  CHECK(through({}, user({{FilterType::HighShelf, 3000, 12, 10}})) ==
        through({}, user({{FilterType::HighShelf, 3000, 12, slopeOne}})));
  CHECK(through({}, user({{FilterType::LowShelf, 200, 12, 2}})) ==
        through({}, user({{FilterType::LowShelf, 200, 12, slopeOne}})));

  const EqBand untyped{FilterType::Unspecified, 1000, 6, 1};
  const EqBand unknown{static_cast<FilterType>(9), 1000, 6, 1};
  const EqBand broken{FilterType::Peaking, nan, 6, 1};
  CHECK(through({}, user({untyped, unknown, broken, band})) == through({}, user({band})));

  std::vector<EqBand> many;
  for (int i = 0; i < 18; ++i)
    many.push_back({FilterType::Peaking, 100.0f * static_cast<float>(i + 1), 3, 2});
  const std::vector<EqBand> sixteen(many.begin(), many.begin() + 16);
  const std::vector<EqBand> ten(many.begin(), many.begin() + 10);
  CHECK(through(user(many), {}) == through(user(sixteen), {}));
  CHECK(through({}, user(many)) == through({}, user(ten)));
}

TEST_CASE("a set while a sine plays crossfades from the old cascade without a click") {
  constexpr std::size_t kFrames = 4 * kSettle;
  // the old cascade has settled by kSettle; the set lands a quarter cycle past a zero crossing, at the sine's peak,
  // where a step would be largest
  constexpr std::size_t kSetAt = 2 * kSettle + 60;
  Eq eq(2);
  const auto process = [&](std::span<float> block) { eq.process(block); };
  eq.set({}, user({{FilterType::Peaking, 200, -3, 1}}, -3));
  auto signal = sine(200, 0.25, kFrames, 2);
  std::span<float> all(signal);
  inBlocks(all.first(kSetAt * 2), 2, 480, process);
  // a fade from the dry signal, or from an old chain reset, would step away from the old -6 dB at once
  eq.set({}, user({{FilterType::Peaking, 200, 3, 1}}, 3));
  inBlocks(all.subspan(kSetAt * 2), 2, 480, process);

  const double before = rms(signal, 2, 0, kSettle, kSettle);
  const double after = rms(signal, 2, 0, kFrames - kSettle, kSettle);
  CHECK_THAT(db(before / (0.25 / std::numbers::sqrt2)), WithinAbs(-6, 0.1));
  CHECK_THAT(db(after / before), WithinAbs(12, 0.1));
  const double sineSteps =
      std::max(maxStep(signal, 2, 0, kSettle, kSettle), maxStep(signal, 2, 0, kFrames - kSettle, kSettle));
  CHECK(maxStep(signal, 2, 0, 0, kFrames) <= sineSteps + 0.001);
  CHECK(maxStep(signal, 2, 1, 0, kFrames) <= sineSteps + 0.001);
}

TEST_CASE("sets from another thread while audio runs hand over only whole published cascades") {
  // levels only, split over both layers: -12 dB as -1 and -11, -6 dB as -1 and -5. sixteen flat bands keep set busy
  // between the two preamps, so a slot read while set writes it mostly carries -1 dB, outside the two levels. that
  // catches set writing a slot process may copy; a race only a preemption mid-copy would show needs a thread sanitizer
  std::vector<EqBand> flatBands;
  for (int i = 0; i < 16; ++i)
    flatBands.push_back({FilterType::Peaking, 100.0f * static_cast<float>(i + 1), 0, 1});
  const auto set = [&](Eq& eq, int which) {
    eq.set(user(flatBands, -1), user({}, which == 0 ? -11.0f : -5.0f));
  };
  const double low = std::pow(10.0, -12 / 20.0);
  const double high = std::pow(10.0, -6 / 20.0);
  // the lowest and highest gain over the samples from first on
  const auto gains = [](const std::vector<float>& in, const std::vector<float>& out, std::size_t first) {
    std::pair<double, double> range{1e9, -1e9};
    for (std::size_t i = first; i < in.size(); ++i) {
      if (std::abs(in[i]) < 0.01f)
        continue;
      const double gain = static_cast<double>(out[i]) / static_cast<double>(in[i]);
      range = {std::min(range.first, gain), std::max(range.second, gain)};
    }
    return range;
  };

  Eq eq(2);
  const auto process = [&](std::span<float> block) { eq.process(block); };
  set(eq, 0);
  auto settle = sine(1000, 0.25, 2 * kEqFadeFrames, 2);
  inBlocks(settle, 2, 64, process);

  std::atomic<bool> done{false};
  std::atomic<int> last{0};
  std::thread setter([&] {
    for (int i = 1; !done.load(); ++i) {
      set(eq, i % 2);
      last = i % 2;
    }
  });
  const auto input = sine(1000, 0.25, 4 * 48000, 2);
  auto output = input;
  inBlocks(output, 2, 64, process);
  done = true;
  setter.join();
  const auto [quietest, loudest] = gains(input, output, 0);
  CHECK(quietest >= low - 1e-6);
  CHECK(loudest <= high + 1e-6);

  // the last design set wins once a fade in flight and its own are over
  const auto tailIn = sine(1000, 0.25, 4 * kEqFadeFrames, 2);
  auto tailOut = tailIn;
  inBlocks(tailOut, 2, 64, process);
  const double expected = last == 0 ? low : high;
  const auto [settledLow, settledHigh] = gains(tailIn, tailOut, 2 * 3 * kEqFadeFrames);
  CHECK_THAT(settledLow, WithinAbs(expected, 1e-6));
  CHECK_THAT(settledHigh, WithinAbs(expected, 1e-6));
}

TEST_CASE("a nan or inf burst counts as silence and leaves the filters working") {
  const float nan = std::numeric_limits<float>::quiet_NaN();
  const float inf = std::numeric_limits<float>::infinity();
  const auto mine = user({{FilterType::Peaking, 1000, 6, 1}, {FilterType::LowShelf, 100, 4, 0.707f}}, -3);
  auto clean = sine(1000, 0.25, 3 * kSettle, 2);
  auto dirty = clean;
  for (std::size_t i = 2 * kSettle + 7; i < 2 * kSettle + 39; ++i) {
    clean[i] = 0;
    dirty[i] = i % 3 == 0 ? nan : i % 3 == 1 ? inf : -inf;
  }
  Eq reference(2);
  Eq eq(2);
  reference.set({}, mine);
  eq.set({}, mine);
  inBlocks(clean, 2, 480, [&](std::span<float> block) { reference.process(block); });
  inBlocks(dirty, 2, 480, [&](std::span<float> block) { eq.process(block); });
  CHECK(dirty == clean);

  Eq flat(2);
  std::vector<float> lone{nan, inf, 0.5f, -inf};
  flat.process(lone);
  CHECK(lone == std::vector<float>{0, 0, 0.5f, 0});
}
