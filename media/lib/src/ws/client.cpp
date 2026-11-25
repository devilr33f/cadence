#include "ws/client.h"

#include "ws/base64.h"
#include "ws/handshake.h"

#include <arpa/inet.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <poll.h>
#include <sys/random.h>
#include <sys/socket.h>
#include <unistd.h>

#include <algorithm>
#include <array>
#include <cerrno>
#include <climits>
#include <utility>

namespace cadence::ws {

namespace {

using Clock = std::chrono::steady_clock;

constexpr std::size_t kChunkBytes = 16384;

enum class Wait { Ready, Timeout, Stopped, Failed };

std::span<const std::uint8_t> bytesOf(std::string_view text) {
  return {reinterpret_cast<const std::uint8_t*>(text.data()), text.size()};
}

bool fillRandom(std::span<std::uint8_t> out) {
  while (!out.empty()) {
    const auto got = ::getrandom(out.data(), out.size(), 0);
    if (got < 0 && errno == EINTR)
      continue;
    if (got <= 0)
      return false;
    out = out.subspan(static_cast<std::size_t>(got));
  }
  return true;
}

// until fd has one of events, the deadline passes or stopFd turns readable
Wait waitFor(int fd, short events, int stopFd, Clock::time_point deadline) {
  for (;;) {
    const auto now = Clock::now();
    if (now >= deadline)
      return Wait::Timeout;
    // rounded up, so the wait never ends early; poll skips a negative stopFd
    const auto left = std::chrono::ceil<std::chrono::milliseconds>(deadline - now).count();
    pollfd fds[] = {{fd, events, 0}, {stopFd, POLLIN, 0}};
    if (::poll(fds, 2, static_cast<int>(std::min<decltype(left)>(left, INT_MAX))) < 0) {
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

// all of bytes before the deadline
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

ConnectError failure(Wait wait) {
  switch (wait) {
    case Wait::Timeout:
      return ConnectError::Timeout;
    case Wait::Stopped:
      return ConnectError::Stopped;
    case Wait::Ready:
    case Wait::Failed:
      break;
  }
  return ConnectError::Connect;
}

class SocketGuard {
 public:
  explicit SocketGuard(int fd) : fd_(fd) {}
  ~SocketGuard() {
    if (fd_ >= 0)
      ::close(fd_);
  }
  SocketGuard(const SocketGuard&) = delete;
  SocketGuard& operator=(const SocketGuard&) = delete;

  [[nodiscard]] int get() const { return fd_; }
  int release() { return std::exchange(fd_, -1); }

 private:
  int fd_;
};

}  // namespace

std::string_view connectErrorToken(ConnectError error) {
  switch (error) {
    case ConnectError::Random:
      return "random";
    case ConnectError::Socket:
      return "socket";
    case ConnectError::Connect:
      return "connect";
    case ConnectError::Timeout:
      return "timeout";
    case ConnectError::Handshake:
      return "handshake";
    case ConnectError::Stopped:
      return "stopped";
  }
  return "connect";
}

Result<Client, ConnectError> Client::connect(std::uint16_t port, ClientOptions options) {
  const auto deadline = Clock::now() + options.connectTimeout;
  if (port == 0)
    return ConnectError::Connect;
  std::array<std::uint8_t, kKeyBytes> nonce{};
  if (!fillRandom(nonce))
    return ConnectError::Random;
  const auto key = base64(nonce);

  SocketGuard owned(::socket(AF_INET, SOCK_STREAM | SOCK_NONBLOCK | SOCK_CLOEXEC, 0));
  if (owned.get() < 0)
    return ConnectError::Socket;
  // commands are small and come in bursts (a volume, then a pause): nagle would hold each behind the last one's ack
  const int noDelay = 1;
  if (::setsockopt(owned.get(), IPPROTO_TCP, TCP_NODELAY, &noDelay, sizeof noDelay) != 0)
    return ConnectError::Socket;
  sockaddr_in address{};
  address.sin_family = AF_INET;
  address.sin_port = htons(port);
  address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
  if (::connect(owned.get(), reinterpret_cast<const sockaddr*>(&address), sizeof address) != 0) {
    if (errno != EINPROGRESS && errno != EINTR)
      return ConnectError::Connect;
    if (const auto wait = waitFor(owned.get(), POLLOUT, options.stopFd, deadline); wait != Wait::Ready)
      return failure(wait);
    int error = 0;
    socklen_t length = sizeof error;
    if (::getsockopt(owned.get(), SOL_SOCKET, SO_ERROR, &error, &length) != 0 || error != 0)
      return ConnectError::Connect;
  }

  if (const auto wait = writeAll(owned.get(), bytesOf(handshakeRequest(port, key)), options.stopFd, deadline);
      wait != Wait::Ready)
    return failure(wait);

  std::string response;
  std::array<char, 4096> chunk{};
  for (;;) {
    const auto check = checkResponse(response, key);
    if (check.status == HandshakeStatus::Refused)
      return ConnectError::Handshake;
    if (check.status == HandshakeStatus::Accepted) {
      MessageReader reader(options.maxMessage);
      reader.append(bytesOf(response).subspan(check.headBytes));
      return Client(owned.release(), options, std::move(reader));
    }
    if (Clock::now() >= deadline)
      return ConnectError::Timeout;
    const auto got = ::recv(owned.get(), chunk.data(), chunk.size(), 0);
    if (got > 0) {
      response.append(chunk.data(), static_cast<std::size_t>(got));
      continue;
    }
    if (got == 0)
      return ConnectError::Handshake;
    if (errno == EINTR)
      continue;
    if (errno != EAGAIN && errno != EWOULDBLOCK)
      return ConnectError::Connect;
    if (const auto wait = waitFor(owned.get(), POLLIN, options.stopFd, deadline); wait != Wait::Ready)
      return failure(wait);
  }
}

Client::Client(int fd, ClientOptions options, MessageReader reader)
    : fd_(fd), options_(options), reader_(std::move(reader)), chunk_(kChunkBytes) {}

Client::Client(Client&& other) noexcept
    : fd_(std::exchange(other.fd_, -1)),
      options_(other.options_),
      reader_(std::move(other.reader_)),
      closeCode_(other.closeCode_),
      chunk_(std::move(other.chunk_)),
      drained_(other.drained_),
      pong_(std::move(other.pong_)) {}

Client& Client::operator=(Client&& other) noexcept {
  if (this != &other) {
    if (fd_ >= 0)
      ::close(fd_);
    fd_ = std::exchange(other.fd_, -1);
    options_ = other.options_;
    reader_ = std::move(other.reader_);
    closeCode_ = other.closeCode_;
    chunk_ = std::move(other.chunk_);
    drained_ = other.drained_;
    pong_ = std::move(other.pong_);
  }
  return *this;
}

Client::~Client() {
  if (fd_ >= 0)
    ::close(fd_);
}

std::optional<std::string> Client::next() {
  // complete frames already read are always handled: only the socket waits for the next drain
  while (open()) {
    if (auto incoming = reader_.next()) {
      switch (incoming->kind) {
        case Incoming::Kind::Text:
          return std::move(incoming->data);
        case Incoming::Kind::Ping:
          pong_ = std::move(incoming->data);
          continue;
        case Incoming::Kind::Close:
        case Incoming::Kind::Fail:
          close(incoming->code);
          continue;
      }
    }
    if (drained_ >= kReadBudget)
      break;
    const auto got = ::recv(fd_, chunk_.data(), chunk_.size(), 0);
    if (got > 0) {
      drained_ += static_cast<std::size_t>(got);
      reader_.append(std::span(chunk_.data(), static_cast<std::size_t>(got)));
      continue;
    }
    if (got < 0 && errno == EINTR)
      continue;
    if (got < 0 && (errno == EAGAIN || errno == EWOULDBLOCK))
      break;
    finish(kCloseAbnormal);
  }
  endDrain();
  return std::nullopt;
}

void Client::endDrain() {
  drained_ = 0;
  const auto pong = std::exchange(pong_, std::nullopt);
  if (open() && pong && !sendFrame(Opcode::Pong, bytesOf(*pong)))
    finish(kCloseAbnormal);
}

bool Client::send(std::string_view text) {
  if (!open())
    return false;
  if (sendFrame(Opcode::Text, bytesOf(text)))
    return true;
  finish(kCloseAbnormal);
  return false;
}

void Client::close(std::uint16_t code) {
  if (!open())
    return;
  const std::array<std::uint8_t, 2> payload{static_cast<std::uint8_t>(code >> 8), static_cast<std::uint8_t>(code)};
  sendFrame(Opcode::Close, code == kCloseNoStatus ? std::span<const std::uint8_t>{} : std::span(payload));
  finish(code);
}

bool Client::sendFrame(Opcode opcode, std::span<const std::uint8_t> payload) {
  MaskKey mask{};
  if (!fillRandom(mask))
    return false;
  const auto frame = encodeClientFrame(opcode, payload, mask);
  return writeAll(fd_, frame, options_.stopFd, Clock::now() + options_.sendTimeout) == Wait::Ready;
}

void Client::finish(std::uint16_t code) {
  if (fd_ >= 0)
    ::close(fd_);
  fd_ = -1;
  closeCode_ = code;
}

}  // namespace cadence::ws
