#pragma once

#include <algorithm>
#include <array>
#include <atomic>
#include <cstddef>
#include <cstdint>

namespace cadence {

// what a window of spans (a callback's period, a decode's time) came to, ns: the 95th percentile is the upper edge of
// its bucket, the max exact
struct SpanSummary {
  std::uint64_t count = 0;
  std::int64_t p95Ns = 0;
  std::int64_t maxNs = 0;
};

// a fixed histogram of spans in buckets of unitNs, the last one open-ended: one thread records (a real-time one may:
// it never locks, allocates or waits), another takes the window and starts the next. a record racing the take lands in
// either window, never lost twice or counted twice
template <std::size_t Buckets>
class SpanHistogram {
 public:
  explicit constexpr SpanHistogram(std::int64_t unitNs) : unitNs_(unitNs > 0 ? unitNs : 1) {}

  void record(std::int64_t ns) {
    if (ns < 0)
      ns = 0;
    // in 64 bits before the index: a long span must land in the last bucket, not wrap a 32-bit size_t
    const auto bucket = std::min<std::int64_t>(ns / unitNs_, static_cast<std::int64_t>(Buckets - 1));
    counts_[static_cast<std::size_t>(bucket)].fetch_add(1, std::memory_order_relaxed);
    if (ns > max_.load(std::memory_order_relaxed))
      max_.store(ns, std::memory_order_relaxed);
  }

  SpanSummary take() {
    std::array<std::uint32_t, Buckets> counts{};
    std::uint64_t total = 0;
    for (std::size_t i = 0; i < Buckets; ++i) {
      counts[i] = counts_[i].exchange(0, std::memory_order_relaxed);
      total += counts[i];
    }
    SpanSummary out;
    out.count = total;
    out.maxNs = max_.exchange(0, std::memory_order_relaxed);
    if (total == 0)
      return out;
    // the smallest bucket at or past 95 % of the spans
    const auto want = (total * 95 + 99) / 100;
    std::uint64_t seen = 0;
    for (std::size_t i = 0; i < Buckets; ++i) {
      seen += counts[i];
      if (seen >= want) {
        out.p95Ns = static_cast<std::int64_t>(i + 1) * unitNs_;
        break;
      }
    }
    // the open bucket's edge says nothing: the max does
    if (out.p95Ns > out.maxNs)
      out.p95Ns = out.maxNs;
    return out;
  }

 private:
  std::int64_t unitNs_;
  std::array<std::atomic<std::uint32_t>, Buckets> counts_{};
  std::atomic<std::int64_t> max_{0};
};

}  // namespace cadence
