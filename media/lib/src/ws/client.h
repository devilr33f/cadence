#pragma once

#include "result.h"
#include "ws/frame.h"

#include <chrono>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <span>
#include <string>
#include <string_view>
#include <vector>

namespace cadence::ws {

// v1's bound on one message: soloist's events are small, a bigger one is a broken peer
inline constexpr std::size_t kMaxMessage = std::size_t{1} << 20;
// what one drain of next() reads from the socket at most (see Client::next)
inline constexpr std::size_t kReadBudget = std::size_t{256} << 10;

struct ClientOptions {
  // the whole connect: tcp, the request and the response
  std::chrono::milliseconds connectTimeout{2000};
  // each frame goes out whole within it, or the connection is dropped
  std::chrono::milliseconds sendTimeout{2000};
  std::size_t maxMessage = kMaxMessage;
  // readable once the owner stops: a connect or a send waiting on the socket gives up at once; -1 for none
  int stopFd = -1;
};

enum class ConnectError : std::uint8_t { Random, Socket, Connect, Timeout, Handshake, Stopped };

// a fixed token for logs
std::string_view connectErrorToken(ConnectError error);

// a websocket client (rfc 6455) of ws://127.0.0.1:<port>/, which is all soloist offers: no tls, no extensions. the
// socket is non-blocking and nothing here starts a thread: the owner polls fd() for reading (beside its stop fd) and
// calls next() until it returns nullopt. only connect and send wait, each bounded and given up once stopFd is
// readable. a failure ends the connection for good: open() turns false and closeCode() says why
class Client {
 public:
  // one try
  static Result<Client, ConnectError> connect(std::uint16_t port, ClientOptions options = {});

  Client(Client&& other) noexcept;
  Client& operator=(Client&& other) noexcept;
  Client(const Client&) = delete;
  Client& operator=(const Client&) = delete;
  ~Client();

  // -1 once closed
  [[nodiscard]] int fd() const { return fd_; }
  [[nodiscard]] bool open() const { return fd_ >= 0; }
  // the server's close code (kCloseNoStatus when it gave none); kCloseProtocolError or kCloseTooBig when this side
  // failed the connection; kCloseAbnormal when it ended without a close (a hangup, a failed read or send); or the code
  // given to close
  [[nodiscard]] std::uint16_t closeCode() const { return closeCode_; }

  // the next whole text message, read without blocking; nullopt when none is there yet or the connection closed. a
  // server's close is echoed with its code (1002 for a code rfc 6455 §7.4 does not allow) before the socket closes,
  // and a protocol error or an oversized message closed with 1002 or 1009.
  //
  // the peer (soloist, jailed and untrusted) must not hold the owner's thread: one drain, the calls up to the one that
  // returns nullopt, reads at most kReadBudget from the socket. the fd then stays readable, so the owner's next poll
  // returns at once, after its stop fd and schedule had their turn. pings are answered once per drain, with the pong
  // of the latest (rfc 6455 §5.5.3), when the drain ends: a ping flood costs one pong, and a stalled one drops the
  // connection within sendTimeout
  std::optional<std::string> next();
  // one masked text frame; false, the connection dropped, when it is closed or the frame is not out within sendTimeout
  bool send(std::string_view text);
  // a close frame with code, best effort, then the socket closes
  void close(std::uint16_t code);

 private:
  Client(int fd, ClientOptions options, MessageReader reader);

  bool sendFrame(Opcode opcode, std::span<const std::uint8_t> payload);
  // the drain's pong, if a ping came, and a fresh budget
  void endDrain();
  // closes the socket, keeping code as the reason
  void finish(std::uint16_t code);

  int fd_ = -1;
  ClientOptions options_;
  MessageReader reader_;
  std::uint16_t closeCode_ = 0;
  std::vector<std::uint8_t> chunk_;
  // read from the socket in this drain
  std::size_t drained_ = 0;
  // the latest ping's payload, answered when the drain ends
  std::optional<std::string> pong_;
};

}  // namespace cadence::ws
