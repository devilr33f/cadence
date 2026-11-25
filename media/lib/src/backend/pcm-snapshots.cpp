#include "backend/pcm-snapshots.h"

#include "dsp/format.h"

#include <algorithm>

namespace cadence {

namespace {

constexpr std::size_t kRing = PcmSnapshot::kFrames;

std::int64_t framesNs(std::size_t frames) {
  return static_cast<std::int64_t>(static_cast<std::uint64_t>(frames) * 1'000'000'000ULL / dsp::kRate);
}

}  // namespace

void PcmSnapshots::push(std::span<const float> stereo, std::int64_t heardNs) {
  const std::size_t frames = stereo.size() / dsp::kStereo;
  const auto lock = lock_.load(std::memory_order_relaxed);
  lock_.store(lock + 1, std::memory_order_relaxed);
  std::atomic_thread_fence(std::memory_order_release);
  // only the last kRing frames of a long period can show
  const std::size_t skip = frames > kRing ? frames - kRing : 0;
  const auto total = total_.load(std::memory_order_relaxed);
  for (std::size_t i = skip; i < frames; ++i) {
    const auto slot = static_cast<std::size_t>((total + i) % kRing);
    ring_[2 * slot].store(stereo[2 * i], std::memory_order_relaxed);
    ring_[2 * slot + 1].store(stereo[2 * i + 1], std::memory_order_relaxed);
  }
  total_.store(total + frames, std::memory_order_relaxed);
  endNs_.store(heardNs != 0 ? heardNs + framesNs(frames) : 0, std::memory_order_relaxed);
  lock_.store(lock + 2, std::memory_order_release);
  seq_.fetch_add(1, std::memory_order_release);
}

bool PcmSnapshots::take(PcmSnapshot& out) const {
  for (int tries = 0; tries < 16; ++tries) {
    const auto before = lock_.load(std::memory_order_acquire);
    if ((before & 1) != 0)
      continue;
    const auto total = total_.load(std::memory_order_relaxed);
    if (total == 0)
      return false;
    const std::size_t frames = static_cast<std::size_t>(std::min<std::uint64_t>(total, kRing));
    for (std::size_t i = 0; i < frames; ++i) {
      const auto slot = static_cast<std::size_t>((total - frames + i) % kRing);
      out.samples[2 * i] = ring_[2 * slot].load(std::memory_order_relaxed);
      out.samples[2 * i + 1] = ring_[2 * slot + 1].load(std::memory_order_relaxed);
    }
    const auto endNs = endNs_.load(std::memory_order_relaxed);
    std::atomic_thread_fence(std::memory_order_acquire);
    if (lock_.load(std::memory_order_relaxed) != before)
      continue;
    out.frames = frames;
    out.monoNs = endNs != 0 ? endNs - framesNs(frames) : 0;
    out.seq = seq_.load(std::memory_order_acquire);
    return true;
  }
  return false;
}

}  // namespace cadence
