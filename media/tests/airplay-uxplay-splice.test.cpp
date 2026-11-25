// the splicer between the placer and the music input: what it emits is continuous where the stream is, and where it
// is cut the join is a short fade, never a step
#include "audiod/airplay-uxplay-splice.h"

#include <catch2/catch_test_macros.hpp>

#include <cmath>
#include <numbers>
#include <vector>

using namespace cadence::audiod::uxplay;

namespace {

constexpr double kRate = 44100;

// a 440 Hz sine, stereo, from frame first
std::vector<float> sine(std::size_t first, std::size_t frames, float amplitude = 0.8f) {
  std::vector<float> out(frames * 2);
  for (std::size_t i = 0; i < frames; ++i)
    out[2 * i] = out[2 * i + 1] =
        amplitude * static_cast<float>(std::sin(2 * std::numbers::pi * 440 * static_cast<double>(first + i) / kRate));
  return out;
}

// the largest step between two frames: a 440 Hz sine at 0.8 never steps more than 0.051 a frame
float largestStep(const std::vector<float>& stereo) {
  float worst = 0;
  for (std::size_t i = 2; i < stereo.size(); i += 2)
    worst = std::max(worst, std::abs(stereo[i] - stereo[i - 2]));
  return worst;
}

void take(Splicer& splicer, std::vector<float>& into) {
  const auto out = splicer.output();
  into.insert(into.end(), out.begin(), out.end());
  splicer.clearOutput();
}

constexpr float kSineStep = 0.052f;

}  // namespace

TEST_CASE("continuous packets come out as they went in, the last few ms held back") {
  Splicer splicer(2);
  std::vector<float> out;
  // the first packet fades in from silence: a stream's start is no click either
  for (std::size_t p = 0; p < 20; ++p) {
    splicer.append(sine(p * 352, 352));
    take(splicer, out);
  }
  CHECK(splicer.heldFrames() == kSpliceFrames);
  CHECK(out.size() / 2 == 20 * 352 - kSpliceFrames);
  const auto whole = sine(0, 20 * 352);
  // past the fade in, every frame is the input's
  for (std::size_t i = 2 * kSpliceFrames; i < out.size(); ++i)
    REQUIRE(out[i] == whole[i]);
  CHECK(std::abs(out[0]) < 0.01f);
  CHECK(largestStep(out) <= kSineStep);
}

TEST_CASE("frames dropped at a packet's start are crossfaded, the join no step") {
  Splicer splicer(2);
  std::vector<float> out;
  for (std::size_t p = 0; p < 4; ++p) {
    splicer.append(sine(p * 352, 352));
    take(splicer, out);
  }
  // the next packet loses its first 150 frames: a hard cut would jump within the wave
  splicer.drop(sine(4 * 352, 352), 150);
  take(splicer, out);
  splicer.append(sine(5 * 352, 352));
  take(splicer, out);
  CHECK(out.size() / 2 == 6 * 352 - 150 - kSpliceFrames);
  CHECK(largestStep(out) <= kSineStep * 1.5f);
  // the same cut unspliced steps far more
  auto hard = sine(0, 4 * 352);
  const auto after = sine(4 * 352 + 150, 202);
  hard.insert(hard.end(), after.begin(), after.end());
  CHECK(largestStep(hard) > kSineStep * 3);
}

TEST_CASE("a gap fades out, holds silence, and fades the next audio in") {
  Splicer splicer(2);
  std::vector<float> out;
  for (std::size_t p = 0; p < 4; ++p) {
    splicer.append(sine(p * 352, 352));
    take(splicer, out);
  }
  const auto before = out.size() / 2;
  splicer.silence(352);
  take(splicer, out);
  // the held frames, faded, then the gap
  CHECK(out.size() / 2 == before + kSpliceFrames + 352);
  CHECK(splicer.heldFrames() == 0);
  for (std::size_t i = 2 * (before + kSpliceFrames); i < out.size(); ++i)
    REQUIRE(out[i] == 0.0f);
  splicer.append(sine(5 * 352, 352));
  take(splicer, out);
  CHECK(largestStep(out) <= kSineStep);
}

TEST_CASE("a packet dropped whole joins the one after it with a fade out and in") {
  Splicer splicer(2);
  std::vector<float> out;
  for (std::size_t p = 0; p < 4; ++p) {
    splicer.append(sine(p * 352, 352));
    take(splicer, out);
  }
  splicer.drop(sine(4 * 352, 352), 352);
  take(splicer, out);
  splicer.drop(sine(5 * 352, 352), 100);
  take(splicer, out);
  splicer.append(sine(6 * 352, 352));
  take(splicer, out);
  CHECK(largestStep(out) <= kSineStep);
}

TEST_CASE("a reset drops what is held and fades the next stream in") {
  Splicer splicer(2);
  std::vector<float> out;
  splicer.append(sine(0, 352));
  take(splicer, out);
  splicer.reset();
  CHECK(splicer.heldFrames() == 0);
  out.clear();
  splicer.append(sine(1000, 352));
  take(splicer, out);
  CHECK(std::abs(out[0]) < 0.02f);
  CHECK(largestStep(out) <= kSineStep);
}
