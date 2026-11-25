#include "sync/clock-socket.h"

#include <cerrno>
#include <cstring>
#include <ctime>
#include <fcntl.h>
#include <netdb.h>
#include <netinet/in.h>
#include <poll.h>
#include <sys/uio.h>
#include <unistd.h>

namespace cadence::sync {

namespace {

std::int64_t clockNs(clockid_t id) {
  timespec ts{};
  clock_gettime(id, &ts);
  return static_cast<std::int64_t>(ts.tv_sec) * 1'000'000'000 + ts.tv_nsec;
}

#if defined(__linux__)
// SCM_TIMESTAMPNS is CLOCK_REALTIME in a timespec of the kernel abi's word size (a 32-bit userland on a 64-bit
// kernel still gets the 32-bit one; a time64 libc asks for SO_TIMESTAMPNS_NEW and gets 64-bit fields): read it by its
// length, then move it onto the monotonic clock by the two clocks' distance now. that distance changes only by ntp's
// slew between the stamp and this read (ns) or by a step, which shows as one wild sample the estimator's rtt filter
// drops
std::optional<std::int64_t> kernelArrival(msghdr& msg) {
  for (cmsghdr* c = CMSG_FIRSTHDR(&msg); c != nullptr; c = CMSG_NXTHDR(&msg, c)) {
    if (c->cmsg_level != SOL_SOCKET)
      continue;
    bool ours = c->cmsg_type == SO_TIMESTAMPNS;
#if defined(SO_TIMESTAMPNS_NEW)
    ours = ours || c->cmsg_type == SO_TIMESTAMPNS_NEW;
#endif
    if (!ours)
      continue;
    const auto* data = CMSG_DATA(c);
    const auto len = c->cmsg_len - CMSG_LEN(0);
    std::int64_t sec = 0;
    std::int64_t nsec = 0;
    if (len == 16) {
      std::int64_t v[2];
      std::memcpy(v, data, sizeof v);
      sec = v[0];
      nsec = v[1];
    } else if (len == 8) {
      std::int32_t v[2];
      std::memcpy(v, data, sizeof v);
      sec = v[0];
      nsec = v[1];
    } else {
      return std::nullopt;
    }
    const auto real = clockNs(CLOCK_REALTIME);
    const auto mono = clockNs(CLOCK_MONOTONIC);
    return sec * 1'000'000'000 + nsec - (real - mono);
  }
  return std::nullopt;
}
#endif

}  // namespace

std::int64_t monoNs() { return clockNs(CLOCK_MONOTONIC); }

std::optional<ClockPeer> ClockSocket::resolve(const std::string& host, std::uint16_t port) {
  addrinfo hints{};
  hints.ai_socktype = SOCK_DGRAM;
  hints.ai_flags = AI_NUMERICSERV;
  if (host.empty())
    hints.ai_flags |= AI_PASSIVE;
  const auto service = std::to_string(port);
  addrinfo* res = nullptr;
  if (getaddrinfo(host.empty() ? nullptr : host.c_str(), service.c_str(), &hints, &res) != 0 || res == nullptr)
    return std::nullopt;
  ClockPeer peer;
  std::memcpy(&peer.addr, res->ai_addr, static_cast<std::size_t>(res->ai_addrlen));
  peer.len = static_cast<socklen_t>(res->ai_addrlen);
  freeaddrinfo(res);
  return peer;
}

Result<ClockSocket, int> ClockSocket::open(const std::string& host, std::uint16_t port) {
  auto local = resolve(host.empty() ? std::string("0.0.0.0") : host, port);
  if (!local)
    return EINVAL;
  const int fd = ::socket(local->addr.ss_family, SOCK_DGRAM, 0);
  if (fd < 0)
    return errno;
  ::fcntl(fd, F_SETFD, FD_CLOEXEC);
  ClockSocket sock(fd);
#if defined(__linux__)
  const int on = 1;
  // without stamps the read's return is used: worse, not wrong
  ::setsockopt(fd, SOL_SOCKET, SO_TIMESTAMPNS, &on, sizeof on);
#endif
  if (::bind(fd, reinterpret_cast<const sockaddr*>(&local->addr), local->len) != 0)
    return errno;
  return sock;
}

ClockSocket::ClockSocket(ClockSocket&& other) noexcept : fd_(other.fd_), lastError_(other.lastError_) {
  other.fd_ = -1;
}

ClockSocket& ClockSocket::operator=(ClockSocket&& other) noexcept {
  if (this != &other) {
    if (fd_ >= 0)
      ::close(fd_);
    fd_ = other.fd_;
    lastError_ = other.lastError_;
    other.fd_ = -1;
  }
  return *this;
}

ClockSocket::~ClockSocket() {
  if (fd_ >= 0)
    ::close(fd_);
}

bool ClockSocket::send(std::span<const std::uint8_t> packet, const ClockPeer& to) {
  const auto n = ::sendto(fd_, packet.data(), packet.size(), 0, reinterpret_cast<const sockaddr*>(&to.addr), to.len);
  if (n < 0) {
    lastError_ = errno;
    return false;
  }
  return true;
}

std::optional<ClockDatagram> ClockSocket::receive(std::span<std::uint8_t> buf, int timeoutMs) {
  pollfd p{.fd = fd_, .events = POLLIN, .revents = 0};
  const int ready = ::poll(&p, 1, timeoutMs);
  if (ready <= 0) {
    if (ready < 0)
      lastError_ = errno;
    return std::nullopt;
  }
  ClockDatagram d;
  iovec iov{.iov_base = buf.data(), .iov_len = buf.size()};
  alignas(cmsghdr) std::uint8_t control[128];
  msghdr msg{};
  msg.msg_name = &d.from.addr;
  msg.msg_namelen = sizeof d.from.addr;
  msg.msg_iov = &iov;
  msg.msg_iovlen = 1;
  msg.msg_control = control;
  msg.msg_controllen = sizeof control;
  const auto n = ::recvmsg(fd_, &msg, 0);
  d.readNs = monoNs();
  if (n < 0) {
    lastError_ = errno;
    return std::nullopt;
  }
  d.size = static_cast<std::size_t>(n);
  d.from.len = msg.msg_namelen;
  d.arrivedNs = d.readNs;
#if defined(__linux__)
  if (const auto k = kernelArrival(msg)) {
    d.arrivedNs = *k;
    d.kernelStamp = true;
  }
#endif
  return d;
}

std::uint16_t ClockSocket::port() const {
  sockaddr_storage addr{};
  socklen_t len = sizeof addr;
  if (::getsockname(fd_, reinterpret_cast<sockaddr*>(&addr), &len) != 0)
    return 0;
  if (addr.ss_family == AF_INET)
    return ntohs(reinterpret_cast<const sockaddr_in*>(&addr)->sin_port);
  if (addr.ss_family == AF_INET6)
    return ntohs(reinterpret_cast<const sockaddr_in6*>(&addr)->sin6_port);
  return 0;
}

}  // namespace cadence::sync
