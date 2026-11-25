#pragma once

#include "trace/trace.h"

#include <array>
#include <atomic>
#include <cstddef>
#include <cstdint>
#include <initializer_list>
#include <string_view>
#include <type_traits>

namespace cadence::trace {

static_assert(std::is_trivially_copyable_v<Mark>, "a mark is copied into the queue's cells whole");

// the marks of every thread on their way to the one that sends them: a bounded queue of fixed cells (dmitry
// vyukov's bounded mpmc queue), so a push never allocates, locks or waits. a push that finds it full drops the
// mark and counts it. the first push after the drainer's rearm writes wakeFd once (an eventfd's 8 bytes), a
// non-blocking write the drainer's loop watches: the only system call a realtime thread makes here
class Queue {
 public:
  // a second's worth of marks of every stream and capture at once, many times over
  static constexpr std::size_t kCapacity = 64;
  static_assert((kCapacity & (kCapacity - 1)) == 0);

  // wakeFd: -1 signals nothing (a test drains by hand)
  explicit Queue(int wakeFd = -1) noexcept;

  Queue(const Queue&) = delete;
  Queue& operator=(const Queue&) = delete;

  // any thread; false when it was full and the mark was dropped
  bool push(const Mark& mark) noexcept;
  // the drainer's: the oldest mark, false when none is queued
  bool pop(Mark& out) noexcept;
  // the drainer's, before it pops: the next push signals again
  void rearm() noexcept;
  // marks dropped since the last call
  std::uint64_t takeDropped() noexcept;

 private:
  struct Cell {
    std::atomic<std::size_t> seq{0};
    Mark mark;
  };

  void signal() noexcept;

  std::array<Cell, kCapacity> cells_;
  alignas(64) std::atomic<std::size_t> enqueue_{0};
  alignas(64) std::atomic<std::size_t> dequeue_{0};
  std::atomic<bool> signalled_{false};
  std::atomic<std::uint64_t> dropped_{0};
  int wakeFd_;
};

// what a realtime thread marks through: a queue, or none. with CADENCE_TRACING off a mark is a no-op the compiler
// drops
class Tracer {
 public:
  Tracer() = default;
  explicit Tracer(Queue* queue) noexcept : queue_(queue) {}

  void mark(Stage stage, Target target, std::string_view id, std::initializer_list<Value> values = {},
            std::int64_t atUnixNs = 0, std::int64_t durationNs = 0) const noexcept {
    if constexpr (kEnabled) {
      if (queue_ != nullptr)
        queue_->push(makeMark(stage, target, id, values, atUnixNs, durationNs));
    } else {
      (void)stage, (void)target, (void)id, (void)values, (void)atUnixNs, (void)durationNs;
    }
  }

  [[nodiscard]] bool active() const noexcept { return kEnabled && queue_ != nullptr; }

 private:
  Queue* queue_ = nullptr;
};

}  // namespace cadence::trace
