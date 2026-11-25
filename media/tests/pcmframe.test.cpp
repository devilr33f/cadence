#include "pcmframe/codec.h"

#include <catch2/catch_test_macros.hpp>

#include <cstddef>
#include <cstdint>
#include <optional>
#include <span>
#include <variant>
#include <vector>

using namespace cadence::pcmframe;

namespace {

using Bytes = std::vector<std::uint8_t>;

// what the server reads from bytes in one push: the first frame, none yet, or the error
struct Decoded {
  std::optional<ToServer> message;
  std::optional<FrameError> error;
};

Decoded toServer(const Bytes& bytes) {
  Decoder decoder;
  decoder.push(bytes);
  const auto next = decoder.nextToServer();
  if (!next.ok())
    return {std::nullopt, next.error()};
  return {next.value(), std::nullopt};
}

std::optional<FrameError> shimError(const Bytes& bytes) {
  Decoder decoder;
  decoder.push(bytes);
  const auto next = decoder.nextToShim();
  return next.ok() ? std::nullopt : std::optional(next.error());
}

Bytes formatFrame(std::uint8_t encoding, std::uint8_t channels, std::uint32_t rate) {
  return {1,
          6,
          0,
          0,
          0,
          encoding,
          channels,
          static_cast<std::uint8_t>(rate),
          static_cast<std::uint8_t>(rate >> 8),
          static_cast<std::uint8_t>(rate >> 16),
          static_cast<std::uint8_t>(rate >> 24)};
}

Bytes encoded(const ToServer& message) {
  Bytes wire;
  REQUIRE(encode(message, wire));
  return wire;
}

std::vector<std::uint8_t> pcmBytes(const ToServer& message) {
  const auto& pcm = std::get<Pcm>(message);
  return {pcm.bytes.begin(), pcm.bytes.end()};
}

}  // namespace

TEST_CASE("every message has the wire layout both sides rely on") {
  CHECK(encoded(Format{Encoding::F32Le, 2, 44100}) == formatFrame(1, 2, 44100));
  CHECK(encoded(Format{Encoding::S16Le, 1, 48000}) == formatFrame(2, 1, 48000));
  const Bytes samples{9, 8};
  CHECK(encoded(Pcm{samples}) == Bytes{2, 2, 0, 0, 0, 9, 8});
  CHECK(encoded(Pcm{}) == Bytes{2, 0, 0, 0, 0});
  CHECK(encoded(Cork{true}) == Bytes{3, 1, 0, 0, 0, 1});
  CHECK(encoded(Cork{false}) == Bytes{3, 1, 0, 0, 0, 0});
  CHECK(encoded(Flush{}) == Bytes{4, 0, 0, 0, 0});

  Bytes played;
  encode(Played{0x0102}, played);
  CHECK(played == Bytes{0x81, 8, 0, 0, 0, 2, 1, 0, 0, 0, 0, 0, 0});
}

TEST_CASE("every message round-trips, a full 64 KiB pcm frame and an empty one included") {
  const Bytes small{1, 2, 3, 4};
  const Bytes full(kMaxPayload, 7);
  const std::vector<ToServer> messages{Format{Encoding::F32Le, 2, 44100},
                                       Format{Encoding::S16Le, 1, 48000},
                                       Pcm{small},
                                       Pcm{full},
                                       Pcm{},
                                       Cork{true},
                                       Cork{false},
                                       Flush{}};
  Bytes wire;
  for (const auto& message : messages)
    REQUIRE(encode(message, wire));
  Decoder decoder;
  decoder.push(wire);
  for (const auto& message : messages) {
    const auto next = decoder.nextToServer();
    REQUIRE(next.ok());
    REQUIRE(next.value().has_value());
    CHECK(*next.value() == message);
  }
  const auto end = decoder.nextToServer();
  REQUIRE(end.ok());
  CHECK_FALSE(end.value().has_value());

  Bytes reports;
  encode(Played{0}, reports);
  encode(Played{UINT64_MAX}, reports);
  Decoder shim;
  shim.push(reports);
  for (const auto total : {std::uint64_t{0}, UINT64_MAX}) {
    const auto next = shim.nextToShim();
    REQUIRE(next.ok());
    REQUIRE(next.value().has_value());
    CHECK(next.value()->total == total);
  }
  CHECK_FALSE(shim.nextToShim().value().has_value());
}

TEST_CASE("a frame split across pushes decodes once complete") {
  Bytes wire;
  const Bytes five(10, 5);
  REQUIRE(encode(Pcm{five}, wire));
  REQUIRE(encode(Flush{}, wire));
  Decoder decoder;
  for (std::size_t i = 0; i + 1 < 15; ++i) {
    decoder.push(std::span(wire).subspan(i, 1));
    const auto next = decoder.nextToServer();
    REQUIRE(next.ok());
    CHECK_FALSE(next.value().has_value());
  }
  decoder.push(std::span(wire).subspan(14, 1));
  const auto pcm = decoder.nextToServer();
  REQUIRE(pcm.ok());
  REQUIRE(pcm.value().has_value());
  CHECK(pcmBytes(*pcm.value()) == five);
  CHECK_FALSE(decoder.nextToServer().value().has_value());
  decoder.push(std::span(wire).subspan(15));
  CHECK(*decoder.nextToServer().value() == ToServer{Flush{}});

  Bytes report;
  encode(Played{77}, report);
  Decoder shim;
  shim.push(std::span(report).first(7));
  CHECK_FALSE(shim.nextToShim().value().has_value());
  shim.push(std::span(report).subspan(7));
  CHECK(shim.nextToShim().value()->total == 77);
}

TEST_CASE("a payload over 65536 bytes is too long, refused from its header alone") {
  const auto over = static_cast<std::uint32_t>(kMaxPayload + 1);
  const Bytes length{static_cast<std::uint8_t>(over), static_cast<std::uint8_t>(over >> 8),
                     static_cast<std::uint8_t>(over >> 16), static_cast<std::uint8_t>(over >> 24)};
  Bytes pcm{2};
  pcm.insert(pcm.end(), length.begin(), length.end());
  CHECK(toServer(pcm).error == FrameError::TooLong);
  Bytes played{0x81};
  played.insert(played.end(), length.begin(), length.end());
  CHECK(shimError(played) == FrameError::TooLong);

  // and never encoded
  Bytes wire;
  const Bytes big(kMaxPayload + 1, 1);
  CHECK_FALSE(encode(Pcm{big}, wire));
  CHECK(wire.empty());
}

TEST_CASE("an unknown type is refused as soon as its first byte arrives") {
  for (const auto kind : Bytes{0, 5, 0x80, 0x81, 0xff})
    CHECK(toServer({kind}).error == FrameError::Unknown);
  for (const auto kind : Bytes{0, 1, 2, 3, 4, 0x82})
    CHECK(shimError({kind}) == FrameError::Unknown);
}

TEST_CASE("a format of three channels, 22050 Hz or an unknown encoding is malformed") {
  CHECK(toServer(formatFrame(1, 3, 44100)).error == FrameError::Malformed);
  CHECK(toServer(formatFrame(2, 2, 22050)).error == FrameError::Malformed);
  CHECK(toServer(formatFrame(3, 2, 48000)).error == FrameError::Malformed);
  CHECK(toServer(formatFrame(1, 0, 48000)).error == FrameError::Malformed);
  CHECK(toServer(formatFrame(2, 2, 96000)).error == FrameError::Malformed);
  CHECK(toServer(formatFrame(2, 2, 48000)).message == ToServer{Format{Encoding::S16Le, 2, 48000}});
}

TEST_CASE("a fixed-length frame of another length is malformed, judged from its header") {
  CHECK(toServer({1, 5, 0, 0, 0}).error == FrameError::Malformed);
  CHECK(toServer({3, 2, 0, 0, 0}).error == FrameError::Malformed);
  CHECK(toServer({4, 1, 0, 0, 0}).error == FrameError::Malformed);
  CHECK(shimError({0x81, 4, 0, 0, 0}) == FrameError::Malformed);
}

TEST_CASE("a cork payload other than 0 or 1 is malformed") {
  CHECK(toServer({3, 1, 0, 0, 0, 2}).error == FrameError::Malformed);
  CHECK(toServer({3, 1, 0, 0, 0, 0}).message == ToServer{Cork{false}});
}

TEST_CASE("after an error the decoder keeps returning it") {
  Decoder decoder;
  decoder.push(Bytes{9});
  CHECK(decoder.nextToServer().error() == FrameError::Unknown);
  decoder.push(encoded(Flush{}));
  CHECK(decoder.nextToServer().error() == FrameError::Unknown);
}

TEST_CASE("a format's frame size follows its encoding and channels") {
  CHECK(Format{Encoding::F32Le, 2, 44100}.frameBytes() == 8);
  CHECK(Format{Encoding::S16Le, 1, 48000}.frameBytes() == 2);
  CHECK(Format{Encoding::S16Le, 2, 48000}.valid());
  CHECK_FALSE(Format{Encoding::F32Le, 2, 96000}.valid());
}
