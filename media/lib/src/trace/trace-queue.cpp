#include "trace/trace-queue.h"

#include <unistd.h>

#include <cstdint>

namespace cadence::trace {

Queue::Queue(int wakeFd) noexcept : wakeFd_(wakeFd) {
  for (std::size_t index = 0; index < kCapacity; ++index)
    cells_[index].seq.store(index, std::memory_order_relaxed);
}

bool Queue::push(const Mark& mark) noexcept {
  auto position = enqueue_.load(std::memory_order_relaxed);
  Cell* cell = nullptr;
  while (true) {
    cell = &cells_[position & (kCapacity - 1)];
    const auto seq = cell->seq.load(std::memory_order_acquire);
    const auto diff = static_cast<std::intptr_t>(seq) - static_cast<std::intptr_t>(position);
    if (diff == 0) {
      if (enqueue_.compare_exchange_weak(position, position + 1, std::memory_order_relaxed))
        break;
    } else if (diff < 0) {
      dropped_.fetch_add(1, std::memory_order_relaxed);
      return false;
    } else {
      position = enqueue_.load(std::memory_order_relaxed);
    }
  }
  cell->mark = mark;
  cell->seq.store(position + 1, std::memory_order_release);
  signal();
  return true;
}

bool Queue::pop(Mark& out) noexcept {
  auto position = dequeue_.load(std::memory_order_relaxed);
  Cell* cell = nullptr;
  while (true) {
    cell = &cells_[position & (kCapacity - 1)];
    const auto seq = cell->seq.load(std::memory_order_acquire);
    const auto diff = static_cast<std::intptr_t>(seq) - static_cast<std::intptr_t>(position + 1);
    if (diff == 0) {
      if (dequeue_.compare_exchange_weak(position, position + 1, std::memory_order_relaxed))
        break;
    } else if (diff < 0) {
      return false;
    } else {
      position = dequeue_.load(std::memory_order_relaxed);
    }
  }
  out = cell->mark;
  cell->seq.store(position + kCapacity, std::memory_order_release);
  return true;
}

// an exchange on both sides: a push whose signal this rearm swallowed is seen by the pops after it
void Queue::rearm() noexcept { signalled_.exchange(false, std::memory_order_acq_rel); }

std::uint64_t Queue::takeDropped() noexcept { return dropped_.exchange(0, std::memory_order_relaxed); }

void Queue::signal() noexcept {
  if (wakeFd_ < 0 || signalled_.exchange(true, std::memory_order_acq_rel))
    return;
  const std::uint64_t one = 1;
  // an eventfd full to its 2^64-2 is the only way this fails, and the drainer is then awake anyway
  [[maybe_unused]] const auto written = ::write(wakeFd_, &one, sizeof one);
}

}  // namespace cadence::trace
