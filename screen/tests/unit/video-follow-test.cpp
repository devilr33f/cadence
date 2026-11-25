// the follower controller (multiroom P4) against a fake player: a picture whose clock drifts a little, whose seeks
// take a while to land, and which runs at the speed asked while it is not paused
#include "player/video-follow.h"

#include <catch2/catch_test_macros.hpp>

#include <cmath>
#include <cstdint>
#include <optional>

using namespace cadence::screen::player;

namespace {

constexpr std::int64_t kStepNs = 16'000'000;
constexpr double kFrameMs = 1000.0 / 30;

// a player as mpv behaves for a follower: loaded at a position, paused or running at its speed (its own clock off by
// driftPpm), a seek landing seekMs later
struct FakePlayer {
  double positionMs = 0;
  bool paused = true;
  double speed = 1;
  double driftPpm = 300;
  double seekMs = 180;
  std::optional<double> seekTo;
  double seekLeftMs = 0;

  void apply(const FollowAction& a) {
    if (a.pause) paused = *a.pause;
    if (a.speed) speed = *a.speed;
    if (a.seekMs) {
      seekTo = *a.seekMs;
      seekLeftMs = seekMs;
    }
  }
  void advance(double ms) {
    if (seekTo) {
      seekLeftMs -= ms;
      if (seekLeftMs <= 0) {
        positionMs = *seekTo;
        seekTo.reset();
      }
      return;
    }
    if (!paused) positionMs += ms * speed * (1 + driftPpm * 1e-6);
  }
  [[nodiscard]] FollowReading reading() const { return {positionMs, seekTo.has_value()}; }
};

// the group's timeline: media position at mono 0, running or stopped, sent as the tap does (4 a second)
struct Group {
  double mediaMs = 0;
  bool playing = true;
  std::uint32_t epoch = 1;
  std::int64_t lastSentNs = -1'000'000'000;

  [[nodiscard]] FollowClock at(std::int64_t now) const {
    return {static_cast<std::int64_t>(mediaMs * 1e6), now, epoch, playing};
  }
};

struct Rig {
  VideoFollower follower;
  FakePlayer player;
  Group group;
  std::int64_t now = 0;

  void run(double ms) {
    const auto until = now + static_cast<std::int64_t>(ms * 1e6);
    while (now < until) {
      if (now - group.lastSentNs >= 250'000'000) {
        follower.clock(group.at(now));
        group.lastSentNs = now;
      }
      player.apply(follower.step(now, player.reading()));
      now += kStepNs;
      const double stepMs = static_cast<double>(kStepNs) / 1e6;
      player.advance(stepMs);
      if (group.playing) group.mediaMs += stepMs;
    }
  }
  [[nodiscard]] double error() const { return player.positionMs - group.mediaMs; }
  // the worst error over the next ms of playing
  double worst(double ms) {
    double w = 0;
    const auto until = now + static_cast<std::int64_t>(ms * 1e6);
    while (now < until) {
      run(16);
      w = std::max(w, std::fabs(error()));
    }
    return w;
  }
};

}  // namespace

TEST_CASE("follower: from 2 s behind, a seek ahead by the seeks' time, then it holds within a frame") {
  Rig r;
  r.group.mediaMs = 60'000;
  r.player.positionMs = 58'000;
  r.run(8000);
  INFO("error " << r.error() << " seeks " << r.follower.stats().seeks);
  CHECK(std::fabs(r.error()) < kFrameMs);
  CHECK(r.follower.stats().seeks <= 3);
  CHECK(r.worst(20'000) < kFrameMs);
  CHECK(std::fabs(r.player.speed - 1) <= 0.05);
}

TEST_CASE("follower: from 2 s ahead, it waits for the group, then holds within a frame") {
  Rig r;
  r.group.mediaMs = 60'000;
  r.player.positionMs = 62'000;
  r.player.paused = false;
  // the first look places it: an epoch's start more than a frame off is sought
  r.run(8000);
  INFO("error " << r.error() << " seeks " << r.follower.stats().seeks);
  CHECK(std::fabs(r.error()) < kFrameMs);
  CHECK(r.worst(20'000) < kFrameMs);
}

TEST_CASE("follower: a small error is steered by the speed, never sought, within 5 %") {
  Rig r;
  r.group.mediaMs = 10'000;
  r.player.positionMs = 10'000;
  r.run(2000);
  const auto seeks = r.follower.stats().seeks;
  // the picture slips 150 ms behind (a stalled decoder)
  r.player.positionMs -= 150;
  double fastest = 1;
  for (int i = 0; i < 200; ++i) {
    r.run(16);
    fastest = std::max(fastest, r.player.speed);
  }
  CHECK(r.follower.stats().seeks == seeks);
  CHECK(fastest > 1.01);
  CHECK(fastest <= 1.05 + 1e-9);
  r.run(8000);
  CHECK(std::fabs(r.error()) < kFrameMs);
}

TEST_CASE("follower: the group's seek is a new epoch it seeks to") {
  Rig r;
  r.group.mediaMs = 10'000;
  r.player.positionMs = 10'000;
  r.run(3000);
  const auto seeks = r.follower.stats().seeks;
  r.group.mediaMs = 300'000;
  r.group.epoch = 2;
  r.group.lastSentNs = -1'000'000'000;
  r.run(6000);
  CHECK(r.follower.stats().seeks > seeks);
  CHECK(std::fabs(r.error()) < kFrameMs);
}

TEST_CASE("follower: a pause of the group pauses it there, and it plays on with the group") {
  Rig r;
  r.group.mediaMs = 10'000;
  r.player.positionMs = 10'000;
  r.run(3000);
  r.group.playing = false;
  r.group.lastSentNs = -1'000'000'000;
  r.run(1000);
  CHECK(r.player.paused);
  const auto at = r.player.positionMs;
  r.run(2000);
  CHECK(r.player.positionMs == at);
  CHECK(std::fabs(r.error()) < kFrameMs);
  // the resume: a new epoch from where it stopped
  r.group.playing = true;
  r.group.epoch = 2;
  r.group.lastSentNs = -1'000'000'000;
  const auto seeks = r.follower.stats().seeks;
  r.run(3000);
  CHECK_FALSE(r.player.paused);
  CHECK(r.follower.stats().seeks == seeks);
  CHECK(r.worst(5000) < kFrameMs);
}

TEST_CASE("follower: a clock that stops coming holds the picture") {
  Rig r;
  r.group.mediaMs = 10'000;
  r.player.positionMs = 10'000;
  r.run(3000);
  REQUIRE_FALSE(r.player.paused);
  // the tap went quiet: no clock for longer than it may be stale
  const auto last = r.now;
  r.group.lastSentNs = last + 1'000'000'000'000;
  r.run(2000);
  CHECK(r.player.paused);
  CHECK(r.player.speed == 1);
  CHECK_FALSE(r.follower.groupMs(r.now));
}
