#include "playback/alarm-tones.h"

#include "playback/earcons.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <cstddef>
#include <cstdint>
#include <cstdlib>
#include <limits>
#include <vector>

using namespace cadence;

namespace {

int peak(const std::vector<std::int16_t>& pcm, std::size_t first = 0,
         std::size_t last = std::numeric_limits<std::size_t>::max()) {
  int result = 0;
  for (std::size_t i = first; i < std::min(last, pcm.size()); ++i)
    result = std::max(result, std::abs(static_cast<int>(pcm[i])));
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

bool silent(const std::vector<std::int16_t>& pcm, std::size_t first, std::size_t last) {
  return peak(pcm, first, last) == 0;
}

// a tone after its 10 ms fades, where it sits at full level
int fullLevelCrossings(const std::vector<std::int16_t>& pcm, std::size_t start, std::size_t samples) {
  return zeroCrossings(pcm, start + 480, start + samples - 480);
}

}  // namespace

TEST_CASE("an alarm period is 2 s and a timer period 1 s at 48 kHz") {
  CHECK(ringPeriodPcm(RingKind::Alarm).size() == 96000);
  CHECK(ringPeriodPcm(RingKind::Timer).size() == 48000);
}

TEST_CASE("a period is audible, never louder than an earcon, and starts and ends on a zero sample") {
  for (const auto kind : {RingKind::Alarm, RingKind::Timer}) {
    const auto pcm = ringPeriodPcm(kind);
    CHECK(peak(pcm) <= kEarconPeak);
    CHECK(peak(pcm) >= 8100);
    CHECK(pcm.front() == 0);
    CHECK(pcm.back() == 0);
  }
}

TEST_CASE("the same samples on every call, so a ring loops without a seam") {
  for (const auto kind : {RingKind::Alarm, RingKind::Timer})
    CHECK(ringPeriodPcm(kind) == ringPeriodPcm(kind));
}

TEST_CASE("an alarm is three rising 150 ms tones, 660, 880 and 1100 Hz, then silence") {
  const auto pcm = ringPeriodPcm(RingKind::Alarm);
  constexpr std::size_t tone = 7200;
  constexpr std::size_t starts[] = {0, 12000, 24000};
  // 2 crossings per period over the 130 ms between the fades
  constexpr int expected[] = {172, 229, 286};

  int previous = 0;
  for (std::size_t i = 0; i < 3; ++i) {
    CAPTURE(i);
    const int crossings = fullLevelCrossings(pcm, starts[i], tone);
    CHECK(crossings >= expected[i] - 4);
    CHECK(crossings <= expected[i] + 4);
    CHECK(crossings > previous);
    previous = crossings;
    CHECK(pcm[starts[i]] == 0);
    CHECK(pcm[starts[i] + tone - 1] == 0);
    CHECK(peak(pcm, starts[i] + 480, starts[i] + tone - 480) >= 8100);
    // each tone fades in and out over 10 ms
    CHECK(peak(pcm, starts[i], starts[i] + 480) < kEarconPeak);
    CHECK(peak(pcm, starts[i] + tone - 480, starts[i] + tone) < kEarconPeak);
  }
  CHECK(silent(pcm, 7200, 12000));
  CHECK(silent(pcm, 19200, 24000));
  CHECK(silent(pcm, 31200, 96000));
}

TEST_CASE("a timer is two 880 Hz 120 ms beeps, then silence") {
  const auto pcm = ringPeriodPcm(RingKind::Timer);
  constexpr std::size_t beep = 5760;
  constexpr std::size_t starts[] = {0, 10560};

  for (const auto start : starts) {
    CAPTURE(start);
    // 100 ms at full level: about 176 crossings
    const int crossings = fullLevelCrossings(pcm, start, beep);
    CHECK(crossings >= 172);
    CHECK(crossings <= 180);
    CHECK(pcm[start] == 0);
    CHECK(pcm[start + beep - 1] == 0);
    CHECK(peak(pcm, start + 480, start + beep - 480) >= 8100);
  }
  CHECK(silent(pcm, 5760, 10560));
  CHECK(silent(pcm, 16320, 48000));
}

TEST_CASE("the loop seam is as smooth as a tone's own fade, so a ring repeats without a click") {
  for (const auto kind : {RingKind::Alarm, RingKind::Timer}) {
    const auto pcm = ringPeriodPcm(kind);
    // the end of one period, then the start of the next, as the ring loops them
    std::vector<std::int16_t> seam(pcm.end() - 480, pcm.end());
    seam.insert(seam.end(), pcm.begin(), pcm.begin() + 480);
    // the 10 samples on either side of the join stay near zero
    CHECK(peak(seam, 470, 490) <= 10);
    int seamStep = 0;
    for (std::size_t i = 1; i < seam.size(); ++i)
      seamStep = std::max(seamStep, std::abs(seam[i] - seam[i - 1]));
    int periodStep = 0;
    for (std::size_t i = 1; i < pcm.size(); ++i)
      periodStep = std::max(periodStep, std::abs(pcm[i] - pcm[i - 1]));
    CHECK(seamStep <= periodStep);
  }
}

TEST_CASE("a reminder's chime is 1.2 s, softer than an alarm, two notes, and joins a loop without a click") {
  const auto pcm = ringPeriodPcm(RingKind::Reminder);
  REQUIRE(pcm.size() == 57600);
  CHECK(pcm.front() == 0);
  CHECK(pcm.back() == 0);
  CHECK(peak(pcm) > kEarconPeak / 4);
  CHECK(peak(pcm) <= kEarconPeak * 6 / 10);
  // the first note alone over its first 100 ms after the fade, then the second over the first's tail
  CHECK(fullLevelCrossings(pcm, 0, 5760) > 0);
  CHECK(peak(pcm, 19200 + 480, 19200 + 2400) > peak(pcm, 19200 - 2400, 19200 - 480));
  // it dies away before the end
  CHECK(peak(pcm, 55000, 57600) < peak(pcm, 0, 4800) / 4);
}
