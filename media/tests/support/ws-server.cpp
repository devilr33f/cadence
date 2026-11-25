#include "support/ws-server.h"

#include "ws/handshake.h"

#include <arpa/inet.h>
#include <netinet/in.h>
#include <poll.h>
#include <sys/eventfd.h>
#include <sys/socket.h>
#include <unistd.h>

#include <algorithm>
#include <cctype>
#include <cerrno>
#include <stdexcept>
#include <utility>

namespace cadence::test_support {

namespace {

constexpr std::size_t kMaxRequest = 16384;
constexpr int kTrickleMs = 50;

std::string lower(std::string_view text) {
  std::string out(text);
  std::transform(out.begin(), out.end(), out.begin(),
                 [](unsigned char c) { return static_cast<char>(std::tolower(c)); });
  return out;
}

// a header's value in an http head, by its name in any case
std::string header(std::string_view head, std::string_view name) {
  for (auto at = head.find("\r\n"); at != std::string_view::npos;) {
    at += 2;
    const auto end = head.find("\r\n", at);
    if (end == std::string_view::npos || end == at)
      break;
    const auto line = head.substr(at, end - at);
    const auto colon = line.find(':');
    if (colon != std::string_view::npos && lower(line.substr(0, colon)) == lower(name)) {
      auto value = line.substr(colon + 1);
      while (!value.empty() && value.front() == ' ')
        value.remove_prefix(1);
      while (!value.empty() && value.back() == ' ')
        value.remove_suffix(1);
      return std::string(value);
    }
    at = end;
  }
  return {};
}

bool writeAll(int fd, std::string_view bytes) {
  while (!bytes.empty()) {
    const auto sent = ::send(fd, bytes.data(), bytes.size(), MSG_NOSIGNAL);
    if (sent < 0 && errno == EINTR)
      continue;
    if (sent <= 0)
      return false;
    bytes.remove_prefix(static_cast<std::size_t>(sent));
  }
  return true;
}

}  // namespace

WsServer::WsServer(WsServerOptions options) : options_(std::move(options)) {
  listener_ = ::socket(AF_INET, SOCK_STREAM | SOCK_CLOEXEC, 0);
  wake_ = ::eventfd(0, EFD_CLOEXEC | EFD_NONBLOCK);
  if (listener_ < 0 || wake_ < 0)
    throw std::runtime_error("ws server: socket");
  if (!options_.reading) {
    // inherited by the accepted socket: the client's window stays small
    const int small = 4096;
    ::setsockopt(listener_, SOL_SOCKET, SO_RCVBUF, &small, sizeof small);
  }
  sockaddr_in address{};
  address.sin_family = AF_INET;
  address.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
  socklen_t length = sizeof address;
  if (::bind(listener_, reinterpret_cast<const sockaddr*>(&address), sizeof address) != 0 ||
      ::listen(listener_, 1) != 0 ||
      ::getsockname(listener_, reinterpret_cast<sockaddr*>(&address), &length) != 0)
    throw std::runtime_error("ws server: listen");
  port_ = ntohs(address.sin_port);
  thread_ = std::thread([this] { serve(); });
}

WsServer::~WsServer() {
  stopping_ = true;
  wake();
  thread_.join();
  if (client_ >= 0)
    ::close(client_);
  ::close(listener_);
  ::close(wake_);
}

std::string WsServer::request() const {
  const std::lock_guard lock(mutex_);
  return request_;
}

bool WsServer::waitHandshake(std::chrono::milliseconds limit) {
  std::unique_lock lock(mutex_);
  return changed_.wait_for(lock, limit, [this] { return handshaken_; });
}

void WsServer::sendRaw(std::string_view bytes) {
  {
    const std::lock_guard lock(mutex_);
    outgoing_.append(bytes);
  }
  wake();
}

void WsServer::sendFrame(std::uint8_t first, std::string_view payload, bool masked) {
  constexpr std::array<std::uint8_t, 4> kMask{1, 2, 3, 4};
  std::string frame(1, static_cast<char>(first));
  const unsigned maskBit = masked ? 0x80U : 0x00U;
  if (payload.size() < 126) {
    frame.push_back(static_cast<char>(maskBit | payload.size()));
  } else if (payload.size() <= 0xFFFF) {
    frame.push_back(static_cast<char>(maskBit | 126U));
    frame.push_back(static_cast<char>(payload.size() >> 8));
    frame.push_back(static_cast<char>(payload.size() & 0xFF));
  } else {
    frame.push_back(static_cast<char>(maskBit | 127U));
    for (int shift = 56; shift >= 0; shift -= 8)
      frame.push_back(static_cast<char>((payload.size() >> shift) & 0xFF));
  }
  if (masked)
    frame.append(kMask.begin(), kMask.end());
  for (std::size_t i = 0; i < payload.size(); ++i)
    frame.push_back(static_cast<char>(static_cast<std::uint8_t>(payload[i]) ^ (masked ? kMask[i % 4] : 0U)));
  sendRaw(frame);
}

void WsServer::sendClose(std::optional<std::uint16_t> code) {
  std::string payload;
  if (code) {
    payload.push_back(static_cast<char>(*code >> 8));
    payload.push_back(static_cast<char>(*code & 0xFF));
  }
  sendFrame(0x88, payload);
}

void WsServer::hangUp() {
  const std::lock_guard lock(mutex_);
  if (client_ >= 0)
    ::shutdown(client_, SHUT_RDWR);
}

std::vector<WsClientFrame> WsServer::frames() const {
  const std::lock_guard lock(mutex_);
  return frames_;
}

bool WsServer::waitFrames(std::size_t count, std::chrono::milliseconds limit) {
  std::unique_lock lock(mutex_);
  return changed_.wait_for(lock, limit, [&] { return frames_.size() >= count; });
}

bool WsServer::waitClientGone(std::chrono::milliseconds limit) {
  std::unique_lock lock(mutex_);
  return changed_.wait_for(lock, limit, [this] { return gone_; });
}

void WsServer::wake() const {
  const std::uint64_t one = 1;
  [[maybe_unused]] const auto written = ::write(wake_, &one, sizeof one);
}

void WsServer::serve() {
  int fd = -1;
  while (!stopping() && fd < 0) {
    pollfd fds[] = {{listener_, POLLIN, 0}, {wake_, POLLIN, 0}};
    if (::poll(fds, 2, -1) < 0 && errno != EINTR)
      return;
    if ((fds[1].revents & POLLIN) != 0) {
      std::uint64_t drained = 0;
      [[maybe_unused]] const auto read = ::read(wake_, &drained, sizeof drained);
    }
    if ((fds[0].revents & POLLIN) != 0)
      fd = ::accept4(listener_, nullptr, nullptr, SOCK_CLOEXEC);
  }
  if (fd < 0)
    return;
  {
    const std::lock_guard lock(mutex_);
    client_ = fd;
  }
  if (!handshake(fd))
    return;
  while (!stopping()) {
    bool writing = false;
    {
      const std::lock_guard lock(mutex_);
      writing = sentOut_ < outgoing_.size();
    }
    const short events = static_cast<short>((options_.reading ? POLLIN : 0) | (writing ? POLLOUT : 0));
    pollfd fds[] = {{fd, events, 0}, {wake_, POLLIN, 0}};
    if (::poll(fds, 2, -1) < 0) {
      if (errno == EINTR)
        continue;
      return;
    }
    if ((fds[1].revents & POLLIN) != 0) {
      std::uint64_t drained = 0;
      [[maybe_unused]] const auto read = ::read(wake_, &drained, sizeof drained);
    }
    if ((fds[0].revents & POLLOUT) != 0) {
      const std::lock_guard lock(mutex_);
      const auto sent =
          ::send(fd, outgoing_.data() + sentOut_, outgoing_.size() - sentOut_, MSG_NOSIGNAL | MSG_DONTWAIT);
      if (sent > 0)
        sentOut_ += static_cast<std::size_t>(sent);
      if ((sent < 0 && errno != EAGAIN && errno != EINTR) || sentOut_ == outgoing_.size()) {
        outgoing_.clear();
        sentOut_ = 0;
      }
    }
    if ((fds[0].revents & (POLLIN | POLLHUP | POLLERR)) != 0) {
      char chunk[65536];
      const auto got = options_.reading ? ::recv(fd, chunk, sizeof chunk, MSG_DONTWAIT) : 0;
      if (got > 0) {
        incoming_.append(chunk, static_cast<std::size_t>(got));
        parse();
      } else if (got == 0 || (errno != EAGAIN && errno != EINTR)) {
        const std::lock_guard lock(mutex_);
        gone_ = true;
        changed_.notify_all();
        return;
      }
    }
  }
}

bool WsServer::handshake(int fd) {
  std::string request;
  while (request.find("\r\n\r\n") == std::string::npos) {
    if (stopping() || request.size() > kMaxRequest)
      return false;
    pollfd fds[] = {{fd, POLLIN, 0}, {wake_, POLLIN, 0}};
    if (::poll(fds, 2, -1) < 0 && errno != EINTR)
      return false;
    if ((fds[0].revents & (POLLIN | POLLHUP | POLLERR)) == 0)
      continue;
    char chunk[4096];
    const auto got = ::recv(fd, chunk, sizeof chunk, 0);
    if (got <= 0)
      return false;
    request.append(chunk, static_cast<std::size_t>(got));
  }
  const auto head = request.find("\r\n\r\n") + 4;
  incoming_ = request.substr(head);
  request.resize(head);
  {
    const std::lock_guard lock(mutex_);
    request_ = request;
  }
  const auto key = header(request, "Sec-WebSocket-Key");
  const auto switching = [](const std::string& accept) {
    return "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
           accept + "\r\n\r\n";
  };
  std::string response;
  switch (options_.handshake) {
    case WsHandshake::Accept:
      response = switching(ws::acceptFor(key));
      break;
    case WsHandshake::BadAccept:
      response = switching(ws::acceptFor(key + "x"));
      break;
    case WsHandshake::NotSwitching:
      response = "HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n";
      break;
    case WsHandshake::Silent:
      return false;
    case WsHandshake::Trickle:
      for (const char byte : switching(ws::acceptFor(key))) {
        pollfd stop{wake_, POLLIN, 0};
        if (::poll(&stop, 1, kTrickleMs) != 0 || stopping() || !writeAll(fd, std::string_view(&byte, 1)))
          return false;
      }
      break;
  }
  if (!writeAll(fd, response + options_.afterHandshake))
    return false;
  const std::lock_guard lock(mutex_);
  handshaken_ = true;
  changed_.notify_all();
  return true;
}

void WsServer::parse() {
  const auto at = [this](std::size_t index) { return static_cast<std::uint8_t>(incoming_[index]); };
  for (;;) {
    if (incoming_.size() < 2)
      return;
    WsClientFrame frame;
    frame.fin = (at(0) & 0x80) != 0;
    frame.opcode = static_cast<std::uint8_t>(at(0) & 0x0F);
    frame.masked = (at(1) & 0x80) != 0;
    std::uint64_t length = at(1) & 0x7FU;
    std::size_t offset = 2;
    if (length == 126) {
      if (incoming_.size() < 4)
        return;
      length = (std::uint64_t{at(2)} << 8) | at(3);
      offset = 4;
    } else if (length == 127) {
      if (incoming_.size() < 10)
        return;
      length = 0;
      for (std::size_t i = 2; i < 10; ++i)
        length = (length << 8) | at(i);
      offset = 10;
    }
    if (frame.masked) {
      if (incoming_.size() < offset + 4)
        return;
      for (std::size_t i = 0; i < 4; ++i)
        frame.mask[i] = at(offset + i);
      offset += 4;
    }
    if (incoming_.size() - offset < length)
      return;
    frame.payload = incoming_.substr(offset, static_cast<std::size_t>(length));
    if (frame.masked)
      for (std::size_t i = 0; i < frame.payload.size(); ++i)
        frame.payload[i] = static_cast<char>(static_cast<std::uint8_t>(frame.payload[i]) ^ frame.mask[i % 4]);
    incoming_.erase(0, offset + static_cast<std::size_t>(length));
    const std::lock_guard lock(mutex_);
    frames_.push_back(std::move(frame));
    changed_.notify_all();
  }
}

}  // namespace cadence::test_support
