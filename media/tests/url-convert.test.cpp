#include "url/pcm-convert.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>

#include <cstdint>
#include <limits>
#include <vector>

using namespace cadence::url;

namespace {

// left 0.5, -0.25; right -1, 0.75 as each format holds them
const std::vector<float> kExpected = {0.5f, -1.0f, -0.25f, 0.75f};

template <typename T>
std::vector<float> convert(AVSampleFormat format, const std::vector<std::vector<T>>& planes, std::size_t channels,
                           std::size_t frames) {
  std::vector<const std::uint8_t*> pointers;
  for (const auto& plane : planes)
    pointers.push_back(reinterpret_cast<const std::uint8_t*>(plane.data()));
  std::vector<float> out;
  REQUIRE(toInterleavedFloat(format, pointers.data(), channels, frames, out));
  return out;
}

}  // namespace

TEST_CASE("every sample format a decoder hands out becomes float interleaved") {
  CHECK(convert<float>(AV_SAMPLE_FMT_FLT, {{0.5f, -1.0f, -0.25f, 0.75f}}, 2, 2) == kExpected);
  CHECK(convert<float>(AV_SAMPLE_FMT_FLTP, {{0.5f, -0.25f}, {-1.0f, 0.75f}}, 2, 2) == kExpected);
  CHECK(convert<double>(AV_SAMPLE_FMT_DBL, {{0.5, -1.0, -0.25, 0.75}}, 2, 2) == kExpected);
  CHECK(convert<double>(AV_SAMPLE_FMT_DBLP, {{0.5, -0.25}, {-1.0, 0.75}}, 2, 2) == kExpected);
  CHECK(convert<std::int16_t>(AV_SAMPLE_FMT_S16, {{16384, -32768, -8192, 24576}}, 2, 2) == kExpected);
  CHECK(convert<std::int16_t>(AV_SAMPLE_FMT_S16P, {{16384, -8192}, {-32768, 24576}}, 2, 2) == kExpected);
  constexpr std::int32_t kHalf = 1 << 30;
  CHECK(convert<std::int32_t>(AV_SAMPLE_FMT_S32, {{kHalf, std::numeric_limits<std::int32_t>::min(), -kHalf / 2,
                                                   kHalf + kHalf / 2}},
                              2, 2) == kExpected);
  CHECK(convert<std::int32_t>(AV_SAMPLE_FMT_S32P,
                              {{kHalf, -kHalf / 2}, {std::numeric_limits<std::int32_t>::min(), kHalf + kHalf / 2}}, 2,
                              2) == kExpected);
}

TEST_CASE("mono stays one channel") {
  CHECK(convert<float>(AV_SAMPLE_FMT_FLTP, {{0.5f, -0.25f}}, 1, 2) == std::vector<float>{0.5f, -0.25f});
}

TEST_CASE("any other sample format is refused and leaves the output alone") {
  const auto format = GENERATE(AV_SAMPLE_FMT_U8, AV_SAMPLE_FMT_U8P, AV_SAMPLE_FMT_S64, AV_SAMPLE_FMT_S64P,
                               AV_SAMPLE_FMT_NONE);
  const std::vector<std::uint8_t> plane(64, 0x80);
  const std::uint8_t* planes[] = {plane.data(), plane.data()};
  std::vector<float> out = {9.0f};
  CHECK_FALSE(toInterleavedFloat(format, planes, 2, 4, out));
  CHECK(out == std::vector<float>{9.0f});
}
