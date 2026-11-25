// the leader's auto-tune on an injected clock: each decision from the events alone
#include "sync/group-tuner.h"

#include <catch2/catch_test_macros.hpp>

#include <cstdint>

using namespace cadence::sync;

namespace {

constexpr std::int64_t kS = 1'000'000'000;

GroupTunerConfig allAuto() {
  GroupTunerConfig c;
  c.autoLatency = true;
  c.autoBitrate = true;
  c.autoWait = true;
  c.latencyMs = 1000;
  c.bitrateKbps = 0;
  c.waitMs = 3000;
  return c;
}

// plays from t on, ticking every second, a member's queue at queueMs after the settle, until t + forNs
std::int64_t play(GroupTuner& t, std::int64_t from, std::int64_t forNs, std::uint32_t queueMs) {
  std::int64_t now = from;
  for (; now < from + forNs; now += kS) {
    t.memberStatus("a", 0, queueMs, now, true);
    t.tick(now, true);
  }
  return now;
}

}  // namespace

TEST_CASE("tuner: a member's underrun raises the latency at the next epoch, at most once in 10 s") {
  GroupTuner t(allAuto());
  t.epochStarted(0);
  t.memberStatus("a", 2, 900, 1 * kS, true);
  CHECK(t.pendingLatencyMs() == 1000);
  t.memberStatus("a", 3, 900, 2 * kS, true);
  CHECK(t.pendingLatencyMs() == 1500);
  // in force from the next epoch's start alone
  CHECK(t.latencyMs() == 1000);
  // within 10 s: nothing more
  t.memberStatus("a", 4, 900, 5 * kS, true);
  CHECK(t.pendingLatencyMs() == 1500);
  CHECK(t.epochStarted(6 * kS) == GroupTuned{"latency", 1000, 1500, "underrun"});
  CHECK(t.latencyMs() == 1500);
  CHECK_FALSE(t.epochStarted(7 * kS));
  // 10 s after the last raise: max(cur + 250, cur * 1.5), rounded up to 50
  t.memberStatus("a", 5, 900, 12 * kS, true);
  CHECK(t.pendingLatencyMs() == 2250);
}

TEST_CASE("tuner: the raise steps and its bound") {
  auto c = allAuto();
  c.latencyMs = 200;
  GroupTuner t(c);
  t.memberRejoined("a", 0, true);
  // 200 * 1.5 = 300 < 200 + 250
  CHECK(t.pendingLatencyMs() == 450);
  t.epochStarted(kS);
  t.memberRejoined("a", 11 * kS, true);
  // 450 + 250 over 450 * 1.5
  CHECK(t.pendingLatencyMs() == 700);
  auto high = allAuto();
  high.latencyMs = 4000;
  GroupTuner h(high);
  h.ownUnderruns(0, 0, true);
  h.ownUnderruns(1, kS, true);
  CHECK(h.pendingLatencyMs() == 5000);
  h.epochStarted(2 * kS);
  h.ownUnderruns(2, 20 * kS, true);
  CHECK(h.pendingLatencyMs() == 5000);
}

TEST_CASE("tuner: nothing raises while the group does not play, nor with auto latency off") {
  GroupTuner t(allAuto());
  t.memberStatus("a", 0, 0, 0, false);
  t.memberStatus("a", 5, 0, kS, false);
  t.memberRejoined("a", 2 * kS, false);
  t.ownUnderruns(0, 3 * kS, false);
  t.ownUnderruns(4, 4 * kS, false);
  CHECK(t.pendingLatencyMs() == 1000);
  auto c = allAuto();
  c.autoLatency = false;
  GroupTuner off(c);
  off.memberStatus("a", 0, 0, 0, true);
  off.memberStatus("a", 5, 0, kS, true);
  off.memberRejoined("a", 2 * kS, true);
  CHECK(off.pendingLatencyMs() == 1000);
  CHECK_FALSE(off.epochStarted(3 * kS));
}

TEST_CASE("tuner: a member's first status is its baseline, and one that went starts over") {
  GroupTuner t(allAuto());
  t.memberStatus("a", 40, 900, 0, true);
  CHECK(t.pendingLatencyMs() == 1000);
  t.memberGone("a");
  t.memberStatus("a", 0, 900, kS, true);
  t.memberStatus("a", 0, 900, 2 * kS, true);
  CHECK(t.pendingLatencyMs() == 1000);
}

TEST_CASE("tuner: 5 min playing with a wide margin lowers the latency, then the window starts over") {
  GroupTuner t(allAuto());
  t.epochStarted(0);
  // a queue of 950 ms at 1000: 1000 - 150, above 2 * 50 + 150
  auto now = play(t, 0, 299 * kS, 950);
  CHECK(t.pendingLatencyMs() == 1000);
  now = play(t, now, 2 * kS, 950);
  CHECK(t.pendingLatencyMs() == 850);
  CHECK(t.epochStarted(now) == GroupTuned{"latency", 1000, 850, "margin"});
  // the next one needs another 5 min, from this epoch's settle on
  now = play(t, now, 200 * kS, 830);
  CHECK(t.pendingLatencyMs() == 850);
}

TEST_CASE("tuner: the lower bound keeps twice the slack the members used") {
  GroupTuner t(allAuto());
  t.epochStarted(0);
  // 600 ms queued at 1000: slack 400, so never below 950; 1000 - 150 would be 850
  const auto now = play(t, 0, 302 * kS, 600);
  CHECK(t.pendingLatencyMs() == 950);
  CHECK(t.epochStarted(now)->to == 950);
}

TEST_CASE("tuner: no lowering with a narrow margin, after a raise trigger, or below 200 ms") {
  SECTION("narrow") {
    GroupTuner t(allAuto());
    t.epochStarted(0);
    play(t, 0, 400 * kS, 499);
    CHECK(t.pendingLatencyMs() == 1000);
  }
  SECTION("a trigger restarts the window") {
    GroupTuner t(allAuto());
    t.epochStarted(0);
    auto now = play(t, 0, 200 * kS, 950);
    // a rejoin within 10 s of no raise: raises, and the window starts over
    t.memberRejoined("a", now, true);
    t.epochStarted(now);
    CHECK(t.latencyMs() == 1500);
    now = play(t, now + kS, 290 * kS, 1450);
    CHECK(t.pendingLatencyMs() == 1500);
  }
  SECTION("the floor") {
    auto c = allAuto();
    c.latencyMs = 250;
    GroupTuner t(c);
    t.epochStarted(0);
    const auto now = play(t, 0, 302 * kS, 250);
    CHECK(t.epochStarted(now)->to == 200);
  }
  SECTION("idle time does not count") {
    GroupTuner t(allAuto());
    t.epochStarted(0);
    auto now = play(t, 0, 200 * kS, 950);
    for (int i = 0; i < 200; ++i, now += kS)
      t.tick(now, false);
    CHECK(t.pendingLatencyMs() == 1000);
    play(t, now, 102 * kS, 950);
    CHECK(t.pendingLatencyMs() == 850);
  }
  SECTION("the first 10 s of an epoch say nothing of the margin") {
    GroupTuner t(allAuto());
    t.epochStarted(0);
    auto now = play(t, 0, 302 * kS, 950);
    t.epochStarted(now);
    // a narrow queue right after the start does not count, a wide one later does
    for (int i = 0; i < 9; ++i, now += kS) {
      t.memberStatus("a", 0, 100, now, true);
      t.tick(now, true);
    }
    play(t, now, 302 * kS, 800);
    CHECK(t.pendingLatencyMs() == 722);
  }
}

TEST_CASE("tuner: the bitrate steps down on a backlog, at most once in 15 s, and up after 120 s clean") {
  GroupTuner t(allAuto());
  CHECK(t.bitrateKbps() == 256);
  t.epochStarted(0);
  // within the settle a backlog is the epoch's first burst
  t.memberBacklog("a", 900, 5 * kS, true);
  CHECK_FALSE(t.tick(5 * kS, true));
  t.memberBacklog("a", 900, 11 * kS, true);
  CHECK(t.tick(11 * kS, true) == GroupTuned{"bitrate", 256, 192, "backlog"});
  CHECK(t.bitrateKbps() == 192);
  t.memberBacklog("a", 900, 20 * kS, true);
  CHECK_FALSE(t.tick(20 * kS, true));
  t.memberBacklog("a", 900, 26 * kS, true);
  CHECK(t.tick(26 * kS, true) == GroupTuned{"bitrate", 192, 128, "backlog"});
  t.memberBacklog("a", 500, 27 * kS, true);
  std::int64_t now = 27 * kS;
  std::optional<GroupTuned> up;
  for (; !up && now < 400 * kS; now += kS)
    up = t.tick(now, true);
  CHECK(up == GroupTuned{"bitrate", 128, 192, "clean"});
  // 120 s from the last step down
  CHECK(now - kS == 26 * kS + 120 * kS);
}

TEST_CASE("tuner: two rejoins of one member within 60 s step the bitrate down") {
  GroupTuner t(allAuto());
  t.tick(0, true);
  t.memberRejoined("a", kS, false);
  t.memberRejoined("b", 2 * kS, false);
  CHECK_FALSE(t.tick(2 * kS, true));
  t.memberRejoined("a", 70 * kS, false);
  CHECK_FALSE(t.tick(70 * kS, true));
  t.memberRejoined("a", 80 * kS, false);
  CHECK(t.tick(80 * kS, true) == GroupTuned{"bitrate", 256, 192, "rejoin"});
}

TEST_CASE("tuner: the ladder below the ceiling, and none with pcm or auto off") {
  auto c = allAuto();
  c.bitrateKbps = 160;
  GroupTuner t(c);
  CHECK(t.bitrateKbps() == 160);
  std::int64_t now = 0;
  t.epochStarted(0);
  std::vector<std::uint32_t> seen;
  for (now = 11 * kS; now < 200 * kS; now += 16 * kS) {
    t.memberBacklog("a", 900, now, true);
    if (const auto d = t.tick(now, true))
      seen.push_back(d->to);
  }
  CHECK(seen == std::vector<std::uint32_t>{128, 96});
  auto pcm = allAuto();
  pcm.pcm = true;
  GroupTuner p(pcm);
  p.epochStarted(0);
  p.memberBacklog("a", 900, 11 * kS, true);
  CHECK_FALSE(p.tick(11 * kS, true));
  CHECK(p.bitrateKbps() == 0);
  auto off = allAuto();
  off.autoBitrate = false;
  off.bitrateKbps = 320;
  GroupTuner o(off);
  o.epochStarted(0);
  o.memberBacklog("a", 900, 11 * kS, true);
  CHECK_FALSE(o.tick(11 * kS, true));
  CHECK(o.bitrateKbps() == 320);
}

TEST_CASE("tuner: the start wait follows the members' ready times, within 500 ms and the ceiling") {
  GroupTuner t(allAuto());
  CHECK(t.waitMs() == 3000);
  t.memberReady("a", 100);
  t.memberReady("b", 400);
  // 400 * 1.5 + 300
  CHECK(t.waitMs() == 900);
  t.memberReady("a", 50);
  CHECK(t.waitMs() == 900);
  // eight later samples push b's 400 out
  for (int i = 0; i < 8; ++i)
    t.memberReady("b", 60);
  CHECK(t.waitMs() == 500);
  t.memberTimedOut("a");
  CHECK(t.waitMs() == 3000);
  auto off = allAuto();
  off.autoWait = false;
  off.waitMs = 0;
  GroupTuner o(off);
  o.memberReady("a", 10);
  CHECK(o.waitMs() == 3000);
  auto low = allAuto();
  low.waitMs = 400;
  GroupTuner l(low);
  l.memberReady("a", 10);
  CHECK(l.waitMs() == 400);
}
