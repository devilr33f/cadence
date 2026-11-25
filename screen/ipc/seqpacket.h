#pragma once

#include <cstdint>
#include <optional>
#include <span>
#include <string>
#include <vector>

namespace cadence::screen::ipc {

// one unix SOCK_SEQPACKET connection, a frame per message. a path that starts with @ is an abstract name (android,
// where the app has no shared runtime dir); anything else a filesystem path
class SeqpacketSocket {
 public:
  SeqpacketSocket() = default;
  explicit SeqpacketSocket(int fd) : fd_(fd) {}
  ~SeqpacketSocket();
  SeqpacketSocket(SeqpacketSocket&& other) noexcept;
  SeqpacketSocket& operator=(SeqpacketSocket&& other) noexcept;
  SeqpacketSocket(const SeqpacketSocket&) = delete;
  SeqpacketSocket& operator=(const SeqpacketSocket&) = delete;

  // errno as a token on failure (enoent, econnrefused, ...)
  static std::optional<SeqpacketSocket> connect(const std::string& path, std::string* error);

  [[nodiscard]] int fd() const { return fd_; }
  [[nodiscard]] bool valid() const { return fd_ >= 0; }
  // one whole frame without waiting, or false (the peer went, its buffer is full, or the frame is too large)
  bool send(std::span<const std::uint8_t> frame);
  enum class Recv { Frame, WouldBlock, Closed, Error };
  // the next frame whole, whatever its size; waits up to timeoutMs (-1 = forever, 0 = not at all)
  Recv recv(std::vector<std::uint8_t>& frame, int timeoutMs);
  // wakes a recv blocked in another thread with Closed
  void shutdown();
  void close();

 private:
  int fd_ = -1;
};

// the sockaddr_un a path names; false when it does not fit
bool fillAddress(const std::string& path, void* sockaddrUn, unsigned* length);

}  // namespace cadence::screen::ipc
