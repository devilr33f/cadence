#include "sync/clock-wire.h"

#include "sync/sha256.h"

#include <cstring>

namespace cadence::sync {

namespace {

constexpr std::uint32_t kMagicRequest = 0x43434b31;  // "CCK1"
constexpr std::uint32_t kMagicReply = 0x43434b32;    // "CCK2"

void put32(std::uint8_t* p, std::uint32_t v) {
  for (std::size_t i = 0; i < 4; ++i)
    p[i] = static_cast<std::uint8_t>(v >> (24 - 8 * i));
}

void put64(std::uint8_t* p, std::int64_t s) {
  const auto v = static_cast<std::uint64_t>(s);
  for (std::size_t i = 0; i < 8; ++i)
    p[i] = static_cast<std::uint8_t>(v >> (56 - 8 * i));
}

std::uint32_t get32(const std::uint8_t* p) {
  std::uint32_t v = 0;
  for (std::size_t i = 0; i < 4; ++i)
    v = (v << 8) | p[i];
  return v;
}

std::int64_t get64(const std::uint8_t* p) {
  std::uint64_t v = 0;
  for (std::size_t i = 0; i < 8; ++i)
    v = (v << 8) | p[i];
  return static_cast<std::int64_t>(v);
}

template <std::size_t N>
void seal(std::span<const std::uint8_t> key, std::array<std::uint8_t, N>& b) {
  const auto tag = hmacSha256(key, std::span<const std::uint8_t>(b.data(), N - kClockTagBytes));
  std::memcpy(b.data() + N - kClockTagBytes, tag.data(), kClockTagBytes);
}

bool open(std::span<const std::uint8_t> key, std::span<const std::uint8_t> p, std::size_t size, std::uint32_t magic) {
  if (p.size() != size || get32(p.data()) != magic)
    return false;
  const auto body = p.first(size - kClockTagBytes);
  const auto tag = hmacSha256(key, body);
  return equalConstantTime(std::span<const std::uint8_t>(tag.data(), kClockTagBytes), p.last(kClockTagBytes));
}

}  // namespace

std::array<std::uint8_t, kClockRequestBytes> encodeRequest(std::span<const std::uint8_t> key, const ClockRequest& r) {
  std::array<std::uint8_t, kClockRequestBytes> b{};
  put32(b.data(), kMagicRequest);
  put32(b.data() + 4, r.seq);
  put64(b.data() + 8, r.t1);
  seal(key, b);
  return b;
}

std::array<std::uint8_t, kClockReplyBytes> encodeReply(std::span<const std::uint8_t> key, const ClockReply& r) {
  std::array<std::uint8_t, kClockReplyBytes> b{};
  put32(b.data(), kMagicReply);
  put32(b.data() + 4, r.seq);
  put64(b.data() + 8, r.t1);
  put64(b.data() + 16, r.t2);
  put64(b.data() + 24, r.t3);
  seal(key, b);
  return b;
}

std::optional<ClockRequest> decodeRequest(std::span<const std::uint8_t> key, std::span<const std::uint8_t> p) {
  if (!open(key, p, kClockRequestBytes, kMagicRequest))
    return std::nullopt;
  return ClockRequest{.seq = get32(p.data() + 4), .t1 = get64(p.data() + 8)};
}

std::optional<ClockReply> decodeReply(std::span<const std::uint8_t> key, std::span<const std::uint8_t> p) {
  if (!open(key, p, kClockReplyBytes, kMagicReply))
    return std::nullopt;
  return ClockReply{
      .seq = get32(p.data() + 4), .t1 = get64(p.data() + 8), .t2 = get64(p.data() + 16), .t3 = get64(p.data() + 24)};
}

}  // namespace cadence::sync
