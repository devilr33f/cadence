#include "ws/base64.h"
#include "ws/frame.h"
#include "ws/handshake.h"
#include "ws/sha1.h"

#include <catch2/catch_test_macros.hpp>

#include <cstdint>
#include <cstdio>
#include <string>
#include <string_view>
#include <vector>

using namespace cadence::ws;

namespace {

std::string hex(const Sha1Digest& digest) {
  std::string out;
  for (const auto byte : digest) {
    char pair[3];
    std::snprintf(pair, sizeof pair, "%02x", byte);
    out += pair;
  }
  return out;
}

std::span<const std::uint8_t> bytesOf(std::string_view text) {
  return {reinterpret_cast<const std::uint8_t*>(text.data()), text.size()};
}

std::vector<std::uint8_t> bytes(std::initializer_list<int> values) {
  std::vector<std::uint8_t> out;
  for (const int value : values)
    out.push_back(static_cast<std::uint8_t>(value));
  return out;
}

// a server frame: unmasked unless mask is given
std::vector<std::uint8_t> serverFrame(int first, std::string_view payload, const std::uint8_t* mask = nullptr) {
  std::vector<std::uint8_t> out{static_cast<std::uint8_t>(first)};
  const std::uint8_t maskBit = mask != nullptr ? 0x80 : 0x00;
  if (payload.size() < 126) {
    out.push_back(static_cast<std::uint8_t>(maskBit | payload.size()));
  } else if (payload.size() <= 0xFFFF) {
    out.push_back(static_cast<std::uint8_t>(maskBit | 126));
    out.push_back(static_cast<std::uint8_t>(payload.size() >> 8));
    out.push_back(static_cast<std::uint8_t>(payload.size() & 0xFF));
  } else {
    out.push_back(static_cast<std::uint8_t>(maskBit | 127));
    for (int shift = 56; shift >= 0; shift -= 8)
      out.push_back(static_cast<std::uint8_t>((payload.size() >> shift) & 0xFF));
  }
  if (mask != nullptr)
    out.insert(out.end(), mask, mask + 4);
  for (std::size_t i = 0; i < payload.size(); ++i)
    out.push_back(static_cast<std::uint8_t>(static_cast<std::uint8_t>(payload[i]) ^ (mask != nullptr ? mask[i % 4] : 0)));
  return out;
}

void feed(MessageReader& reader, const std::vector<std::uint8_t>& data) { reader.append(data); }

void requireIncoming(const std::optional<Incoming>& got, Incoming::Kind kind, std::string_view data, std::uint16_t code = 0) {
  REQUIRE(got.has_value());
  CHECK(got->kind == kind);
  CHECK(got->data == data);
  CHECK(got->code == code);
}

void requireFail(MessageReader& reader, std::uint16_t code) {
  const auto got = reader.next();
  requireIncoming(got, Incoming::Kind::Fail, "", code);
  // nothing follows a failure, whatever arrives
  reader.append(serverFrame(0x81, "late"));
  CHECK_FALSE(reader.next().has_value());
}

}  // namespace

TEST_CASE("Sha1MatchesTheFipsVectors") {
  CHECK(hex(sha1("")) == "da39a3ee5e6b4b0d3255bfef95601890afd80709");
  CHECK(hex(sha1("abc")) == "a9993e364706816aba3e25717850c26c9cd0d89d");
  CHECK(hex(sha1("abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq")) ==
        "84983e441c3bd26ebaae4aa1f95129e5e54670f1");
  CHECK(hex(sha1(std::string(1000000, 'a'))) == "34aa973cd4c4daa4f61eeb2bdbad27316534016f");
  std::string repeated;
  for (int i = 0; i < 10; ++i)
    repeated += "0123456701234567012345670123456701234567012345670123456701234567";
  CHECK(hex(sha1(repeated)) == "dea356a2cddd90c7a7ecedc5ebb563934f460452");
  // 55 and 56 bytes: the padding fits the block, or needs one more
  CHECK(hex(sha1(std::string(55, 'a'))) == "c1c8bbdc22796e28c0e15163d20899b65621d65a");
  CHECK(hex(sha1(std::string(56, 'a'))) == "c2db330f6083854c99d4b5bfb6e8f29f201be699");
}

TEST_CASE("Base64MatchesTheRfc4648Vectors") {
  CHECK(base64(bytesOf("")) == "");
  CHECK(base64(bytesOf("f")) == "Zg==");
  CHECK(base64(bytesOf("fo")) == "Zm8=");
  CHECK(base64(bytesOf("foo")) == "Zm9v");
  CHECK(base64(bytesOf("foob")) == "Zm9vYg==");
  CHECK(base64(bytesOf("fooba")) == "Zm9vYmE=");
  CHECK(base64(bytesOf("foobar")) == "Zm9vYmFy");
  CHECK(base64(bytes({0xFB, 0xFF, 0xBF})) == "+/+/");
}

TEST_CASE("AcceptIsTheRfc6455Example") { CHECK(acceptFor("dGhlIHNhbXBsZSBub25jZQ==") == "s3pPLMBiTxaQ9kYGzzhZRbK+xOo="); }

TEST_CASE("RequestAsksForAWebsocketOnTheLoopback") {
  CHECK(handshakeRequest(40123, "dGhlIHNhbXBsZSBub25jZQ==") ==
        "GET / HTTP/1.1\r\n"
        "Host: 127.0.0.1:40123\r\n"
        "Upgrade: websocket\r\n"
        "Connection: Upgrade\r\n"
        "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"
        "Sec-WebSocket-Version: 13\r\n"
        "\r\n");
}

TEST_CASE("ResponseIsAcceptedOnlyAsRfc6455Demands") {
  constexpr std::string_view key = "dGhlIHNhbXBsZSBub25jZQ==";
  const std::string good =
      "HTTP/1.1 101 Switching Protocols\r\n"
      "Upgrade: websocket\r\n"
      "Connection: Upgrade\r\n"
      "Sec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n"
      "\r\n";

  SECTION("accepted, with the frames behind the head left for the reader") {
    const auto check = checkResponse(good + "\x81\x02hi", key);
    CHECK(check.status == HandshakeStatus::Accepted);
    CHECK(check.headBytes == good.size());
  }
  SECTION("header names and the upgrade tokens in any case, among other headers and tokens") {
    const auto check = checkResponse(
        "HTTP/1.1 101 \r\n"
        "server: soloist\r\n"
        "UPGRADE:WebSocket \r\n"
        "connection: keep-alive, upgrade\r\n"
        "sec-websocket-accept:   s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n"
        "\r\n",
        key);
    CHECK(check.status == HandshakeStatus::Accepted);
  }
  SECTION("incomplete until the blank line") {
    for (std::size_t cut = 0; cut < good.size(); ++cut)
      CHECK(checkResponse(std::string_view(good).substr(0, cut), key).status == HandshakeStatus::Incomplete);
  }
  SECTION("refused") {
    const auto replace = [&](std::string_view from, std::string_view to) {
      std::string response = good;
      response.replace(response.find(from), from.size(), to);
      return response;
    };
    for (const auto& response : {
             replace("101 Switching Protocols", "200 OK"),
             replace("HTTP/1.1 101", "HTTP/1.1 1010"),
             replace("HTTP/1.1", "HTTP/1.0"),
             replace("s3pPLMBiTxaQ9kYGzzhZRbK+xOo=", "s3pPLMBiTxaQ9kYGzzhZRbK+xOp="),
             replace("Sec-WebSocket-Accept: s3pPLMBiTxaQ9kYGzzhZRbK+xOo=\r\n", ""),
             replace("Upgrade: websocket\r\n", ""),
             replace("Upgrade: websocket", "Upgrade: h2c"),
             replace("Connection: Upgrade\r\n", ""),
             replace("Connection: Upgrade", "Connection: close"),
             replace("\r\n\r\n", "\r\nSec-WebSocket-Extensions: permessage-deflate\r\n\r\n"),
             replace("\r\n\r\n", "\r\nSec-WebSocket-Protocol: chat\r\n\r\n"),
             replace("\r\n\r\n", "\r\nno colon here\r\n\r\n"),
         }) {
      INFO(response);
      CHECK(checkResponse(response, key).status == HandshakeStatus::Refused);
    }
  }
  SECTION("a head that never ends is refused at its bound") {
    std::string endless = "HTTP/1.1 101 Switching Protocols\r\nX-Pad: ";
    endless.append(kMaxResponseHead, 'a');
    CHECK(checkResponse(endless, key).status == HandshakeStatus::Refused);
  }
}

TEST_CASE("ClientFramesAreMasked") {
  // rfc 6455 §5.7: a masked "Hello"
  const std::string_view hello = "Hello";
  CHECK(encodeClientFrame(Opcode::Text, bytesOf(hello), MaskKey{0x37, 0xfa, 0x21, 0x3d}) ==
        bytes({0x81, 0x85, 0x37, 0xfa, 0x21, 0x3d, 0x7f, 0x9f, 0x4d, 0x51, 0x58}));
  const std::string medium(300, 'x');
  const auto mediumFrame = encodeClientFrame(Opcode::Pong, bytesOf(medium), MaskKey{0, 0, 0, 0});
  CHECK(std::vector(mediumFrame.begin(), mediumFrame.begin() + 4) == bytes({0x8A, 0xFE, 0x01, 0x2C}));
  CHECK(mediumFrame.size() == 4 + 4 + 300);
  const std::string large(70000, 'x');
  const auto largeFrame = encodeClientFrame(Opcode::Text, bytesOf(large), MaskKey{0, 0, 0, 0});
  CHECK(std::vector(largeFrame.begin(), largeFrame.begin() + 10) ==
        bytes({0x81, 0xFF, 0, 0, 0, 0, 0, 0x01, 0x11, 0x70}));
  CHECK(largeFrame.size() == 10 + 4 + 70000);
}

TEST_CASE("ReaderReassemblesTextAndPassesControlFramesBetweenFragments") {
  MessageReader reader(1024);
  std::vector<std::uint8_t> stream;
  for (const auto& frame : {serverFrame(0x01, "Hel"), serverFrame(0x89, "are you there"), serverFrame(0x00, "l"),
                            serverFrame(0x8A, "pong"), serverFrame(0x80, "o"), serverFrame(0x81, "")}) {
    stream.insert(stream.end(), frame.begin(), frame.end());
  }
  // byte by byte: nothing comes out before its frame is whole
  std::vector<Incoming> out;
  for (const auto byte : stream) {
    reader.append(std::span(&byte, 1));
    while (auto incoming = reader.next())
      out.push_back(*incoming);
  }
  REQUIRE(out.size() == 3);
  CHECK(out[0].kind == Incoming::Kind::Ping);
  CHECK(out[0].data == "are you there");
  CHECK(out[1].kind == Incoming::Kind::Text);
  CHECK(out[1].data == "Hello");
  CHECK(out[2].kind == Incoming::Kind::Text);
  CHECK(out[2].data == "");
}

TEST_CASE("ReaderDropsBinaryMessages") {
  MessageReader reader(1024);
  feed(reader, serverFrame(0x02, "\x01\x02"));
  feed(reader, serverFrame(0x80, "\x03"));
  feed(reader, serverFrame(0x82, "\x04"));
  feed(reader, serverFrame(0x81, "after"));
  requireIncoming(reader.next(), Incoming::Kind::Text, "after");
  CHECK_FALSE(reader.next().has_value());
}

TEST_CASE("ReaderTakesBothExtendedLengths") {
  MessageReader reader(std::size_t{1} << 20);
  const std::string medium(126, 'm');
  const std::string large(65536, 'l');
  feed(reader, serverFrame(0x81, medium));
  feed(reader, serverFrame(0x81, large));
  requireIncoming(reader.next(), Incoming::Kind::Text, medium);
  requireIncoming(reader.next(), Incoming::Kind::Text, large);
}

TEST_CASE("ReaderReadsTheClose") {
  SECTION("with a code, whose reason is not kept") {
    MessageReader reader(1024);
    feed(reader, serverFrame(0x88, std::string("\x03\xE8", 2) + "bye"));
    requireIncoming(reader.next(), Incoming::Kind::Close, "", 1000);
    reader.append(serverFrame(0x81, "late"));
    CHECK_FALSE(reader.next().has_value());
  }
  SECTION("without one") {
    MessageReader reader(1024);
    feed(reader, serverFrame(0x88, ""));
    requireIncoming(reader.next(), Incoming::Kind::Close, "", kCloseNoStatus);
  }
  SECTION("a one-byte payload is a protocol error") {
    MessageReader reader(1024);
    feed(reader, serverFrame(0x88, "\x03"));
    requireFail(reader, kCloseProtocolError);
  }
}

TEST_CASE("ReaderFailsACloseCodeTheRfcDoesNotAllow") {
  const auto payload = [](unsigned code) {
    return std::string{static_cast<char>(code >> 8), static_cast<char>(code & 0xFF)};
  };
  for (const unsigned code : {0U, 999U, 1004U, 1005U, 1006U, 1015U, 1016U, 2999U, 5000U, 65535U}) {
    INFO(code);
    MessageReader reader(1024);
    feed(reader, serverFrame(0x88, payload(code)));
    requireFail(reader, kCloseProtocolError);
  }
  for (const unsigned code : {1000U, 1003U, 1007U, 1011U, 1014U, 3000U, 4999U}) {
    INFO(code);
    MessageReader reader(1024);
    feed(reader, serverFrame(0x88, payload(code)));
    requireIncoming(reader.next(), Incoming::Kind::Close, "", static_cast<std::uint16_t>(code));
  }
}

TEST_CASE("ReaderFailsFramesAgainstTheProtocol") {
  MessageReader reader(1024);
  const std::uint8_t mask[] = {1, 2, 3, 4};
  SECTION("a masked server frame") { feed(reader, serverFrame(0x81, "hi", mask)); }
  SECTION("reserved bits") {
    for (const int rsv : {0x40, 0x20, 0x10}) {
      MessageReader fresh(1024);
      fresh.append(serverFrame(0x81 | rsv, "hi"));
      requireFail(fresh, kCloseProtocolError);
    }
    feed(reader, serverFrame(0xC1, "hi"));
  }
  SECTION("a reserved opcode") {
    for (const int opcode : {0x3, 0x7, 0xB, 0xF}) {
      MessageReader fresh(1024);
      fresh.append(serverFrame(0x80 | opcode, ""));
      requireFail(fresh, kCloseProtocolError);
    }
    feed(reader, serverFrame(0x83, ""));
  }
  SECTION("a continuation with nothing to continue") { feed(reader, serverFrame(0x80, "x")); }
  SECTION("a new message inside a fragmented one") {
    feed(reader, serverFrame(0x01, "a"));
    feed(reader, serverFrame(0x81, "b"));
  }
  SECTION("a fragmented control frame") { feed(reader, serverFrame(0x09, "")); }
  SECTION("a control frame over 125 bytes") { feed(reader, serverFrame(0x89, std::string(126, 'p'))); }
  SECTION("a 64-bit length with its top bit set") {
    feed(reader, bytes({0x81, 0x7F, 0x80, 0, 0, 0, 0, 0, 0, 1}));
  }
  requireFail(reader, kCloseProtocolError);
}

TEST_CASE("ReaderFailsAMessageOverItsBoundFromTheHeaders") {
  constexpr std::size_t kMax = 1000;
  SECTION("exactly the bound is a message") {
    MessageReader reader(kMax);
    feed(reader, serverFrame(0x01, std::string(600, 'a')));
    feed(reader, serverFrame(0x80, std::string(400, 'b')));
    const auto got = reader.next();
    REQUIRE(got.has_value());
    CHECK(got->data.size() == kMax);
  }
  SECTION("one frame over it, before its payload arrived") {
    MessageReader reader(kMax);
    feed(reader, bytes({0x81, 0x7E, 0x03, 0xE9}));
    requireFail(reader, kCloseTooBig);
  }
  SECTION("fragments that add up to more") {
    MessageReader reader(kMax);
    feed(reader, serverFrame(0x01, std::string(600, 'a')));
    feed(reader, bytes({0x00, 0x7E, 0x01, 0x91}));
    requireFail(reader, kCloseTooBig);
  }
  SECTION("binary messages too") {
    MessageReader reader(kMax);
    feed(reader, bytes({0x82, 0x7E, 0x03, 0xE9}));
    requireFail(reader, kCloseTooBig);
  }
  SECTION("a 64-bit length far past it") {
    MessageReader reader(kMax);
    feed(reader, bytes({0x81, 0x7F, 0x7F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}));
    requireFail(reader, kCloseTooBig);
  }
}
