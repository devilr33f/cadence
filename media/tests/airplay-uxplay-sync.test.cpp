#include "audiod/airplay-uxplay-sync.h"

#include <catch2/catch_test_macros.hpp>

using namespace cadence::audiod::uxplay;

namespace {

constexpr std::int64_t kMs = 1'000'000;
constexpr std::uint32_t kRate = 44100;

}  // namespace

TEST_CASE("frames of a span at a rate") {
  CHECK(framesIn(1'000 * kMs, kRate) == 44100);
  CHECK(framesIn(10 * kMs, 48000) == 480);
  CHECK(framesIn(0, kRate) == 0);
  CHECK(framesIn(-5 * kMs, kRate) == 0);
}

TEST_CASE("the mixer frame heard at a time, from a period the playhead knows") {
  // frame 48000 is heard at 5 s: 10 ms later is 480 frames on, 10 ms earlier 480 back
  CHECK(frameAt(5'010 * kMs, 48000, 5'000 * kMs) == 48480);
  CHECK(frameAt(4'990 * kMs, 48000, 5'000 * kMs) == 47520);
  // never before the first frame
  CHECK(frameAt(0, 100, 5'000 * kMs) == 0);
}

TEST_CASE("a stream's first packet is placed with silence before it, so it is heard when the sender asked") {
  Placer placer;
  // due at 1.10 s, and what is pushed now is heard at 1.03 s: 70 ms of silence first
  const auto placed = placer.place(1'100 * kMs, 1'030 * kMs, 352, kRate);
  CHECK(placed.silence == framesIn(70 * kMs, kRate));
  CHECK(placed.drop == 0);
  CHECK_FALSE(placed.shifted);
  CHECK(placer.started());
  CHECK(placer.delayNs() == 0);
}

TEST_CASE("a stream that starts a little late drops its start to be on time") {
  Placer placer;
  // 30 ms late: under the shift, so the first 30 ms go
  const auto placed = placer.place(1'000 * kMs, 1'030 * kMs, 4410, kRate);
  CHECK(placed.drop == framesIn(30 * kMs, kRate));
  // from a short packet, and the next
  Placer shortPackets;
  CHECK(shortPackets.place(1'000 * kMs, 1'030 * kMs, 352, kRate).drop == 352);
  CHECK(shortPackets.place(1'008 * kMs, 1'030 * kMs, 352, kRate).drop == 352);
  CHECK(shortPackets.place(1'016 * kMs, 1'030 * kMs, 352, kRate).drop == 352);
  CHECK(shortPackets.place(1'024 * kMs, 1'030 * kMs, 352, kRate).drop == framesIn(30 * kMs, kRate) - 3 * 352);
  CHECK(shortPackets.place(1'032 * kMs, 1'030 * kMs, 352, kRate).drop == 0);
  CHECK(placed.silence == 0);
  CHECK(placer.delayNs() == 0);
}

TEST_CASE("a stream the output cannot play on time is delayed by what it lacks, and the delay holds") {
  Placer placer;
  // aac-eld's 170 ms lead against a 300 ms output: 130 ms late
  const auto first = placer.place(1'170 * kMs, 1'300 * kMs, 480, kRate);
  CHECK(first.shifted);
  CHECK(placer.delayNs() == 130 * kMs);
  CHECK(first.silence == 0);
  CHECK(first.drop == 0);
  // the next packets come on time against that delay: nothing to correct
  for (int i = 1; i < 50; ++i) {
    const auto at = static_cast<std::int64_t>(i) * 10 * kMs;
    const auto placed = placer.place(1'170 * kMs + at, 1'300 * kMs + at, 480, kRate);
    CHECK(placed.silence == 0);
    CHECK(placed.drop == 0);
    CHECK_FALSE(placed.shifted);
  }
  CHECK(placer.corrections() == 0);
  // after a flush the delay is what the next stream needs: still 100 ms here
  placer.reset();
  const auto again = placer.place(2'000 * kMs, 2'100 * kMs, 480, kRate);
  CHECK_FALSE(again.shifted);
  CHECK(placer.delayNs() == 100 * kMs);
  CHECK(again.silence == 0);
  CHECK(again.drop == 0);
  // and none once the output can play it on time
  placer.reset();
  const auto onTime = placer.place(3'000 * kMs, 2'880 * kMs, 480, kRate);
  CHECK(placer.delayNs() == 0);
  CHECK(onTime.silence == framesIn(120 * kMs, kRate));
}

TEST_CASE("jitter inside the dead band is never corrected") {
  Placer placer;
  placer.place(1'000 * kMs, 1'000 * kMs, 352, kRate);
  for (int i = 0; i < 200; ++i) {
    const auto jitter = (i % 2 == 0 ? 1 : -1) * 15 * kMs;
    const auto placed = placer.place(1'000 * kMs, 1'000 * kMs + jitter, 352, kRate);
    CHECK(placed.silence == 0);
    CHECK(placed.drop == 0);
  }
  CHECK(placer.corrections() == 0);
}

TEST_CASE("a steady error is steered, never cut, while it stays under the hard bound") {
  Placer placer;
  placer.place(0, 0, 352, kRate);
  // the device plays 30 ms ahead of the sender from here on: packets would be heard late, so it plays faster
  Placement placed;
  for (int i = 0; i < 100; ++i) {
    placed = placer.place(0, 30 * kMs, 352, kRate);
    CHECK(placed.drop == 0);
    CHECK(placed.silence == 0);
  }
  CHECK(placer.corrections() == 0);
  CHECK(placed.steerPpm == kMaxSteerPpm);
  // early (the device runs slow): slower, in proportion
  Placer slow;
  slow.place(0, 0, 352, kRate);
  for (int i = 0; i < 100; ++i)
    placed = slow.place(5 * kMs, 0, 352, kRate);
  CHECK(placed.silence == 0);
  CHECK(placed.steerPpm <= -5 * kSteerPpmPerMs + 10);
  CHECK(placed.steerPpm >= -5 * kSteerPpmPerMs - 10);
  // a reset forgets it
  slow.reset();
  CHECK(slow.place(0, 0, 352, kRate).steerPpm == 0);
}

TEST_CASE("an error past the hard bound is cut once, and the steering goes on from there") {
  Placer placer;
  placer.place(0, 0, 352, kRate);
  // 80 ms late, under the shift: dropped, spread over the packets
  std::size_t dropped = 0;
  int cuts = 0;
  for (int i = 0; i < 200; ++i) {
    const auto placed = placer.place(0, 80 * kMs, 352, kRate);
    cuts += placed.hard ? 1 : 0;
    dropped += placed.drop;
    if (dropped > 0 && placed.drop == 0)
      break;
  }
  CHECK(cuts == 1);
  CHECK(dropped > framesIn(kHardNs, kRate));
  CHECK(dropped <= framesIn(80 * kMs, kRate));
  // early by as much: silence fills it
  Placer slow;
  slow.place(0, 0, 352, kRate);
  std::size_t silence = 0;
  for (int i = 0; i < 100 && silence == 0; ++i)
    silence = slow.place(90 * kMs, 0, 352, kRate).silence;
  CHECK(silence > framesIn(kHardNs, kRate));
}

TEST_CASE("two clocks that drift apart stay in sync by steering alone") {
  // the sender's 44.1 kHz against an output whose clock runs 150 ppm fast: without steering the queue drains 6.6 ms a
  // minute early. the model: what a pushed frame is heard at moves with what was pushed and what the output played
  constexpr double kDrift = 150e-6;
  constexpr std::int64_t kPacketNs = 352LL * 1'000'000'000 / kRate;
  Placer placer;
  double queueEndNs = 100.0 * kMs;  // the output's queue end, ahead of now
  std::int64_t now = 0;
  std::int64_t worst = 0;
  for (int i = 0; i < 10 * 60 * 125; ++i) {
    now += kPacketNs;
    // the output plays its queue faster than real time
    queueEndNs -= static_cast<double>(kPacketNs) * (1.0 + kDrift);
    const auto heard = now + static_cast<std::int64_t>(queueEndNs);
    const auto target = static_cast<std::int64_t>(i) * kPacketNs + 100 * kMs;
    const auto placed = placer.place(target, heard, 352, kRate);
    REQUIRE(placed.silence == 0);
    REQUIRE(placed.drop == 0);
    // the packet's span, stretched or squeezed by the steering
    queueEndNs += static_cast<double>(kPacketNs) * (1.0 - placed.steerPpm * 1e-6);
    if (i > 125 * 60)
      worst = std::max(worst, std::abs(target - heard));
  }
  CHECK(placer.corrections() == 0);
  // the steering settles where it plays as slow as the output runs fast, a ms or two off
  CHECK(placer.steerPpm() <= -140);
  CHECK(placer.steerPpm() >= -160);
  CHECK(worst < 3 * kMs);
}

TEST_CASE("a latency that jumps past the shift delays the stream instead of dropping it") {
  Placer placer;
  placer.place(0, 0, 352, kRate);
  bool shifted = false;
  int packets = 0;
  for (; packets < 10 && !shifted; ++packets) {
    const auto placed = placer.place(0, 400 * kMs, 352, kRate);
    CHECK(placed.drop == 0);
    shifted = placed.shifted;
  }
  CHECK(shifted);
  CHECK(packets == kShiftAfter);
  CHECK(placer.delayNs() == 400 * kMs);
  // one late estimate alone shifts nothing
  Placer once;
  once.place(0, 0, 352, kRate);
  CHECK_FALSE(once.place(0, 400 * kMs, 352, kRate).shifted);
  CHECK_FALSE(once.place(0, 0, 352, kRate).shifted);
  CHECK(once.delayNs() == 0);
}

TEST_CASE("a burst later than the most the stream is delayed is dropped to catch up") {
  Placer placer;
  placer.place(0, 0, 480, kRate);
  // libairplay held its packets for a lost one's resend: 2.5 s late, three packets in a row
  Placement placed;
  for (int i = 0; i < kShiftAfter; ++i)
    placed = placer.place(0, 2'500 * kMs, 480, kRate);
  CHECK(placed.shifted);
  CHECK(placer.delayNs() == kMaxDelayNs);
  CHECK(placed.caughtUpNs == 2'500 * kMs - kMaxDelayNs);
  // what the delay could not take is dropped from this packet and the next ones
  CHECK(placed.drop == 480);
  std::size_t dropped = placed.drop;
  for (int i = 0; i < 1000 && dropped < framesIn(1'500 * kMs, kRate); ++i)
    dropped += placer.place(0, 2'500 * kMs, 480, kRate).drop;
  CHECK(dropped == framesIn(1'500 * kMs, kRate));
  // and a later burst still never delays it further
  for (int i = 0; i < kShiftAfter; ++i)
    placed = placer.place(0, 4'000 * kMs, 480, kRate);
  CHECK_FALSE(placed.shifted);
  CHECK(placer.delayNs() == kMaxDelayNs);
}

TEST_CASE("packets that keep arriving early give back the delay a burst made the stream take") {
  Placer placer;
  placer.place(0, 0, 352, kRate);
  for (int i = 0; i < kShiftAfter; ++i)
    placer.place(0, 400 * kMs, 352, kRate);
  REQUIRE(placer.delayNs() == 400 * kMs);
  // on time again: each packet arrives 300 ms before it must be in the output, for a whole window
  for (std::int64_t at = 0; at <= kRecoverWindowNs; at += 10 * kMs)
    placer.arrived(at + 100 * kMs, at + 200 * kMs);
  const auto placed = placer.place(0, 400 * kMs, 352, kRate);
  CHECK(placed.recovered);
  CHECK_FALSE(placed.shifted);
  // 300 ms of margin less the slack kept
  CHECK(placer.delayNs() == 400 * kMs - (300 * kMs - kSlackNs));
  std::size_t dropped = placed.drop;
  for (int i = 0; i < 100 && dropped < framesIn(300 * kMs - kSlackNs, kRate); ++i)
    dropped += placer.place(0, 400 * kMs, 352, kRate).drop;
  CHECK(dropped == framesIn(300 * kMs - kSlackNs, kRate));
  // a window with one late packet gives nothing back
  Placer late;
  late.place(0, 0, 352, kRate);
  for (int i = 0; i < kShiftAfter; ++i)
    late.place(0, 400 * kMs, 352, kRate);
  for (std::int64_t at = 0; at <= kRecoverWindowNs; at += 10 * kMs)
    late.arrived(at + 100 * kMs, at == 500 * kMs ? at + 900 * kMs : at + 200 * kMs);
  CHECK_FALSE(late.place(0, 400 * kMs, 352, kRate).recovered);
  CHECK(late.delayNs() == 400 * kMs);
}

TEST_CASE("a mirror's audio with a late burst every few seconds stays in sync, its delay bounded") {
  // the stick's case: aac-eld's 170 ms lead, a 300 ms output, and libairplay handing over packets in late bursts
  // (a lost packet's resend wait) for two minutes. the output is modelled by when the next pushed frame is heard
  constexpr std::int64_t kLead = 170 * kMs;
  constexpr std::int64_t kLatency = 300 * kMs;
  constexpr std::int64_t kPacketNs = 480LL * 1'000'000'000 / kRate;
  Placer placer;
  std::int64_t queueEnd = 0;
  std::int64_t maxDelay = 0;
  std::int64_t lastOffset = 0;
  const std::int64_t start = 10'000 * kMs;
  for (std::int64_t i = 0; i < 120'000 * kMs / kPacketNs; ++i) {
    const auto target = start + i * kPacketNs;
    auto arrival = target - kLead;
    // every 15 s, 0.5 to 2.7 s of packets arrive together at the stall's end
    const auto inCycle = (arrival - start) % (15'000 * kMs);
    const auto stall = (500 + ((arrival - start) / (15'000 * kMs)) * 300 % 2'200) * kMs;
    if (inCycle > 5'000 * kMs && inCycle < 5'000 * kMs + stall)
      arrival = arrival - inCycle + 5'000 * kMs + stall;
    placer.arrived(target, arrival + kLatency);
    const auto heard = std::max(arrival + kLatency, queueEnd);
    const auto placed = placer.place(target, heard, 480, kRate);
    const auto first = heard + static_cast<std::int64_t>(placed.silence) * 1'000'000'000 / kRate;
    queueEnd = first + static_cast<std::int64_t>(480 - placed.drop) * 1'000'000'000 / kRate;
    maxDelay = std::max(maxDelay, placer.delayNs());
    // when this packet's first frame kept is heard, against when the sender asked plus the delay the clock follows
    if (placed.drop < 480)
      lastOffset = first - (target + static_cast<std::int64_t>(placed.drop) * 1'000'000'000 / kRate);
  }
  CHECK(maxDelay <= kMaxDelayNs);
  // the delay the output needs (latency less the lead) and the slack, not the sum of the bursts
  CHECK(placer.delayNs() >= kLatency - kLead);
  CHECK(placer.delayNs() <= kLatency - kLead + kSlackNs + kDeadBandNs);
  // and the audio is heard where the clock puts it: the target plus the delay, within the dead band
  CHECK(std::abs(lastOffset - placer.delayNs()) <= kDeadBandNs);
}
