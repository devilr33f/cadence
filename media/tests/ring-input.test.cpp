#include "ring/ring-input.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/matchers/catch_matchers_floating_point.hpp>

#include <cstddef>
#include <cstdint>
#include <ctime>
#include <memory>
#include <vector>

using namespace cadence;
using Catch::Matchers::WithinAbs;

namespace {

// interleaved stereo frames first, first + 1, ... with the right channel negated, so each frame can be traced
std::vector<float> traced(std::size_t frames, std::size_t first = 1) {
  std::vector<float> samples(frames * 2);
  for (std::size_t i = 0; i < frames; ++i) {
    samples[2 * i] = static_cast<float>(first + i) / 1e5f;
    samples[2 * i + 1] = -samples[2 * i];
  }
  return samples;
}

float frameOf(float sample) { return sample * 1e5f; }

struct Rig {
  Rig() {
    auto made = RingInput::create(1024);
    REQUIRE(made.ok());
    ring = std::move(made.value());
    writer = RingWriter::attach(ring->ringFd());
    REQUIRE(writer);
    ring->setOpen(true);
    writer->setState(CADENCE_RING_STREAMING);
  }

  std::vector<float> read(std::size_t frames, std::int64_t heardNs = 0, std::size_t* got = nullptr) {
    std::vector<float> out(frames * 2, 9.0f);
    const auto n = ring->read(out, heardNs);
    if (got != nullptr)
      *got = n;
    return out;
  }

  std::unique_ptr<RingInput> ring;
  std::unique_ptr<RingWriter> writer;
};

std::int64_t monotonicNs() {
  timespec now{};
  clock_gettime(CLOCK_MONOTONIC, &now);
  return static_cast<std::int64_t>(now.tv_sec) * 1'000'000'000 + now.tv_nsec;
}

}  // namespace

TEST_CASE("the ring refuses a capacity that is no power of two in range") {
  CHECK_FALSE(RingInput::create(1000).ok());
  CHECK_FALSE(RingInput::create(128).ok());
  CHECK_FALSE(RingInput::create(1U << 17).ok());
  CHECK(RingInput::create(256).ok());
}

TEST_CASE("frames come out in order, with silence after them, and wrap around the ring") {
  Rig rig;
  for (std::size_t round = 0; round < 5; ++round) {
    const auto first = 1 + round * 400;
    REQUIRE(rig.writer->write(traced(400, first)) == 400);
    std::size_t got = 0;
    const auto out = rig.read(480, 0, &got);
    REQUIRE(got == 400);
    CHECK_THAT(frameOf(out[0]), WithinAbs(static_cast<double>(first), 1e-2));
    CHECK_THAT(frameOf(out[2 * 399]), WithinAbs(static_cast<double>(first + 399), 1e-2));
    CHECK(out[2 * 399 + 1] == -out[2 * 399]);
    CHECK(out[2 * 400] == 0.0f);
  }
  // a short read while streaming is an underrun
  CHECK(rig.ring->status().underruns == 5);
}

TEST_CASE("the writer never overwrites what the reader has not read") {
  Rig rig;
  CHECK(rig.writer->write(traced(1500)) == 1024);
  CHECK(rig.writer->write(traced(10)) == 0);
  rig.read(100);
  CHECK(rig.writer->write(traced(200, 2000)) == 100);
}

TEST_CASE("a flush drops what was queued before it") {
  Rig rig;
  rig.ring->setRefillTarget(50);
  rig.writer->write(traced(600));
  rig.read(100);
  rig.writer->flush();
  rig.writer->write(traced(50, 9000));
  std::size_t got = 0;
  const auto out = rig.read(100, 0, &got);
  REQUIRE(got == 50);
  CHECK_THAT(frameOf(out[0]), WithinAbs(9000.0, 1e-1));
  CHECK(rig.ring->status().generation == 1);
}

TEST_CASE("a paused writer's frames wait for its resume, read as silence meanwhile") {
  Rig rig;
  rig.writer->write(traced(300));
  rig.writer->setState(CADENCE_RING_PAUSED);
  std::size_t got = 1;
  auto out = rig.read(100, 0, &got);
  CHECK(got == 0);
  CHECK(out[0] == 0.0f);
  // a pause is no underrun
  CHECK(rig.ring->status().underruns == 0);
  rig.writer->setState(CADENCE_RING_STREAMING);
  out = rig.read(100, 0, &got);
  CHECK(got == 100);
  CHECK_THAT(frameOf(out[0]), WithinAbs(1.0, 1e-3));
}

// a session that lost the speaker is not read at all: a writer that did not pause waits on a full ring and loses
// nothing
TEST_CASE("a closed ring is never read") {
  Rig rig;
  rig.writer->write(traced(300));
  rig.ring->setOpen(false);
  std::size_t got = 1;
  rig.read(100, 0, &got);
  CHECK(got == 0);
  CHECK(rig.ring->status().readPos == 0);
  rig.ring->setOpen(true);
  const auto out = rig.read(100, 0, &got);
  CHECK(got == 100);
  CHECK_THAT(frameOf(out[0]), WithinAbs(1.0, 1e-3));
}

TEST_CASE("the reader publishes when the frame at its position is heard") {
  Rig rig;
  std::uint64_t pos = 0;
  std::int64_t heard = 0;
  CHECK_FALSE(rig.writer->timestamp(pos, heard));
  rig.writer->write(traced(960));
  const auto at = monotonicNs() + 40'000'000;
  rig.read(480, at);
  REQUIRE(rig.writer->timestamp(pos, heard));
  CHECK(pos == 480);
  // the block's first frame at `at`, so the frame after its 480 at 10 ms later
  CHECK(heard == at + 10'000'000);
  // an unknown device delay publishes nothing new
  rig.read(480, 0);
  REQUIRE(rig.writer->timestamp(pos, heard));
  CHECK(pos == 480);
  CHECK(rig.ring->status().latencyNs > 0);
}

// rule 7: the session's own gain only ever lowers it
TEST_CASE("the session's gain ramps the frames read, and a mute silences them") {
  Rig rig;
  std::vector<float> loud(2000 * 2, 0.5f);
  rig.ring->setGain(0.2f, 0);
  rig.writer->write(std::span<const float>(loud).first(200 * 2));
  auto out = rig.read(200);
  CHECK_THAT(out[0], WithinAbs(0.1, 1e-6));
  rig.ring->setGain(3.0f, 0);
  rig.writer->write(std::span<const float>(loud).first(200 * 2));
  out = rig.read(200);
  CHECK_THAT(out[0], WithinAbs(0.5, 1e-6));
  rig.ring->setMuted(true);
  rig.writer->write(std::span<const float>(loud).first(480 * 2));
  out = rig.read(480);
  // 5 ms to silence, then nothing
  CHECK(out[2 * 479] == 0.0f);
  rig.ring->setMuted(false);
  rig.writer->write(std::span<const float>(loud).first(480 * 2));
  out = rig.read(480);
  CHECK_THAT(out[2 * 479], WithinAbs(0.5, 1e-6));
}

TEST_CASE("a writer attaches only to a ring") {
  CHECK(RingWriter::attach(-1) == nullptr);
}

// s5 on the stick: one burst or 40 ms underran; two bursts and at least 120 ms ran clean
TEST_CASE("a ring holds two bursts and at least 120 ms, as a power of two") {
  CHECK(RingInput::capacityFor(0) == 8192);
  CHECK(RingInput::capacityFor(480) == 8192);
  CHECK(RingInput::capacityFor(2050) == 8192);
  CHECK(RingInput::capacityFor(4800) == 16384);
  CHECK(RingInput::capacityFor(1U << 20) == 65536);
}

// s5: a seek emptied the ring, and the output drained it before mpv refilled it: ~13 underruns and 0.1 s lost
TEST_CASE("after a flush the reader holds, position frozen, until the ring refilled to its target") {
  Rig rig;
  rig.ring->setRefillTarget(512);
  rig.writer->write(traced(600));
  rig.read(100);
  rig.writer->flush();
  rig.writer->write(traced(300, 5000));
  const auto at = monotonicNs() + 50'000'000;
  std::size_t got = 1;
  auto out = rig.read(240, at, &got);
  CHECK(got == 0);
  CHECK(out[0] == 0.0f);
  std::uint64_t pos = 0;
  std::int64_t heard = 0;
  REQUIRE(rig.writer->timestamp(pos, heard));
  // frozen at the flush, heard no sooner than after the silent block
  CHECK(pos == 600);
  CHECK(heard == at + 5'000'000);
  // holding is no underrun, and plays nothing of what the flush dropped
  CHECK(rig.ring->status().underruns == 0);
  rig.writer->write(traced(300, 5300));
  out = rig.read(240, 0, &got);
  REQUIRE(got == 240);
  CHECK_THAT(frameOf(out[0]), WithinAbs(5000.0, 1e-1));
  // refilled once, it plays on as before, down to empty
  rig.read(1000, 0, &got);
  CHECK(got == 360);
}

// the stick: ao_cadence flushes at its init and then queues 40 ms (its default --ao-cadence-buffer, 1920 frames),
// never the 4096 frames half an 8192-frame ring asks for, so the session was held silent for good
TEST_CASE("a writer that queues less than the refill target is played once the hold ran its bound") {
  auto made = RingInput::create(8192);
  REQUIRE(made.ok());
  auto ring = std::move(made.value());
  auto writer = RingWriter::attach(ring->ringFd());
  REQUIRE(writer);
  ring->setOpen(true);
  // ao_cadence's init and start: a flush, then streaming, filled to its own target and no further
  writer->flush();
  writer->setState(CADENCE_RING_STREAMING);
  REQUIRE(writer->write(traced(1920, 1)) == 1920);
  constexpr std::size_t kBurst = 96;
  std::vector<float> out(kBurst * 2);
  std::size_t silent = 0;
  std::size_t got = 0;
  for (int i = 0; i < 1000 && got == 0; ++i) {
    got = ring->read(out, 0);
    if (got == 0)
      silent += kBurst;
  }
  REQUIRE(got == kBurst);
  CHECK_THAT(frameOf(out[0]), WithinAbs(1.0, 1e-3));
  // held for the bound, not longer
  CHECK(silent >= 48 * RingInput::kMaxRefillHoldMs);
  CHECK(silent < 48 * RingInput::kMaxRefillHoldMs + kBurst);
  const auto status = ring->status();
  CHECK(status.refillTimeouts == 1);
  CHECK(status.heldReads == silent / kBurst);
  CHECK(status.framesRead == kBurst);
  CHECK_FALSE(status.refilling);
  CHECK(status.underruns == 0);
  // and it plays on as the writer keeps up
  REQUIRE(writer->write(traced(kBurst, 1921)) == kBurst);
  for (int i = 0; i < 20; ++i)
    REQUIRE(ring->read(out, 0) == kBurst);
  CHECK(ring->status().framesRead == 21 * kBurst);
}

TEST_CASE("an empty flushed ring holds without a bound: there is nothing to play") {
  Rig rig;
  rig.writer->flush();
  std::size_t got = 1;
  for (int i = 0; i < 100; ++i)
    rig.read(480, 0, &got);
  CHECK(got == 0);
  const auto status = rig.ring->status();
  CHECK(status.refilling);
  CHECK(status.refillTimeouts == 0);
  CHECK(status.heldReads == 100);
  CHECK(status.underruns == 0);
  // the first frames end the hold once it ran its bound
  rig.writer->write(traced(10));
  rig.read(480, 0, &got);
  CHECK(got == 10);
}
