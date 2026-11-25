#include "playback/alsa-playback.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/matchers/catch_matchers_string.hpp>

#include <chrono>
#include <cstdint>
#include <string>
#include <vector>

using namespace cadence;
using Catch::Matchers::ContainsSubstring;
using namespace std::chrono_literals;

TEST_CASE("plays to alsa's null device at exactly 48 kHz stereo s16, writing no more than the fill at once") {
  AlsaPlayback playback("null");

  const auto granted = playback.open(PlaybackFormat{48000, 2, SampleFormat::S16Le, 960, 7680, 2880});

  REQUIRE(granted.ok());
  CHECK(granted.value().sampleRate == 48000);
  CHECK(granted.value().channels == 2);
  CHECK(granted.value().sampleFormat == SampleFormat::S16Le);
  CHECK(granted.value().periodFrames > 0);
  CHECK(granted.value().bufferFrames >= granted.value().periodFrames);
  CHECK(granted.value().fillFrames == 2880);

  const std::vector<std::int16_t> silence(7680 * 2, 0);
  const auto first = playback.write(silence, 100ms);
  CHECK(first.kind == PlaybackWrite::Kind::Frames);
  CHECK(first.frames > 0);
  CHECK(first.frames <= 2880);
  CHECK(playback.rewind().ok());
  CHECK_FALSE(playback.drop().has_value());
  CHECK(playback.write(silence, 100ms).kind == PlaybackWrite::Kind::Frames);
  playback.close();
}

TEST_CASE("using playback that is not open fails with a named error") {
  AlsaPlayback playback("null");
  const std::vector<std::int16_t> silence(960 * 2, 0);

  const auto write = playback.write(silence, 10ms);

  CHECK(write.kind == PlaybackWrite::Kind::Failed);
  REQUIRE(write.error.has_value());
  CHECK(write.error->code == PlaybackErrorCode::NotOpen);
  const auto rewound = playback.rewind();
  REQUIRE_FALSE(rewound.ok());
  CHECK(rewound.error().code == PlaybackErrorCode::NotOpen);
  const auto dropped = playback.drop();
  REQUIRE(dropped.has_value());
  CHECK(dropped->code == PlaybackErrorCode::NotOpen);
}

TEST_CASE("accepts only exactly 48 kHz stereo s16 playback, whatever period and buffer are granted") {
  const PlaybackFormat requested{48000, 2, SampleFormat::S16Le, 960, 7680, 2880};
  CHECK_FALSE(checkPlaybackFormat(requested, {48000, 2, SampleFormat::S16Le, 1024, 4096}).has_value());

  CHECK(checkPlaybackFormat(requested, {48000, 1, SampleFormat::S16Le, 960, 7680}).has_value());
  const auto error = checkPlaybackFormat(requested, {48000, 2, SampleFormat::Unsupported, 960, 7680});
  REQUIRE(error.has_value());
  CHECK(error->code == PlaybackErrorCode::FormatMismatch);
  CHECK_THAT(describe(*error), ContainsSubstring("granted 48000 Hz x 2 unsupported format"));
}

TEST_CASE("requires a period that fits the buffer and a fill between two granted periods and the buffer") {
  const PlaybackFormat requested{48000, 2, SampleFormat::S16Le, 960, 7680, 2880};
  CHECK_FALSE(checkPlaybackFormat(requested, {48000, 2, SampleFormat::S16Le, 1440, 2880}).has_value());

  for (const PlaybackFormat granted : {PlaybackFormat{48000, 2, SampleFormat::S16Le, 1500, 7680},
                                       PlaybackFormat{48000, 2, SampleFormat::S16Le, 960, 2048},
                                       PlaybackFormat{48000, 2, SampleFormat::S16Le, 0, 7680},
                                       PlaybackFormat{48000, 2, SampleFormat::S16Le, 960, 480}}) {
    CAPTURE(granted.periodFrames, granted.bufferFrames);
    const auto error = checkPlaybackFormat(requested, granted);
    REQUIRE(error.has_value());
    CHECK(error->code == PlaybackErrorCode::FormatMismatch);
    CHECK_THAT(describe(*error), ContainsSubstring("period " + std::to_string(granted.periodFrames)));
    CHECK_THAT(describe(*error), ContainsSubstring("buffer " + std::to_string(granted.bufferFrames)));
  }
}

TEST_CASE("bounds playback error descriptions to printable text") {
  const auto text = describe(PlaybackError{PlaybackErrorCode::WriteFailed, std::string(1000, 'x') + "\n\x01"});
  CHECK(text.size() <= 256);
  CHECK(text.find('\n') == std::string::npos);
}
