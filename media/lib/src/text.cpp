#include "text.h"

namespace cadence::text {

namespace {

char printable(char c) {
  const auto byte = static_cast<unsigned char>(c);
  return byte >= 0x20 && byte < 0x7F ? c : '?';
}

// the bytes of the well-formed utf-8 character text starts with, 0 when it starts none: no overlong form, surrogate
// or code point past U+10FFFF, which a strict decoder (go's protobuf) refuses
std::size_t sequenceLength(std::string_view text) {
  const auto lead = static_cast<unsigned char>(text.front());
  std::size_t length = 0;
  char32_t point = 0;
  if (lead < 0x80)
    return 1;
  if (lead >= 0xc2 && lead <= 0xdf) {
    length = 2;
    point = lead & 0x1fU;
  } else if (lead >= 0xe0 && lead <= 0xef) {
    length = 3;
    point = lead & 0x0fU;
  } else if (lead >= 0xf0 && lead <= 0xf4) {
    length = 4;
    point = lead & 0x07U;
  } else {
    return 0;
  }
  if (text.size() < length)
    return 0;
  for (std::size_t index = 1; index < length; ++index) {
    const auto next = static_cast<unsigned char>(text[index]);
    if ((next & 0xc0U) != 0x80U)
      return 0;
    point = (point << 6) | (next & 0x3fU);
  }
  if (length == 3 && (point < 0x800 || (point >= 0xd800 && point <= 0xdfff)))
    return 0;
  if (length == 4 && (point < 0x10000 || point > 0x10ffff))
    return 0;
  return length;
}

}  // namespace

std::string boundedMessage(const char* message) {
  std::string text;
  for (; message != nullptr && *message != '\0' && text.size() < kMaxBoundedLength; ++message)
    text.push_back(printable(*message));
  return text;
}

std::string boundedUtf8(std::string_view text, std::size_t maxBytes) {
  constexpr std::string_view replacement = "\xef\xbf\xbd";
  std::string bounded;
  while (!text.empty()) {
    const auto length = sequenceLength(text);
    const auto piece = length == 0 ? replacement : text.substr(0, length);
    if (bounded.size() + piece.size() > maxBytes)
      break;
    bounded.append(piece);
    text.remove_prefix(length == 0 ? 1 : length);
  }
  return bounded;
}

}  // namespace cadence::text
