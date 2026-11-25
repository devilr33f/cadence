#include "aaudio/aaudio-capture.h"
#include "support/fake-aaudio.h"

#include <catch2/catch_test_macros.hpp>

#include <cstdint>
#include <vector>

using namespace cadence;
using cadence::fake_aaudio::FakeApi;

namespace {

constexpr CaptureFormat kMono16k{16000, 1, 320, 1280};

std::vector<std::int16_t> ramp(std::size_t n, std::int16_t first) {
  std::vector<std::int16_t> out(n);
  for (std::size_t i = 0; i < n; ++i)
    out[i] = static_cast<std::int16_t>(first + static_cast<std::int16_t>(i));
  return out;
}

}  // namespace

TEST_CASE("aaudio capture opens an input at the rate and channels asked, with a ring of at least 200 ms") {
  FakeApi api;
  AAudioCapture source(api);
  const auto granted = source.open(kMono16k);
  REQUIRE(granted.ok());
  CHECK(api.requests[0].direction == aaudio::Direction::Input);
  CHECK(api.requests[0].sampleRate == 16000);
  CHECK(api.requests[0].channels == 1);
  CHECK(granted.value().periodFrames == 320);
  CHECK(granted.value().bufferFrames >= 3200);
  CHECK_FALSE(checkExactFormat(kMono16k, granted.value()).has_value());
}

TEST_CASE("aaudio capture is never resampled") {
  FakeApi api;
  api.grantRate = 48000;
  AAudioCapture source(api);
  const auto r = source.open(kMono16k);
  REQUIRE_FALSE(r.ok());
  CHECK(r.error().code == CaptureErrorCode::FormatMismatch);
}

TEST_CASE("aaudio capture reads what the callbacks brought, in order") {
  FakeApi api;
  AAudioCapture source(api);
  REQUIRE(source.open(kMono16k).ok());
  std::vector<std::int16_t> buf(320);
  CHECK(source.read(buf, std::chrono::milliseconds(0)).kind == CaptureRead::Kind::Idle);
  api.push(ramp(192, 1));
  api.push(ramp(192, 193));
  auto r = source.read(buf, std::chrono::milliseconds(0));
  REQUIRE(r.kind == CaptureRead::Kind::Frames);
  REQUIRE(r.frames == 320);
  CHECK(buf == ramp(320, 1));
  r = source.read(buf, std::chrono::milliseconds(0));
  REQUIRE(r.frames == 64);
  CHECK(std::vector<std::int16_t>(buf.begin(), buf.begin() + 64) == ramp(64, 321));
}

TEST_CASE("a ring that overflowed is reported once and reading goes on after the gap") {
  FakeApi api;
  AAudioCapture source(api);
  const auto granted = source.open(kMono16k);
  REQUIRE(granted.ok());
  const auto cap = granted.value().bufferFrames;
  api.push(ramp(cap, 1));
  api.push(ramp(100, 1));
  CHECK(source.overruns() == 1);
  std::vector<std::int16_t> buf(320);
  const auto r = source.read(buf, std::chrono::milliseconds(0));
  CHECK(r.kind == CaptureRead::Kind::Recovered);
  REQUIRE(r.error.has_value());
  CHECK(r.error->code == CaptureErrorCode::Overrun);
  api.push(ramp(50, 900));
  const auto next = source.read(buf, std::chrono::milliseconds(0));
  REQUIRE(next.frames == 50);
  CHECK(buf[0] == 900);
}

TEST_CASE("a capture stream that dies is opened again by the next read") {
  FakeApi api;
  AAudioCapture source(api);
  REQUIRE(source.open(kMono16k).ok());
  api.disconnect();
  std::vector<std::int16_t> buf(320);
  CHECK(source.read(buf, std::chrono::milliseconds(0)).kind == CaptureRead::Kind::Recovered);
  CHECK(api.opens == 2);
  api.push(ramp(10, 1));
  CHECK(source.read(buf, std::chrono::milliseconds(0)).frames == 10);
}
