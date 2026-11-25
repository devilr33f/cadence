#include "backend/pcm-input.h"
#include "file/file-output.h"
#include "file/wav.h"
#include "playback/alsa-player.h"
#include "support/eventually.h"
#include "support/fake-sink.h"
#include "support/player-harness.h"
#include "support/samples.h"
#include "support/signals.h"
#include "support/temp-dir.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/matchers/catch_matchers_floating_point.hpp>

#include <algorithm>
#include <atomic>
#include <chrono>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <numbers>
#include <span>
#include <string>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::fake_sink;
using namespace cadence::test_support;
using Catch::Matchers::WithinAbs;
using namespace std::chrono_literals;

namespace {

std::vector<std::int16_t> after(const std::vector<std::int16_t>& samples, std::size_t from) {
  return {samples.begin() + static_cast<std::ptrdiff_t>(from), samples.end()};
}

std::size_t risingCrossings(const std::vector<std::int16_t>& left, std::size_t first, std::size_t frames) {
  std::size_t crossings = 0;
  for (std::size_t i = first + 1; i < first + frames; ++i)
    if (left[i - 1] <= 0 && left[i] > 0)
      ++crossings;
  return crossings;
}

}  // namespace

TEST_CASE("MusicPlaysThroughChainToFile") {
  TempDir dir;
  FileOutput out(dir.str());
  PcmInput music;
  AlsaPlayer player(out, kConfig, {.music = &music});

  // 1 s of a 1 kHz tone at 44.1 kHz stereo, as a decoder hands it over; queued before the player starts, so a slow
  // push cannot leave a gap in it
  const auto tone = sine(1000, 0.5, 44100, 2, 44100);
  REQUIRE(music.push(std::span<const float>(tone), 44100, 2));
  player.setGateOpen(true);
  REQUIRE(player.start().ok());
  const auto before = music.status().underruns;
  music.attach();
  REQUIRE(eventually([&] { return music.status().underruns > before; }));
  // what the device and the look-ahead still hold plays out
  std::this_thread::sleep_for(100ms);
  player.stop();

  REQUIRE(out.files().size() == 1);
  auto wav = WavReader::open(out.files()[0]);
  REQUIRE(wav.ok());
  CHECK(wav.value().format().sampleRate == 48000);
  REQUIRE(wav.value().format().channels == 2);
  const auto& samples = wav.value().samples();
  const auto left = channel(samples, 0);
  // the silence in a period that also carries music is dithered to +-1
  const auto sounding = [](std::int16_t s) { return s > 1 || s < -1; };
  const auto first = static_cast<std::size_t>(std::find_if(left.begin(), left.end(), sounding) - left.begin());
  const auto last = static_cast<std::size_t>(left.rend() - std::find_if(left.rbegin(), left.rend(), sounding));
  // the resampler's input latency, 1.5 ms, stays in it at the end
  CHECK(last - first >= 48000 - 128);
  CHECK(last - first <= 48000 + 64);

  // 0.6 s away from both ends: 600 cycles, at the level pushed on both channels
  const std::size_t from = first + 9600;
  CHECK(risingCrossings(left, from, 28800) >= 599);
  CHECK(risingCrossings(left, from, 28800) <= 601);
  CHECK_THAT(db(amplitudeAt(samples, 2, 0, from, 28800, 1000) / 32768 / 0.5), WithinAbs(0, 0.5));
  CHECK_THAT(db(amplitudeAt(samples, 2, 1, from, 28800, 1000) / 32768 / 0.5), WithinAbs(0, 0.5));
}

TEST_CASE("CancelLeavesNoVoiceTail") {
  FakePcmSink sink;
  AlsaPlayer player(sink, kConfig);
  REQUIRE(player.begin("r1"));
  REQUIRE(player.push("r1", constant(kTarget * 2, 1000)) == PushResult::Accepted);
  REQUIRE(player.start().ok());
  REQUIRE(fillsWith(sink, 1000));

  SECTION("a cancel") { player.cancel("r1"); }
  SECTION("a newer response") { REQUIRE(player.begin("r2")); }

  // the discard is done once the call returns, so everything written from here on came after it
  const auto cut = sink.written().size();
  listen(sink, kFill * 3);
  CHECK(silent(after(sink.written(), cut)));
}

TEST_CASE("CompletionWatchAccountsLatency") {
  FakePcmSink sink;
  AlsaPlayer player(sink, kConfig);
  std::atomic<int> successes = 0;
  std::atomic<int> failures = 0;
  player.onCompletion([&](const std::string&, bool success) { ++(success ? successes : failures); });
  REQUIRE(player.start().ok());
  REQUIRE(player.begin("r1"));
  REQUIRE(player.push("r1", constant(kTarget, 1000)) == PushResult::Accepted);
  REQUIRE(player.end("r1"));
  REQUIRE(player.watchCompletion("r1"));

  // the speaker plays a period at a time until the response's last frame is in the device
  std::size_t played = 0;
  REQUIRE(eventually([&] {
    if (audible(sink.written()).size() == kTarget * 2)
      return true;
    played += sink.play(kPeriod).size() / 2;
    return false;
  }));
  const auto left = channel(sink.written(), 0);
  const auto last = static_cast<std::size_t>(left.rend() - std::find_if(left.rbegin(), left.rend(), [](std::int16_t s) { return s != 0; })) - 1;
  REQUIRE(played <= last);

  // every frame up to the last one played: nothing settles, and the player is not idle
  played += sink.play(last - played).size() / 2;
  REQUIRE(played == last);
  std::this_thread::sleep_for(30ms);
  CHECK(successes == 0);
  CHECK(failures == 0);
  CHECK_FALSE(player.idle());

  sink.play(1);
  CHECK(eventually([&] { return successes == 1; }));
  CHECK(eventually([&] { return player.idle(); }));
  CHECK(failures == 0);
}

TEST_CASE("a sounding tone and a response still buffering each duck the music") {
  FakePcmSink sink;
  PcmInput music;
  AlsaPlayer player(sink, kConfig, {.music = &music});
  REQUIRE(player.start().ok());
  player.setGateOpen(true);
  // 1 kHz at 0.25 for longer than the test listens
  const auto tone = sine(1000, 0.25, 48000, 2);
  REQUIRE(music.push(std::span<const float>(tone), 48000, 2));
  // the 1 kHz level over the last 100 ms heard, so a 300 Hz tone cancels out
  const auto level = [](const std::vector<std::int16_t>& heard) {
    return amplitudeAt(heard, 2, 0, heard.size() / 2 - 4800, 4800, 1000) / 32768;
  };
  CHECK_THAT(db(level(listen(sink, 9600)) / 0.25), WithinAbs(0, 0.5));

  SECTION("an earcon on the tone source") {
    std::vector<std::int16_t> earcon(72000);
    for (std::size_t i = 0; i < earcon.size(); ++i)
      earcon[i] = static_cast<std::int16_t>(3000 * std::sin(2 * std::numbers::pi * 300 * static_cast<double>(i) / 48000));
    REQUIRE(player.playTone("t1", earcon) == PushResult::Accepted);
  }
  SECTION("a response below its start target, so nothing of it sounds yet") {
    REQUIRE(player.begin("r1"));
    REQUIRE(player.push("r1", constant(kTarget / 4, 1000)) == PushResult::Accepted);
  }
  SECTION("a ring looping on the tone source, through its silent stretches too") {
    // 100 ms of a 300 Hz tone, then 100 ms of silence
    std::vector<std::int16_t> ring(9600);
    for (std::size_t i = 0; i < 4800; ++i)
      ring[i] = static_cast<std::int16_t>(3000 * std::sin(2 * std::numbers::pi * 300 * static_cast<double>(i) / 48000));
    player.loopTone(ring);
  }

  // past the device's fill, the look-ahead and the 200 ms ramp: down to 0.15
  CHECK_THAT(db(level(listen(sink, 24000)) / 0.25), WithinAbs(db(0.15), 1));
}

TEST_CASE("shutting the gate takes the music back from the device at once") {
  FakePcmSink sink;
  PcmInput music;
  AlsaPlayer player(sink, kConfig, {.music = &music});
  REQUIRE(player.start().ok());
  player.setGateOpen(true);
  const auto tone = sine(1000, 0.25, 48000, 2);
  REQUIRE(music.push(std::span<const float>(tone), 48000, 2));
  // what was written before the music plays out, and the device fills with music
  listen(sink, kFill + kPeriod + kLatency);
  REQUIRE(eventually([&] { return sink.bufferedFrames() >= kFill; }));
  // a sine's dithered zero crossings are often exact zeros
  REQUIRE(audible(sink.buffered()).size() > kFill);

  player.setGateOpen(false);

  CHECK(sink.calls() == "rd");
  expectAtMostOneStalePeriod(sink);
  const auto cut = sink.written().size();
  listen(sink, kFill * 3);
  CHECK(silent(after(sink.written(), cut)));
}
