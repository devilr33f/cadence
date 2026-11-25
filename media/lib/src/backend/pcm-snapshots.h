#pragma once

#include <array>
#include <atomic>
#include <cstddef>
#include <cstdint>
#include <span>

namespace cadence {

// the last frames of the music as the screen's visualizer takes them (TapEnvelope's PcmSnapshot), before anything
// levels it: interleaved float stereo 48 kHz
struct PcmSnapshot {
  static constexpr std::size_t kFrames = 2048;

  // grows with every period the playback thread pushed
  std::uint64_t seq = 0;
  // CLOCK_MONOTONIC ns when the first frame is heard (output and limiter latency, the delay trim), 0 when the device's
  // delay is not known
  std::int64_t monoNs = 0;
  std::size_t frames = 0;
  std::array<float, kFrames * 2> samples{};
};

// the playback thread pushes each period's music; any thread takes a consistent copy of the latest kFrames frames.
// a seqlock over a ring the size of a snapshot: push never waits, a take that races a push tries again
class PcmSnapshots {
 public:
  // the playback thread; heardNs is when the block's first frame sounds
  void push(std::span<const float> stereo, std::int64_t heardNs);
  // false until the first push, or when pushes kept racing it
  bool take(PcmSnapshot& out) const;

 private:
  std::atomic<std::uint32_t> lock_{0};
  std::atomic<std::uint64_t> seq_{0};
  // frames ever pushed, and the heard time of the frame at that count
  std::atomic<std::uint64_t> total_{0};
  std::atomic<std::int64_t> endNs_{0};
  // relaxed atomics, so a take that races a push reads torn values it then throws away, never a data race
  std::array<std::atomic<float>, PcmSnapshot::kFrames * 2> ring_{};
};

}  // namespace cadence
