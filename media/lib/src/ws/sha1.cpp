#include "ws/sha1.h"

#include <bit>

namespace cadence::ws {

namespace {

constexpr std::size_t kBlockBytes = 64;

class Sha1 {
 public:
  void update(std::span<const std::uint8_t> bytes) {
    for (const auto byte : bytes) {
      block_[used_++] = byte;
      if (used_ == kBlockBytes) {
        compress();
        used_ = 0;
      }
    }
    length_ += bytes.size();
  }

  Sha1Digest finish() {
    const std::uint64_t bits = length_ * 8;
    const std::uint8_t one = 0x80;
    update(std::span(&one, 1));
    const std::uint8_t zero = 0;
    while (used_ != kBlockBytes - 8)
      update(std::span(&zero, 1));
    for (int shift = 56; shift >= 0; shift -= 8) {
      const auto byte = static_cast<std::uint8_t>(bits >> shift);
      update(std::span(&byte, 1));
    }
    Sha1Digest digest{};
    for (std::size_t i = 0; i < state_.size(); ++i)
      for (std::size_t b = 0; b < 4; ++b)
        digest[i * 4 + b] = static_cast<std::uint8_t>(state_[i] >> (24 - 8 * b));
    return digest;
  }

 private:
  void compress() {
    std::array<std::uint32_t, 80> w{};
    for (std::size_t i = 0; i < 16; ++i)
      w[i] = (std::uint32_t{block_[i * 4]} << 24) | (std::uint32_t{block_[i * 4 + 1]} << 16) |
             (std::uint32_t{block_[i * 4 + 2]} << 8) | std::uint32_t{block_[i * 4 + 3]};
    for (std::size_t i = 16; i < w.size(); ++i)
      w[i] = std::rotl(w[i - 3] ^ w[i - 8] ^ w[i - 14] ^ w[i - 16], 1);
    auto [a, b, c, d, e] = state_;
    for (std::size_t i = 0; i < w.size(); ++i) {
      std::uint32_t f = 0;
      std::uint32_t k = 0;
      if (i < 20) {
        f = (b & c) | (~b & d);
        k = 0x5A827999;
      } else if (i < 40) {
        f = b ^ c ^ d;
        k = 0x6ED9EBA1;
      } else if (i < 60) {
        f = (b & c) | (b & d) | (c & d);
        k = 0x8F1BBCDC;
      } else {
        f = b ^ c ^ d;
        k = 0xCA62C1D6;
      }
      const std::uint32_t next = std::rotl(a, 5) + f + e + k + w[i];
      e = d;
      d = c;
      c = std::rotl(b, 30);
      b = a;
      a = next;
    }
    state_[0] += a;
    state_[1] += b;
    state_[2] += c;
    state_[3] += d;
    state_[4] += e;
  }

  std::array<std::uint32_t, 5> state_{0x67452301, 0xEFCDAB89, 0x98BADCFE, 0x10325476, 0xC3D2E1F0};
  std::array<std::uint8_t, kBlockBytes> block_{};
  std::size_t used_ = 0;
  std::uint64_t length_ = 0;
};

}  // namespace

Sha1Digest sha1(std::span<const std::uint8_t> bytes) {
  Sha1 hash;
  hash.update(bytes);
  return hash.finish();
}

Sha1Digest sha1(std::string_view text) {
  return sha1(std::span(reinterpret_cast<const std::uint8_t*>(text.data()), text.size()));
}

}  // namespace cadence::ws
