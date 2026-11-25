#include "audio/opus-codec.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>
#include <catch2/matchers/catch_matchers_string.hpp>

#include <cmath>
#include <cstddef>
#include <cstdint>
#include <numbers>
#include <random>
#include <string>
#include <vector>

using namespace cadence;
using Catch::Matchers::ContainsSubstring;

namespace {

using Packet = std::vector<std::uint8_t>;

// 20 ms of a 16 kHz tone, continuing from the given frame index
std::vector<std::int16_t> tone(std::size_t frame, double hz = 440.0) {
  std::vector<std::int16_t> samples(kEncoderFrameSamples);
  for (std::size_t index = 0; index < samples.size(); ++index) {
    const double seconds = static_cast<double>(frame * samples.size() + index) / 16000.0;
    samples[index] = static_cast<std::int16_t>(8000.0 * std::sin(2.0 * std::numbers::pi * hz * seconds));
  }
  return samples;
}

double rms(const DecodedFrame& samples) {
  double sum = 0;
  for (const auto sample : samples)
    sum += static_cast<double>(sample) * sample;
  return std::sqrt(sum / static_cast<double>(samples.size()));
}

std::vector<Packet> encodeTone(OpusEncoder& encoder, std::size_t frames, double hz) {
  std::vector<Packet> packets;
  for (std::size_t frame = 0; frame < frames; ++frame) {
    auto packet = encoder.encode(tone(frame, hz));
    REQUIRE(packet.ok());
    packets.push_back(packet.value());
  }
  return packets;
}

std::vector<DecodedFrame> decodeAll(OpusDecoder& decoder, const std::vector<Packet>& packets) {
  std::vector<DecodedFrame> frames;
  for (const auto& packet : packets) {
    const auto decoded = decoder.decode(packet);
    REQUIRE(decoded.ok());
    frames.push_back(decoded.value());
  }
  return frames;
}

void checkBounded(const OpusError& error) {
  const auto text = describe(error);
  CHECK_FALSE(text.empty());
  CHECK(text.size() <= 256);
  CHECK(text.find('\n') == std::string::npos);
}

}  // namespace

TEST_CASE("encodes 20 ms of 16 kHz as 320 samples into one bounded packet that decodes to 960 samples of 48 kHz") {
  CHECK(kDecodeSampleRate == 48000);
  CHECK(kDecodedFrameSamples == 960);
  CHECK(kEncoderFrameSamples == 320);
  OpusEncoder encoder;
  REQUIRE(encoder.open().ok());
  const auto packets = encodeTone(encoder, 50, 440.0);
  for (const auto& packet : packets) {
    CHECK_FALSE(packet.empty());
    CHECK(packet.size() <= kMaxAudioFrameBytes);
  }
  OpusDecoder decoder;

  const auto frames = decodeAll(decoder, packets);

  REQUIRE(frames.size() == 50);
  CHECK(frames.back().size() == kDecodedFrameSamples);
  // a full-scale sine of amplitude 8000 has an rms near 5657
  CHECK(rms(frames.back()) > 2000.0);
}

TEST_CASE("encodes 16 kHz mono voice at 24 kbps with dtx and in-band fec off, and keeps sending real packets through silence") {
  OpusEncoder encoder;

  const auto opened = encoder.open();

  REQUIRE(opened.ok());
  const auto& settings = opened.value();
  CHECK(settings.sampleRate == 16000);
  CHECK(settings.voip);
  CHECK(settings.bitrate == 24000);
  CHECK(settings.complexity == 5);
  CHECK_FALSE(settings.dtx);
  CHECK_FALSE(settings.inbandFec);
  // with dtx on, a packet of two bytes or less would mean nothing needs sending
  const std::vector<std::int16_t> silence(kEncoderFrameSamples, 0);
  for (int frame = 0; frame < 50; ++frame) {
    const auto packet = encoder.encode(silence);
    REQUIRE(packet.ok());
    CHECK(packet.value().size() > 2);
  }
}

TEST_CASE("rejects packets that do not hold exactly 20 ms") {
  const auto [packet, samples] = GENERATE(table<Packet, int>({
      // celt 10 ms
      {{0x90}, 480},
      // two 20 ms frames
      {{0x99, 0x00, 0x00}, 1920},
  }));
  OpusDecoder decoder;

  const auto decoded = decoder.decode(packet);

  REQUIRE_FALSE(decoded.ok());
  CHECK(decoded.error().code == OpusErrorCode::WrongDuration);
  CHECK_THAT(describe(decoded.error()), ContainsSubstring(std::to_string(samples)));
  CHECK_THAT(describe(decoded.error()), ContainsSubstring("960"));
  checkBounded(decoded.error());
}

TEST_CASE("rejects empty, oversized, and malformed packets with bounded errors") {
  Packet atLimit(kMaxAudioFrameBytes, 0x00);
  atLimit[0] = 0x98;
  Packet overLimit(kMaxAudioFrameBytes + 1, 0x00);
  overLimit[0] = 0x98;
  const auto [packet, code] = GENERATE_COPY(table<Packet, OpusErrorCode>({
      {Packet{}, OpusErrorCode::EmptyPacket},
      {overLimit, OpusErrorCode::PacketTooLarge},
      // a frame larger than opus allows
      {atLimit, OpusErrorCode::InvalidPacket},
      // code 3 without its frame count
      {{0x9B}, OpusErrorCode::InvalidPacket},
  }));
  OpusDecoder decoder;

  const auto decoded = decoder.decode(packet);

  REQUIRE_FALSE(decoded.ok());
  CHECK(decoded.error().code == code);
  checkBounded(decoded.error());
}

TEST_CASE("arbitrary bytes decode to 20 ms or a named error, and the decoder keeps working") {
  std::mt19937 random(20261024);
  OpusDecoder decoder;
  int unexpected = 0;
  for (int attempt = 0; attempt < 3000; ++attempt) {
    Packet packet(1 + random() % 300);
    for (auto& byte : packet)
      byte = static_cast<std::uint8_t>(random());
    const auto decoded = decoder.decode(packet);
    if (!decoded.ok() && decoded.error().code != OpusErrorCode::InvalidPacket &&
        decoded.error().code != OpusErrorCode::WrongDuration)
      ++unexpected;
  }
  CHECK(unexpected == 0);

  OpusEncoder encoder;
  REQUIRE(encoder.open().ok());
  CHECK(rms(decodeAll(decoder, encodeTone(encoder, 10, 440.0)).back()) > 2000.0);
}

TEST_CASE("a rejected packet leaves the decoder exactly where it was") {
  OpusEncoder encoder;
  REQUIRE(encoder.open().ok());
  const auto packets = encodeTone(encoder, 10, 440.0);
  const std::vector earlier(packets.begin(), packets.begin() + 5);
  const std::vector later(packets.begin() + 5, packets.end());

  OpusDecoder uninterrupted;
  decodeAll(uninterrupted, earlier);
  const auto expected = decodeAll(uninterrupted, later);

  OpusDecoder interrupted;
  decodeAll(interrupted, earlier);
  // libopus itself refuses the first; the second is refused for its duration before libopus sees it
  const auto invalid = interrupted.decode(Packet{0x91, 0x00, 0x00, 0x00});
  REQUIRE_FALSE(invalid.ok());
  CHECK(invalid.error().code == OpusErrorCode::InvalidPacket);
  const auto tooShort = interrupted.decode(Packet{0x90});
  REQUIRE_FALSE(tooShort.ok());
  CHECK(tooShort.error().code == OpusErrorCode::WrongDuration);

  CHECK(decodeAll(interrupted, later) == expected);
}

TEST_CASE("reset forgets the previous stream, so a new response never continues an old one") {
  OpusEncoder encoder;
  REQUIRE(encoder.open().ok());
  const auto earlier = encodeTone(encoder, 10, 440.0);
  const auto later = encodeTone(encoder, 5, 880.0);

  OpusDecoder fresh;
  const auto fromFresh = decodeAll(fresh, later);
  OpusDecoder continued;
  decodeAll(continued, earlier);
  const auto fromContinued = decodeAll(continued, later);
  OpusDecoder reused;
  decodeAll(reused, earlier);
  REQUIRE_FALSE(reused.reset().has_value());
  const auto fromReset = decodeAll(reused, later);

  CHECK(fromContinued != fromFresh);
  CHECK(fromReset == fromFresh);
}

TEST_CASE("encodes with a complexity of 0 to 10 and refuses one outside it") {
  for (const int complexity : {0, 10}) {
    OpusEncoder encoder;
    const auto opened = encoder.open({.complexity = complexity});
    REQUIRE(opened.ok());
    CHECK(opened.value().complexity == complexity);
    CHECK(encoder.encode(tone(0)).ok());
  }
  for (const int complexity : {-1, 11}) {
    OpusEncoder encoder;
    const auto opened = encoder.open({.complexity = complexity});
    REQUIRE_FALSE(opened.ok());
    CHECK(opened.error().code == OpusErrorCode::ConfigureFailed);
    CHECK_THAT(describe(opened.error()), ContainsSubstring("complexity"));
    checkBounded(opened.error());
    CHECK(encoder.encode(tone(0)).error().code == OpusErrorCode::NotOpen);
  }
}

TEST_CASE("refuses frames that are not 320 samples") {
  const auto samples = GENERATE(as<std::size_t>{}, 0, 319, 321);
  OpusEncoder encoder;
  REQUIRE(encoder.open().ok());

  const auto packet = encoder.encode(std::vector<std::int16_t>(samples, 100));

  REQUIRE_FALSE(packet.ok());
  CHECK(packet.error().code == OpusErrorCode::WrongFrameSize);
  CHECK_THAT(describe(packet.error()), ContainsSubstring("320"));
}
