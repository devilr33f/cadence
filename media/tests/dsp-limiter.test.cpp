#include "dsp/limiter.h"
#include "support/signals.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/matchers/catch_matchers_floating_point.hpp>

#include <cmath>
#include <cstddef>
#include <numbers>
#include <vector>

using namespace cadence::dsp;
using namespace cadence::test_support;
using Catch::Matchers::WithinAbs;

namespace {

constexpr std::size_t kLatency = 240;

// a 1 kHz stereo sine at 0.5 (-6 dBFS) with a +12 dB stretch over frames [from, to)
std::vector<float> burstSine(std::size_t frames, std::size_t from, std::size_t to) {
  std::vector<float> samples(frames * 2);
  for (std::size_t i = 0; i < frames; ++i) {
    const double level = i >= from && i < to ? 2.0 : 0.5;
    const auto value = static_cast<float>(level * std::sin(2 * std::numbers::pi * 1000 * static_cast<double>(i) / 48000));
    samples[2 * i] = value;
    samples[2 * i + 1] = -value;
  }
  return samples;
}

void limit(Limiter& limiter, std::vector<float>& samples) {
  inBlocks(samples, 2, 480, [&](std::span<float> block) { limiter.process(block); });
}

}  // namespace

TEST_CASE("the latency is the 5 ms look-ahead") {
  CHECK(Limiter::latencyFrames() == kLatency);
}

TEST_CASE("a +12 dB burst never passes the ceiling") {
  constexpr std::size_t kFrom = framesFor(200);
  constexpr std::size_t kTo = framesFor(300);
  Limiter limiter;
  auto input = burstSine(framesFor(800), kFrom, kTo);
  // and a lone spike far above everything
  input[2 * framesFor(600)] = 8;
  auto samples = input;
  limit(limiter, samples);

  float peak = 0;
  for (const float sample : samples)
    peak = std::max(peak, std::abs(sample));
  CHECK(peak <= kLimiterCeiling);
  CHECK(peak >= kLimiterCeiling - 1e-4f);
  // untouched until the burst enters the look-ahead
  for (std::size_t i = 2 * kLatency; i < 2 * kFrom; ++i)
    REQUIRE(samples[i] == input[i - 2 * kLatency]);
}

TEST_CASE("a -6 dBFS sine passes at unity once the look-ahead filled") {
  Limiter limiter;
  auto samples = burstSine(framesFor(500), 0, 0);
  const auto input = samples;
  limit(limiter, samples);
  for (std::size_t i = 0; i < 2 * kLatency; ++i)
    REQUIRE(samples[i] == 0);
  for (std::size_t i = 2 * kLatency; i < samples.size(); ++i)
    REQUIRE_THAT(samples[i], WithinAbs(input[i - 2 * kLatency], 1e-6));
  CHECK_THAT(db(rms(samples, 2, 0, kLatency, framesFor(400)) / rms(input, 2, 0, 0, framesFor(400))),
             WithinAbs(0, 0.01));
}

TEST_CASE("the gain drops just enough within the look-ahead and releases with a 100 ms time constant") {
  constexpr std::size_t kFrom = framesFor(100);
  constexpr std::size_t kTo = framesFor(200);
  constexpr std::size_t kFrames = framesFor(1200);
  // dc keeps the gain readable on every frame
  std::vector<float> samples(kFrames * 2);
  for (std::size_t i = 0; i < kFrames; ++i)
    samples[2 * i] = samples[2 * i + 1] = i >= kFrom && i < kTo ? 2.0f : 0.5f;
  const auto input = samples;
  Limiter limiter;
  limit(limiter, samples);
  // the gain applied to input frame i
  const auto gain = [&](std::size_t i) {
    return static_cast<double>(samples[2 * (i + kLatency)]) / static_cast<double>(input[2 * i]);
  };

  const double need = kLimiterCeiling / 2.0;
  CHECK(gain(kFrom - kLatency - 1) == 1);
  for (std::size_t i = kFrom; i < kTo; ++i)
    REQUIRE_THAT(gain(i), WithinAbs(need, 1e-6));

  // 1 - gain falls to 1/e of its depth after one time constant, and within 1 dB of unity after
  // 100 ms * ln((1 - need) / (1 - 0.891)) = 163 ms; the hold window adds about 2.5 ms to both
  std::size_t tau = 0;
  std::size_t withinDb = 0;
  for (std::size_t i = kTo; i + kLatency < kFrames; ++i) {
    if (tau == 0 && 1 - gain(i) <= (1 - need) / std::numbers::e)
      tau = i - kTo;
    if (withinDb == 0 && gain(i) >= 0.891) {
      withinDb = i - kTo;
      break;
    }
  }
  CHECK(tau >= framesFor(95));
  CHECK(tau <= framesFor(110));
  CHECK(withinDb >= framesFor(155));
  CHECK(withinDb <= framesFor(180));
  CHECK_THAT(gain(kFrames - kLatency - 1), WithinAbs(1, 1e-3));
}
