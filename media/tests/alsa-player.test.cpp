#include "dsp/limiter.h"
#include "playback/alsa-player.h"
#include "support/eventually.h"
#include "support/fake-sink.h"
#include "support/player-harness.h"
#include "support/samples.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>

#include <atomic>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <string>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::fake_sink;
using namespace cadence::test_support;
using namespace std::chrono_literals;

TEST_CASE("plays every s16 value up to the limiter's ceiling on both channels unchanged, after the look-ahead") {
  constexpr int kCeiling = static_cast<int>(dsp::kLimiterCeiling * 32768);
  constexpr std::size_t kValues = 2 * kCeiling + 1;
  std::vector<std::int16_t> every(kValues);
  for (std::size_t index = 0; index < kValues; ++index)
    every[index] = static_cast<std::int16_t>(static_cast<int>(index) - kCeiling);
  auto config = kConfig;
  config.jitter.maxSamples = kValues + kTarget * 2;
  FakePcmSink sink;
  AlsaPlayer player(sink, config);
  REQUIRE(player.begin("r1"));
  REQUIRE(player.push("r1", every) == PushResult::Accepted);
  REQUIRE(player.end("r1"));
  REQUIRE(player.start().ok());

  const auto heard = listen(sink, kLatency + kValues + kFill * 2);

  const auto left = channel(heard, 0);
  CHECK(left == channel(heard, 1));
  CHECK(silent(std::vector(left.begin(), left.begin() + kLatency)));
  CHECK(std::vector(left.begin() + kLatency, left.begin() + kLatency + kValues) == every);
  CHECK(silent(std::vector(left.begin() + kLatency + kValues, left.end())));
  CHECK(eventually([&] { return player.status().queue.drained == 1; }));
}

TEST_CASE("cancel drops the response's queued audio and audio the device holds before it returns") {
  const bool byId = GENERATE(true, false);
  FakePcmSink sink;
  AlsaPlayer player(sink, kConfig);
  REQUIRE(player.begin("r1"));
  REQUIRE(player.push("r1", constant(kTarget * 2, 1000)) == PushResult::Accepted);
  REQUIRE(player.start().ok());
  REQUIRE(fillsWith(sink, 1000));

  // a cancel for a response that is not active leaves the device alone
  player.cancel("r0");
  CHECK(sink.calls().empty());

  if (byId)
    player.cancel("r1");
  else
    player.cancel();

  CHECK(sink.calls() == "rd");
  CHECK(player.status().rewoundFrames == kFill - kPeriod);
  expectAtMostOneStalePeriod(sink);
  expectSilenceAfterOnePeriod(sink);
  CHECK(player.push("r1", constant(960, 1000)) == PushResult::Stale);
  CHECK_FALSE(player.begin("r1"));

  REQUIRE(player.begin("r2"));
  REQUIRE(player.push("r2", constant(kTarget, 2000)) == PushResult::Accepted);
  REQUIRE(player.end("r2"));
  const auto heard = audible(listen(sink, kTarget + kFill * 2));
  CHECK(heard.size() == kTarget * 2);
  CHECK(all(heard, 2000));
}

TEST_CASE("a newer response replaces one that has not ended, including audio the device holds") {
  FakePcmSink sink;
  AlsaPlayer player(sink, kConfig);
  REQUIRE(player.begin("r1"));
  REQUIRE(player.push("r1", constant(kTarget * 2, 1000)) == PushResult::Accepted);
  REQUIRE(player.start().ok());
  REQUIRE(fillsWith(sink, 1000));

  REQUIRE(player.begin("r2"));

  CHECK(sink.calls() == "rd");
  CHECK(player.status().rewoundFrames == kFill - kPeriod);
  expectAtMostOneStalePeriod(sink);
  // the period dmix is already playing
  CHECK(all(listen(sink, kPeriod), 1000));
  REQUIRE(player.push("r2", constant(kTarget, 2000)) == PushResult::Accepted);
  REQUIRE(player.end("r2"));
  const auto heard = audible(listen(sink, kTarget + kFill * 2));
  CHECK(heard.size() == kTarget * 2);
  CHECK(all(heard, 2000));
}

TEST_CASE("control calls from other threads race safely with playback") {
  FakePcmSink sink;
  AlsaPlayer player(sink, kConfig);
  REQUIRE(player.start().ok());
  std::atomic<bool> done = false;

  std::thread speaker([&] {
    while (!done) {
      sink.play(240);
      std::this_thread::sleep_for(1ms);
    }
  });
  std::thread responses([&] {
    for (int response = 0; response < 40; ++response) {
      const auto id = "s" + std::to_string(response);
      player.begin(id);
      for (int packet = 0; packet < 6; ++packet)
        player.push(id, constant(960, static_cast<std::int16_t>(response + 1)));
      player.end(id);
    }
  });
  std::thread interrupts([&] {
    for (int round = 0; round < 40; ++round) {
      if (round % 3 == 0)
        player.cancel();
      std::this_thread::sleep_for(1ms);
    }
  });
  responses.join();
  interrupts.join();

  player.cancel();
  expectAtMostOneStalePeriod(sink);
  done = true;
  speaker.join();
  CHECK(silent(listen(sink, kFill * 2)));
}

TEST_CASE("a looped tone repeats its period exactly in the device until stopped, and the stop takes it back") {
  FakePcmSink sink;
  AlsaPlayer player(sink, kConfig);
  // every sample traceable, so a gap or a repeat at the seam shows
  const auto period = ramp(1000);
  player.loopTone(period);
  REQUIRE(player.start().ok());

  const auto heard = listen(sink, kLatency + 3 * period.size() + 37);
  const auto left = channel(heard, 0);
  CHECK(left == channel(heard, 1));
  CHECK(silent(std::vector(left.begin(), left.begin() + kLatency)));
  std::vector<std::int16_t> expected;
  for (std::size_t i = 0; i < left.size() - kLatency; ++i)
    expected.push_back(period[i % period.size()]);
  CHECK(std::vector(left.begin() + kLatency, left.end()) == expected);
  REQUIRE(eventually([&] { return sink.bufferedFrames() >= kFill; }));

  CHECK(player.stopLoop());

  CHECK(sink.calls() == "rd");
  expectAtMostOneStalePeriod(sink);
  expectSilenceAfterOnePeriod(sink);
  CHECK_FALSE(player.stopLoop());
}

TEST_CASE("cancelling the tones stops a loop too") {
  FakePcmSink sink;
  AlsaPlayer player(sink, kConfig);
  player.loopTone(constant(500, 700));
  REQUIRE(player.start().ok());
  REQUIRE(fillsWith(sink, 700));

  player.cancelTones();

  CHECK(sink.calls() == "rd");
  expectAtMostOneStalePeriod(sink);
  expectSilenceAfterOnePeriod(sink);
  CHECK_FALSE(player.stopLoop());
}
