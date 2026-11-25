#include "dbus/auth.h"

#include <algorithm>

namespace cadence::dbus {

namespace {

constexpr char kHexDigits[] = "0123456789abcdef";

int hexValue(char c) {
  if (c >= '0' && c <= '9')
    return c - '0';
  if (c >= 'a' && c <= 'f')
    return c - 'a' + 10;
  if (c >= 'A' && c <= 'F')
    return c - 'A' + 10;
  return -1;
}

// the bytes a server address may hold unescaped
bool optionallyEscaped(char c) {
  return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '-' || c == '_' ||
         c == '/' || c == '.' || c == '\\' || c == '*';
}

std::optional<std::string> unescape(std::string_view value) {
  std::string out;
  for (std::size_t at = 0; at < value.size(); ++at) {
    const char c = value[at];
    if (c == '%') {
      if (value.size() - at < 3)
        return std::nullopt;
      const int high = hexValue(value[at + 1]);
      const int low = hexValue(value[at + 2]);
      if (high < 0 || low < 0)
        return std::nullopt;
      out += static_cast<char>(high * 16 + low);
      at += 2;
    } else if (optionallyEscaped(c)) {
      out += c;
    } else {
      return std::nullopt;
    }
  }
  return out;
}

// the path of one unix:path= address
std::optional<std::string> pathOf(std::string_view address) {
  const auto colon = address.find(':');
  if (colon == std::string_view::npos || address.substr(0, colon) != "unix")
    return std::nullopt;
  std::optional<std::string> path;
  auto pairs = address.substr(colon + 1);
  while (!pairs.empty()) {
    const auto comma = pairs.find(',');
    const auto pair = pairs.substr(0, comma);
    pairs = comma == std::string_view::npos ? std::string_view{} : pairs.substr(comma + 1);
    const auto equals = pair.find('=');
    if (equals == std::string_view::npos)
      return std::nullopt;
    if (pair.substr(0, equals) != "path")
      continue;
    path = unescape(pair.substr(equals + 1));
    if (!path || path->empty() || path->find('\0') != std::string::npos)
      return std::nullopt;
  }
  return path;
}

}  // namespace

std::optional<std::string> unixSocketPath(std::string_view address) {
  while (!address.empty()) {
    const auto semicolon = address.find(';');
    if (auto path = pathOf(address.substr(0, semicolon)))
      return path;
    address = semicolon == std::string_view::npos ? std::string_view{} : address.substr(semicolon + 1);
  }
  return std::nullopt;
}

std::string unixAddress(std::string_view path) {
  std::string address = "unix:path=";
  for (const char c : path) {
    if (c != '\\' && optionallyEscaped(c)) {
      address += c;
      continue;
    }
    const auto byte = static_cast<unsigned char>(c);
    address += '%';
    address += kHexDigits[byte >> 4];
    address += kHexDigits[byte & 0xf];
  }
  return address;
}

std::string AuthClient::greeting() const {
  std::string greeting("\0AUTH EXTERNAL ", 15);
  for (const char c : std::to_string(uid_)) {
    greeting += kHexDigits[static_cast<unsigned char>(c) >> 4];
    greeting += kHexDigits[static_cast<unsigned char>(c) & 0xf];
  }
  greeting += "\r\n";
  return greeting;
}

std::string AuthClient::receive(std::string_view bytes) {
  if (state_ != State::Waiting)
    return {};
  pending_.append(bytes);
  std::string answer;
  while (state_ == State::Waiting) {
    const auto end = pending_.find('\n');
    if (end == std::string::npos) {
      if (pending_.size() > kMaxLine)
        state_ = State::Failed;
      break;
    }
    if (end > kMaxLine || end == 0 || pending_[end - 1] != '\r') {
      state_ = State::Failed;
      break;
    }
    const std::string current = pending_.substr(0, end - 1);
    pending_.erase(0, end + 1);
    answer += line(current);
  }
  if (state_ == State::Failed) {
    pending_.clear();
    return {};
  }
  return answer;
}

std::string AuthClient::line(std::string_view line) {
  // the protocol is ascii, its commands [A-Z_]
  if (!std::all_of(line.begin(), line.end(), [](char c) { return c >= 0x20 && c < 0x7f; })) {
    state_ = State::Failed;
    return {};
  }
  const auto space = line.find(' ');
  const auto command = line.substr(0, space);
  const auto argument = space == std::string_view::npos ? std::string_view{} : line.substr(space + 1);
  if (command.empty() || !std::all_of(command.begin(), command.end(), [](char c) {
        return (c >= 'A' && c <= 'Z') || c == '_';
      })) {
    state_ = State::Failed;
    return {};
  }
  if (command == "OK") {
    if (argument.empty() || !std::all_of(argument.begin(), argument.end(), [](char c) { return hexValue(c) >= 0; })) {
      state_ = State::Failed;
      return {};
    }
    guid_ = argument;
    state_ = State::Authenticated;
    return "BEGIN\r\n";
  }
  // REJECTED, DATA and ERROR end the one mechanism there is; AGREE_UNIX_FD answers nothing this client sent
  if (command == "REJECTED" || command == "DATA" || command == "ERROR" || command == "AGREE_UNIX_FD") {
    state_ = State::Failed;
    return {};
  }
  return "ERROR\r\n";
}

}  // namespace cadence::dbus
