#pragma once

#include "dsp/format.h"
#include "dsp/resampler.h"

#include <atomic>
#include <cstddef>
#include <cstdint>
#include <limits>
#include <optional>
#include <span>
#include <vector>

namespace cadence {

// counters since construction; never audio
struct PcmInputStatus {
  // 48 kHz frames queued
  std::uint64_t framesIn = 0;
  // 48 kHz frames tryPush had no room for
  std::uint64_t dropped = 0;
  // reads that came up short while a producer was attached
  std::uint64_t underruns = 0;
  // 48 kHz frames the consumer has still to read: none from before a clear, and none while a clear waits for the
  // producer, so a producer that pushed its last can wait for 0 before it says it ended
  std::uint64_t queued = 0;
  // 48 kHz frames the consumer handed out, which a producer counts its position by
  std::uint64_t framesRead = 0;
  // the ring's positions, 48 kHz frames since construction: where the next push lands and the next read starts
  // (a clear moves the reader up to what was written before it)
  std::uint64_t writePos = 0;
  std::uint64_t readPos = 0;
};

// one source's way into the pcm backend: a single-producer single-consumer ring of float stereo 48 kHz that holds
// at most 2 s. the producer pushes interleaved float or s16 at its own rate, 1 or 2 channels; push resamples on the
// producer's thread (a resampler made again, after the old one's tail, whenever the rate or channels change; 48 kHz
// is only copied), so the consumer, the playback thread, never locks, allocates or waits.
//
// push waits while the ring is full, polling, as a decoder that pulls its input should; tryPush drops what does not
// fit, as a real-time producer must. clear, from any thread, moves the generation on: a push in progress stops, the
// next push acknowledges the clear first, and the consumer never hands out audio queued before the clear, also when
// it had already copied some of it in the same read. until a producer acknowledges, the ring reads empty
class PcmInput {
 public:
  static constexpr std::size_t kCapacityFrames = 2 * dsp::kRate;

  PcmInput();

  PcmInput(const PcmInput&) = delete;
  PcmInput& operator=(const PcmInput&) = delete;

  // producer: one thread at a time. the resampler, the scratch buffers and the generation the producer last saw are
  // its own and unsynchronized, so a thread that takes over pushing needs a happens-before with the last push of the
  // one before (join it, or hand over under a lock). push waits for as long as the ring is full and nothing reads it
  // (no player, or a stopped one), until a clear: a producer that stops calls clear(), and is seen done with its last
  // push (joined, or idle under a lock) before another starts.
  //
  // true when all of it was queued; false when a clear cut it short or the format is refused (1 or 2 channels,
  // 8-192 kHz), and then the rest is not queued
  bool push(std::span<const float> interleaved, std::uint32_t rate, std::size_t channels);
  bool push(std::span<const std::int16_t> interleaved, std::uint32_t rate, std::size_t channels);
  // as push, but drops what finds no room instead of waiting
  bool tryPush(std::span<const float> interleaved, std::uint32_t rate, std::size_t channels);
  bool tryPush(std::span<const std::int16_t> interleaved, std::uint32_t rate, std::size_t channels);
  // the end of a stream: lets the resampler's tail out (waiting while the ring is full, as push does), so its last
  // milliseconds play too, and the next push starts afresh. false when a clear cut it short
  bool finish();

  // producer: plays what it pushes this many ppm faster (positive) or slower than its rate says, through the
  // resampler, without a gap (Resampler::setTrim): a source whose clock drifts against the output's is steered by it
  // instead of cut. it applies to a resampled rate alone (48 kHz input is copied as it is, unless setAlwaysResample),
  // and lasts until a clear
  void steer(std::int32_t ppm);
  [[nodiscard]] std::int32_t steering() const { return trimPpm_; }
  // producer: input frames the resampler holds back (its latency), 0 while 48 kHz is copied: a frame pushed now
  // lands this much after the write position
  [[nodiscard]] std::size_t heldFrames() const { return resampler_ ? resampler_->latencyFrames() : 0; }
  // the same to a fraction of a frame, as the resampler's phase and steering have it
  [[nodiscard]] double pendingFrames() const { return resampler_ ? resampler_->pendingFrames() : 0.0; }

  // any thread: 48 kHz input goes through the resampler too, from the producer's next push, so steer acts on it (a
  // group's playout steers its 48 kHz frames onto the device's clock). it outlasts a clear
  void setAlwaysResample(bool on) { alwaysResample_.store(on, std::memory_order_relaxed); }
  // any thread: the producer steers by ppm at its next push, as if it called steer itself (a consumer that sees the
  // input run ahead or behind, the group leader's feed)
  void requestSteer(std::int32_t ppm) { requestedPpm_.store(ppm, std::memory_order_relaxed); }

  // any thread
  void clear();
  // any thread: the clears so far, so a consumer tells a clear apart from a producer that merely paused
  [[nodiscard]] std::uint64_t clears() const { return generation_.load(std::memory_order_acquire); }
  // the reader's position (PcmInputStatus::readPos), for the consumer's own records
  [[nodiscard]] std::uint64_t readPosition() const { return readPos_.load(std::memory_order_acquire); }
  // while a producer is attached, a read that comes up short counts as an underrun
  void attach();
  void detach();
  [[nodiscard]] PcmInputStatus status() const;

  // consumer: fills stereo (interleaved, whole frames) with the next frames and silence after them, and returns
  // how many frames it read
  std::size_t read(std::span<float> stereo);

 private:
  template <typename Sample>
  bool enqueue(std::span<const Sample> interleaved, std::uint32_t rate, std::size_t channels, bool wait);
  // producer: a clear since the last push makes the ring's audio stale up to the write position
  void acknowledge(std::uint64_t generation);
  [[nodiscard]] bool prepare(std::uint32_t rate, std::size_t channels, bool wait);
  // producer: queues what the resampler still holds and drops it; false when the ring took not all of it
  bool flushResampler(bool wait);
  // producer: queues 48 kHz frames of 1 or 2 channels as stereo
  bool writeConverted(std::span<const float> samples, std::size_t channels, bool wait);
  // producer: queues 48 kHz stereo frames; false when a clear stopped it or tryPush dropped some
  bool write(std::span<const float> stereo, bool wait);

  std::vector<float> ring_;
  // frames ever written and read; written only by the producer and the consumer respectively
  std::atomic<std::uint64_t> writePos_{0};
  std::atomic<std::uint64_t> readPos_{0};

  std::atomic<std::uint64_t> generation_{0};
  // the producer's acknowledgement of a clear: frames before ackPos_ are of an older generation than ackGeneration_.
  // ackPos_ is stored before ackGeneration_, so a reader that sees the generation sees at least its position
  std::atomic<std::uint64_t> ackPos_{0};
  std::atomic<std::uint64_t> ackGeneration_{0};

  std::atomic<bool> attached_{false};
  std::atomic<bool> alwaysResample_{false};
  static constexpr std::int32_t kNoRequest = std::numeric_limits<std::int32_t>::min();
  std::atomic<std::int32_t> requestedPpm_{kNoRequest};
  std::atomic<std::uint64_t> framesIn_{0};
  std::atomic<std::uint64_t> dropped_{0};
  std::atomic<std::uint64_t> underruns_{0};
  std::atomic<std::uint64_t> framesRead_{0};

  // the producer's own
  std::uint64_t producerGeneration_ = 0;
  std::uint32_t rate_ = 0;
  std::size_t channels_ = 0;
  // whether the resampler in use was made for 48 kHz because alwaysResample_ was set
  bool forced_ = false;
  std::optional<dsp::Resampler> resampler_;
  std::int32_t trimPpm_ = 0;
  std::vector<float> converted_;
  std::vector<float> resampled_;
  std::vector<float> stereo_;

  // the consumer's own
  std::uint64_t consumerGeneration_ = 0;
};

}  // namespace cadence
