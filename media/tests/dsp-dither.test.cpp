#include "dsp/dither.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/matchers/catch_matchers_floating_point.hpp>

#include <cmath>
#include <cstddef>
#include <cstdint>
#include <limits>
#include <random>
#include <set>
#include <vector>

using namespace cadence::dsp;
using Catch::Matchers::WithinAbs;

TEST_CASE("the dither error stays within 1.5 lsb and averages zero") {
  constexpr std::size_t kSamples = 100000;
  std::mt19937 random(3);
  std::uniform_real_distribution<float> uniform(-0.99f, 0.99f);
  std::vector<float> in(kSamples);
  for (auto& sample : in)
    sample = uniform(random);
  std::vector<std::int16_t> out(kSamples);
  Rng rng(1);
  ditherToS16(in, out, rng);

  double sum = 0;
  for (std::size_t i = 0; i < kSamples; ++i) {
    const double error = out[i] - static_cast<double>(in[i]) * 32768;
    REQUIRE(std::abs(error) <= 1.5);
    sum += error;
  }
  CHECK_THAT(sum / static_cast<double>(kSamples), WithinAbs(0, 0.02));
}

TEST_CASE("full scale and beyond clamp to the int16 limits, nan is silence") {
  const float inf = std::numeric_limits<float>::infinity();
  const std::vector<float> in{1.0f, -1.0f, 2.0f, -2.0f, inf, -inf};
  const std::vector<std::int16_t> limits{32767, -32768, 32767, -32768, 32767, -32768};
  Rng rng(2);
  for (int round = 0; round < 1000; ++round) {
    std::vector<std::int16_t> out(in.size());
    ditherToS16(in, out, rng);
    REQUIRE(out == limits);

    const float nan = std::numeric_limits<float>::quiet_NaN();
    std::int16_t quiet = 99;
    ditherToS16(std::span(&nan, 1), std::span(&quiet, 1), rng);
    REQUIRE(std::abs(quiet) <= 1);
  }
}

TEST_CASE("silence dithers to -1, 0 or 1") {
  const std::vector<float> in(10000, 0.0f);
  std::vector<std::int16_t> out(in.size());
  Rng rng(4);
  ditherToS16(in, out, rng);
  const std::set<std::int16_t> values(out.begin(), out.end());
  CHECK(values == std::set<std::int16_t>{-1, 0, 1});
}

TEST_CASE("each rng seed gives its own noise") {
  const std::vector<float> in(1000, 0.0f);
  std::vector<std::int16_t> first(in.size());
  std::vector<std::int16_t> second(in.size());
  Rng a(5);
  Rng b(6);
  ditherToS16(in, first, a);
  ditherToS16(in, second, b);
  CHECK(first != second);
}
