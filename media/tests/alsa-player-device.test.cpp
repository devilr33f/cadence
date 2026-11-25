#include "playback/alsa-player.h"
#include "support/eventually.h"
#include "support/fake-sink.h"
#include "support/player-harness.h"
#include "support/samples.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>
#include <catch2/matchers/catch_matchers_string.hpp>

#include <algorithm>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <functional>
#include <string>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::fake_sink;
using namespace cadence::test_support;
using Catch::Matchers::ContainsSubstring;
using namespace std::chrono_literals;

TEST_CASE("requests exactly 48 kHz stereo s16 playback with the configured period, buffer, and fill") {
  FakePcmSink sink;
  AlsaPlayer player(sink, kConfig);

  const auto started = player.start();

  REQUIRE(started.ok());
  REQUIRE(sink.requested.has_value());
  CHECK(sink.requested->sampleRate == 48000);
  CHECK(sink.requested->channels == 2);
  CHECK(sink.requested->sampleFormat == SampleFormat::S16Le);
  CHECK(sink.requested->periodFrames == 480);
  CHECK(sink.requested->bufferFrames == 1920);
  CHECK(sink.requested->fillFrames == 1440);
  CHECK(started.value().fillFrames == 1440);
  CHECK(player.status().running);
}

TEST_CASE("refuses a device that grants other than exactly 48 kHz stereo s16, and closes it") {
  FakePcmSink sink;
  sink.grant = PlaybackFormat{44100, 2, SampleFormat::S16Le, 480, 1920};
  AlsaPlayer player(sink, kConfig);

  const auto started = player.start();

  REQUIRE_FALSE(started.ok());
  CHECK(started.error().code == PlaybackErrorCode::FormatMismatch);
  CHECK(describe(started.error()).size() <= 256);
  CHECK(sink.closes == 1);
  CHECK_FALSE(player.status().running);
}

TEST_CASE("reports a playback device that fails to open, and refuses to start twice") {
  FakePcmSink broken;
  broken.openError = PlaybackError{PlaybackErrorCode::OpenFailed, "default: no such device"};
  AlsaPlayer unopened(broken, kConfig);

  const auto failed = unopened.start();

  REQUIRE_FALSE(failed.ok());
  CHECK(failed.error().code == PlaybackErrorCode::OpenFailed);
  CHECK_THAT(describe(failed.error()), ContainsSubstring("default: no such device"));
  CHECK_FALSE(unopened.status().running);

  FakePcmSink sink;
  AlsaPlayer player(sink, kConfig);
  REQUIRE(player.start().ok());

  const auto again = player.start();

  REQUIRE_FALSE(again.ok());
  CHECK(again.error().code == PlaybackErrorCode::AlreadyRunning);
  CHECK(sink.opens == 1);
}

TEST_CASE("keeps no more than the fill cap in the device, so audio pushed to a full device is heard within the cap") {
  FakePcmSink sink;
  AlsaPlayer player(sink, kConfig);
  REQUIRE(player.start().ok());
  REQUIRE(eventually([&] { return sink.bufferedFrames() >= kFill; }));
  // gives a player that ignored the cap time to overfill
  std::this_thread::sleep_for(20ms);
  CHECK(sink.peakFrames() == kFill);

  REQUIRE(player.begin("r1"));
  REQUIRE(player.push("r1", constant(kTarget, 1000)) == PushResult::Accepted);
  REQUIRE(player.end("r1"));

  // the period read just before the push is still on its way, and the look-ahead delays the response behind it
  const auto left = channel(listen(sink, kFill + kPeriod + kLatency + kTarget), 0);
  const auto first = std::find_if(left.begin(), left.end(), [](std::int16_t sample) { return sample != 0; });
  CHECK(static_cast<std::size_t>(first - left.begin()) <= kFill + kPeriod + kLatency);
  CHECK(audible(left) == constant(kTarget, 1000));
  CHECK(sink.peakFrames() == kFill);
}

TEST_CASE("a device underrun restarts the stream and never replays audio") {
  FakePcmSink sink;
  AlsaPlayer player(sink, kConfig);
  REQUIRE(player.begin("r1"));
  REQUIRE(player.push("r1", ramp(kTarget * 2)) == PushResult::Accepted);
  REQUIRE(player.end("r1"));
  REQUIRE(player.start().ok());
  REQUIRE(eventually([&] { return sink.bufferedFrames() >= kFill; }));

  // the fake loses what its buffer held, as a device that ran dry has nothing left to play
  sink.pushUnderrun();
  const auto heard = channel(audible(listen(sink, kTarget * 2 + kFill)), 0);

  REQUIRE_FALSE(heard.empty());
  CHECK(std::adjacent_find(heard.begin(), heard.end(), std::greater_equal<>()) == heard.end());
  CHECK(heard.back() == static_cast<std::int16_t>(kTarget * 2));
  const auto written = channel(audible(sink.written()), 0);
  CHECK(written == ramp(kTarget * 2));
  CHECK(player.status().recoveries == 1);
}

// regression: a stream that kept failing left the daemon deaf or mute (0e87461)
TEST_CASE("failed writes are reported, timed from the first failure until a write succeeds, and playback carries on") {
  FakePcmSink sink;
  sink.pushFailure();
  sink.pushFailure();
  AlsaPlayer player(sink, kConfig);
  REQUIRE(player.begin("r1"));
  REQUIRE(player.push("r1", ramp(kTarget)) == PushResult::Accepted);
  REQUIRE(player.end("r1"));
  CHECK_FALSE(player.status().failingSince);
  REQUIRE(player.start().ok());

  REQUIRE(eventually([&] { return player.status().writeErrors >= 1; }));
  const auto first = player.status().failingSince;
  REQUIRE(first);
  REQUIRE(eventually([&] { return player.status().writeErrors == 2; }));
  CHECK(player.status().failingSince == first);
  REQUIRE(player.status().lastError.has_value());
  CHECK(player.status().lastError->code == PlaybackErrorCode::WriteFailed);

  CHECK(eventually([&] { return !player.status().failingSince; }));
  CHECK(channel(audible(listen(sink, kTarget + kFill)), 0) == ramp(kTarget));
  CHECK(eventually([&] { return player.status().frames >= kTarget; }));
}

TEST_CASE("stop drops everything queued and a restart plays only newer responses") {
  FakePcmSink sink;
  AlsaPlayer player(sink, kConfig);
  REQUIRE(player.begin("r1"));
  REQUIRE(player.push("r1", constant(kTarget * 2, 1000)) == PushResult::Accepted);
  REQUIRE(player.start().ok());
  REQUIRE(fillsWith(sink, 1000));

  player.stop();

  CHECK(sink.calls() == "rdc");
  CHECK(sink.bufferedFrames() <= kPeriod);
  CHECK(player.status().rewoundFrames == kFill - kPeriod);
  CHECK(sink.closes == 1);
  CHECK_FALSE(player.status().running);
  CHECK_FALSE(player.status().queue.activeResponse.has_value());
  // dmix plays out the period the hardware was on
  sink.play(kPeriod);
  CHECK(player.push("r1", constant(960, 1000)) == PushResult::Stale);

  REQUIRE(player.start().ok());
  CHECK(sink.opens == 2);
  REQUIRE(player.begin("r2"));
  REQUIRE(player.push("r2", constant(kTarget, 2000)) == PushResult::Accepted);
  REQUIRE(player.end("r2"));
  const auto heard = audible(listen(sink, kTarget + kFill * 2));
  CHECK(heard.size() == kTarget * 2);
  CHECK(all(heard, 2000));
}
