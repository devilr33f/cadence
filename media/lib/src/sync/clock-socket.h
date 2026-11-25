#pragma once

#include "result.h"

#include <cstddef>
#include <cstdint>
#include <optional>
#include <span>
#include <string>
#include <sys/socket.h>

namespace cadence::sync {

// CLOCK_MONOTONIC in ns, the clock the mixer's playhead speaks
std::int64_t monoNs();

// a udp peer
struct ClockPeer {
  sockaddr_storage addr{};
  socklen_t len = 0;
};

// a datagram and when it arrived on the monotonic clock: the kernel's receive stamp where the platform gives one
// (linux and android: SO_TIMESTAMPNS), else the time the read returned. the spike measured midi's reads returning
// 240 µs (p50) to 530 µs (p95) after the kernel stamp, the stick's up to 2.5 ms when busy
struct ClockDatagram {
  std::size_t size = 0;
  ClockPeer from;
  std::int64_t arrivedNs = 0;
  std::int64_t readNs = 0;
  bool kernelStamp = false;
};

// a udp socket that stamps what it receives. the error of open is an errno
class ClockSocket {
 public:
  // host empty: every address; port 0: any. v4 and v6 by the host's form
  static Result<ClockSocket, int> open(const std::string& host, std::uint16_t port);
  // the leader's address, resolved once
  static std::optional<ClockPeer> resolve(const std::string& host, std::uint16_t port);

  ClockSocket(ClockSocket&& other) noexcept;
  ClockSocket& operator=(ClockSocket&& other) noexcept;
  ClockSocket(const ClockSocket&) = delete;
  ClockSocket& operator=(const ClockSocket&) = delete;
  ~ClockSocket();

  // false on a send that failed (its errno in lastError)
  bool send(std::span<const std::uint8_t> packet, const ClockPeer& to);
  // waits up to timeoutMs; none on a timeout or an error. a datagram larger than buf is cut and still returned,
  // so the decoder refuses it by its size
  std::optional<ClockDatagram> receive(std::span<std::uint8_t> buf, int timeoutMs);
  [[nodiscard]] std::uint16_t port() const;
  [[nodiscard]] int lastError() const { return lastError_; }

 private:
  explicit ClockSocket(int fd) : fd_(fd) {}

  int fd_ = -1;
  int lastError_ = 0;
};

}  // namespace cadence::sync
