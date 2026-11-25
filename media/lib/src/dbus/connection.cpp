#include "dbus/connection.h"

#include "dbus/auth.h"
#include "dbus/io.h"

#include <poll.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <unistd.h>

#include <algorithm>
#include <array>
#include <cerrno>
#include <cstring>
#include <limits>
#include <string>
#include <utility>

namespace cadence::dbus {

namespace {

constexpr std::size_t kChunkBytes = 16384;

std::span<const std::uint8_t> bytesOf(std::string_view text) {
  return {reinterpret_cast<const std::uint8_t*>(text.data()), text.size()};
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

// the authentication exchange on a connected socket; the bytes that followed the server's OK, or why it failed
Result<std::string, ConnectError> authenticate(int fd, int stopFd, Clock::time_point deadline) {
  AuthClient auth(static_cast<std::uint32_t>(::getuid()));
  if (const auto wait = writeAll(fd, bytesOf(auth.greeting()), stopFd, deadline); wait != Wait::Ready)
    return failure(wait);
  std::array<char, 512> chunk{};
  while (auth.state() == AuthClient::State::Waiting) {
    const auto got = ::recv(fd, chunk.data(), chunk.size(), 0);
    if (got > 0) {
      const auto answer = auth.receive(std::string_view(chunk.data(), static_cast<std::size_t>(got)));
      if (!answer.empty())
        if (const auto wait = writeAll(fd, bytesOf(answer), stopFd, deadline); wait != Wait::Ready)
          return failure(wait);
      continue;
    }
    if (got == 0)
      return ConnectError::Auth;
    if (errno == EINTR)
      continue;
    if (errno != EAGAIN && errno != EWOULDBLOCK)
      return ConnectError::Connect;
    if (const auto wait = waitFor(fd, POLLIN, stopFd, deadline); wait != Wait::Ready)
      return failure(wait);
  }
  if (auth.state() != AuthClient::State::Authenticated)
    return ConnectError::Auth;
  return auth.rest();
}

}  // namespace

std::string_view connectErrorToken(ConnectError error) {
  switch (error) {
    case ConnectError::Address:
      return "address";
    case ConnectError::Socket:
      return "socket";
    case ConnectError::Connect:
      return "connect";
    case ConnectError::Timeout:
      return "timeout";
    case ConnectError::Auth:
      return "auth";
    case ConnectError::Hello:
      return "hello";
    case ConnectError::Stopped:
      return "stopped";
  }
  return "connect";
}

std::string_view closeCodeToken(CloseCode code) {
  switch (code) {
    case CloseCode::None:
      return "none";
    case CloseCode::Closed:
      return "closed";
    case CloseCode::Hangup:
      return "hangup";
    case CloseCode::Io:
      return "io";
    case CloseCode::SendTimeout:
      return "send_timeout";
    case CloseCode::Malformed:
      return "malformed";
  }
  return "io";
}

Result<Connection, ConnectError> Connection::connect(std::string_view address, ConnectionOptions options) {
  const auto deadline = Clock::now() + options.connectTimeout;
  const auto path = unixSocketPath(address);
  sockaddr_un socketAddress{};
  if (!path || path->size() >= sizeof socketAddress.sun_path)
    return ConnectError::Address;
  socketAddress.sun_family = AF_UNIX;
  std::memcpy(socketAddress.sun_path, path->c_str(), path->size() + 1);

  SocketGuard owned(::socket(AF_UNIX, SOCK_STREAM | SOCK_NONBLOCK | SOCK_CLOEXEC, 0));
  if (owned.get() < 0)
    return ConnectError::Socket;
  if (::connect(owned.get(), reinterpret_cast<const sockaddr*>(&socketAddress), sizeof socketAddress) != 0) {
    // a unix socket connects at once or not at all; EAGAIN (the listener's backlog full) is a failure to retry later
    if (errno != EINPROGRESS)
      return ConnectError::Connect;
    if (const auto wait = waitFor(owned.get(), POLLOUT, options.stopFd, deadline); wait != Wait::Ready)
      return failure(wait);
    int error = 0;
    socklen_t length = sizeof error;
    if (::getsockopt(owned.get(), SOL_SOCKET, SO_ERROR, &error, &length) != 0 || error != 0)
      return ConnectError::Connect;
  }
  auto rest = authenticate(owned.get(), options.stopFd, deadline);
  if (!rest.ok())
    return rest.error();
  MessageReader reader;
  reader.append(bytesOf(rest.value()));
  return Connection(owned.release(), options, std::move(reader));
}

Connection::Connection(int fd, ConnectionOptions options, MessageReader reader)
    : fd_(fd), options_(options), reader_(std::move(reader)), chunk_(kChunkBytes) {
  options_.readBudget = std::max<std::size_t>(options_.readBudget, 1);
}

Connection::Connection(Connection&& other) noexcept
    : fd_(std::exchange(other.fd_, -1)),
      options_(other.options_),
      reader_(std::move(other.reader_)),
      serial_(other.serial_),
      closeCode_(other.closeCode_),
      wireError_(other.wireError_),
      chunk_(std::move(other.chunk_)),
      drained_(other.drained_) {}

Connection& Connection::operator=(Connection&& other) noexcept {
  if (this != &other) {
    if (fd_ >= 0)
      ::close(fd_);
    fd_ = std::exchange(other.fd_, -1);
    options_ = other.options_;
    reader_ = std::move(other.reader_);
    serial_ = other.serial_;
    closeCode_ = other.closeCode_;
    wireError_ = other.wireError_;
    chunk_ = std::move(other.chunk_);
    drained_ = other.drained_;
  }
  return *this;
}

Connection::~Connection() {
  if (fd_ >= 0)
    ::close(fd_);
}

std::optional<Message> Connection::next() {
  // whole messages already read are always handed out: only the socket waits for the next drain
  while (open()) {
    if (auto message = reader_.next())
      return message;
    if (const auto failure = reader_.failure()) {
      wireError_ = *failure;
      finish(CloseCode::Malformed);
      break;
    }
    if (drained_ >= options_.readBudget)
      break;
    const auto want = std::min(chunk_.size(), options_.readBudget - drained_);
    const auto got = ::recv(fd_, chunk_.data(), want, 0);
    if (got > 0) {
      drained_ += static_cast<std::size_t>(got);
      reader_.append(std::span(chunk_.data(), static_cast<std::size_t>(got)));
      continue;
    }
    if (got < 0 && errno == EINTR)
      continue;
    if (got < 0 && (errno == EAGAIN || errno == EWOULDBLOCK))
      break;
    finish(got == 0 || errno == ECONNRESET ? CloseCode::Hangup : CloseCode::Io);
  }
  drained_ = 0;
  return std::nullopt;
}

std::uint32_t Connection::send(Message message) {
  if (!open())
    return 0;
  const auto serial = serial_ == std::numeric_limits<std::uint32_t>::max() ? 1 : serial_ + 1;
  message.serial = serial;
  const auto bytes = encodeMessage(message);
  if (!bytes.ok())
    return 0;
  switch (writeAll(fd_, bytes.value(), options_.stopFd, Clock::now() + options_.sendTimeout)) {
    case Wait::Ready:
      serial_ = serial;
      return serial;
    case Wait::Timeout:
      finish(CloseCode::SendTimeout);
      return 0;
    case Wait::Stopped:
      // a message cut short leaves the stream unusable
      finish(CloseCode::Closed);
      return 0;
    case Wait::Failed:
      break;
  }
  finish(errno == EPIPE || errno == ECONNRESET ? CloseCode::Hangup : CloseCode::Io);
  return 0;
}

void Connection::close() {
  if (open())
    finish(CloseCode::Closed);
}

void Connection::finish(CloseCode code) {
  if (fd_ >= 0)
    ::close(fd_);
  fd_ = -1;
  closeCode_ = code;
}

}  // namespace cadence::dbus
