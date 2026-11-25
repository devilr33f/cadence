// the keyword frontend against the oracle's vectors (kws/frontend_ref.py, kws/vectors/README.md: the layout, and
// features compared by an absolute tolerance of 1e-3 dB, silence exactly at the -100 dB floor)
#include "kws/frontend.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>

#include <algorithm>
#include <bit>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <fstream>
#include <iterator>
#include <span>
#include <string>
#include <vector>

using namespace cadence::kws;

namespace {

constexpr std::size_t kFragment = 1280;
constexpr float kToleranceDb = 1e-3f;

struct Vector {
  std::vector<std::int16_t> samples;
  std::size_t frames = 0;
  // frame-major
  std::vector<float> features;
};

Vector readVector(const std::string& name) {
  static_assert(std::endian::native == std::endian::little, "the vectors are little-endian float32");
  std::ifstream in(std::string(CADENCE_KWS_VECTORS) + "/" + name + ".f32", std::ios::binary);
  REQUIRE(in);
  const std::vector<char> bytes{std::istreambuf_iterator<char>(in), std::istreambuf_iterator<char>()};
  REQUIRE(bytes.size() % sizeof(float) == 0);
  std::vector<float> values(bytes.size() / sizeof(float));
  std::copy(bytes.begin(), bytes.end(), reinterpret_cast<char*>(values.data()));

  REQUIRE(values.size() >= 4);
  REQUIRE(values[0] == 1.0f);
  REQUIRE(values[3] == static_cast<float>(KwsFrontend::kMels));
  Vector vector;
  const auto samples = static_cast<std::size_t>(values[1]);
  vector.frames = static_cast<std::size_t>(values[2]);
  REQUIRE(values.size() == 4 + samples + vector.frames * KwsFrontend::kMels);
  REQUIRE(samples % kFragment == 0);
  for (std::size_t i = 0; i < samples; ++i) {
    const float sample = values[4 + i];
    REQUIRE(sample == std::trunc(sample));
    REQUIRE(sample >= -32768.0f);
    REQUIRE(sample <= 32767.0f);
    vector.samples.push_back(static_cast<std::int16_t>(sample));
  }
  vector.features.assign(values.begin() + static_cast<std::ptrdiff_t>(4 + samples), values.end());
  return vector;
}

std::vector<KwsFrontend::Frame> stream(KwsFrontend& frontend, std::span<const std::int16_t> samples,
                                       std::span<const std::size_t> sizes) {
  std::vector<KwsFrontend::Frame> frames;
  std::size_t at = 0;
  for (std::size_t i = 0; at < samples.size(); ++i) {
    const std::size_t size = std::min(sizes[i % sizes.size()], samples.size() - at);
    for (const auto& frame : frontend.push(samples.subspan(at, size)))
      frames.push_back(frame);
    at += size;
  }
  return frames;
}

std::vector<KwsFrontend::Frame> stream(KwsFrontend& frontend, std::span<const std::int16_t> samples,
                                       std::size_t size) {
  return stream(frontend, samples, std::span<const std::size_t>(&size, 1));
}

std::vector<std::int16_t> noise(std::size_t count, std::uint32_t seed) {
  std::vector<std::int16_t> samples(count);
  std::uint32_t x = seed;
  for (auto& sample : samples) {
    x = 1664525u * x + 1013904223u;
    sample = static_cast<std::int16_t>(static_cast<std::int32_t>(x >> 16) - 32768);
  }
  return samples;
}

}  // namespace

TEST_CASE("frontend matches the reference vectors within 1e-3") {
  const std::string name =
      GENERATE(as<std::string>{}, "silence", "sine-1k", "two-tone", "chirp", "noise", "quiet", "edge-full-scale");
  INFO(name);
  const Vector vector = readVector(name);
  KwsFrontend frontend(FrontendParams::defaultCapsula());
  const auto frames = stream(frontend, vector.samples, kFragment);
  REQUIRE(frames.size() == vector.frames);

  float worst = 0;
  for (std::size_t t = 0; t < frames.size(); ++t)
    for (std::size_t m = 0; m < KwsFrontend::kMels; ++m) {
      const float expected = vector.features[t * KwsFrontend::kMels + m];
      if (name == "silence")
        REQUIRE(frames[t][m] == -100.0f);
      worst = std::max(worst, std::abs(frames[t][m] - expected));
    }
  INFO("worst " << worst << " dB");
  CHECK(worst <= kToleranceDb);
}

TEST_CASE("frontend frames per second") {
  KwsFrontend frontend(FrontendParams::defaultCapsula());
  const auto second = noise(16000, 1);
  // (16000 - 640) / 160 + 1
  CHECK(stream(frontend, second, 320).size() == 97);

  SECTION("the stock's 1280-sample fragments give 5 frames, then 8 each") {
    KwsFrontend fresh(FrontendParams::defaultCapsula());
    const auto audio = noise(4 * kFragment, 2);
    const std::span<const std::int16_t> samples(audio);
    CHECK(fresh.push(samples.subspan(0, kFragment)).size() == 5);
    CHECK(fresh.push(samples.subspan(kFragment, kFragment)).size() == 8);
    CHECK(fresh.push(samples.subspan(2 * kFragment, kFragment)).size() == 8);
    CHECK(fresh.push(samples.subspan(3 * kFragment, kFragment)).size() == 8);
  }
}

TEST_CASE("frontend frames do not depend on how the stream is cut") {
  const Vector vector = readVector("chirp");
  KwsFrontend whole(FrontendParams::defaultCapsula());
  const auto expected = stream(whole, vector.samples, vector.samples.size());
  REQUIRE(expected.size() == vector.frames);

  const std::vector<std::size_t> sizes{1, 159, 160, 161, 7, 320, 639, 640, 641, 1280, 3};
  KwsFrontend cut(FrontendParams::defaultCapsula());
  CHECK(stream(cut, vector.samples, sizes) == expected);
}

TEST_CASE("frontend reset forgets the kept samples") {
  const auto before = noise(1000, 3);
  const auto after = noise(2000, 4);
  KwsFrontend used(FrontendParams::defaultCapsula());
  REQUIRE(used.push(before).size() == 3);
  used.reset();

  KwsFrontend fresh(FrontendParams::defaultCapsula());
  const std::span<const std::int16_t> rest(after);
  CHECK(used.push(rest.subspan(0, 639)).empty());
  CHECK(fresh.push(rest.subspan(0, 639)).empty());
  CHECK(stream(used, rest.subspan(639), 320) == stream(fresh, rest.subspan(639), 320));
}

TEST_CASE("frontend defaults are the stock's") {
  const auto params = FrontendParams::defaultCapsula();
  CHECK(params.sampleRateHz == 16000.0);
  CHECK(params.sampleScale == 32767.0f);
  CHECK(params.melLowHz == 0.0);
  CHECK(params.melHighHz == 8000.0);
  CHECK(params.powerFloor == 1e-10f);
}
