#include "playback/jitter-buffer.h"
#include "support/jitter-reads.h"
#include "support/samples.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <cstddef>
#include <cstdint>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::test_support;

TEST_CASE("defaults to a 100 ms start target and a 500 ms maximum") {
  const JitterBufferConfig config;
  CHECK(config.targetSamples == kTarget);
  CHECK(config.maxSamples == kMaximum);
}

TEST_CASE("holds a response silent until 100 ms is queued") {
  JitterBuffer buffer;
  REQUIRE(buffer.begin("r1"));
  REQUIRE(buffer.push("r1", ramp(kTarget - 1)) == PushResult::Accepted);
  std::vector<std::int16_t> piece(kRead, -1);

  const auto waiting = buffer.read(piece);

  CHECK(waiting.samples == 0);
  CHECK_FALSE(waiting.underrun);
  CHECK(silent(piece));
  CHECK_FALSE(buffer.status().playing);

  REQUIRE(buffer.push("r1", ramp(1, static_cast<int>(kTarget))) == PushResult::Accepted);
  const auto playing = buffer.read(piece);

  CHECK(playing.samples == kRead);
  CHECK(piece == ramp(kRead));
  CHECK(buffer.status().playing);
}

TEST_CASE("plays out the rest of an ended response below the target, then reports it drained") {
  JitterBuffer buffer;
  REQUIRE(buffer.begin("r1"));
  REQUIRE(buffer.push("r1", ramp(1000)) == PushResult::Accepted);
  REQUIRE(buffer.end("r1"));
  std::vector<std::int16_t> piece(kRead);

  CHECK(buffer.read(piece).samples == kRead);
  CHECK(buffer.read(piece).samples == kRead);
  const auto last = buffer.read(piece);

  CHECK(last.samples == 40);
  CHECK(last.drained);
  CHECK_FALSE(last.underrun);
  CHECK(std::vector(piece.begin(), piece.begin() + 40) == ramp(40, 961));
  CHECK(std::all_of(piece.begin() + 40, piece.end(), [](std::int16_t sample) { return sample == 0; }));
  const auto status = buffer.status();
  CHECK_FALSE(status.activeResponse.has_value());
  CHECK(status.drained == 1);
  CHECK(status.underruns == 0);
}

TEST_CASE("an ended response accepts no more packets and cannot start again") {
  JitterBuffer buffer;
  REQUIRE(buffer.begin("r1"));
  REQUIRE(buffer.push("r1", ramp(100)) == PushResult::Accepted);
  REQUIRE(buffer.end("r1"));

  CHECK(buffer.push("r1", ramp(100)) == PushResult::Stale);
  CHECK_FALSE(buffer.end("r1"));
  readSamples(buffer, kRead);
  CHECK_FALSE(buffer.begin("r1"));
  CHECK(buffer.push("r1", ramp(100)) == PushResult::Stale);
}

TEST_CASE("rejects packets and an end for any response other than the active one") {
  JitterBuffer buffer;

  CHECK(buffer.push("r1", ramp(10)) == PushResult::Stale);
  REQUIRE(buffer.begin("r2"));
  CHECK(buffer.push("r1", ramp(10)) == PushResult::Stale);
  CHECK(buffer.push("r2", ramp(10)) == PushResult::Accepted);
  CHECK(buffer.push("r3", ramp(10)) == PushResult::Stale);
  CHECK_FALSE(buffer.end("r1"));

  const auto status = buffer.status();
  CHECK(status.staleRejected == 3);
  CHECK(status.queuedSamples == 10);
  CHECK(status.activeResponse == "r2");
  CHECK_FALSE(status.ended);
}

TEST_CASE("cancel clears queued audio and rejects every later packet of that response") {
  JitterBuffer buffer;
  REQUIRE(buffer.begin("r1"));
  REQUIRE(buffer.push("r1", constant(kTarget * 2, 1000)) == PushResult::Accepted);
  const auto before = buffer.generation();

  CHECK(buffer.cancel("r1"));

  CHECK(buffer.generation() > before);
  CHECK(buffer.status().queuedSamples == 0);
  CHECK(silent(readSamples(buffer, kTarget)));
  CHECK(buffer.push("r1", constant(kTarget, 1000)) == PushResult::Stale);
  CHECK_FALSE(buffer.begin("r1"));
  REQUIRE(buffer.begin("r2"));
  CHECK(buffer.push("r1", constant(kTarget, 1000)) == PushResult::Stale);
}

TEST_CASE("cancelling an id that is not active leaves the active response playing and rejects that id later") {
  JitterBuffer buffer;
  REQUIRE(buffer.begin("r2"));
  REQUIRE(buffer.push("r2", ramp(kTarget)) == PushResult::Accepted);
  const auto before = buffer.generation();

  CHECK_FALSE(buffer.cancel("r1"));

  CHECK(buffer.generation() == before);
  CHECK(readSamples(buffer, kRead) == ramp(kRead));
  CHECK_FALSE(buffer.begin("r1"));
}

TEST_CASE("a fresh begin keeps the generation, and a repeated begin of the active response changes nothing") {
  JitterBuffer buffer;
  const auto fresh = buffer.generation();

  REQUIRE(buffer.begin("r1"));
  REQUIRE(buffer.push("r1", ramp(kTarget)) == PushResult::Accepted);
  CHECK(buffer.generation() == fresh);
  CHECK_FALSE(buffer.begin("r1"));

  CHECK(buffer.generation() == fresh);
  CHECK(buffer.status().queuedSamples == kTarget);
  CHECK(readSamples(buffer, kRead) == ramp(kRead));
}

TEST_CASE("each read reports its generation, and a cancel without an id advances it even when nothing is active") {
  JitterBuffer buffer;
  std::vector<std::int16_t> piece(kRead);
  const auto first = buffer.read(piece).generation;

  CHECK(buffer.cancel());

  CHECK(buffer.generation() == first + 1);
  CHECK(buffer.read(piece).generation == first + 1);
}

TEST_CASE("drops and counts a packet that would exceed 500 ms instead of blocking") {
  JitterBuffer buffer;
  REQUIRE(buffer.begin("r1"));
  REQUIRE(buffer.push("r1", ramp(kMaximum - 10)) == PushResult::Accepted);

  CHECK(buffer.push("r1", constant(11, -5)) == PushResult::Overflow);
  CHECK(buffer.push("r1", constant(kMaximum + 1, -5)) == PushResult::Overflow);
  CHECK(buffer.push("r1", ramp(10, static_cast<int>(kMaximum - 9))) == PushResult::Accepted);

  const auto status = buffer.status();
  CHECK(status.overflowDropped == 2);
  CHECK(status.queuedSamples == kMaximum);
  REQUIRE(buffer.end("r1"));
  CHECK(readSamples(buffer, kMaximum) == ramp(kMaximum));
}

TEST_CASE("an underrun plays silence, never repeats a sample, and waits for the target again") {
  JitterBuffer buffer;
  REQUIRE(buffer.begin("r1"));
  REQUIRE(buffer.push("r1", ramp(kTarget)) == PushResult::Accepted);
  CHECK(readSamples(buffer, kTarget) == ramp(kTarget));
  std::vector<std::int16_t> piece(kRead, -1);

  const auto dry = buffer.read(piece);

  CHECK(dry.underrun);
  CHECK(dry.samples == 0);
  CHECK(silent(piece));
  CHECK_FALSE(buffer.read(piece).underrun);
  CHECK(silent(piece));
  CHECK(buffer.status().underruns == 1);

  REQUIRE(buffer.push("r1", ramp(kTarget / 2, static_cast<int>(kTarget + 1))) == PushResult::Accepted);
  CHECK(silent(readSamples(buffer, kRead)));
  REQUIRE(buffer.push("r1", ramp(kTarget / 2, static_cast<int>(kTarget + kTarget / 2 + 1))) == PushResult::Accepted);
  CHECK(readSamples(buffer, kTarget) == ramp(kTarget, static_cast<int>(kTarget + 1)));
}

TEST_CASE("a read that runs out partway is completed with silence") {
  JitterBuffer buffer;
  REQUIRE(buffer.begin("r1"));
  REQUIRE(buffer.push("r1", ramp(kTarget + 100)) == PushResult::Accepted);
  readSamples(buffer, kTarget);
  std::vector<std::int16_t> piece(kRead, -1);

  const auto read = buffer.read(piece);

  CHECK(read.samples == 100);
  CHECK(read.underrun);
  CHECK(std::vector(piece.begin(), piece.begin() + 100) == ramp(100, static_cast<int>(kTarget + 1)));
  CHECK(std::all_of(piece.begin() + 100, piece.end(), [](std::int16_t sample) { return sample == 0; }));
}

TEST_CASE("remembers a bounded number of retired responses") {
  JitterBuffer buffer(JitterBufferConfig{.retiredIds = 4});
  for (const char* id : {"r1", "r2", "r3", "r4", "r5"})
    buffer.cancel(id);

  for (const char* id : {"r2", "r3", "r4", "r5"})
    CHECK_FALSE(buffer.begin(id));
  CHECK(buffer.begin("r1"));
}

TEST_CASE("a producer and the playback reader may run at the same time") {
  JitterBuffer buffer;
  REQUIRE(buffer.begin("r1"));
  constexpr std::size_t kPackets = 200;
  constexpr std::size_t kPacket = 960;

  std::size_t accepted = 0;
  std::thread producer([&buffer, &accepted] {
    for (std::size_t packet = 0; packet < kPackets; ++packet) {
      while (buffer.status().queuedSamples + kPacket > kMaximum)
        std::this_thread::yield();
      if (buffer.push("r1", ramp(kPacket, static_cast<int>(packet % 30) * 1000 + 1)) == PushResult::Accepted)
        ++accepted;
    }
    buffer.end("r1");
  });
  std::size_t heard = 0;
  std::vector<std::int16_t> piece(kRead);
  while (!buffer.read(piece).drained) {
    heard += audible(piece).size();
    std::this_thread::yield();
  }
  heard += audible(piece).size();
  producer.join();

  CHECK(accepted == kPackets);
  CHECK(heard == kPackets * kPacket);
}

TEST_CASE("names, for the trace marks, the responses a read begins and the one that runs dry") {
  JitterBuffer buffer;
  REQUIRE(buffer.begin("r1"));
  REQUIRE(buffer.push("r1", ramp(100)) == PushResult::Accepted);
  REQUIRE(buffer.end("r1"));
  REQUIRE(buffer.begin("r2"));
  REQUIRE(buffer.push("r2", ramp(kTarget + 10)) == PushResult::Accepted);
  std::vector<std::int16_t> piece(kTarget);

  const auto first = buffer.read(piece);
  const auto later = buffer.read(piece);
  if constexpr (!trace::kEnabled) {
    CHECK(first.trace.starts == 0);
    return;
  }
  // r1's 100 samples, then r2 from where they end
  REQUIRE(first.trace.starts == 2);
  CHECK(first.trace.startIds[0].view() == "r1");
  CHECK(first.trace.startOffsets[0] == 0);
  CHECK(first.trace.startIds[1].view() == "r2");
  CHECK(first.trace.startOffsets[1] == 100);
  // r2 began already, and runs dry before its end
  CHECK(later.trace.starts == 0);
  REQUIRE(later.underrun);
  CHECK(later.trace.underrun.view() == "r2");
}
