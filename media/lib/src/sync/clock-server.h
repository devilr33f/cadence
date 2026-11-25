#pragma once

#include "sync/clock-socket.h"
#include "sync/sync-clock.h"

#include <atomic>
#include <cstdint>
#include <functional>
#include <thread>
#include <vector>

namespace cadence::sync {

struct ClockServerStats {
  std::uint64_t served = 0;
  // packets the key did not make, or of another kind or size
  std::uint64_t refused = 0;
  std::uint64_t sendErrors = 0;
  bool kernelStamps = false;
};

// the leader's half: answers each member's probe with when it arrived and when the answer left. a thread of its own,
// started by start; onThread runs first on it (audiod raises its priority there: the spike's stick, answering from a
// busy default-priority thread, was 4x less even than midi). the key is copied
class ClockServer {
 public:
  ClockServer(ClockSocket socket, std::vector<std::uint8_t> key, std::function<void()> onThread = {});
  ClockServer(const ClockServer&) = delete;
  ClockServer& operator=(const ClockServer&) = delete;
  ~ClockServer();

  // the clock its stamps speak (the sim's skewed one); before start
  void setClock(SyncClock clock) { clock_ = clock; }
  void start();
  void stop();
  [[nodiscard]] std::uint16_t port() const { return socket_.port(); }
  [[nodiscard]] ClockServerStats stats() const;

 private:
  void run();

  ClockSocket socket_;
  std::vector<std::uint8_t> key_;
  std::function<void()> onThread_;
  SyncClock clock_;
  std::thread thread_;
  std::atomic<bool> stop_{false};
  std::atomic<std::uint64_t> served_{0};
  std::atomic<std::uint64_t> refused_{0};
  std::atomic<std::uint64_t> sendErrors_{0};
  std::atomic<bool> kernelStamps_{false};
};

}  // namespace cadence::sync
