#include "backend/pcm-snapshots.h"
#include "backend/presentation-clock.h"

#include <catch2/catch_test_macros.hpp>

#include <atomic>
#include <cstddef>
#include <thread>
#include <vector>

using namespace cadence;

namespace {

std::vector<float> frames(std::size_t count, float first) {
  std::vector<float> out(count * 2);
  for (std::size_t i = 0; i < count; ++i) {
    out[2 * i] = first + static_cast<float>(i);
    out[2 * i + 1] = -out[2 * i];
  }
  return out;
}

}  // namespace

TEST_CASE("a snapshot holds the last kFrames frames pushed, with the time its first sounds") {
  PcmSnapshots snapshots;
  PcmSnapshot out;
  CHECK_FALSE(snapshots.take(out));
  snapshots.push(frames(300, 0), 1'000'000'000);
  REQUIRE(snapshots.take(out));
  CHECK(out.frames == 300);
  CHECK(out.seq == 1);
  CHECK(out.monoNs == 1'000'000'000);
  static_assert(PcmSnapshot::kFrames == 2048);
  snapshots.push(frames(1920, 300), 2'000'000'000);
  REQUIRE(snapshots.take(out));
  CHECK(out.frames == PcmSnapshot::kFrames);
  CHECK(out.seq == 2);
  // the 2048 frames ending with the last pushed: 172..2219
  CHECK(out.samples[0] == 172.0f);
  CHECK(out.samples[1] == -172.0f);
  CHECK(out.samples[2 * 2047] == 2219.0f);
  // 1920 frames end 40 ms after the period's start; 2048 begin 42.67 ms before that
  CHECK(out.monoNs == 2'000'000'000 + 40'000'000 - 42'666'666);
}

TEST_CASE("an unknown delay leaves the snapshot's time at 0") {
  PcmSnapshots snapshots;
  snapshots.push(frames(10, 0), 0);
  PcmSnapshot out;
  REQUIRE(snapshots.take(out));
  CHECK(out.monoNs == 0);
}

TEST_CASE("a take that races pushes gets one whole snapshot") {
  PcmSnapshots snapshots;
  std::atomic<bool> done{false};
  std::thread pusher([&] {
    for (int period = 0; period < 2000; ++period) {
      std::vector<float> block(480 * 2, static_cast<float>(period));
      snapshots.push(block, 1);
    }
    done = true;
  });
  PcmSnapshot out;
  while (!done) {
    if (!snapshots.take(out) || out.frames < PcmSnapshot::kFrames)
      continue;
    // at most two periods show, each a constant, and never a value out of order
    for (std::size_t i = 1; i < out.frames; ++i)
      REQUIRE(out.samples[2 * i] >= out.samples[2 * (i - 1)]);
  }
  pusher.join();
}

TEST_CASE("the presentation clock maps the mixer's frames onto the anchored media time") {
  PresentationClock clock;
  CHECK_FALSE(clock.sample());
  clock.period(48000, 5'000'000'000, 30'000'000);
  CHECK_FALSE(clock.sample());
  // the media time 10 s is at frame 24000
  clock.anchor(10'000'000'000, 24000);
  auto sample = clock.sample();
  REQUIRE(sample);
  CHECK(sample->mediaNs == 10'500'000'000);
  CHECK(sample->monoNs == 5'000'000'000);
  CHECK(sample->latencyNs == 30'000'000);
  // an unknown delay says nothing
  clock.period(96000, 0, 0);
  CHECK_FALSE(clock.sample());
  clock.period(96000, 6'000'000'000, 30'000'000);
  REQUIRE(clock.sample());
  CHECK(clock.sample()->mediaNs == 11'500'000'000);
  clock.clearAnchor();
  CHECK_FALSE(clock.sample());
}

TEST_CASE("the presentation clock's playhead is the last period with a known delay, anchored or not") {
  PresentationClock clock;
  CHECK_FALSE(clock.playhead());
  clock.period(48000, 5'000'000'000, 30'000'000);
  auto playhead = clock.playhead();
  REQUIRE(playhead);
  CHECK(playhead->frames == 48000);
  CHECK(playhead->heardNs == 5'000'000'000);
  CHECK(playhead->latencyNs == 30'000'000);
  clock.period(96000, 0, 0);
  CHECK_FALSE(clock.playhead());
}
