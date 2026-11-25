#include "audiod/airplay-uxplay-decoder.h"

#include "ALACEncoder.h"

#include <catch2/catch_test_macros.hpp>

#include <cmath>
#include <cstring>
#include <numbers>
#include <vector>

using namespace cadence::audiod::uxplay;

namespace {

// a packet of the sender's alac, made with apple's own encoder as airplay's senders have it: 352 frames, 16 bits
std::vector<std::uint8_t> alacPacket(const std::vector<std::int16_t>& pcm) {
  AudioFormatDescription input{};
  input.mSampleRate = kStreamRate;
  input.mFormatID = kALACFormatLinearPCM;
  input.mFormatFlags = kALACFormatFlagIsSignedInteger | kALACFormatFlagIsPacked;
  input.mBytesPerPacket = 4;
  input.mFramesPerPacket = 1;
  input.mBytesPerFrame = 4;
  input.mChannelsPerFrame = 2;
  input.mBitsPerChannel = 16;
  AudioFormatDescription output{};
  output.mSampleRate = kStreamRate;
  output.mFormatID = kALACFormatAppleLossless;
  // the encoder's own flag for 16-bit source data
  output.mFormatFlags = 1;
  output.mFramesPerPacket = kAlacFramesPerPacket;
  output.mChannelsPerFrame = 2;
  ALACEncoder encoder;
  encoder.SetFrameSize(kAlacFramesPerPacket);
  REQUIRE(encoder.InitializeEncoder(output) == 0);
  std::vector<std::uint8_t> in(pcm.size() * 2);
  std::memcpy(in.data(), pcm.data(), in.size());
  std::vector<std::uint8_t> out(in.size() * 2 + 64);
  auto bytes = static_cast<int32_t>(in.size());
  REQUIRE(encoder.Encode(input, input, in.data(), out.data(), &bytes) == 0);
  out.resize(static_cast<std::size_t>(bytes));
  return out;
}

std::vector<std::int16_t> stereoSine(std::size_t frames, double hz, std::size_t start = 0) {
  std::vector<std::int16_t> out;
  for (std::size_t i = 0; i < frames; ++i) {
    const auto t = static_cast<double>(start + i) / kStreamRate;
    out.push_back(static_cast<std::int16_t>(12000 * std::sin(2 * std::numbers::pi * hz * t)));
    out.push_back(static_cast<std::int16_t>(-8000 * std::sin(2 * std::numbers::pi * hz * 1.5 * t)));
  }
  return out;
}

}  // namespace

TEST_CASE("raop's compression types name the codecs") {
  CHECK(codecOf(1) == AudioCodec::Pcm);
  CHECK(codecOf(2) == AudioCodec::Alac);
  CHECK(codecOf(4) == AudioCodec::AacLc);
  CHECK(codecOf(8) == AudioCodec::AacEld);
  CHECK(codecOf(3) == AudioCodec::Unknown);
  CHECK(codecToken(AudioCodec::AacEld) == "aac_eld");
}

TEST_CASE("alac's cookie is airplay's stream: 352 frames, 16 bits, rice 40/10/14, stereo, 44.1 kHz") {
  const auto cookie = alacCookie(352);
  const std::vector<std::uint8_t> expected{0, 0, 1, 0x60, 0, 16, 40, 10, 14, 2, 0, 0xff,
                                           0, 0, 0, 0,    0, 0,  0,  0,  0,  0, 0xac, 0x44};
  CHECK(cookie == expected);
}

TEST_CASE("the sender's alac decodes losslessly, packet after packet") {
  auto decoder = makeDecoder(AudioCodec::Alac, kAlacFramesPerPacket);
  REQUIRE(decoder);
  std::vector<float> out;
  for (std::size_t packet = 0; packet < 4; ++packet) {
    const auto pcm = stereoSine(kAlacFramesPerPacket, 440, packet * kAlacFramesPerPacket);
    out.clear();
    REQUIRE(decoder->decode(alacPacket(pcm), out));
    REQUIRE(out.size() == pcm.size());
    for (std::size_t i = 0; i < pcm.size(); ++i)
      REQUIRE(out[i] == static_cast<float>(pcm[i]) / 32768.0f);
  }
}

TEST_CASE("a broken alac packet is refused and the stream goes on") {
  auto decoder = makeDecoder(AudioCodec::Alac, kAlacFramesPerPacket);
  REQUIRE(decoder);
  std::vector<float> out;
  const std::vector<std::uint8_t> garbage(40, 0xff);
  decoder->decode(garbage, out);
  out.clear();
  const auto pcm = stereoSine(kAlacFramesPerPacket, 1000);
  REQUIRE(decoder->decode(alacPacket(pcm), out));
  CHECK(out.size() == pcm.size());
}

TEST_CASE("l16 is big-endian stereo") {
  auto decoder = makeDecoder(AudioCodec::Pcm, 0);
  REQUIRE(decoder);
  std::vector<float> out;
  REQUIRE(decoder->decode(std::vector<std::uint8_t>{0x40, 0x00, 0xc0, 0x00}, out));
  REQUIRE(out.size() == 2);
  CHECK(out[0] == 0.5f);
  CHECK(out[1] == -0.5f);
  CHECK_FALSE(decoder->decode(std::vector<std::uint8_t>{0x40, 0x00}, out));
}

TEST_CASE("aac-lc and aac-eld open with airplay's audio specific config") {
  // lc: object type 2, 44.1 kHz, stereo; eld: object type 39, 480 frames
  const std::vector<std::uint8_t> lc(aacConfig(AudioCodec::AacLc).begin(), aacConfig(AudioCodec::AacLc).end());
  CHECK(lc == std::vector<std::uint8_t>{0x12, 0x10});
  const std::vector<std::uint8_t> eld(aacConfig(AudioCodec::AacEld).begin(), aacConfig(AudioCodec::AacEld).end());
  CHECK(eld == std::vector<std::uint8_t>{0xf8, 0xe8, 0x50, 0x00});
  for (const auto codec : {AudioCodec::AacLc, AudioCodec::AacEld}) {
    auto decoder = makeDecoder(codec, 0);
    REQUIRE(decoder);
    // garbage is never a crash, nor more than one frame of the stream
    std::vector<float> out;
    decoder->decode(std::vector<std::uint8_t>(64, 0x5a), out);
    CHECK(out.size() <= 2 * 1024 * kStreamChannels);
    decoder->reset();
  }
  CHECK_FALSE(makeDecoder(AudioCodec::Unknown, 0));
}
