#include "url/icy.h"

namespace cadence::url {

std::optional<std::string> icyStreamTitle(std::string_view packet) {
  constexpr std::string_view key = "StreamTitle='";
  const auto start = packet.find(key);
  if (start == std::string_view::npos)
    return std::nullopt;
  const auto value = packet.substr(start + key.size());
  auto end = value.find("';");
  if (end == std::string_view::npos) {
    end = value.rfind('\'');
    if (end == std::string_view::npos)
      end = value.size();
  }
  return std::string(value.substr(0, end));
}

IcyTitle splitIcyTitle(std::string_view streamTitle) {
  constexpr std::string_view separator = " - ";
  const auto at = streamTitle.find(separator);
  if (at == std::string_view::npos)
    return {{}, std::string(streamTitle)};
  return {std::string(streamTitle.substr(0, at)), std::string(streamTitle.substr(at + separator.size()))};
}

}  // namespace cadence::url
