#pragma once

#include <array>
#include <chrono>
#include <condition_variable>
#include <cstddef>
#include <cstdint>
#include <deque>
#include <mutex>
#include <optional>
#include <span>
#include <vector>

namespace cadence {

// 20 ms of 16 kHz mono: the samples in one opus frame
inline constexpr std::size_t kPcm16FrameSamples = 320;
using Pcm16Frame = std::array<std::int16_t, kPcm16FrameSamples>;

// cuts a mono stream into whole frames whatever size its chunks arrive in
class PcmFramer {
 public:
  // consumes input until a frame is complete and returns it; a shorter tail stays pending
  std::optional<Pcm16Frame> take(std::span<const std::int16_t>& input);
  void clear() { pending_ = 0; }
  [[nodiscard]] std::size_t pending() const { return pending_; }

 private:
  Pcm16Frame frame_{};
  std::size_t pending_ = 0;
};

struct FifoRead {
  std::size_t frames = 0;
  // audio was lost before these frames, so they do not continue the previous read
  bool afterGap = false;
};

// bounded fifo of interleaved capture frames from one producer thread to one consumer thread.
// when full it drops the oldest frames; every loss is recorded as a gap that no read spans
class CaptureFifo {
 public:
  CaptureFifo(std::size_t capacityFrames, std::size_t channels);

  // returns how many of the oldest frames were dropped to make room
  std::size_t push(std::span<const std::int16_t> interleaved);
  // the frames pushed next do not continue those pushed before
  void markGap();
  // waits up to timeout for frames and returns at most interleaved.size() / channels of them
  FifoRead pop(std::span<std::int16_t> interleaved, std::chrono::milliseconds timeout);
  // drops every queued frame and records a gap, so the consumer also drops any frames it holds
  void discard();
  // wakes a waiting pop; later pops no longer wait
  void close();
  [[nodiscard]] std::size_t size() const;

 private:
  void copyFrames(std::uint64_t position, const std::int16_t* source, std::size_t frames);

  const std::size_t capacity_;
  const std::size_t channels_;
  mutable std::mutex mutex_;
  std::condition_variable readable_;
  std::vector<std::int16_t> ring_;
  // positions count frames ever pushed, so they never wrap
  std::uint64_t readPosition_ = 0;
  std::uint64_t writePosition_ = 0;
  // ascending positions of frames that follow lost audio
  std::deque<std::uint64_t> gaps_;
  bool closed_ = false;
};

}  // namespace cadence
