#include "sync/clock-map.h"

#include <algorithm>
#include <cmath>

namespace cadence::sync {

std::int64_t ClockMapping::toLeader(std::int64_t localNs) const {
  const double d = static_cast<double>(localNs - atNs);
  return localNs + static_cast<std::int64_t>(std::llround(offsetNs + skew * d));
}

// leader = local + offset + skew * (local - at), so local = (leader - offset + skew * at) / (1 + skew), written
// around at so no product of two clock readings is ever formed
std::int64_t ClockMapping::toLocal(std::int64_t leaderNs) const {
  const double d = static_cast<double>(leaderNs - atNs) - offsetNs;
  return atNs + static_cast<std::int64_t>(std::llround(d / (1.0 + skew)));
}

ClockSmoother::ClockSmoother(ClockSmootherConfig config) : config_(config) {}

void ClockSmoother::reset() { current_.reset(); }

ClockMapping ClockSmoother::update(const ClockEstimate& e) {
  const bool good = e.splitNs < config_.syncedBelowNs && e.spanNs >= config_.minSpanNs;
  if (!current_) {
    current_ = ClockMapping{.offsetNs = e.offsetNs, .skew = e.skew, .atNs = e.at, .errorNs = e.splitNs,
                            .generation = generation_, .synced = good};
    return *current_;
  }
  auto& m = *current_;
  // what the published line says now, against the estimate
  const double now = m.offsetNs + m.skew * static_cast<double>(e.at - m.atNs);
  const double diff = e.offsetNs - now;
  if (std::abs(diff) > config_.stepNs) {
    ++generation_;
    lastStepNs_ = diff;
    m = ClockMapping{.offsetNs = e.offsetNs, .skew = e.skew, .atNs = e.at, .errorNs = e.splitNs,
                     .generation = generation_, .synced = good};
    return m;
  }
  const double elapsed = static_cast<double>(std::max<std::int64_t>(e.at - m.atNs, 0));
  const double room = config_.maxSlew * elapsed;
  m.offsetNs = now + std::clamp(diff, -room, room);
  m.skew = e.skew;
  m.atNs = e.at;
  m.errorNs = e.splitNs;
  m.synced = good;
  return m;
}

void SharedClockMapping::store(const ClockMapping& m) {
  const auto lock = lock_.load(std::memory_order_relaxed);
  lock_.store(lock + 1, std::memory_order_relaxed);
  std::atomic_thread_fence(std::memory_order_release);
  offsetNs_.store(m.offsetNs, std::memory_order_relaxed);
  skew_.store(m.skew, std::memory_order_relaxed);
  atNs_.store(m.atNs, std::memory_order_relaxed);
  errorNs_.store(m.errorNs, std::memory_order_relaxed);
  generation_.store(m.generation, std::memory_order_relaxed);
  synced_.store(m.synced, std::memory_order_relaxed);
  valid_.store(true, std::memory_order_relaxed);
  lock_.store(lock + 2, std::memory_order_release);
}

void SharedClockMapping::clear() {
  const auto lock = lock_.load(std::memory_order_relaxed);
  lock_.store(lock + 1, std::memory_order_relaxed);
  std::atomic_thread_fence(std::memory_order_release);
  valid_.store(false, std::memory_order_relaxed);
  lock_.store(lock + 2, std::memory_order_release);
}

std::optional<ClockMapping> SharedClockMapping::load() const {
  for (int tries = 0; tries < 16; ++tries) {
    const auto before = lock_.load(std::memory_order_acquire);
    if ((before & 1) != 0)
      continue;
    ClockMapping m;
    const bool valid = valid_.load(std::memory_order_relaxed);
    m.offsetNs = offsetNs_.load(std::memory_order_relaxed);
    m.skew = skew_.load(std::memory_order_relaxed);
    m.atNs = atNs_.load(std::memory_order_relaxed);
    m.errorNs = errorNs_.load(std::memory_order_relaxed);
    m.generation = generation_.load(std::memory_order_relaxed);
    m.synced = synced_.load(std::memory_order_relaxed);
    std::atomic_thread_fence(std::memory_order_acquire);
    if (lock_.load(std::memory_order_relaxed) != before)
      continue;
    if (!valid)
      return std::nullopt;
    return m;
  }
  // a writer that never lets go: none rather than a torn mapping
  return std::nullopt;
}

}  // namespace cadence::sync
