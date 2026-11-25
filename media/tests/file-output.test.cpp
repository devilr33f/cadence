#include "file/file-output.h"
#include "playback/alsa-player.h"
#include "support/eventually.h"
#include "support/fake-clock.h"
#include "support/samples.h"
#include "support/temp-dir.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <chrono>
#include <cstdint>
#include <vector>

using namespace cadence;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

constexpr PlaybackFormat kFormat{.sampleRate = 48000,
                                 .channels = 2,
                                 .sampleFormat = SampleFormat::S16Le,
                                 .periodFrames = 480,
                                 .bufferFrames = 1920,
                                 .fillFrames = 1440};

std::vector<std::int16_t> readBack(const std::string& path) {
  const auto reader = WavReader::open(path);
  REQUIRE(reader.ok());
  CHECK(reader.value().format().sampleRate == 48000);
  CHECK(reader.value().format().channels == 2);
  return reader.value().samples();
}

}  // namespace

TEST_CASE("file output: the file holds exactly the samples written", "[file-output]") {
  TempDir dir;
  FakeFileClock clock;
  FileOutput out(dir.str(), "out", clock);
  REQUIRE(out.open(kFormat).ok());

  const auto first = ramp(480 * 2, 1);
  const auto second = ramp(240 * 2, 5000);
  CHECK(out.write(first, 0ms).frames == 480);
  CHECK(out.write(second, 0ms).frames == 240);
  out.close();

  auto expected = first;
  expected.insert(expected.end(), second.begin(), second.end());
  REQUIRE(out.files().size() == 1);
  CHECK(readBack(out.files()[0]) == expected);
}

TEST_CASE("file output: each open is its own file", "[file-output]") {
  TempDir dir;
  FakeFileClock clock;
  FileOutput out(dir.str(), "reply", clock);
  REQUIRE(out.open(kFormat).ok());
  CHECK(out.write(constant(960, 7), 0ms).frames == 480);
  REQUIRE(out.open(kFormat).ok());
  CHECK(out.write(constant(960, 9), 0ms).frames == 480);
  out.close();

  REQUIRE(out.files().size() == 2);
  CHECK(out.files()[0].ends_with("reply-000.wav"));
  CHECK(out.files()[1].ends_with("reply-001.wav"));
  CHECK(readBack(out.files()[0]) == constant(960, 7));
  CHECK(readBack(out.files()[1]) == constant(960, 9));
}

TEST_CASE("file output: the device buffer fills to the cap and drains at the stream's rate", "[file-output]") {
  TempDir dir;
  FakeFileClock clock;
  FileOutput out(dir.str(), "out", clock);
  REQUIRE(out.open(kFormat).ok());

  const auto audio = constant(480 * 2 * 4, 3);
  const auto filled = out.write(audio, 0ms);
  CHECK(filled.frames == 1440);
  CHECK(out.write(audio, 0ms).kind == PlaybackWrite::Kind::Idle);
  REQUIRE(out.delayFrames().ok());
  CHECK(out.delayFrames().value() == 1440);

  clock.advance(10ms);
  CHECK(out.delayFrames().value() == 1440 - 480);

  // a write with a timeout waits until a period has played
  const auto before = clock.now();
  const auto more = out.write(audio, 100ms);
  CHECK(more.kind == PlaybackWrite::Kind::Frames);
  CHECK(more.frames >= 480);
  CHECK(clock.now() - before <= 10ms);

  clock.advance(1s);
  CHECK(out.delayFrames().value() == 0);
}

TEST_CASE("file output: rewind takes back what has not played, close plays out the rest", "[file-output]") {
  TempDir dir;
  FakeFileClock clock;
  FileOutput out(dir.str(), "out", clock);
  REQUIRE(out.open(kFormat).ok());

  REQUIRE(out.write(constant(960 * 2, 1), 0ms).frames == 960);
  clock.advance(10ms);
  const auto rewound = out.rewind();
  REQUIRE(rewound.ok());
  CHECK(rewound.value() == 480);
  CHECK(out.delayFrames().value() == 0);

  REQUIRE(out.write(constant(240 * 2, 2), 0ms).frames == 240);
  CHECK_FALSE(out.drop());
  out.close();

  auto expected = constant(480 * 2, 1);
  const auto tail = constant(240 * 2, 2);
  expected.insert(expected.end(), tail.begin(), tail.end());
  CHECK(readBack(out.files()[0]) == expected);
}

TEST_CASE("file output: refuses unwritable formats and use before open", "[file-output]") {
  TempDir dir;
  FakeFileClock clock;
  FileOutput out(dir.str(), "out", clock);
  CHECK(out.write(constant(8, 1), 0ms).kind == PlaybackWrite::Kind::Failed);
  CHECK(out.rewind().error().code == PlaybackErrorCode::NotOpen);
  CHECK(out.delayFrames().error().code == PlaybackErrorCode::NotOpen);
  CHECK(out.drop()->code == PlaybackErrorCode::NotOpen);

  auto unsupported = kFormat;
  unsupported.sampleFormat = SampleFormat::Unsupported;
  CHECK(out.open(unsupported).error().code == PlaybackErrorCode::FormatMismatch);

  auto sixChannels = kFormat;
  sixChannels.channels = 6;
  CHECK(out.open(sixChannels).error().code == PlaybackErrorCode::FormatMismatch);

  FileOutput missing(dir.file("nope"), "out", clock);
  CHECK(missing.open(kFormat).error().code == PlaybackErrorCode::OpenFailed);
}

TEST_CASE("file output: the player plays a response into the file", "[file-output]") {
  TempDir dir;
  FileOutput out(dir.str());
  AlsaPlayer player(out, {.periodFrames = 480, .bufferFrames = 1920, .maxDeviceFillFrames = 1440, .jitter = {}});
  REQUIRE(player.start().ok());

  // above the 100 ms start target, so the response plays at once
  const auto pcm = ramp(4800, 1);
  REQUIRE(player.begin("r1"));
  REQUIRE(player.push("r1", pcm) == PushResult::Accepted);
  REQUIRE(player.end("r1"));
  REQUIRE(player.watchCompletion("r1"));
  REQUIRE(eventually([&] { return player.idle(); }));
  player.stop();

  REQUIRE(out.files().size() == 1);
  const auto heard = readBack(out.files()[0]);
  std::vector<std::int16_t> left;
  for (std::size_t i = 0; i + 1 < heard.size(); i += 2) {
    CHECK(heard[i] == heard[i + 1]);
    left.push_back(heard[i]);
  }
  CHECK(audible(left) == pcm);
}

TEST_CASE("file output: dropping silence keeps only the stretches that sound", "[file-output]") {
  TempDir dir;
  FakeFileClock clock;
  FileOutput out(dir.str(), "out", clock, FileSilence::Drop);
  REQUIRE(out.open(kFormat).ok());
  const auto silence = constant(480 * 2, 0);
  const auto sound = ramp(480 * 2, 1);

  // each write first plays out the 10 ms written before it
  CHECK(out.write(silence, 0ms).frames == 480);
  clock.advance(10ms);
  CHECK(out.write(sound, 0ms).frames == 480);
  clock.advance(10ms);
  CHECK(out.write(silence, 0ms).frames == 480);
  out.close();

  CHECK(readBack(out.files()[0]) == sound);
}
