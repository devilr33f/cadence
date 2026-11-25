#pragma once

#include "playback/thread-priority.h"

#include <atomic>
#include <optional>

namespace cadence::sync {

// what raising one thread's priority came to (android alone acts, see raiseThreadPriority): set once by that thread,
// read from any; none until it ran
class RaisedPriority {
 public:
  void raise(int nice) {
    const auto priority = raiseThreadPriority(nice);
    attempted_.store(priority.attempted, std::memory_order_relaxed);
    nice_.store(priority.nice, std::memory_order_relaxed);
    error_.store(priority.error, std::memory_order_relaxed);
    done_.store(true, std::memory_order_release);
  }
  [[nodiscard]] std::optional<ThreadPriority> get() const {
    if (!done_.load(std::memory_order_acquire))
      return std::nullopt;
    return ThreadPriority{attempted_.load(std::memory_order_relaxed), nice_.load(std::memory_order_relaxed),
                          error_.load(std::memory_order_relaxed)};
  }

 private:
  std::atomic<bool> done_{false};
  std::atomic<bool> attempted_{false};
  std::atomic<int> nice_{0};
  std::atomic<int> error_{0};
};

// a group device's threads: the playout pushes into the music input the mixer reads (kNiceAudio); the leader's
// timeline, the stream and the clock feed it with a buffer of their own ahead (kNiceAudioFeed)
struct GroupThreads {
  RaisedPriority playout;
  // the leader's alone
  RaisedPriority timeline;
  RaisedPriority stream;
  RaisedPriority clock;
};

}  // namespace cadence::sync
