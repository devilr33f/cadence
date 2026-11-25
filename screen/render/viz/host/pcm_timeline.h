#pragma once

#include <atomic>
#include <cstddef>
#include <cstdint>
#include <mutex>
#include <span>
#include <vector>

namespace cadence::render::viz {

// the tap's pcm on the timeline of when it is heard. audiod's snapshots overlap (512 frames about every 800) and are
// stamped with the CLOCK_MONOTONIC time their first frame sounds; each is written at its place on the timeline, so
// overlaps land on the same frames and gaps stay silent, and a reader takes the contiguous frames ending at what is
// heard now: never the newest written (S5's viz ran ahead of the sound). the writer (the tap reader, real time) only
// try-locks and drops a snapshot when the reader holds the lock for its copy
class PcmTimeline {
 public:
  // keeps `frames` frames of mono at `rate`
  // 4 s: snapshots come stamped with when they are heard, a second ahead in a group and two over airplay; a ring of
  // 4096 frames (85 ms) had overwritten the frames heard now by then, and the visualizer saw silence
  explicit PcmTimeline(std::size_t frames = 4 * 48000, std::uint32_t rate = 48000);

  // a snapshot: interleaved samples of `channels` (mixed to mono) whose first frame sounds at mono_ns. a snapshot at
  // another rate, or more than a second off the timeline, starts it afresh
  void push(std::span<const float> interleaved, std::uint32_t channels, std::uint32_t rate, std::int64_t mono_ns);

  // the `out.size()` frames ending at the one heard at now_ns, oldest first; frames never written are 0. false when
  // nothing has been pushed
  bool window(std::int64_t now_ns, std::span<float> out);

  [[nodiscard]] std::uint32_t rate() const { return rate_; }
  // how far after now_ns the newest frame written is heard, ms (negative: the feed is behind); 0 before any
  double ahead_ms(std::int64_t now_ns);
  [[nodiscard]] std::uint64_t dropped() const { return dropped_.load(std::memory_order_relaxed); }

  // the feed's health since the last call (the host logs it every 10 s): snapshots placed and dropped, fresh starts
  // (a jump of a second or a new rate), frames a gap left silent, and frames a window asked for that were never written
  // or already gone (the feed behind, or the ring too short)
  struct Stats {
    std::uint64_t pushes = 0, dropped = 0, resets = 0, gap_frames = 0, missing_frames = 0;
  };
  Stats take_stats();

  // the timeline's frame indices, for a reader that takes every frame in order (the beat grid, fed ahead): the index
  // heard at t, one past the newest written, the frames [from, to) still held (fewer when the ring has moved past
  // `from`; the first index copied is returned), and a count that moves each time the timeline starts afresh (its
  // indices then begin again)
  std::int64_t index_of(std::int64_t t_ns);
  std::int64_t newest();
  std::int64_t read(std::int64_t from, std::int64_t to, std::vector<float>& out);
  std::uint64_t epoch();

 private:
  // the sample index heard at t
  [[nodiscard]] std::int64_t index_at(std::int64_t t_ns) const;

  std::mutex mu_;
  std::vector<float> ring_;
  std::uint32_t rate_;
  bool started_ = false;
  std::int64_t origin_ns_ = 0;
  // one past the newest index written; indices below newest_ - ring size are gone
  std::int64_t newest_ = 0;
  std::atomic<std::uint64_t> dropped_{0};
  Stats stats_;
  std::uint64_t epoch_ = 0;
};

}  // namespace cadence::render::viz
