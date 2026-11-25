#include "backend/presentation-clock.h"

#include "dsp/format.h"

namespace cadence {

namespace {

std::int64_t framesNs(std::int64_t frames) {
  return frames * 1'000'000'000LL / static_cast<std::int64_t>(dsp::kRate);
}

}  // namespace

void PresentationClock::period(std::uint64_t frames, std::int64_t heardNs, std::int64_t latencyNs,
                               std::uint64_t periodFrames, std::uint64_t musicRead) {
  const auto lock = lock_.load(std::memory_order_relaxed);
  lock_.store(lock + 1, std::memory_order_relaxed);
  std::atomic_thread_fence(std::memory_order_release);
  frames_.store(frames, std::memory_order_relaxed);
  heardNs_.store(heardNs, std::memory_order_relaxed);
  latencyNs_.store(latencyNs, std::memory_order_relaxed);
  periodFrames_.store(periodFrames, std::memory_order_relaxed);
  musicRead_.store(musicRead, std::memory_order_relaxed);
  lock_.store(lock + 2, std::memory_order_release);
}

void PresentationClock::anchor(std::int64_t mediaNs, std::uint64_t frames) {
  const auto lock = anchorLock_.load(std::memory_order_relaxed);
  anchorLock_.store(lock + 1, std::memory_order_relaxed);
  std::atomic_thread_fence(std::memory_order_release);
  anchorMediaNs_.store(mediaNs, std::memory_order_relaxed);
  anchorFrames_.store(frames, std::memory_order_relaxed);
  anchored_.store(true, std::memory_order_relaxed);
  anchorLock_.store(lock + 2, std::memory_order_release);
}

void PresentationClock::clearAnchor() {
  const auto lock = anchorLock_.load(std::memory_order_relaxed);
  anchorLock_.store(lock + 1, std::memory_order_relaxed);
  std::atomic_thread_fence(std::memory_order_release);
  anchored_.store(false, std::memory_order_relaxed);
  anchorLock_.store(lock + 2, std::memory_order_release);
}

std::optional<PresentationSample> PresentationClock::sample() const {
  for (int tries = 0; tries < 16; ++tries) {
    const auto before = lock_.load(std::memory_order_acquire);
    const auto anchorBefore = anchorLock_.load(std::memory_order_acquire);
    if ((before & 1) != 0 || (anchorBefore & 1) != 0)
      continue;
    const auto frames = frames_.load(std::memory_order_relaxed);
    const auto heardNs = heardNs_.load(std::memory_order_relaxed);
    const auto latencyNs = latencyNs_.load(std::memory_order_relaxed);
    const bool anchored = anchored_.load(std::memory_order_relaxed);
    const auto mediaNs = anchorMediaNs_.load(std::memory_order_relaxed);
    const auto anchorFrames = anchorFrames_.load(std::memory_order_relaxed);
    std::atomic_thread_fence(std::memory_order_acquire);
    if (lock_.load(std::memory_order_relaxed) != before || anchorLock_.load(std::memory_order_relaxed) != anchorBefore)
      continue;
    if (!anchored || heardNs == 0)
      return std::nullopt;
    const auto offset = static_cast<std::int64_t>(frames) - static_cast<std::int64_t>(anchorFrames);
    return PresentationSample{mediaNs + framesNs(offset), heardNs, latencyNs};
  }
  return std::nullopt;
}

std::optional<Playhead> PresentationClock::playhead() const {
  for (int tries = 0; tries < 16; ++tries) {
    const auto before = lock_.load(std::memory_order_acquire);
    if ((before & 1) != 0)
      continue;
    const Playhead out{frames_.load(std::memory_order_relaxed), heardNs_.load(std::memory_order_relaxed),
                       latencyNs_.load(std::memory_order_relaxed), periodFrames_.load(std::memory_order_relaxed),
                       musicRead_.load(std::memory_order_relaxed)};
    std::atomic_thread_fence(std::memory_order_acquire);
    if (lock_.load(std::memory_order_relaxed) != before)
      continue;
    if (out.heardNs == 0)
      return std::nullopt;
    return out;
  }
  return std::nullopt;
}

}  // namespace cadence
