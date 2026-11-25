#pragma once

#include <atomic>
#include <cstdint>
#include <optional>

namespace cadence {

// what PresentationClock says: the media time sounding at a monotonic time, with the output latency in it
struct PresentationSample {
  std::int64_t mediaNs = 0;
  std::int64_t monoNs = 0;
  std::int64_t latencyNs = 0;
};

// the last period the playback thread told: the frame count before it, when its first frame sounds, the latency in it
struct Playhead {
  std::uint64_t frames = 0;
  std::int64_t heardNs = 0;
  std::int64_t latencyNs = 0;
  // the period's length, and the music input's read position once the period had read it: a frame pushed into the
  // music input at its write position w is heard at heardNs + (periodFrames + w - musicRead) frames, while the input
  // keeps up, however long ago the period was (no race with the queue, which a latency sampled then has)
  std::uint64_t periodFrames = 0;
  std::uint64_t musicRead = 0;
};

// the mixer's playback clock against CLOCK_MONOTONIC, for the one source whose media timeline the screen follows
// (airplay mirroring's pts): the playback thread says, each period, how many frames it processed before the period and
// when the period's first frame sounds; the source says which media time a given frame count carries. one clock for
// every source, the mixer's, so a mirror frame shows when its sound is heard. period never waits; the rest is safe
// from any thread
class PresentationClock {
 public:
  // the playback thread; heardNs 0 = the device's delay is not known
  void period(std::uint64_t frames, std::int64_t heardNs, std::int64_t latencyNs, std::uint64_t periodFrames = 0,
              std::uint64_t musicRead = 0);
  // the media time of the frame the mixer processes as frame number frames
  void anchor(std::int64_t mediaNs, std::uint64_t frames);
  void clearAnchor();

  // none until a period with a known delay and an anchor
  [[nodiscard]] std::optional<PresentationSample> sample() const;
  // none until a period with a known delay; what a source anchors by (the frame its media time is heard at)
  [[nodiscard]] std::optional<Playhead> playhead() const;

 private:
  std::atomic<std::uint32_t> lock_{0};
  std::atomic<std::uint64_t> frames_{0};
  std::atomic<std::int64_t> heardNs_{0};
  std::atomic<std::int64_t> latencyNs_{0};
  std::atomic<std::uint64_t> periodFrames_{0};
  std::atomic<std::uint64_t> musicRead_{0};

  std::atomic<std::uint32_t> anchorLock_{0};
  std::atomic<bool> anchored_{false};
  std::atomic<std::int64_t> anchorMediaNs_{0};
  std::atomic<std::uint64_t> anchorFrames_{0};
};

}  // namespace cadence
