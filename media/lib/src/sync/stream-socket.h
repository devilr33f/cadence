#pragma once

#include "result.h"
#include "sync/stream-wire.h"

#include <cstddef>
#include <cstdint>
#include <string>
#include <vector>

namespace cadence::sync {

// an errno
struct SocketError {
  int code = 0;
};

// a tcp listener on every address of host's family (empty: v4 any); port 0 picks one. the error is an errno
Result<int, SocketError> listenTcp(const std::string& host, std::uint16_t port);
// a non-blocking connect that waits up to timeoutMs; the fd comes back non-blocking. the error is an errno
// (ETIMEDOUT, ECONNREFUSED, EHOSTUNREACH, or EINVAL for a host that does not resolve)
Result<int, SocketError> connectTcp(const std::string& host, std::uint16_t port, int timeoutMs);
// the bound port of a socket, 0 when unknown
std::uint16_t localPort(int fd);

// a pipe another thread writes a byte into to wake a poll
class WakePipe {
 public:
  WakePipe();
  ~WakePipe();
  WakePipe(const WakePipe&) = delete;
  WakePipe& operator=(const WakePipe&) = delete;

  void wake() const;
  void drain() const;
  [[nodiscard]] int fd() const { return read_; }
  [[nodiscard]] bool valid() const { return read_ >= 0; }

 private:
  int read_ = -1;
  int write_ = -1;
};

// one stream connection over a non-blocking fd it owns: messages sealed into a send buffer, written as the socket
// takes them, and the bytes it reads opened back into messages. one thread
class StreamConn {
 public:
  enum class Read : std::uint8_t { Ok, Closed };

  StreamConn(int fd, std::vector<std::uint8_t> key);
  ~StreamConn();
  StreamConn(const StreamConn&) = delete;
  StreamConn& operator=(const StreamConn&) = delete;

  [[nodiscard]] int fd() const { return fd_; }
  // sealed onto the send buffer; false when it does not encode
  bool send(const SyncEnvelope& envelope);
  // writes what the socket takes; false on an error (the peer went)
  bool flush();
  [[nodiscard]] std::size_t pending() const { return out_.size() - sent_; }
  // reads what the socket has; Closed at the end of the stream or an error
  Read receive();
  OpenResult next(SyncEnvelope& out) { return opener_.next(out); }
  // the session key, both ways, counters at 0
  void rekey(const std::vector<std::uint8_t>& key);

 private:
  int fd_;
  StreamSealer sealer_;
  StreamOpener opener_;
  std::vector<std::uint8_t> out_;
  std::size_t sent_ = 0;
};

}  // namespace cadence::sync
