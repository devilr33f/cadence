// the group's music codec: 20 ms of 48 kHz stereo as opus or s16
#include "audio/music-codec.h"

#include <catch2/catch_test_macros.hpp>

#include <cmath>
#include <numbers>
#include <vector>

using namespace cadence;

namespace {

// left a 440 Hz sine, right a 1 kHz one, frame n of a continuous stream
std::vector<float> tones(std::size_t n) {
  std::vector<float> out(kGroupFrameSamples * 2);
  for (std::size_t i = 0; i < kGroupFrameSamples; ++i) {
    const double t = static_cast<double>(n * kGroupFrameSamples + i) / kGroupRate;
    out[2 * i] = static_cast<float>(0.5 * std::sin(2 * std::numbers::pi * 440 * t));
    out[2 * i + 1] = static_cast<float>(0.5 * std::sin(2 * std::numbers::pi * 1000 * t));
  }
  return out;
}

double energy(const std::vector<float>& v, std::size_t channel) {
  double e = 0;
  for (std::size_t i = channel; i < v.size(); i += 2)
    e += static_cast<double>(v[i]) * v[i];
  return e / static_cast<double>(v.size() / 2);
}

}  // namespace

TEST_CASE("pcm s16 is exact to its quantum and as long as the bound allows") {
  MusicEncoder enc(MusicCodec::PcmS16, 0);
  MusicDecoder dec(MusicCodec::PcmS16);
  const auto in = tones(3);
  std::vector<std::uint8_t> bytes;
  REQUIRE(enc.encode(in, bytes));
  CHECK(bytes.size() == 3840);
  CHECK(bytes.size() <= kGroupFrameBytes);
  std::vector<float> out(kGroupFrameSamples * 2);
  REQUIRE(dec.decode(bytes, out));
  for (std::size_t i = 0; i < out.size(); ++i)
    CHECK(std::abs(out[i] - in[i]) < 1.0f / 16384);
  // a frame of another length is refused
  bytes.pop_back();
  CHECK_FALSE(dec.decode(bytes, out));
  dec.conceal(out);
  CHECK(energy(out, 0) == 0.0);
}

TEST_CASE("opus carries both channels at the group's bitrate, and conceals a lost frame") {
  MusicEncoder enc(MusicCodec::Opus, 256);
  MusicDecoder dec(MusicCodec::Opus);
  REQUIRE(enc.ok());
  REQUIRE(dec.ok());
  std::vector<std::uint8_t> bytes;
  std::vector<float> out(kGroupFrameSamples * 2);
  std::size_t total = 0;
  for (std::size_t n = 0; n < 50; ++n) {
    REQUIRE(enc.encode(tones(n), bytes));
    CHECK(bytes.size() <= kGroupFrameBytes);
    total += bytes.size();
    REQUIRE(dec.decode(bytes, out));
  }
  // about 256 kbps: 640 bytes a frame, vbr around it
  CHECK(total / 50 > 300);
  CHECK(total / 50 < 1000);
  // both tones come through at their level (0.125 is a 0.5 sine's mean square)
  CHECK(std::abs(energy(out, 0) - 0.125) < 0.02);
  CHECK(std::abs(energy(out, 1) - 0.125) < 0.02);
  dec.conceal(out);
  CHECK(energy(out, 0) > 0.01);
  // a 10 ms packet is not a group frame
  std::vector<float> half(480 * 2, 0.1f);
  CHECK_FALSE(enc.encode(half, bytes));
  // a toc byte of config 0, one frame: 10 ms of silk
  CHECK_FALSE(dec.decode(std::vector<std::uint8_t>{0x00, 0x00}, out));
}
