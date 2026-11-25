#include "audiod/soloist-output.h"

#include <unistd.h>

#include <algorithm>
#include <array>
#include <cerrno>

namespace cadence::audiod {

namespace {

constexpr std::string_view kLoginFailed = "login failed";
// the logger's prefix on every line it writes, d for a digit
constexpr std::string_view kStamp = "dddd-dd-dd dd:dd:dd.ddd: ";
constexpr std::size_t kReadChunk = 1024;

bool stamped(std::string_view line) {
  if (line.size() < kStamp.size())
    return false;
  for (std::size_t i = 0; i < kStamp.size(); ++i) {
    const char byte = line[i];
    if (kStamp[i] == 'd' ? (byte < '0' || byte > '9') : byte != kStamp[i])
      return false;
  }
  return true;
}

}  // namespace

bool loginFailed(std::string_view line) {
  const auto message = stamped(line) ? line.substr(kStamp.size()) : line;
  return message.starts_with(kLoginFailed);
}

bool OutputScanner::feed(std::span<const char> bytes) {
  bool found = false;
  for (const char byte : bytes) {
    if (byte == '\n') {
      found = endLine() || found;
    } else if (skipping_) {
      continue;
    } else if (line_.size() < kOutputLineCap) {
      line_.push_back(byte);
    } else {
      // an over-long line counts by its first 4 KiB, and the rest is dropped unread
      found = report(loginFailed(line_)) || found;
      line_.clear();
      skipping_ = true;
    }
  }
  return found;
}

bool OutputScanner::finish() { return endLine(); }

bool OutputScanner::endLine() {
  const bool found = report(!skipping_ && loginFailed(line_));
  line_.clear();
  skipping_ = false;
  return found;
}

bool OutputScanner::report(bool found) {
  if (!found || reported_)
    return false;
  reported_ = true;
  return true;
}

Drained drainOutput(int fd, OutputScanner& scanner, std::size_t budget,
                    const std::function<void(std::span<const char>)>& also) {
  Drained drained;
  std::array<char, kReadChunk> chunk{};
  for (std::size_t read = 0; read < budget;) {
    const auto size = ::read(fd, chunk.data(), std::min(chunk.size(), budget - read));
    if (size < 0 && errno == EINTR)
      continue;
    if (size < 0 && errno == EAGAIN)
      break;
    if (size <= 0) {
      drained.loginFailed = scanner.finish() || drained.loginFailed;
      drained.closed = true;
      break;
    }
    read += static_cast<std::size_t>(size);
    const std::span<const char> bytes(chunk.data(), static_cast<std::size_t>(size));
    drained.loginFailed = scanner.feed(bytes) || drained.loginFailed;
    if (also)
      also(bytes);
  }
  return drained;
}

}  // namespace cadence::audiod
