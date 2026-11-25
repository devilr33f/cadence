#pragma once

#include "aaudio/frame-ring.h"
#include "aaudio/stream.h"
#include "backend/span-histogram.h"
#include "playback/alsa-playback.h"

#include <atomic>
#include <cstddef>
#include <cstdint>
#include <memory>

namespace cadence {

// counters of the aaudio output since it was made; never audio
struct AAudioPlaybackStatus {
  // callbacks that found the ring short of a whole callback while playing, and played the rest as silence
  std::uint64_t underruns = 0;
  // reopens after the stream died (a disconnect)
  std::uint64_t reopens = 0;
  // aaudio's own xrun count on the current stream
  std::int32_t xruns = 0;
  std::int32_t framesPerBurst = 0;
  // the ring's fill target and capacity
  std::size_t fillFrames = 0;
  std::size_t capacityFrames = 0;
  // whether the last delay came from aaudio's presentation timestamp (else from the frames it has read)
  bool timestamped = false;
};

// what the callback saw since the last take: how often it ran (the time between two callbacks), and the least the ring
// held when one began, which says how close the player came to an underrun
struct AAudioWindow {
  SpanSummary period;
  // frames; the capacity when no callback ran
  std::size_t fillMinFrames = 0;
};

// the ring's fill target in aaudio's bursts: the callback takes one, so two more are the player's headroom against a
// late wake (a ui that holds the cpus, the stick's four slow cores); two alone underran in s5 tests under load
inline constexpr std::size_t kFillBursts = 3;

// audiod's playback device on android: an aaudio output stream in low-latency mode, s16 at the player's rate and
// channels, fed by a data callback from a ring the player writes. the ring's fill target is the player's fill, but
// never under kFillBursts of aaudio's bursts: one burst underran on the stick (s5 device half), and the burst is
// aaudio's choice, so it is read from the stream, never fixed. the stream starts at open and plays silence until the ring has
// filled to its target; it holds again the same way after a rewind or an underrun, so it never plays out a ring that
// was just emptied. the delay is the ring's fill plus what aaudio holds: from its presentation timestamp, which takes
// the output's own latency into account, and, until it has one, from the frames it has read. a stream that dies is
// reopened by the next write, which reports Recovered. the ring is the only thing a rewind takes back: aaudio's own
// buffer always plays out
class AAudioPlayback final : public PcmSink {
 public:
  explicit AAudioPlayback(aaudio::Api& api);
  ~AAudioPlayback() override;

  AAudioPlayback(const AAudioPlayback&) = delete;
  AAudioPlayback& operator=(const AAudioPlayback&) = delete;

  // s16 only, the rate and channels exactly; grants the period, a fill of at least kFillBursts bursts and a buffer
  // of at least the fill and a period
  Result<PlaybackFormat, PlaybackError> open(const PlaybackFormat& requested) override;
  PlaybackWrite write(std::span<const std::int16_t> interleaved, std::chrono::milliseconds timeout) override;
  Result<std::size_t, PlaybackError> rewind() override;
  Result<std::size_t, PlaybackError> delayFrames() override;
  std::optional<PlaybackError> drop() override;
  void close() override;

  [[nodiscard]] AAudioPlaybackStatus status() const;
  // any thread: the callback's window since the last take, and a new one begun
  AAudioWindow takeWindow();

 private:
  // the stream and the ring for format_; the error names the step
  std::optional<PlaybackError> openStream();
  void closeStream();
  void onData(std::span<std::int16_t> out);

  aaudio::Api& api_;
  PlaybackFormat format_;
  std::unique_ptr<aaudio::Stream> stream_;
  aaudio::FrameRing ring_;
  std::size_t fill_ = 0;
  std::int32_t burst_ = 0;
  bool open_ = false;
  std::uint64_t reopens_ = 0;
  bool timestamped_ = false;

  // the callback's own, published for the player's thread
  std::atomic<bool> holding_{true};
  std::atomic<bool> starved_{false};
  std::atomic<bool> dead_{false};
  std::atomic<std::uint64_t> underruns_{0};
  // frames the callbacks handed aaudio (silence too), what its timestamps count, under a sequence the callback makes
  // odd while it runs: the delay reads it with the ring's fill as one, never a ring a callback emptied against a count
  // it has not yet raised (a burst too little, ~43 ms on the stick)
  std::atomic<std::uint32_t> sequence_{0};
  std::atomic<std::int64_t> delivered_{0};
  // the window's
  std::int64_t lastCallbackNs_ = 0;
  SpanHistogram<256> periods_{1'000'000};
  std::atomic<std::size_t> fillMin_{SIZE_MAX};
};

}  // namespace cadence
