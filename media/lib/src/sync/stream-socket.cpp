#include "sync/stream-socket.h"

#include <array>
#include <cerrno>
#include <cstring>
#include <fcntl.h>
#include <netdb.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <poll.h>
#include <sys/socket.h>
#include <unistd.h>

namespace cadence::sync {

namespace {

#if defined(MSG_NOSIGNAL)
constexpr int kSendFlags = MSG_NOSIGNAL;
#else
constexpr int kSendFlags = 0;
#endif

void nonBlocking(int fd) {
  ::fcntl(fd, F_SETFL, ::fcntl(fd, F_GETFL) | O_NONBLOCK);
  ::fcntl(fd, F_SETFD, FD_CLOEXEC);
}

void tuned(int fd) {
  const int on = 1;
  // a frame is small and due: nagle would hold it for the next
  ::setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &on, sizeof on);
#if defined(SO_NOSIGPIPE)
  ::setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &on, sizeof on);
#endif
}

addrinfo* resolve(const std::string& host, std::uint16_t port, bool passive) {
  addrinfo hints{};
  hints.ai_socktype = SOCK_STREAM;
  hints.ai_flags = AI_NUMERICSERV | (passive ? AI_PASSIVE : 0);
  const auto service = std::to_string(port);
  addrinfo* res = nullptr;
  if (getaddrinfo(host.empty() ? (passive ? "0.0.0.0" : nullptr) : host.c_str(), service.c_str(), &hints, &res) != 0)
    return nullptr;
  return res;
}

}  // namespace

Result<int, SocketError> listenTcp(const std::string& host, std::uint16_t port) {
  auto* res = resolve(host, port, true);
  if (res == nullptr)
    return SocketError{EINVAL};
  const int fd = ::socket(res->ai_family, SOCK_STREAM, 0);
  if (fd < 0) {
    const int e = errno;
    freeaddrinfo(res);
    return SocketError{e};
  }
  const int on = 1;
  ::setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &on, sizeof on);
  if (::bind(fd, res->ai_addr, res->ai_addrlen) != 0 || ::listen(fd, 16) != 0) {
    const int e = errno;
    freeaddrinfo(res);
    ::close(fd);
    return SocketError{e};
  }
  freeaddrinfo(res);
  nonBlocking(fd);
  return fd;
}

Result<int, SocketError> connectTcp(const std::string& host, std::uint16_t port, int timeoutMs) {
  auto* res = resolve(host, port, false);
  if (res == nullptr)
    return SocketError{EINVAL};
  const int fd = ::socket(res->ai_family, SOCK_STREAM, 0);
  if (fd < 0) {
    const int e = errno;
    freeaddrinfo(res);
    return SocketError{e};
  }
  nonBlocking(fd);
  tuned(fd);
  int rc = ::connect(fd, res->ai_addr, res->ai_addrlen);
  freeaddrinfo(res);
  if (rc != 0 && errno != EINPROGRESS) {
    const int e = errno;
    ::close(fd);
    return SocketError{e};
  }
  if (rc != 0) {
    pollfd p{.fd = fd, .events = POLLOUT, .revents = 0};
    rc = ::poll(&p, 1, timeoutMs);
    if (rc <= 0) {
      ::close(fd);
      return SocketError{rc == 0 ? ETIMEDOUT : errno};
    }
    int error = 0;
    socklen_t len = sizeof error;
    ::getsockopt(fd, SOL_SOCKET, SO_ERROR, &error, &len);
    if (error != 0) {
      ::close(fd);
      return SocketError{error};
    }
  }
  return fd;
}

std::uint16_t localPort(int fd) {
  sockaddr_storage addr{};
  socklen_t len = sizeof addr;
  if (::getsockname(fd, reinterpret_cast<sockaddr*>(&addr), &len) != 0)
    return 0;
  if (addr.ss_family == AF_INET)
    return ntohs(reinterpret_cast<const sockaddr_in*>(&addr)->sin_port);
  if (addr.ss_family == AF_INET6)
    return ntohs(reinterpret_cast<const sockaddr_in6*>(&addr)->sin6_port);
  return 0;
}

WakePipe::WakePipe() {
  int fds[2];
  if (::pipe(fds) != 0)
    return;
  nonBlocking(fds[0]);
  nonBlocking(fds[1]);
  read_ = fds[0];
  write_ = fds[1];
}

WakePipe::~WakePipe() {
  if (read_ >= 0)
    ::close(read_);
  if (write_ >= 0)
    ::close(write_);
}

void WakePipe::wake() const {
  const std::uint8_t b = 1;
  // a full pipe already wakes the poll
  static_cast<void>(::write(write_, &b, 1));
}

void WakePipe::drain() const {
  std::array<std::uint8_t, 64> buf{};
  while (::read(read_, buf.data(), buf.size()) > 0) {
  }
}

StreamConn::StreamConn(int fd, std::vector<std::uint8_t> key) : fd_(fd), sealer_(key), opener_(std::move(key)) {
  nonBlocking(fd_);
  tuned(fd_);
}

StreamConn::~StreamConn() {
  if (fd_ >= 0)
    ::close(fd_);
}

bool StreamConn::send(const SyncEnvelope& envelope) { return sealer_.seal(envelope, out_); }

bool StreamConn::flush() {
  while (sent_ < out_.size()) {
    const auto n = ::send(fd_, out_.data() + sent_, out_.size() - sent_, kSendFlags);
    if (n < 0) {
      if (errno == EAGAIN || errno == EWOULDBLOCK)
        break;
      if (errno == EINTR)
        continue;
      return false;
    }
    sent_ += static_cast<std::size_t>(n);
  }
  if (sent_ == out_.size()) {
    out_.clear();
    sent_ = 0;
  } else if (sent_ > 256 * 1024) {
    out_.erase(out_.begin(), out_.begin() + static_cast<std::ptrdiff_t>(sent_));
    sent_ = 0;
  }
  return true;
}

StreamConn::Read StreamConn::receive() {
  std::array<std::uint8_t, 16384> buf{};
  for (;;) {
    const auto n = ::recv(fd_, buf.data(), buf.size(), 0);
    if (n > 0) {
      opener_.feed(std::span<const std::uint8_t>(buf.data(), static_cast<std::size_t>(n)));
      if (static_cast<std::size_t>(n) < buf.size())
        return Read::Ok;
      continue;
    }
    if (n == 0)
      return Read::Closed;
    if (errno == EAGAIN || errno == EWOULDBLOCK)
      return Read::Ok;
    if (errno == EINTR)
      continue;
    return Read::Closed;
  }
}

void StreamConn::rekey(const std::vector<std::uint8_t>& key) {
  sealer_.rekey(key);
  opener_.rekey(key);
}

}  // namespace cadence::sync
