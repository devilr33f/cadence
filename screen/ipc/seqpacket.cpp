#include "ipc/seqpacket.h"

#include <cerrno>
#include <cstring>
#include <fcntl.h>
#include <poll.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

namespace cadence::screen::ipc {

namespace {

std::string errnoToken(int err) {
  switch (err) {
    case ENOENT:
      return "enoent";
    case ECONNREFUSED:
      return "econnrefused";
    case EACCES:
      return "eacces";
    case ENAMETOOLONG:
      return "enametoolong";
    default:
      return "errno_" + std::to_string(err);
  }
}

}  // namespace

bool fillAddress(const std::string& path, void* out, unsigned* length) {
  auto* addr = static_cast<sockaddr_un*>(out);
  std::memset(addr, 0, sizeof *addr);
  addr->sun_family = AF_UNIX;
  if (path.empty() || path.size() >= sizeof addr->sun_path) return false;
  std::memcpy(addr->sun_path, path.data(), path.size());
  if (path[0] == '@') {
    // abstract: a leading nul and no terminator; the length says where the name ends
    addr->sun_path[0] = '\0';
    *length = static_cast<unsigned>(offsetof(sockaddr_un, sun_path) + path.size());
  } else {
    *length = static_cast<unsigned>(sizeof *addr);
  }
  return true;
}

#if defined(__APPLE__)

// macos has no SOCK_SEQPACKET for AF_UNIX: the same interface over SOCK_STREAM, one 4-byte big-endian length ahead
// of each frame. only the sim's own ends ever speak it (the screen and its fake cadenced, both built from this
// tree); linux and android keep the kernel's seqpacket, byte for byte as before

namespace {

// the largest frame either side will read; the codec's envelopes stay far under it
constexpr std::uint32_t kMaxFrame = 8u << 20;

bool sendAll(int fd, const std::uint8_t* data, std::size_t size) {
  std::size_t done = 0;
  while (done < size) {
    const ssize_t n = ::send(fd, data + done, size - done, MSG_NOSIGNAL);
    if (n >= 0) {
      done += static_cast<std::size_t>(n);
      continue;
    }
    if (errno == EINTR) continue;
    return false;
  }
  return true;
}

// bytes whole, or false when the peer went or the timeout passed
bool recvAll(int fd, std::uint8_t* data, std::size_t size, int timeoutMs) {
  std::size_t done = 0;
  while (done < size) {
    pollfd p{fd, POLLIN, 0};
    const int ready = ::poll(&p, 1, timeoutMs);
    if (ready == 0) return false;
    if (ready < 0 && errno != EINTR) return false;
    const ssize_t n = ::recv(fd, data + done, size - done, 0);
    if (n == 0) return false;
    if (n < 0) {
      if (errno == EINTR || errno == EAGAIN || errno == EWOULDBLOCK) continue;
      return false;
    }
    done += static_cast<std::size_t>(n);
  }
  return true;
}

}  // namespace

SeqpacketSocket::~SeqpacketSocket() { close(); }

SeqpacketSocket::SeqpacketSocket(SeqpacketSocket&& other) noexcept : fd_(other.fd_) { other.fd_ = -1; }

SeqpacketSocket& SeqpacketSocket::operator=(SeqpacketSocket&& other) noexcept {
  if (this != &other) {
    close();
    fd_ = other.fd_;
    other.fd_ = -1;
  }
  return *this;
}

std::optional<SeqpacketSocket> SeqpacketSocket::connect(const std::string& path, std::string* error) {
  sockaddr_un addr{};
  unsigned length = 0;
  if (!fillAddress(path, &addr, &length) || path[0] == '@') {
    // no abstract sockets on darwin either
    if (error != nullptr) *error = "bad_path";
    return std::nullopt;
  }
  const int fd = ::socket(AF_UNIX, SOCK_STREAM, 0);
  if (fd < 0) {
    if (error != nullptr) *error = errnoToken(errno);
    return std::nullopt;
  }
  // darwin has no SOCK_CLOEXEC
  ::fcntl(fd, F_SETFD, FD_CLOEXEC);
  if (::connect(fd, reinterpret_cast<sockaddr*>(&addr), static_cast<socklen_t>(length)) != 0) {
    if (error != nullptr) *error = errnoToken(errno);
    ::close(fd);
    return std::nullopt;
  }
  return SeqpacketSocket(fd);
}

bool SeqpacketSocket::send(std::span<const std::uint8_t> frame) {
  if (fd_ < 0 || frame.size() > kMaxFrame) return false;
  // blocking (the sim's ends are in one process, a few frames of headroom in the buffers); a write that cannot
  // finish in 5 s fails, as a stalled peer would drop the frame on the seqpacket side
  timeval tv{5, 0};
  ::setsockopt(fd_, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof tv);
  std::uint8_t header[4] = {static_cast<std::uint8_t>(frame.size() >> 24), static_cast<std::uint8_t>(frame.size() >> 16),
                            static_cast<std::uint8_t>(frame.size() >> 8), static_cast<std::uint8_t>(frame.size())};
  if (!sendAll(fd_, header, sizeof header)) return false;
  return sendAll(fd_, frame.data(), frame.size());
}

SeqpacketSocket::Recv SeqpacketSocket::recv(std::vector<std::uint8_t>& frame, int timeoutMs) {
  if (fd_ < 0) return Recv::Closed;
  std::uint8_t header[4] = {};
  if (!recvAll(fd_, header, sizeof header, timeoutMs)) {
    // a peer that went reads as 0 bytes, as one that only made us wait reads as a timeout
    pollfd p{fd_, POLLIN, 0};
    const int ready = ::poll(&p, 1, 0);
    if (ready == 0) return Recv::WouldBlock;
    char probe = 0;
    return ::recv(fd_, &probe, 1, MSG_PEEK | MSG_DONTWAIT) == 0 ? Recv::Closed : Recv::WouldBlock;
  }
  std::uint32_t size = (static_cast<std::uint32_t>(header[0]) << 24) | (static_cast<std::uint32_t>(header[1]) << 16) |
                       (static_cast<std::uint32_t>(header[2]) << 8) | static_cast<std::uint32_t>(header[3]);
  if (size == 0 || size > kMaxFrame) return Recv::Error;
  frame.resize(size);
  if (!recvAll(fd_, frame.data(), size, timeoutMs)) return Recv::Closed;
  return Recv::Frame;
}

void SeqpacketSocket::shutdown() {
  if (fd_ >= 0) ::shutdown(fd_, SHUT_RDWR);
}

void SeqpacketSocket::close() {
  if (fd_ >= 0) ::close(fd_);
  fd_ = -1;
}

#else  // the kernel's seqpacket: linux, android

SeqpacketSocket::~SeqpacketSocket() { close(); }

SeqpacketSocket::SeqpacketSocket(SeqpacketSocket&& other) noexcept : fd_(other.fd_) { other.fd_ = -1; }

SeqpacketSocket& SeqpacketSocket::operator=(SeqpacketSocket&& other) noexcept {
  if (this != &other) {
    close();
    fd_ = other.fd_;
    other.fd_ = -1;
  }
  return *this;
}

std::optional<SeqpacketSocket> SeqpacketSocket::connect(const std::string& path, std::string* error) {
  sockaddr_un addr{};
  unsigned length = 0;
  if (!fillAddress(path, &addr, &length)) {
    if (error != nullptr) *error = "bad_path";
    return std::nullopt;
  }
  const int fd = ::socket(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0);
  if (fd < 0) {
    if (error != nullptr) *error = errnoToken(errno);
    return std::nullopt;
  }
  if (::connect(fd, reinterpret_cast<sockaddr*>(&addr), static_cast<socklen_t>(length)) != 0) {
    if (error != nullptr) *error = errnoToken(errno);
    ::close(fd);
    return std::nullopt;
  }
  return SeqpacketSocket(fd);
}

bool SeqpacketSocket::send(std::span<const std::uint8_t> frame) {
  if (fd_ < 0) return false;
  for (;;) {
    // never blocks: the main loop sends, and a stalled cadenced must not freeze the screen (the frame is dropped)
    const ssize_t n = ::send(fd_, frame.data(), frame.size(), MSG_NOSIGNAL | MSG_DONTWAIT);
    if (n >= 0) return static_cast<std::size_t>(n) == frame.size();
    if (errno != EINTR) return false;
  }
}

SeqpacketSocket::Recv SeqpacketSocket::recv(std::vector<std::uint8_t>& frame, int timeoutMs) {
  if (fd_ < 0) return Recv::Closed;
  pollfd p{fd_, POLLIN, 0};
  for (;;) {
    const int ready = ::poll(&p, 1, timeoutMs);
    if (ready > 0) break;
    if (ready == 0) return Recv::WouldBlock;
    if (errno != EINTR) return Recv::Error;
  }
  // the frame's size first, so a large one (a page of cards) is read whole
  std::uint8_t probe = 0;
  ssize_t size = ::recv(fd_, &probe, 1, MSG_PEEK | MSG_TRUNC | MSG_DONTWAIT);
  // a closed peer reads as 0; so would an empty frame, which screen/v1 never sends (an envelope always has a body)
  if (size == 0) return Recv::Closed;
  if (size < 0) {
    if (errno == EAGAIN || errno == EWOULDBLOCK || errno == EINTR) return Recv::WouldBlock;
    return Recv::Error;
  }
  frame.resize(static_cast<std::size_t>(size));
  const ssize_t n = ::recv(fd_, frame.data(), frame.size(), MSG_DONTWAIT);
  if (n < 0) return errno == EAGAIN ? Recv::WouldBlock : Recv::Error;
  frame.resize(static_cast<std::size_t>(n));
  return Recv::Frame;
}

void SeqpacketSocket::shutdown() {
  if (fd_ >= 0) ::shutdown(fd_, SHUT_RDWR);
}

void SeqpacketSocket::close() {
  if (fd_ >= 0) ::close(fd_);
  fd_ = -1;
}

#endif

}  // namespace cadence::screen::ipc
