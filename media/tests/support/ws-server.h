#pragma once

#include <array>
#include <atomic>
#include <chrono>
#include <condition_variable>
#include <cstdint>
#include <mutex>
#include <optional>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

namespace cadence::test_support {

// how the server answers the client's opening request
enum class WsHandshake {
  Accept,
  // a 101 with the accept of another key
  BadAccept,
  // 200 instead of 101
  NotSwitching,
  // never a word
  Silent,
  // the right answer, one byte every 50 ms
  Trickle,
};

struct WsServerOptions {
  WsHandshake handshake = WsHandshake::Accept;
  // written right behind the 101, in the same write: the frames of a server that speaks at once
  std::string afterHandshake{};
  // false: nothing is read after the handshake and the receive buffer is small, so a large send stalls
  bool reading = true;
};

// a frame the client sent, unmasked
struct WsClientFrame {
  bool fin = false;
  std::uint8_t opcode = 0;
  bool masked = false;
  std::array<std::uint8_t, 4> mask{};
  std::string payload;
};

// a websocket server for tests on 127.0.0.1 and a port of its own. its thread accepts one client, answers the
// handshake as told, then writes what a test queues (any frame, bad ones included) and records what the client sends,
// parsed on its own (not with the client's code). destroying it hangs up
class WsServer {
 public:
  explicit WsServer(WsServerOptions options = {});
  ~WsServer();

  WsServer(const WsServer&) = delete;
  WsServer& operator=(const WsServer&) = delete;

  [[nodiscard]] std::uint16_t port() const { return port_; }
  // the client's opening request, empty until it arrived whole
  [[nodiscard]] std::string request() const;
  // true once the 101 went out
  bool waitHandshake(std::chrono::milliseconds limit = std::chrono::seconds(5));

  // queued for the server thread, which writes it once the handshake is done
  void sendRaw(std::string_view bytes);
  // a frame as given; a masked one with the key 1 2 3 4
  void sendFrame(std::uint8_t first, std::string_view payload, bool masked = false);
  void sendText(std::string_view text) { sendFrame(0x81, text); }
  // a close frame with code, or an empty one
  void sendClose(std::optional<std::uint16_t> code);
  // shuts the connection down without a close frame
  void hangUp();

  [[nodiscard]] std::vector<WsClientFrame> frames() const;
  bool waitFrames(std::size_t count, std::chrono::milliseconds limit = std::chrono::seconds(5));
  // true once the client closed its end (end of stream)
  bool waitClientGone(std::chrono::milliseconds limit = std::chrono::seconds(5));

 private:
  void serve();
  bool handshake(int fd);
  void parse();
  void wake() const;
  [[nodiscard]] bool stopping() const { return stopping_.load(); }

  const WsServerOptions options_;
  int listener_ = -1;
  int wake_ = -1;
  std::uint16_t port_ = 0;
  std::atomic<bool> stopping_{false};

  mutable std::mutex mutex_;
  std::condition_variable changed_;
  // under mutex_
  int client_ = -1;
  std::string request_;
  bool handshaken_ = false;
  bool gone_ = false;
  std::string outgoing_;
  // what of outgoing_ went out: a flood of megabytes is not moved on each write
  std::size_t sentOut_ = 0;
  std::vector<WsClientFrame> frames_;

  // the server thread's
  std::string incoming_;

  std::thread thread_;
};

}  // namespace cadence::test_support
