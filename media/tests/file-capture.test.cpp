#include "file/file-capture.h"
#include "support/fake-clock.h"
#include "support/samples.h"
#include "support/temp-dir.h"

#include <catch2/catch_test_macros.hpp>

#include <chrono>
#include <cstdint>
#include <vector>

using namespace cadence;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

std::string writeWav(const TempDir& dir, const std::vector<std::int16_t>& samples, int rate = 16000, std::size_t channels = 1) {
  const auto path = dir.file("in.wav");
  auto writer = WavWriter::create(path, {rate, channels});
  REQUIRE(writer.ok());
  REQUIRE_FALSE(writer.value().write(samples));
  REQUIRE_FALSE(writer.value().close());
  return path;
}

}  // namespace

TEST_CASE("file capture: audio arrives one 20 ms frame at a time at real-time pace", "[file-capture]") {
  TempDir dir;
  // 50 ms: two full frames and a 10 ms tail
  const auto samples = ramp(800);
  FakeFileClock clock;
  FileCapture capture(writeWav(dir, samples), clock);
  const auto granted = capture.open({});
  REQUIRE(granted.ok());
  CHECK(granted.value().sampleRate == 16000);
  CHECK(granted.value().channels == 1);

  std::vector<std::int16_t> buffer(1600);
  const auto start = clock.now();

  auto read = capture.read(buffer, 100ms);
  REQUIRE(read.kind == CaptureRead::Kind::Frames);
  CHECK(read.frames == 320);
  CHECK(clock.now() - start == 20ms);
  CHECK(std::vector(buffer.begin(), buffer.begin() + 320) == std::vector(samples.begin(), samples.begin() + 320));

  read = capture.read(buffer, 100ms);
  CHECK(read.frames == 320);
  CHECK(clock.now() - start == 40ms);

  read = capture.read(buffer, 100ms);
  REQUIRE(read.kind == CaptureRead::Kind::Frames);
  CHECK(read.frames == 160);
  CHECK(clock.now() - start == 60ms);
  CHECK(buffer[0] == samples[640]);
  CHECK(capture.finished());
}

TEST_CASE("file capture: a read that finds no frame due waits out its timeout and reports idle", "[file-capture]") {
  TempDir dir;
  FakeFileClock clock;
  FileCapture capture(writeWav(dir, ramp(3200)), clock);
  REQUIRE(capture.open({}).ok());

  std::vector<std::int16_t> buffer(320);
  const auto start = clock.now();
  const auto read = capture.read(buffer, 5ms);
  CHECK(read.kind == CaptureRead::Kind::Idle);
  CHECK(clock.now() - start == 5ms);
}

TEST_CASE("file capture: a slow reader gets the frames that piled up, capped by its buffer", "[file-capture]") {
  TempDir dir;
  const auto samples = ramp(3200);
  FakeFileClock clock;
  FileCapture capture(writeWav(dir, samples), clock);
  REQUIRE(capture.open({}).ok());
  clock.advance(100ms);

  std::vector<std::int16_t> buffer(1000);
  auto read = capture.read(buffer, 0ms);
  CHECK(read.frames == 1000);
  read = capture.read(buffer, 0ms);
  CHECK(read.frames == 600);
  CHECK(buffer[0] == samples[1000]);
  CHECK(capture.read(buffer, 0ms).kind == CaptureRead::Kind::Idle);
}

TEST_CASE("file capture: after the file ends reads stay idle and open starts it over", "[file-capture]") {
  TempDir dir;
  const auto samples = ramp(320);
  FakeFileClock clock;
  FileCapture capture(writeWav(dir, samples), clock);
  REQUIRE(capture.open({}).ok());
  std::vector<std::int16_t> buffer(320);
  REQUIRE(capture.read(buffer, 100ms).frames == 320);

  const auto before = clock.now();
  CHECK(capture.read(buffer, 30ms).kind == CaptureRead::Kind::Idle);
  CHECK(clock.now() - before == 30ms);

  capture.close();
  REQUIRE(capture.open({}).ok());
  const auto read = capture.read(buffer, 100ms);
  CHECK(read.frames == 320);
  CHECK(buffer[0] == samples[0]);
}

TEST_CASE("file capture: refuses files that are not 16 kHz mono and reads before open", "[file-capture]") {
  TempDir dir;
  FakeFileClock clock;
  std::vector<std::int16_t> buffer(320);
  {
    FileCapture capture(writeWav(dir, ramp(960), 48000), clock);
    CHECK(capture.open({}).error().code == CaptureErrorCode::FormatMismatch);
    CHECK(capture.read(buffer, 0ms).kind == CaptureRead::Kind::Failed);
  }
  {
    FileCapture capture(writeWav(dir, ramp(960), 16000, 2), clock);
    CHECK(capture.open({}).error().code == CaptureErrorCode::FormatMismatch);
  }
  FileCapture missing(dir.file("none.wav"), clock);
  CHECK(missing.open({}).error().code == CaptureErrorCode::OpenFailed);
  const auto read = missing.read(buffer, 0ms);
  CHECK(read.kind == CaptureRead::Kind::Failed);
  CHECK(read.error->code == CaptureErrorCode::NotOpen);
}
