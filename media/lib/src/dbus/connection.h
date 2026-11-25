#pragma once

#include "dbus/message.h"
#include "result.h"

#include <chrono>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <string_view>
#include <vector>

namespace cadence::dbus {

// what one drain of next() reads from the socket at most by default (see Connection::next)
inline constexpr std::size_t kReadBudget = std::size_t{256} << 10;

struct ConnectionOptions {
  // the whole connect: the socket and the authentication, and for a client the Hello
  std::chrono::milliseconds connectTimeout{2000};
  // each message goes out whole within it, or the connection is dropped
  std::chrono::milliseconds sendTimeout{2000};
  // readable once the owner stops: a connect or a send waiting on the socket gives up at once; -1 for none
  int stopFd = -1;
  // what one drain reads from the socket at most (0 reads as 1)
  std::size_t readBudget = kReadBudget;
};

enum class ConnectError : std::uint8_t { Address, Socket, Connect, Timeout, Auth, Hello, Stopped };
// a fixed token for logs
std::string_view connectErrorToken(ConnectError error);

// why a connection ended
enum class CloseCode : std::uint8_t {
  None,         // still open
  Closed,       // by its owner
  Hangup,       // the peer closed it
  Io,           // a read or a write failed
  SendTimeout,  // a message was not out within sendTimeout
  Malformed,    // the peer broke the protocol (wireError says how)
};
// a fixed token for logs
std::string_view closeCodeToken(CloseCode code);

// a d-bus connection over a unix socket (the address's unix:path=), authenticated with EXTERNAL as the process's uid.
// the socket is non-blocking and nothing here starts a thread: the owner polls fd() for reading and calls next() until
// it returns nullopt. only the connect and send wait, each bounded and given up once stopFd is readable. a failure ends
// the connection for good: open() turns false and closeCode() says why
class Connection {
 public:
  // one try
  static Result<Connection, ConnectError> connect(std::string_view address, ConnectionOptions options = {});

  Connection(Connection&& other) noexcept;
  Connection& operator=(Connection&& other) noexcept;
  Connection(const Connection&) = delete;
  Connection& operator=(const Connection&) = delete;
  ~Connection();

  // -1 once closed
  [[nodiscard]] int fd() const { return fd_; }
  [[nodiscard]] bool open() const { return fd_ >= 0; }
  [[nodiscard]] CloseCode closeCode() const { return closeCode_; }
  // how the peer broke the protocol, with CloseCode::Malformed
  [[nodiscard]] std::optional<WireError> wireError() const { return wireError_; }
  [[nodiscard]] const ConnectionOptions& options() const { return options_; }

  // the next whole message, read without blocking; nullopt when none is there yet or the connection closed. a malformed
  // or oversized message closes it. the peer must not hold the owner's thread: one drain, the calls up to the one that
  // returns nullopt, reads at most readBudget, and the fd then stays readable for the owner's next poll
  std::optional<Message> next();
  // a whole message (or a broken one) waits in the buffer, where the socket's readiness does not show it (a connect
  // can return with what came after its answer): the owner calls next() before it polls again
  [[nodiscard]] bool hasBuffered() const { return open() && reader_.ready(); }
  // the message under the next serial, out whole within sendTimeout. that serial; 0 when the message breaks a rule (the
  // connection stays open) or the connection is or then got closed
  std::uint32_t send(Message message);
  void close();

 private:
  Connection(int fd, ConnectionOptions options, MessageReader reader);
  void finish(CloseCode code);

  int fd_ = -1;
  ConnectionOptions options_;
  MessageReader reader_;
  std::uint32_t serial_ = 0;
  CloseCode closeCode_ = CloseCode::None;
  std::optional<WireError> wireError_;
  std::vector<std::uint8_t> chunk_;
  std::size_t drained_ = 0;
};

}  // namespace cadence::dbus
