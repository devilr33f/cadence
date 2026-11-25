#include "audiod/framing.h"
#include "support/audiod-client.h"

#include <catch2/catch_test_macros.hpp>

#include <array>
#include <cstdint>
#include <cstring>
#include <vector>

using namespace cadence::audiod;
using namespace cadence::test_support;

TEST_CASE("an envelope survives encode and decode") {
  const auto bytes = encode(ping(42));

  Envelope decoded = cadence_audiod_v1_Envelope_init_zero;
  REQUIRE_FALSE(decodeFrame(bytes, decoded).has_value());
  CHECK(decoded.which_body == cadence_audiod_v1_Envelope_ping_tag);
  CHECK(decoded.body.ping.seq == 42);
}

TEST_CASE("a frame of exactly the limit decodes and one byte more is oversize") {
  Envelope decoded = cadence_audiod_v1_Envelope_init_zero;
  CHECK_FALSE(decodeFrame(paddedPing(kMaxFrameBytes), decoded).has_value());
  CHECK(decoded.which_body == cadence_audiod_v1_Envelope_ping_tag);

  CHECK(decodeFrame(paddedPing(kMaxFrameBytes + 1), decoded) == FrameError::oversize);
}

TEST_CASE("garbage is undecodable") {
  const std::vector<std::uint8_t> garbage(6, 0xff);
  Envelope decoded = cadence_audiod_v1_Envelope_init_zero;
  CHECK(decodeFrame(garbage, decoded) == FrameError::undecodable);
}

TEST_CASE("a frame without a known body decodes with no body") {
  Envelope decoded = cadence_audiod_v1_Envelope_init_zero;
  REQUIRE_FALSE(decodeFrame(unknownBody(), decoded).has_value());
  CHECK(decoded.which_body == 0);
}

TEST_CASE("the largest envelope fits the encode buffer") {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_audio_down_tag;
  auto& down = envelope.body.audio_down;
  std::memset(down.stream_id, 'x', sizeof down.stream_id - 1);
  down.seq = UINT32_MAX;
  down.opus.size = sizeof down.opus.bytes;
  std::memset(down.opus.bytes, 0xab, sizeof down.opus.bytes);

  std::array<std::uint8_t, kMaxEncodedBytes> out{};
  const auto written = encodeFrame(envelope, out);
  REQUIRE(written.has_value());
  CHECK(*written <= kMaxEncodedBytes);
  CHECK(*written > sizeof down.opus.bytes);
}
