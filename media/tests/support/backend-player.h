#pragma once

#include "backend/pcm-backend.h"
#include "backend/pcm-input.h"

#include <algorithm>
#include <atomic>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <mutex>
#include <thread>
#include <vector>

namespace cadence::test_support {

// a pcm backend over one music input with its gate open, run period by period on its own thread at the device's pace
// (a 20 ms period every 20 ms, never faster: a late period is not caught up), keeping all it wrote as s16 stereo
class BackendPlayer {
 public:
  static constexpr std::size_t kPeriod = 960;

  explicit BackendPlayer(PcmInput& music) : backend_(2, {&music, nullptr}) {
    backend_.prepare(kPeriod);
    backend_.setGateOpen(true);
    thread_ = std::thread([this] { run(); });
  }

  ~BackendPlayer() { stop(); }

  BackendPlayer(const BackendPlayer&) = delete;
  BackendPlayer& operator=(const BackendPlayer&) = delete;

  void stop() {
    running_ = false;
    if (thread_.joinable())
      thread_.join();
  }

  [[nodiscard]] std::vector<std::int16_t> output() const {
    std::scoped_lock lock(mutex_);
    return output_;
  }

 private:
  void run() {
    const std::vector<std::int16_t> overlays(kPeriod, 0);
    std::vector<std::int16_t> out(kPeriod * 2);
    auto next = std::chrono::steady_clock::now();
    while (running_) {
      backend_.process(overlays, false, out);
      {
        std::scoped_lock lock(mutex_);
        output_.insert(output_.end(), out.begin(), out.end());
      }
      next = std::max(next + std::chrono::milliseconds(20), std::chrono::steady_clock::now());
      std::this_thread::sleep_until(next);
    }
  }

  PcmBackend backend_;
  std::atomic<bool> running_{true};
  mutable std::mutex mutex_;
  std::vector<std::int16_t> output_;
  std::thread thread_;
};

}  // namespace cadence::test_support
