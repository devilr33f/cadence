#pragma once

#include <array>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <span>

namespace cadence::sync {

// the group clock's packets, one udp datagram each, big-endian: magic, seq, t1 (a request), then t2 and t3 (a reply),
// then the first 16 bytes of hmac-sha256 over what precedes it under the group key. fixed binary rather than
// protobuf: 32 or 48 bytes that are parsed on the timing path, with no field that could ever be optional
inline constexpr std::size_t kClockTagBytes = 16;
inline constexpr std::size_t kClockRequestBytes = 16 + kClockTagBytes;
inline constexpr std::size_t kClockReplyBytes = 32 + kClockTagBytes;
// a key shorter than this is refused where a group is configured
inline constexpr std::size_t kClockMinKeyBytes = 16;

// a member's probe: t1 is its CLOCK_MONOTONIC as it sent
struct ClockRequest {
  std::uint32_t seq = 0;
  std::int64_t t1 = 0;
};

// the leader's answer: t1 echoed, t2 when the request arrived, t3 as the reply left, on the leader's clock
struct ClockReply {
  std::uint32_t seq = 0;
  std::int64_t t1 = 0;
  std::int64_t t2 = 0;
  std::int64_t t3 = 0;
};

std::array<std::uint8_t, kClockRequestBytes> encodeRequest(std::span<const std::uint8_t> key, const ClockRequest& r);
std::array<std::uint8_t, kClockReplyBytes> encodeReply(std::span<const std::uint8_t> key, const ClockReply& r);
// none for a packet of another size or kind, or one whose tag the key does not make
std::optional<ClockRequest> decodeRequest(std::span<const std::uint8_t> key, std::span<const std::uint8_t> packet);
std::optional<ClockReply> decodeReply(std::span<const std::uint8_t> key, std::span<const std::uint8_t> packet);

}  // namespace cadence::sync
