#include "backend/pcm-input.h"
#include "support/eventually.h"
#include "support/signals.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>
#include <catch2/matchers/catch_matchers_floating_point.hpp>

#include <algorithm>
#include <atomic>
#include <chrono>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <future>
#include <span>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::test_support;
using Catch::Matchers::WithinAbs;
using namespace std::chrono_literals;

namespace {

constexpr std::size_t kCapacity = PcmInput::kCapacityFrames;

// interleaved stereo frames 1, 2, 3... with the right channel negated, so each frame can be traced
std::vector<float> traced(std::size_t frames, std::size_t first = 1) {
  std::vector<float> samples(frames * 2);
  for (std::size_t i = 0; i < frames; ++i) {
    samples[2 * i] = static_cast<float>(first + i) / 1e6f;
    samples[2 * i + 1] = -samples[2 * i];
  }
  return samples;
}

std::vector<float> readFrames(PcmInput& input, std::size_t frames, std::size_t* got = nullptr) {
  std::vector<float> out(frames * 2, 9.0f);
  const auto read = input.read(out);
  if (got != nullptr)
    *got = read;
  return out;
}

std::vector<float> silence(std::size_t frames) { return std::vector<float>(frames * 2, 0.0f); }

}  // namespace

TEST_CASE("48 kHz stereo is queued as pushed and a read fills silence after it") {
  PcmInput input;
  const auto pushed = traced(100);
  REQUIRE(input.push(std::span<const float>(pushed), 48000, 2));

  std::size_t got = 0;
  const auto out = readFrames(input, 160, &got);
  CHECK(got == 100);
  CHECK(std::vector(out.begin(), out.begin() + 200) == pushed);
  CHECK(std::vector(out.begin() + 200, out.end()) == silence(60));
  CHECK(input.status().framesIn == 100);
}

TEST_CASE("mono goes to both channels and s16 is scaled to -1..1") {
  PcmInput input;
  const std::vector<std::int16_t> mono{16384, -32768, 1};
  REQUIRE(input.push(std::span<const std::int16_t>(mono), 48000, 1));

  CHECK(readFrames(input, 3) == std::vector<float>{0.5f, 0.5f, -1.0f, -1.0f, 1.0f / 32768, 1.0f / 32768});
}

TEST_CASE("another rate is resampled to 48 kHz on the producer's thread, its tone and length kept") {
  PcmInput input;
  const auto second = sine(1000, 0.5, 44100, 2, 44100);
  REQUIRE(input.push(std::span<const float>(second), 44100, 2));

  // less the resampler's input latency, 1.5 ms, which stays in it
  const auto queued = input.status().framesIn;
  CHECK(queued >= 48000 - 128);
  CHECK(queued <= 48000);
  const auto out = readFrames(input, 48000);
  CHECK_THAT(db(amplitudeAt(out, 2, 0, 4800, 38400, 1000) / 0.5), WithinAbs(0, 0.5));
  CHECK_THAT(db(amplitudeAt(out, 2, 1, 4800, 38400, 1000) / 0.5), WithinAbs(0, 0.5));
}

TEST_CASE("a change of format lets the old resampler's tail out before the new audio") {
  PcmInput input;
  // 100 ms at 44.1 kHz
  const auto first = sine(1000, 0.5, 4410, 2, 44100);
  REQUIRE(input.push(std::span<const float>(first), 44100, 2));
  const auto held = input.status().framesIn;
  CHECK(held < 4800 - 32);

  const auto next = traced(480);
  REQUIRE(input.push(std::span<const float>(next), 48000, 2));
  const auto total = input.status().framesIn;
  CHECK(total >= 4800 + 480 - 8);
  CHECK(total <= 4800 + 480 + 8);
  // the new audio follows the whole of the old
  readFrames(input, static_cast<std::size_t>(total) - 480);
  CHECK(readFrames(input, 480) == next);
}

TEST_CASE("finish lets the resampler's tail out at the end of a stream, and the next push starts afresh") {
  PcmInput input;
  const auto second = sine(1000, 0.5, 44100, 2, 44100);
  REQUIRE(input.push(std::span<const float>(second), 44100, 2));
  CHECK(input.status().framesIn <= 48000 - 32);

  REQUIRE(input.finish());
  const auto total = input.status().framesIn;
  CHECK(total >= 48000 - 2);
  CHECK(total <= 48000 + 2);
  // nothing more in it
  REQUIRE(input.finish());
  CHECK(input.status().framesIn == total);

  // at 48 kHz now: copied, and not held back by an old filter
  const auto next = traced(480);
  REQUIRE(input.push(std::span<const float>(next), 48000, 2));
  CHECK(input.status().framesIn == total + 480);
  readFrames(input, static_cast<std::size_t>(total));
  CHECK(readFrames(input, 480) == next);
}

TEST_CASE("finish after a clear has nothing of the old stream to let out") {
  PcmInput input;
  const auto second = sine(1000, 0.5, 4410, 2, 44100);
  REQUIRE(input.push(std::span<const float>(second), 44100, 2));
  const auto held = input.status().framesIn;
  input.clear();
  REQUIRE(input.finish());
  CHECK(input.status().framesIn == held);
  CHECK(input.status().queued == 0);
}

TEST_CASE("framesRead counts what the consumer handed out, nothing a clear dropped") {
  PcmInput input;
  const auto pushed = traced(100);
  REQUIRE(input.push(std::span<const float>(pushed), 48000, 2));
  readFrames(input, 60);
  CHECK(input.status().framesRead == 60);
  readFrames(input, 60);
  CHECK(input.status().framesRead == 100);

  REQUIRE(input.push(std::span<const float>(pushed), 48000, 2));
  input.clear();
  readFrames(input, 60);
  CHECK(input.status().framesRead == 100);
  const auto fresh = traced(20, 1000);
  REQUIRE(input.push(std::span<const float>(fresh), 48000, 2));
  readFrames(input, 60);
  CHECK(input.status().framesRead == 120);
}

TEST_CASE("a format outside 1-2 channels or 8-192 kHz is refused and queues nothing") {
  PcmInput input;
  const auto pushed = traced(10);
  CHECK_FALSE(input.push(std::span<const float>(pushed), 48000, 3));
  CHECK_FALSE(input.push(std::span<const float>(pushed), 7999, 2));
  CHECK_FALSE(input.tryPush(std::span<const float>(pushed), 192001, 2));
  CHECK(input.status().framesIn == 0);
}

TEST_CASE("clear drops what was queued, and the ring reads empty until the next push") {
  PcmInput input;
  const auto old = traced(500);
  REQUIRE(input.push(std::span<const float>(old), 48000, 2));
  input.clear();

  std::size_t got = 1;
  CHECK(readFrames(input, 100, &got) == silence(100));
  CHECK(got == 0);
  CHECK(readFrames(input, 100, &got) == silence(100));

  const auto fresh = traced(50, 1000);
  REQUIRE(input.push(std::span<const float>(fresh), 48000, 2));
  const auto out = readFrames(input, 80, &got);
  CHECK(got == 50);
  CHECK(std::vector(out.begin(), out.begin() + 100) == fresh);
}

TEST_CASE("queued counts what the consumer has still to read, nothing from before a clear") {
  PcmInput input;
  const auto pushed = traced(100);
  REQUIRE(input.push(std::span<const float>(pushed), 48000, 2));
  CHECK(input.status().queued == 100);
  readFrames(input, 60);
  CHECK(input.status().queued == 40);
  readFrames(input, 60);
  CHECK(input.status().queued == 0);

  REQUIRE(input.push(std::span<const float>(pushed), 48000, 2));
  input.clear();
  CHECK(input.status().queued == 0);
  const auto fresh = traced(20, 1000);
  REQUIRE(input.push(std::span<const float>(fresh), 48000, 2));
  CHECK(input.status().queued == 20);
  readFrames(input, 60);
  CHECK(input.status().queued == 0);
}

TEST_CASE("push waits while the ring is full, and a clear lets it go without queueing the rest") {
  PcmInput input;
  const auto full = traced(kCapacity);
  REQUIRE(input.push(std::span<const float>(full), 48000, 2));
  const auto more = traced(4800, kCapacity + 1);

  SECTION("a read makes room") {
    auto pushed = std::async(std::launch::async, [&] { return input.push(std::span<const float>(more), 48000, 2); });
    CHECK(pushed.wait_for(50ms) == std::future_status::timeout);
    readFrames(input, 4800);
    REQUIRE(pushed.wait_for(2s) == std::future_status::ready);
    CHECK(pushed.get());
    CHECK(input.status().framesIn == kCapacity + 4800);
  }

  SECTION("a clear") {
    auto pushed = std::async(std::launch::async, [&] { return input.push(std::span<const float>(more), 48000, 2); });
    CHECK(pushed.wait_for(50ms) == std::future_status::timeout);
    input.clear();
    REQUIRE(pushed.wait_for(2s) == std::future_status::ready);
    CHECK_FALSE(pushed.get());
    std::size_t got = 1;
    readFrames(input, 100, &got);
    CHECK(got == 0);
  }
}

// regression: a group leader's feed is read only while the group plays, so after a pause cleared a full ring the
// resumed source found no room, never queued, and the group never started again
TEST_CASE("after a clear of a full ring, the next push has the whole ring though nothing read since") {
  PcmInput input;
  const auto full = traced(kCapacity);
  REQUIRE(input.push(std::span<const float>(full), 48000, 2));
  input.clear();
  CHECK(input.status().queued == 0);
  const auto next = traced(kCapacity, 7);
  auto pushed = std::async(std::launch::async, [&] { return input.push(std::span<const float>(next), 48000, 2); });
  REQUIRE(pushed.wait_for(2s) == std::future_status::ready);
  CHECK(pushed.get());
  CHECK(input.status().queued == kCapacity);
  CHECK(input.status().dropped == 0);
  // the reader skips to the new audio: none of what the clear dropped is handed out
  std::size_t got = 0;
  const auto out = readFrames(input, 3, &got);
  CHECK(got == 3);
  CHECK(out[0] == next[0]);
  CHECK(out[4] == next[4]);
  // and the ring is full again but for those 3: tryPush finds room for 3 alone
  CHECK_FALSE(input.tryPush(std::span<const float>(traced(10)), 48000, 2));
  CHECK(input.status().dropped == 7);
}

TEST_CASE("tryPush drops what finds no room, counted at 48 kHz") {
  PcmInput input;
  const auto most = traced(kCapacity - 100);
  REQUIRE(input.tryPush(std::span<const float>(most), 48000, 2));

  SECTION("within one conversion step") {
    const auto more = traced(300);
    CHECK_FALSE(input.tryPush(std::span<const float>(more), 48000, 2));
    CHECK(input.status().framesIn == kCapacity);
    CHECK(input.status().dropped == 200);
  }

  SECTION("over several conversion steps") {
    const auto more = traced(3000);
    CHECK_FALSE(input.tryPush(std::span<const float>(more), 48000, 2));
    CHECK(input.status().framesIn == kCapacity);
    CHECK(input.status().dropped == 2900);
  }

  SECTION("resampled") {
    // 3000 frames at 24 kHz are 6000 at 48 kHz, less what the resampler holds back of the first
    const auto more = sine(1000, 0.5, 3000, 2, 24000);
    CHECK_FALSE(input.tryPush(std::span<const float>(more), 24000, 2));
    const auto status = input.status();
    CHECK(status.framesIn == kCapacity);
    CHECK(status.dropped + 100 >= 6000 - 160);
    CHECK(status.dropped + 100 <= 6000);
  }
}

TEST_CASE("a read that comes up short is an underrun only while a producer is attached") {
  PcmInput input;
  readFrames(input, 10);
  CHECK(input.status().underruns == 0);
  input.attach();
  readFrames(input, 10);
  CHECK(input.status().underruns == 1);
  const auto enough = traced(10);
  REQUIRE(input.push(std::span<const float>(enough), 48000, 2));
  readFrames(input, 10);
  CHECK(input.status().underruns == 1);
  input.detach();
  readFrames(input, 10);
  CHECK(input.status().underruns == 1);
}

TEST_CASE("a clear racing the producer never lets audio of a push finished before it be read") {
  PcmInput input;
  // every sample of a push carries the push's number
  std::atomic<int> finished{0};
  std::atomic<bool> done{false};
  std::thread producer([&] {
    std::vector<float> block(480 * 2);
    for (int number = 1; !done; ++number) {
      std::fill(block.begin(), block.end(), static_cast<float>(number));
      input.push(std::span<const float>(block), 48000, 2);
      finished = number;
    }
  });

  std::vector<float> period(480 * 2);
  float stale = 0;
  std::size_t reads = 0;
  bool clean = true;
  for (int round = 0; round < 400; ++round) {
    if (round % 8 == 0) {
      stale = static_cast<float>(finished.load());
      input.clear();
    }
    reads += input.read(period);
    clean = clean && std::all_of(period.begin(), period.end(), [&](float sample) { return sample == 0 || sample > stale; });
    std::this_thread::sleep_for(50us);
  }
  done = true;
  input.clear();
  producer.join();
  CHECK(clean);
  CHECK(reads > 0);
}

TEST_CASE("steering a resampled stream queues as many ppm more or less, and a clear ends it") {
  PcmInput input;
  input.steer(-2000);
  CHECK(input.steering() == -2000);
  const auto second = sine(1000, 0.5, 44100, 2, 44100);
  REQUIRE(input.push(std::span<const float>(second), 44100, 2));
  const auto slower = input.status().framesIn;
  // 2000 ppm slower: 96 frames more of the second
  CHECK(slower >= 48000 + 96 - 128);
  CHECK(slower <= 48000 + 96);
  input.clear();
  REQUIRE(input.push(std::span<const float>(second), 44100, 2));
  CHECK(input.steering() == 0);
}

TEST_CASE("48 kHz is copied and never steered, unless the input always resamples") {
  const auto second = sine(1000, 0.5, 48000, 2, 48000);
  PcmInput copied;
  copied.steer(-2000);
  REQUIRE(copied.push(std::span<const float>(second), 48000, 2));
  CHECK(copied.status().framesIn == 48000);
  CHECK(copied.heldFrames() == 0);

  PcmInput forced;
  forced.setAlwaysResample(true);
  forced.steer(-2000);
  REQUIRE(forced.push(std::span<const float>(second), 48000, 2));
  // 2000 ppm slower: 96 frames more, less what the filter still holds
  const auto held = forced.heldFrames();
  CHECK(held > 0);
  CHECK(held < 200);
  const auto in = forced.status().framesIn;
  CHECK(in + held >= 48000 + 96 - 2);
  CHECK(in + held <= 48000 + 96 + 2);
  // a steer asked for from another thread is applied at the next push
  forced.requestSteer(1000);
  REQUIRE(forced.push(std::span<const float>(second), 48000, 2));
  CHECK(forced.steering() == 1000);
}

TEST_CASE("forced through the resampler, a frame lands held frames after the write position") {
  PcmInput input;
  input.setAlwaysResample(true);
  // a click at frame 1000 of a silent stream: the first push makes the filter
  std::vector<float> pcm(4800 * 2, 0.0f);
  REQUIRE(input.push(std::span<const float>(pcm.data(), 1000 * 2), 48000, 2));
  const auto at = input.status().writePos + input.heldFrames();
  pcm[0] = 1.0f;
  pcm[1] = 1.0f;
  REQUIRE(input.push(std::span<const float>(pcm), 48000, 2));
  std::vector<float> out(6000 * 2);
  const auto got = input.read(out);
  REQUIRE(got > at);
  std::size_t peak = 0;
  for (std::size_t i = 0; i < got; ++i)
    if (std::abs(out[2 * i]) > std::abs(out[2 * peak]))
      peak = i;
  CHECK(peak == at);
}

TEST_CASE("steered, a frame still lands held frames after the write position") {
  const std::int32_t first = GENERATE(0, -80, 300);
  const std::int32_t ppm = GENERATE(-80, 5, 25, 300);
  PcmInput input;
  input.setAlwaysResample(true);
  input.steer(first);
  std::vector<float> pcm(960 * 2, 0.0f);
  std::vector<float> out(48000 * 2);
  // a second of silence in 20 ms pushes, read as it goes, the steering flipping from halfway
  for (int i = 0; i < 50; ++i) {
    // halfway it starts flipping between the two, a push each
    if (i >= 25)
      input.steer(i % 2 == 0 ? ppm : first);
    REQUIRE(input.push(std::span<const float>(pcm), 48000, 2));
    input.read(std::span<float>(out.data(), 960 * 2));
  }
  const auto at = static_cast<long long>(std::llround(static_cast<double>(input.status().writePos) + input.pendingFrames()));
  const auto read = input.readPosition();
  pcm[0] = 1.0f;
  pcm[1] = 1.0f;
  REQUIRE(input.push(std::span<const float>(pcm), 48000, 2));
  pcm[0] = pcm[1] = 0.0f;
  REQUIRE(input.push(std::span<const float>(pcm), 48000, 2));
  const auto got = input.read(out);
  std::size_t peak = 0;
  for (std::size_t i = 0; i < got; ++i)
    if (std::abs(out[2 * i]) > std::abs(out[2 * peak]))
      peak = i;
  INFO("ppm " << first << " then " << ppm << " peak " << read + peak << " predicted " << at);
  CHECK(std::llabs(static_cast<long long>(read + peak) - at) <= 1);
}

TEST_CASE("the status carries the ring's write and read positions") {
  PcmInput input;
  const auto pcm = traced(480);
  REQUIRE(input.push(std::span<const float>(pcm), 48000, 2));
  readFrames(input, 100);
  const auto status = input.status();
  CHECK(status.writePos == 480);
  CHECK(status.readPos == 100);
  CHECK(input.readPosition() == 100);
}
