#pragma once

#include "audiod/framing.h"
#include "audiod/sys.h"

#include <catch2/catch_test_macros.hpp>

#include <poll.h>
#include <sys/socket.h>
#include <sys/types.h>
#include <sys/un.h>

#include <algorithm>
#include <array>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <cstring>
#include <span>
#include <string>
#include <vector>

namespace cadence::test_support {

inline audiod::Envelope ping(std::uint64_t seq) {
  audiod::Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_ping_tag;
  envelope.body.ping.seq = seq;
  return envelope;
}

inline audiod::Envelope configure(cadence_audiod_v1_Backend capture, cadence_audiod_v1_Backend output) {
  audiod::Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_configure_tag;
  auto& body = envelope.body.configure;
  body.has_capture = true;
  body.capture.backend = capture;
  body.has_output = true;
  body.output.backend = output;
  return envelope;
}

inline std::vector<std::uint8_t> encode(const audiod::Envelope& envelope) {
  std::array<std::uint8_t, audiod::kMaxEncodedBytes> out{};
  const auto written = audiod::encodeFrame(envelope, out);
  REQUIRE(written.has_value());
  return {out.begin(), out.begin() + static_cast<std::ptrdiff_t>(*written)};
}

inline void appendVarint(std::vector<std::uint8_t>& out, std::size_t value) {
  for (; value >= 0x80; value >>= 7)
    out.push_back(static_cast<std::uint8_t>(value | 0x80));
  out.push_back(static_cast<std::uint8_t>(value));
}

// field 100 is in no message, so a decoder skips it
inline constexpr std::uint8_t kUnknownFieldTag[] = {0xa2, 0x06};

// a valid frame of exactly total bytes: ping 1 followed by an unknown field that fills the rest
inline std::vector<std::uint8_t> paddedPing(std::size_t total) {
  auto frame = encode(ping(1));
  frame.insert(frame.end(), std::begin(kUnknownFieldTag), std::end(kUnknownFieldTag));
  // the length fits two varint bytes for every total these tests use
  const auto payload = total - frame.size() - 2;
  REQUIRE(payload >= 0x80);
  REQUIRE(payload < 0x4000);
  appendVarint(frame, payload);
  frame.resize(total, 0);
  return frame;
}

// a well-formed envelope carrying no body this side knows, as a newer peer could send
inline std::vector<std::uint8_t> unknownBody() {
  return {kUnknownFieldTag[0], kUnknownFieldTag[1], 0x03, 0x01, 0x02, 0x03};
}

// invalid when nothing listens at path; @name is an abstract socket
inline audiod::UniqueFd tryConnect(const std::string& path) {
  audiod::UniqueFd fd(::socket(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0));
  sockaddr_un address{};
  address.sun_family = AF_UNIX;
  if (!fd || path.empty() || path.size() >= sizeof address.sun_path)
    return {};
  std::memcpy(address.sun_path, path.data(), path.size());
  socklen_t length = sizeof address;
  if (path.front() == '@') {
    address.sun_path[0] = '\0';
    length = static_cast<socklen_t>(offsetof(sockaddr_un, sun_path) + path.size());
  }
  if (::connect(fd.get(), reinterpret_cast<const sockaddr*>(&address), length) != 0)
    return {};
  return fd;
}

inline audiod::UniqueFd connectTo(const std::string& path) {
  auto fd = tryConnect(path);
  REQUIRE(fd);
  return fd;
}

inline void sendBytes(int fd, std::span<const std::uint8_t> bytes) {
  REQUIRE(::send(fd, bytes.data(), bytes.size(), MSG_NOSIGNAL) == static_cast<ssize_t>(bytes.size()));
}

inline void sendFrame(int fd, const audiod::Envelope& envelope) { sendBytes(fd, encode(envelope)); }

struct Received {
  enum class Kind { frame, closed, timeout };
  Kind kind = Kind::timeout;
  audiod::Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
};

// the next frame within limit. audiod's trace marks (TraceMark) come between the frames a suite waits for whenever a
// capture or a stream runs, so they are skipped unless marks is set
inline Received receive(int fd, std::chrono::milliseconds limit = std::chrono::seconds(2), bool marks = false) {
  const auto deadline = std::chrono::steady_clock::now() + limit;
  for (;;) {
    Received received;
    const auto left = std::chrono::duration_cast<std::chrono::milliseconds>(deadline - std::chrono::steady_clock::now());
    pollfd ready{.fd = fd, .events = POLLIN, .revents = 0};
    if (::poll(&ready, 1, static_cast<int>(std::max<std::int64_t>(left.count(), 0))) != 1)
      return received;
    std::vector<std::uint8_t> buffer(audiod::kReceiveBufferBytes);
    const auto size = ::recv(fd, buffer.data(), buffer.size(), MSG_DONTWAIT);
    if (size <= 0) {
      received.kind = Received::Kind::closed;
      return received;
    }
    const auto frame = std::span<const std::uint8_t>(buffer.data(), static_cast<std::size_t>(size));
    REQUIRE_FALSE(audiod::decodeFrame(frame, received.envelope).has_value());
    received.kind = Received::Kind::frame;
    if (marks || received.envelope.which_body != cadence_audiod_v1_Envelope_trace_mark_tag)
      return received;
  }
}

// the next frame, which must arrive and carry the given body
inline audiod::Envelope expectFrame(int fd, pb_size_t body) {
  const auto received = receive(fd);
  REQUIRE(received.kind == Received::Kind::frame);
  REQUIRE(received.envelope.which_body == body);
  return received.envelope;
}

// Ready, and the media state audiod sends after every Ready
inline cadence_audiod_v1_Ready expectReady(int fd) {
  const auto ready = expectFrame(fd, cadence_audiod_v1_Envelope_ready_tag).body.ready;
  expectFrame(fd, cadence_audiod_v1_Envelope_media_state_tag);
  return ready;
}

inline void expectClosed(int fd) { REQUIRE(receive(fd).kind == Received::Kind::closed); }

inline void expectPong(int fd, std::uint64_t seq) {
  sendFrame(fd, ping(seq));
  CHECK(expectFrame(fd, cadence_audiod_v1_Envelope_pong_tag).body.pong.seq == seq);
}

}  // namespace cadence::test_support
