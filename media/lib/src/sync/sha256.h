#pragma once

#include <array>
#include <cstddef>
#include <cstdint>
#include <span>

namespace cadence::sync {

using Sha256Digest = std::array<std::uint8_t, 32>;

// fips 180-4. here so the clock's packet tags need no crypto library: openssl is only in the url builds, android has
// none, and the tag is the one hash this plane needs
class Sha256 {
 public:
  Sha256();
  void update(std::span<const std::uint8_t> data);
  Sha256Digest finish();

 private:
  void block(const std::uint8_t* p);

  std::array<std::uint32_t, 8> h_;
  std::array<std::uint8_t, 64> buf_{};
  std::size_t used_ = 0;
  std::uint64_t bytes_ = 0;
};

Sha256Digest sha256(std::span<const std::uint8_t> data);

// rfc 2104
Sha256Digest hmacSha256(std::span<const std::uint8_t> key, std::span<const std::uint8_t> data);

// compares in time independent of where they differ
bool equalConstantTime(std::span<const std::uint8_t> a, std::span<const std::uint8_t> b);

}  // namespace cadence::sync
