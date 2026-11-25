#include "capture/alsa-capture.h"

#include <catch2/catch_test_macros.hpp>

#include <chrono>
#include <cstdint>
#include <string>
#include <vector>

using namespace cadence;
using namespace std::chrono_literals;

namespace {

const CaptureFormat kPickleCapture{48000, 4, 256, 8192};

}  // namespace

TEST_CASE("bounds capture error descriptions to printable text") {
  const auto text = describe(CaptureError{CaptureErrorCode::ReadFailed, std::string(1000, 'x') + "\n\x01"});
  CHECK(text.size() <= 256);
  CHECK(text.find('\n') == std::string::npos);
}

TEST_CASE("captures from alsa's null device at exactly 48 kHz with four channels") {
  AlsaCapture capture("null");

  const auto granted = capture.open(kPickleCapture);

  REQUIRE(granted.ok());
  CHECK(granted.value().sampleRate == 48000);
  CHECK(granted.value().channels == 4);
  CHECK(granted.value().periodFrames > 0);
  CHECK(granted.value().bufferFrames >= granted.value().periodFrames);

  std::vector<std::int16_t> buffer(256 * 4, 1);
  const auto read = capture.read(buffer, 500ms);
  CHECK(read.kind == CaptureRead::Kind::Frames);
  CHECK(read.frames > 0);
  CHECK(read.frames <= 256);
  capture.close();
}

TEST_CASE("reading a capture that is not open fails with a named error") {
  AlsaCapture capture("null");
  std::vector<std::int16_t> buffer(256 * 4);

  const auto read = capture.read(buffer, 10ms);

  CHECK(read.kind == CaptureRead::Kind::Failed);
  REQUIRE(read.error.has_value());
  CHECK(read.error->code == CaptureErrorCode::NotOpen);
}
