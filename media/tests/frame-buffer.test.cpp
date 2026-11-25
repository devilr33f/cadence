#include "capture/frame-buffer.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>

#include <algorithm>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <future>
#include <numeric>
#include <span>
#include <vector>

using namespace cadence;
using namespace std::chrono_literals;

namespace {

constexpr std::size_t kChannels = 4;

std::vector<std::int16_t> ramp(std::size_t count, std::int16_t first) {
  std::vector<std::int16_t> samples(count);
  std::iota(samples.begin(), samples.end(), first);
  return samples;
}

// frame n holds 10n + channel, so a reordered frame or channel changes the values
std::vector<std::int16_t> frames(std::size_t first, std::size_t count) {
  std::vector<std::int16_t> samples;
  for (std::size_t frame = first; frame < first + count; ++frame)
    for (std::size_t channel = 0; channel < kChannels; ++channel)
      samples.push_back(static_cast<std::int16_t>(frame * 10 + channel));
  return samples;
}

struct Popped {
  std::vector<std::int16_t> samples;
  bool afterGap = false;
};

Popped popUpTo(CaptureFifo& fifo, std::size_t maxFrames) {
  std::vector<std::int16_t> buffer(maxFrames * kChannels);
  const auto read = fifo.pop(buffer, 0ms);
  buffer.resize(read.frames * kChannels);
  return {buffer, read.afterGap};
}

}  // namespace

TEST_CASE("frames any vqe chunk size into whole 320-sample frames") {
  const auto chunk = GENERATE(as<std::size_t>{}, 1, 256, 320, 480);
  const auto input = ramp(3000, 0);
  PcmFramer framer;
  std::vector<std::int16_t> output;

  for (std::size_t offset = 0; offset < input.size(); offset += chunk) {
    std::span<const std::int16_t> pending(input.data() + offset, std::min(chunk, input.size() - offset));
    while (!pending.empty())
      if (const auto frame = framer.take(pending))
        output.insert(output.end(), frame->begin(), frame->end());
  }

  STATIC_REQUIRE(kPcm16FrameSamples == 320);
  CHECK(output.size() == 9 * kPcm16FrameSamples);
  CHECK(std::equal(output.begin(), output.end(), input.begin()));
  CHECK(framer.pending() == 3000 % kPcm16FrameSamples);
}

TEST_CASE("clearing the framer drops only the partial frame") {
  PcmFramer framer;
  const auto stale = ramp(100, 1000);
  std::span<const std::int16_t> first(stale);
  CHECK_FALSE(framer.take(first).has_value());
  CHECK(first.empty());
  CHECK(framer.pending() == 100);

  framer.clear();
  CHECK(framer.pending() == 0);

  const auto fresh = ramp(330, 5000);
  std::span<const std::int16_t> next(fresh);
  const auto frame = framer.take(next);
  REQUIRE(frame.has_value());
  CHECK(std::equal(frame->begin(), frame->end(), fresh.begin()));
  CHECK(next.size() == 10);
}

TEST_CASE("the fifo returns frames in order with their channels intact, also around its capacity") {
  CaptureFifo fifo(8, kChannels);
  CHECK(fifo.push(frames(0, 5)) == 0);
  CHECK(fifo.push(frames(5, 3)) == 0);
  CHECK(fifo.size() == 8);

  const auto first = popUpTo(fifo, 6);
  CHECK(first.samples == frames(0, 6));
  CHECK_FALSE(first.afterGap);
  CHECK(popUpTo(fifo, 6).samples == frames(6, 2));
  CHECK(fifo.size() == 0);

  for (std::size_t round = 1; round < 5; ++round) {
    fifo.push(frames(round * 6 + 2, 6));
    CHECK(popUpTo(fifo, 8).samples == frames(round * 6 + 2, 6));
  }
}

TEST_CASE("an empty fifo pop returns nothing after its timeout") {
  CaptureFifo fifo(16, kChannels);
  std::vector<std::int16_t> buffer(4 * kChannels);
  const auto started = std::chrono::steady_clock::now();

  const auto read = fifo.pop(buffer, 20ms);

  CHECK(read.frames == 0);
  CHECK(std::chrono::steady_clock::now() - started >= 20ms);
}

TEST_CASE("an overflow after a partial read flags the next read") {
  CaptureFifo fifo(8, kChannels);
  fifo.push(frames(0, 4));
  // the consumer now holds frames 0 and 1 of an unfinished chunk
  CHECK_FALSE(popUpTo(fifo, 2).afterGap);

  CHECK(fifo.push(frames(4, 10)) == 4);

  const auto read = popUpTo(fifo, 16);
  CHECK(read.afterGap);
  CHECK(read.samples == frames(6, 8));
}

TEST_CASE("one push larger than the fifo keeps only its newest frames") {
  CaptureFifo fifo(8, kChannels);
  CHECK(fifo.push(frames(0, 20)) == 12);

  const auto read = popUpTo(fifo, 16);
  CHECK(read.afterGap);
  CHECK(read.samples == frames(12, 8));
}

TEST_CASE("a marked gap splits reads so audio is never joined across it") {
  CaptureFifo fifo(16, kChannels);
  fifo.push(frames(0, 3));
  fifo.markGap();
  fifo.markGap();
  fifo.push(frames(3, 3));

  const auto before = popUpTo(fifo, 16);
  CHECK(before.samples == frames(0, 3));
  CHECK_FALSE(before.afterGap);
  const auto after = popUpTo(fifo, 16);
  CHECK(after.samples == frames(3, 3));
  CHECK(after.afterGap);
  CHECK(popUpTo(fifo, 16).samples.empty());
}

TEST_CASE("a gap marked on a drained fifo flags the next frames") {
  CaptureFifo fifo(16, kChannels);
  fifo.push(frames(0, 2));
  popUpTo(fifo, 16);
  fifo.markGap();

  const auto none = popUpTo(fifo, 16);
  CHECK(none.samples.empty());
  CHECK_FALSE(none.afterGap);

  fifo.push(frames(2, 2));
  const auto read = popUpTo(fifo, 16);
  CHECK(read.afterGap);
  CHECK(read.samples == frames(2, 2));
}

TEST_CASE("discarding drops every queued frame and flags the next read once") {
  CaptureFifo fifo(16, kChannels);
  fifo.push(frames(0, 3));
  fifo.markGap();
  fifo.push(frames(3, 3));
  // the consumer now holds frames 0 and 1 of an unfinished chunk
  CHECK_FALSE(popUpTo(fifo, 2).afterGap);

  fifo.discard();

  CHECK(fifo.size() == 0);
  CHECK(popUpTo(fifo, 16).samples.empty());
  fifo.push(frames(6, 3));
  const auto read = popUpTo(fifo, 16);
  CHECK(read.afterGap);
  CHECK(read.samples == frames(6, 3));
  fifo.push(frames(9, 2));
  const auto next = popUpTo(fifo, 16);
  CHECK_FALSE(next.afterGap);
  CHECK(next.samples == frames(9, 2));
}

TEST_CASE("a waiting pop wakes for pushed frames and for close") {
  CaptureFifo fifo(16, kChannels);
  const auto waitingPop = [&fifo] {
    std::vector<std::int16_t> buffer(4 * kChannels);
    return fifo.pop(buffer, 10s).frames;
  };

  auto reader = std::async(std::launch::async, waitingPop);
  fifo.push(frames(0, 2));
  REQUIRE(reader.wait_for(5s) == std::future_status::ready);
  CHECK(reader.get() == 2);

  auto closing = std::async(std::launch::async, waitingPop);
  fifo.close();
  REQUIRE(closing.wait_for(5s) == std::future_status::ready);
  CHECK(closing.get() == 0);
}
