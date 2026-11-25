#pragma once

#include <cstdint>
#include <span>
#include <string>

namespace cadence::ws {

// rfc 4648 base64, padded
std::string base64(std::span<const std::uint8_t> bytes);

}  // namespace cadence::ws
