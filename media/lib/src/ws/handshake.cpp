#include "ws/handshake.h"

#include "ws/base64.h"
#include "ws/sha1.h"

namespace cadence::ws {

namespace {

constexpr std::string_view kGuid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11";
constexpr std::string_view kSwitching = "HTTP/1.1 101";

char lower(char c) { return c >= 'A' && c <= 'Z' ? static_cast<char>(c - 'A' + 'a') : c; }

bool sameText(std::string_view a, std::string_view b) {
  if (a.size() != b.size())
    return false;
  for (std::size_t i = 0; i < a.size(); ++i)
    if (lower(a[i]) != lower(b[i]))
      return false;
  return true;
}

std::string_view trim(std::string_view text) {
  while (!text.empty() && (text.front() == ' ' || text.front() == '\t'))
    text.remove_prefix(1);
  while (!text.empty() && (text.back() == ' ' || text.back() == '\t'))
    text.remove_suffix(1);
  return text;
}

// whether a comma-separated header value holds token, in any case
bool hasToken(std::string_view list, std::string_view token) {
  for (;;) {
    const auto comma = list.find(',');
    if (sameText(trim(list.substr(0, comma)), token))
      return true;
    if (comma == std::string_view::npos)
      return false;
    list.remove_prefix(comma + 1);
  }
}

}  // namespace

std::string handshakeRequest(std::uint16_t port, std::string_view key) {
  std::string request = "GET / HTTP/1.1\r\nHost: 127.0.0.1:";
  request += std::to_string(port);
  request += "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: ";
  request += key;
  request += "\r\nSec-WebSocket-Version: 13\r\n\r\n";
  return request;
}

std::string acceptFor(std::string_view key) {
  std::string joined(key);
  joined += kGuid;
  return base64(sha1(joined));
}

HandshakeCheck checkResponse(std::string_view received, std::string_view key) {
  const auto end = received.find("\r\n\r\n");
  if (end == std::string_view::npos)
    return {received.size() >= kMaxResponseHead ? HandshakeStatus::Refused : HandshakeStatus::Incomplete, 0};
  const std::size_t headBytes = end + 4;
  if (headBytes > kMaxResponseHead)
    return {HandshakeStatus::Refused, 0};
  // every line of it, the last header's included, ends in crlf
  const auto head = received.substr(0, end + 2);
  const auto statusEnd = head.find("\r\n");
  const auto status = head.substr(0, statusEnd);
  if (!status.starts_with(kSwitching) || (status.size() > kSwitching.size() && status[kSwitching.size()] != ' '))
    return {HandshakeStatus::Refused, 0};
  const auto accept = acceptFor(key);
  bool upgrade = false;
  bool connection = false;
  bool accepted = false;
  for (auto at = statusEnd + 2; at < head.size();) {
    const auto lineEnd = head.find("\r\n", at);
    const auto line = head.substr(at, lineEnd - at);
    at = lineEnd + 2;
    const auto colon = line.find(':');
    if (colon == std::string_view::npos)
      return {HandshakeStatus::Refused, 0};
    const auto name = line.substr(0, colon);
    const auto value = trim(line.substr(colon + 1));
    if (sameText(name, "upgrade")) {
      if (!sameText(value, "websocket"))
        return {HandshakeStatus::Refused, 0};
      upgrade = true;
    } else if (sameText(name, "connection")) {
      connection = connection || hasToken(value, "upgrade");
    } else if (sameText(name, "sec-websocket-accept")) {
      if (value != accept)
        return {HandshakeStatus::Refused, 0};
      accepted = true;
    } else if (sameText(name, "sec-websocket-extensions") || sameText(name, "sec-websocket-protocol")) {
      return {HandshakeStatus::Refused, 0};
    }
  }
  if (!upgrade || !connection || !accepted)
    return {HandshakeStatus::Refused, 0};
  return {HandshakeStatus::Accepted, headBytes};
}

}  // namespace cadence::ws
