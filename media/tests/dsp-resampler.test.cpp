#include "dsp/resampler.h"
#include "support/signals.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/matchers/catch_matchers_floating_point.hpp>

#include <algorithm>
#include <cmath>
#include <cstddef>
#include <span>
#include <vector>

using namespace cadence::dsp;
using namespace cadence::test_support;
using Catch::Matchers::WithinAbs;

namespace {

std::vector<float> resample(Resampler& resampler, std::span<const float> in, std::size_t channels) {
  std::vector<float> out;
  std::vector<float> chunk(1024 * channels);
  // 10 ms blocks at 44.1 kHz, as a decoder hands them over
  const std::size_t block = 441 * channels;
  for (std::size_t first = 0; first < in.size(); first += block) {
    auto rest = in.subspan(first, std::min(block, in.size() - first));
    while (!rest.empty()) {
      const auto done = resampler.process(rest, chunk);
      REQUIRE((done.consumed > 0 || done.produced > 0));
      out.insert(out.end(), chunk.begin(), chunk.begin() + static_cast<std::ptrdiff_t>(done.produced * channels));
      rest = rest.subspan(done.consumed * channels);
    }
  }
  for (int round = 0; round < 100; ++round) {
    const std::size_t produced = resampler.flush(chunk);
    if (produced == 0)
      return out;
    out.insert(out.end(), chunk.begin(), chunk.begin() + static_cast<std::ptrdiff_t>(produced * channels));
  }
  FAIL("flush never ended");
  return out;
}

std::size_t risingCrossings(std::span<const float> stereo, std::size_t first, std::size_t frames) {
  std::size_t crossings = 0;
  for (std::size_t i = first + 1; i < first + frames; ++i)
    if (stereo[2 * (i - 1)] <= 0 && stereo[2 * i] > 0)
      ++crossings;
  return crossings;
}

}  // namespace

TEST_CASE("44.1 kHz stereo to 48 kHz keeps a 1 kHz sine's frequency, level and length") {
  auto created = Resampler::create(2, 44100);
  REQUIRE(created.ok());
  const auto in = sine(1000, 0.5, 44100, 2, 44100);
  const auto out = resample(created.value(), in, 2);

  const std::size_t frames = out.size() / 2;
  CHECK(frames >= 48000 - 64);
  CHECK(frames <= 48000 + 64);
  // 0.8 s away from both ends
  const std::size_t first = 4800;
  const std::size_t window = 38400;
  const std::size_t crossings = risingCrossings(out, first, window);
  CHECK(crossings >= 799);
  CHECK(crossings <= 801);
  const double expected = 0.5 / std::sqrt(2.0);
  CHECK_THAT(db(rms(out, 2, 0, first, window) / expected), WithinAbs(0, 0.5));
  CHECK_THAT(db(rms(out, 2, 1, first, window) / expected), WithinAbs(0, 0.5));
}

TEST_CASE("channels other than 1 or 2 and rates outside 8-192 kHz are refused") {
  CHECK(Resampler::create(0, 44100).error() == ResamplerError::BadChannels);
  CHECK(Resampler::create(3, 44100).error() == ResamplerError::BadChannels);
  CHECK(Resampler::create(2, 7999).error() == ResamplerError::BadRate);
  CHECK(Resampler::create(2, 192001).error() == ResamplerError::BadRate);
  CHECK(Resampler::create(1, 8000).ok());
  CHECK(Resampler::create(2, 192000).ok());
}

TEST_CASE("48 kHz mono to an output rate of 16 kHz keeps a 1 kHz sine's frequency and level") {
  auto created = Resampler::create(1, 48000, 16000);
  REQUIRE(created.ok());
  const auto in = sine(1000, 0.5, 48000, 1, 48000);
  const auto out = resample(created.value(), in, 1);

  CHECK(out.size() >= 16000 - 32);
  CHECK(out.size() <= 16000 + 32);
  // 0.25 s away from both ends, a whole number of cycles
  CHECK_THAT(amplitudeAt(out, 1, 0, 4000, 8000, 1000, 16000), WithinAbs(0.5, 0.01));
  CHECK(Resampler::create(1, 48000, 7999).error() == ResamplerError::BadRate);
}

TEST_CASE("a trim plays the input faster or slower, the output continuous across every change") {
  auto created = Resampler::create(2, 44100);
  REQUIRE(created.ok());
  auto& resampler = created.value();
  // 10 s of a 1 kHz sine in 10 ms blocks, the trim moved every 100 ms as a steering would
  const auto in = sine(1000, 0.5, 441000, 2, 44100);
  std::vector<float> out;
  std::vector<float> chunk(2048 * 2);
  const std::size_t block = 441 * 2;
  std::size_t blocks = 0;
  for (std::size_t first = 0; first < in.size(); first += block, ++blocks) {
    if (blocks % 10 == 0)
      resampler.setTrim(blocks % 20 == 0 ? 2000 : -2000);
    auto rest = std::span<const float>(in).subspan(first, std::min(block, in.size() - first));
    while (!rest.empty()) {
      const auto done = resampler.process(rest, chunk);
      REQUIRE((done.consumed > 0 || done.produced > 0));
      out.insert(out.end(), chunk.begin(), chunk.begin() + static_cast<std::ptrdiff_t>(done.produced * 2));
      rest = rest.subspan(done.consumed * 2);
    }
  }
  // half the time 2000 ppm faster, half slower: the length evens out, less the filter's latency (no flush here)
  const std::size_t frames = out.size() / 2;
  CHECK(frames >= 480000 - 400);
  CHECK(frames <= 480000 + 100);
  // a 1 kHz sine at 0.5 steps at most 0.5 * 2 pi * 1000 / 48000 = 0.0655 a frame; a gap or a repeat at a change
  // would step up to the whole amplitude
  float worst = 0;
  for (std::size_t i = 4800; i < frames; ++i)
    worst = std::max(worst, std::abs(out[2 * i] - out[2 * (i - 1)]));
  CHECK(worst < 0.0670f);
  // and the level holds through the interpolating filter
  CHECK_THAT(db(rms(out, 2, 0, 4800, 384000) / (0.5 / std::sqrt(2.0))), WithinAbs(0, 0.2));
}

TEST_CASE("a steady trim changes the output's length by as many ppm") {
  for (const std::int32_t ppm : {1000, -1000}) {
    auto created = Resampler::create(2, 44100);
    REQUIRE(created.ok());
    created.value().setTrim(ppm);
    const auto in = sine(1000, 0.5, 441000, 2, 44100);
    const auto out = resample(created.value(), in, 2);
    const auto expected = 480000.0 * (1.0 - ppm * 1e-6);
    CHECK(std::abs(static_cast<double>(out.size() / 2) - expected) < 16);
  }
  // clamped, and 0 again is the direct filter's rate exactly
  auto created = Resampler::create(2, 44100);
  REQUIRE(created.ok());
  created.value().setTrim(1'000'000);
  CHECK(created.value().trim() == Resampler::kMaxTrimPpm);
  created.value().setTrim(0);
  const auto out = resample(created.value(), sine(1000, 0.5, 44100, 2, 44100), 2);
  CHECK(out.size() / 2 >= 48000 - 2);
  CHECK(out.size() / 2 <= 48000 + 2);
}

TEST_CASE("a trim changed at every block, to any value in its range, never breaks the filter") {
  // regression: a finer ratio overflowed speexdsp's 32-bit phase rescale, which left the phase past its table
  auto created = Resampler::create(2, 44100);
  REQUIRE(created.ok());
  auto& resampler = created.value();
  const auto in = sine(1000, 0.5, 44100 * 5, 2, 44100);
  std::vector<float> chunk(2048 * 2);
  std::uint32_t state = 12345;
  float worst = 0;
  float last = 0;
  std::size_t produced = 0;
  for (std::size_t first = 0; first < in.size(); first += 352 * 2) {
    state = state * 1664525U + 1013904223U;
    resampler.setTrim(static_cast<std::int32_t>(state % 4001) - 2000);
    auto rest = std::span<const float>(in).subspan(first, std::min<std::size_t>(352 * 2, in.size() - first));
    while (!rest.empty()) {
      const auto done = resampler.process(rest, chunk);
      REQUIRE((done.consumed > 0 || done.produced > 0));
      for (std::size_t i = 0; i < done.produced; ++i, ++produced) {
        if (produced > 4800)
          worst = std::max(worst, std::abs(chunk[2 * i] - last));
        last = chunk[2 * i];
      }
      rest = rest.subspan(done.consumed * 2);
    }
  }
  CHECK(worst < 0.0670f);
}
