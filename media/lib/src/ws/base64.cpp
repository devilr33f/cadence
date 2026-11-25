#include "ws/base64.h"

#include <string_view>

namespace cadence::ws {

std::string base64(std::span<const std::uint8_t> bytes) {
  constexpr std::string_view kAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
  std::string out;
  out.reserve((bytes.size() + 2) / 3 * 4);
  for (std::size_t i = 0; i < bytes.size(); i += 3) {
    const std::size_t left = bytes.size() - i;
    const std::uint32_t group = (std::uint32_t{bytes[i]} << 16) |
                                (left > 1 ? std::uint32_t{bytes[i + 1]} << 8 : 0U) |
                                (left > 2 ? std::uint32_t{bytes[i + 2]} : 0U);
    out.push_back(kAlphabet[(group >> 18) & 0x3F]);
    out.push_back(kAlphabet[(group >> 12) & 0x3F]);
    out.push_back(left > 1 ? kAlphabet[(group >> 6) & 0x3F] : '=');
    out.push_back(left > 2 ? kAlphabet[group & 0x3F] : '=');
  }
  return out;
}

}  // namespace cadence::ws
