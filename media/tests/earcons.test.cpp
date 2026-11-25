#include "playback/earcons.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <cstddef>
#include <cstdint>
#include <cstdlib>
#include <vector>

using namespace cadence;

namespace {

int peak(const std::vector<std::int16_t>& pcm) {
  int result = 0;
  for (const auto sample : pcm)
    result = std::max(result, std::abs(static_cast<int>(sample)));
  return result;
}

// sign changes in [first, last), skipping zero samples
int zeroCrossings(const std::vector<std::int16_t>& pcm, std::size_t first, std::size_t last) {
  int crossings = 0;
  int previous = 0;
  for (std::size_t i = first; i < last; ++i) {
    const int sign = (pcm[i] > 0) - (pcm[i] < 0);
    if (sign == 0)
      continue;
    if (previous != 0 && sign != previous)
      ++crossings;
    previous = sign;
  }
  return crossings;
}

}  // namespace

TEST_CASE("earcons are 48 kHz: wake is two 70 ms notes and failure two 90 ms notes") {
  CHECK(kEarconRate == 48000);
  CHECK(earconPcm(Earcon::Wake).size() == 6720);
  CHECK(earconPcm(Earcon::Failure).size() == 8640);
}

TEST_CASE("peaks sit just under -12 dBFS and never clip, and each note fades in and out over 240 samples") {
  for (const auto earcon : {Earcon::Wake, Earcon::Failure}) {
    const int level = peak(earconPcm(earcon));
    CHECK(level >= 8100);
    CHECK(level <= 8200);
  }
  const auto wake = earconPcm(Earcon::Wake);
  CHECK(peak({wake.begin(), wake.begin() + 240}) < 8200);
  CHECK(peak({wake.begin() + 240, wake.begin() + 3120}) >= 8100);
  CHECK(peak({wake.begin() + 3120, wake.begin() + 3360}) < 8200);
}

TEST_CASE("every note starts and ends on a zero sample") {
  const auto wake = earconPcm(Earcon::Wake);
  CHECK(wake[0] == 0);
  CHECK(wake[3359] == 0);
  CHECK(wake[3360] == 0);
  CHECK(wake[6719] == 0);

  const auto failure = earconPcm(Earcon::Failure);
  CHECK(failure[0] == 0);
  CHECK(failure[4319] == 0);
  CHECK(failure[4320] == 0);
  CHECK(failure[8639] == 0);
}

TEST_CASE("the wake note is 880 Hz and rises, the failure note 659 Hz and falls") {
  // 2 crossings per period over 2880 and 3840 full-level samples
  const auto wake = earconPcm(Earcon::Wake);
  const int wakeFirst = zeroCrossings(wake, 240, 3120);
  CHECK(wakeFirst >= 102);
  CHECK(wakeFirst <= 110);
  CHECK(zeroCrossings(wake, 3360 + 240, 3360 + 3120) > wakeFirst);
  const auto failure = earconPcm(Earcon::Failure);
  const int failureFirst = zeroCrossings(failure, 240, 4080);
  CHECK(failureFirst >= 101);
  CHECK(failureFirst <= 110);
  CHECK(zeroCrossings(failure, 4320 + 240, 4320 + 4080) < failureFirst);
}
