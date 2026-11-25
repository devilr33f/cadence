#include "audiod/airplay-config.h"

#include "dbus/auth.h"

#include <net/if.h>

#include <algorithm>
#include <cstdio>
#include <filesystem>
#include <fstream>
#include <string_view>
#include <system_error>
#include <vector>

namespace cadence::audiod::airplay {

namespace {

int hexDigit(char c) {
  if (c >= '0' && c <= '9')
    return c - '0';
  if (c >= 'a' && c <= 'f')
    return c - 'a' + 10;
  if (c >= 'A' && c <= 'F')
    return c - 'A' + 10;
  return -1;
}

// c0, del, and c1 as utf-8 spells it (c2 80 to c2 9f)
bool hasControl(std::string_view value) {
  for (std::size_t i = 0; i < value.size(); ++i) {
    const auto byte = static_cast<unsigned char>(value[i]);
    if (byte < 0x20 || byte == 0x7f)
      return true;
    if (byte == 0xc2 && i + 1 < value.size() && static_cast<unsigned char>(value[i + 1]) >= 0x80 &&
        static_cast<unsigned char>(value[i + 1]) <= 0x9f)
      return true;
  }
  return false;
}

std::string xml(std::string_view value) {
  std::string out;
  for (const char c : value) {
    switch (c) {
      case '&':
        out += "&amp;";
        break;
      case '<':
        out += "&lt;";
        break;
      case '>':
        out += "&gt;";
        break;
      case '"':
        out += "&quot;";
        break;
      case '\'':
        out += "&apos;";
        break;
      default:
        out += c;
    }
  }
  return out;
}

std::string hex12(std::uint64_t id) {
  char text[13]{};
  std::snprintf(text, sizeof text, "%012llX", static_cast<unsigned long long>(id));
  return text;
}

std::optional<std::string> firstLine(const std::filesystem::path& path) {
  std::ifstream in(path);
  std::string line;
  if (!in || !std::getline(in, line))
    return std::nullopt;
  return line;
}

bool loopback(const std::filesystem::path& interface) {
  const auto flags = firstLine(interface / "flags");
  if (!flags)
    return true;
  std::string_view text = *flags;
  if (text.starts_with("0x"))
    text.remove_prefix(2);
  if (text.empty())
    return true;
  unsigned long value = 0;
  for (const char c : text) {
    const int digit = hexDigit(c);
    if (digit < 0 || value > (~0UL >> 4))
      return true;
    value = value * 16 + static_cast<unsigned long>(digit);
  }
  return (value & IFF_LOOPBACK) != 0;
}

}  // namespace

std::optional<std::uint64_t> parseAddress(std::string_view text) {
  while (!text.empty() && (text.back() == '\n' || text.back() == ' '))
    text.remove_suffix(1);
  // six two-digit octets and the five colons between them
  if (text.size() != 17)
    return std::nullopt;
  std::uint64_t id = 0;
  for (std::size_t octet = 0; octet < 6; ++octet) {
    const auto at = octet * 3;
    if (octet > 0 && text[at - 1] != ':')
      return std::nullopt;
    const int high = hexDigit(text[at]);
    const int low = hexDigit(text[at + 1]);
    if (high < 0 || low < 0)
      return std::nullopt;
    id = (id << 8) | static_cast<std::uint64_t>(high * 16 + low);
  }
  if (id == 0)
    return std::nullopt;
  return id;
}

std::optional<std::uint64_t> receiverId(const std::string& interfaces) {
  std::error_code error;
  std::vector<std::filesystem::path> found;
  for (std::filesystem::directory_iterator it(interfaces, error), end; !error && it != end; it.increment(error))
    found.push_back(it->path());
  if (error)
    return std::nullopt;
  std::sort(found.begin(), found.end());
  for (const auto& interface : found) {
    if (loopback(interface))
      continue;
    if (const auto address = firstLine(interface / "address"))
      if (const auto id = parseAddress(*address))
        return id;
  }
  return std::nullopt;
}

std::optional<std::string> quote(std::string_view value) {
  if (hasControl(value))
    return std::nullopt;
  std::string quoted;
  for (const char c : value) {
    if (c == '\\' || c == '"')
      quoted += '\\';
    quoted += c;
  }
  std::string out;
  for (std::size_t at = 0; at < quoted.size(); ++at) {
    if (quoted.compare(at, 2, "${") == 0)
      out += '$';
    out += quoted[at];
  }
  return out;
}

std::string busConfig(const std::string& socket, uid_t uid) {
  return "<!DOCTYPE busconfig PUBLIC \"-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN\" "
         "\"http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd\">\n"
         "<busconfig>\n"
         "  <type>system</type>\n"
         "  <listen>" +
         xml(dbus::unixAddress(socket)) +
         "</listen>\n"
         "  <auth>EXTERNAL</auth>\n"
         "  <policy context=\"default\">\n"
         "    <deny user=\"*\"/>\n"
         "    <allow user=\"" +
         std::to_string(uid) +
         "\"/>\n"
         "    <allow own=\"*\"/>\n"
         "    <allow send_destination=\"*\"/>\n"
         "    <allow receive_sender=\"*\"/>\n"
         "    <allow eavesdrop=\"true\"/>\n"
         "  </policy>\n"
         "</busconfig>\n";
}

std::string avahiConfig(std::uint64_t receiverId) {
  return "[server]\nhost-name=cadence-airplay-" + hex12(receiverId) +
         "\nuse-ipv4=yes\nuse-ipv6=yes\nenable-dbus=yes\ndisallow-other-stacks=no\nratelimit-interval-usec=1000000\n"
         "ratelimit-burst=1000\n\n"
         "[wide-area]\nenable-wide-area=no\n\n"
         "[publish]\ndisable-publishing=no\npublish-addresses=yes\npublish-hinfo=no\npublish-workstation=no\n"
         "publish-domain=no\npublish-resolv-conf-dns-servers=no\n\n"
         "[reflector]\nenable-reflector=no\n";
}

std::optional<std::string> shairportConfig(std::string_view name, std::uint64_t receiverId,
                                           const std::string& audioFifo, const std::string& metadataFifo) {
  const auto quotedName = quote(name);
  const auto audio = quote(audioFifo);
  const auto metadata = quote(metadataFifo);
  if (!quotedName || !audio || !metadata)
    return std::nullopt;
  return "general =\n{\n"
         "  name = \"" +
         *quotedName +
         "\";\n"
         "  service_type = \"airplay2\";\n"
         "  interpolation = \"vernier\";\n"
         "  output_backend = \"pipe\";\n"
         "  mdns_backend = \"avahi\";\n"
         "  dbus_service_bus = \"system\";\n"
         "  ignore_volume_control = \"yes\";\n"
         "  airplay_device_id = 0x" +
         hex12(receiverId) +
         "L;\n"
         "  audio_backend_buffer_desired_length_in_seconds = 0.1;\n"
         "};\n\n"
         "pipe =\n{\n"
         "  name = \"" +
         *audio +
         "\";\n"
         "  output_rate = 48000;\n"
         "  output_format = \"S16_LE\";\n"
         "  output_channels = 2;\n"
         "};\n\n"
         "metadata =\n{\n"
         "  enabled = \"yes\";\n"
         "  include_cover_art = \"no\";\n"
         "  cover_art_cache_directory = \"\";\n"
         "  pipe_name = \"" +
         *metadata +
         "\";\n"
         "};\n";
}

}  // namespace cadence::audiod::airplay
