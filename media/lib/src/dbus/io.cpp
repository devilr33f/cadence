#include "dbus/io.h"

#include <poll.h>
#include <sys/socket.h>

#include <algorithm>
#include <cerrno>
#include <climits>

namespace cadence::dbus {

int pollTimeout(const Clock::time_point* deadline) {
  if (deadline == nullptr)
    return -1;
  const auto now = Clock::now();
  if (*deadline <= now)
    return 0;
  const auto left = std::chrono::ceil<std::chrono::milliseconds>(*deadline - now).count();
  return static_cast<int>(std::min<decltype(left)>(left, INT_MAX));
}

Wait waitFor(int fd, short events, int stopFd, Clock::time_point deadline) {
  for (;;) {
    if (Clock::now() >= deadline)
      return Wait::Timeout;
    // poll skips a negative stopFd
    pollfd fds[] = {{fd, events, 0}, {stopFd, POLLIN, 0}};
    if (::poll(fds, 2, pollTimeout(&deadline)) < 0) {
      if (errno == EINTR)
        continue;
      return Wait::Failed;
    }
    if (fds[1].revents != 0)
      return Wait::Stopped;
    if (fds[0].revents != 0)
      return Wait::Ready;
  }
}

Wait writeAll(int fd, std::span<const std::uint8_t> bytes, int stopFd, Clock::time_point deadline) {
  while (!bytes.empty()) {
    const auto sent = ::send(fd, bytes.data(), bytes.size(), MSG_NOSIGNAL);
    if (sent > 0) {
      bytes = bytes.subspan(static_cast<std::size_t>(sent));
      continue;
    }
    if (sent < 0 && errno == EINTR)
      continue;
    if (sent < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
      if (const auto wait = waitFor(fd, POLLOUT, stopFd, deadline); wait != Wait::Ready)
        return wait;
      continue;
    }
    return Wait::Failed;
  }
  return Wait::Ready;
}

}  // namespace cadence::dbus
