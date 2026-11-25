#pragma once

#include <array>
#include <cstddef>
#include <cstdint>
#include <span>
#include <string_view>

namespace cadence::ws {

inline constexpr std::size_t kSha1Bytes = 20;
using Sha1Digest = std::array<std::uint8_t, kSha1Bytes>;

// sha-1 (fips 180-4) for the handshake's accept value, which rfc 6455 defines with it; never for security
Sha1Digest sha1(std::span<const std::uint8_t> bytes);
Sha1Digest sha1(std::string_view text);

}  // namespace cadence::ws
