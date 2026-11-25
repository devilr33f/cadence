#pragma once

#include <cstddef>
#include <string>
#include <string_view>

namespace cadence::text {

inline constexpr std::size_t kMaxBoundedLength = 256;

// printable ascii, at most kMaxBoundedLength characters and read no further, so an unterminated
// foreign string cannot run away; safe to log
std::string boundedMessage(const char* message);

// valid utf-8 of at most maxBytes: cut before the first character that does not fit, each byte that starts no valid
// sequence replaced by U+FFFD, since a protobuf string must be utf-8 for its reader to decode the message
std::string boundedUtf8(std::string_view text, std::size_t maxBytes);

}  // namespace cadence::text
