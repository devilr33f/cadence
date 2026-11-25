#pragma once

#include "backend/pcm-reader.h"
#include "dsp/gain-ramp.h"
#include "result.h"
#include "ring/cadence_ring.h"

#include <atomic>
#include <cstddef>
#include <cstdint>
#include <memory>
#include <span>
#include <string_view>

namespace cadence {

// counters of a ring since it was made; never audio
struct RingStatus {
  std::uint64_t readPos = 0;
  std::uint64_t writePos = 0;
  std::uint32_t generation = 0;
  std::uint32_t writerState = CADENCE_RING_STOPPED;
  std::uint32_t underruns = 0;
  std::int64_t latencyNs = 0;
  // the reader's own, since the ring was made: frames handed out, reads while open, reads that found nothing, silent
  // reads held for a refill, and refills that ended at kMaxRefillHoldMs rather than at their target
  std::uint64_t framesRead = 0;
  std::uint64_t reads = 0;
  std::uint64_t emptyReads = 0;
  std::uint64_t heldReads = 0;
  std::uint64_t refillTimeouts = 0;
  bool open = false;
  bool refilling = false;
  bool muted = false;
  float gain = 1.0f;
};

// the mixer's end of a player's pcm ring (mpv's ao_cadence, ring/cadence_ring.h): one memfd sealed against grow and
// shrink, holding the header and float stereo 48 kHz frames, and one eventfd the reader signals after each period so
// the writer wakes for room. the reader makes both; the player maps the ring by its fd (in process on android, over
// SCM_RIGHTS otherwise) and never closes either.
//
// read is the playback thread's: a flush the writer made drops what it queued before it, a paused writer holds its
// frames (as a device pause does) and reads as silence, and after each read the reader publishes when the frame at its
// position will be heard, so the writer's own a/v sync follows the device's clock. the ring is heard only while open:
// a closed one (the session lost the speaker, or never had it) is not read at all, so a writer that is not paused
// blocks on a full ring rather than lose its place. after a flush (a seek, a loop's restart) the reader holds: it plays
// silence and keeps its position, published as frozen, until the writer refilled the ring to its refill target, so
// the output never drains a ring that has just been emptied (s5: ~13 underruns and 0.1 s lost per seek otherwise).
// the session's own gain (its duck, its mute) ramps on the frames
// read; it never raises them (rule 7). open, gain and status are safe from any thread
class RingInput final : public PcmReader {
 public:
  // capacity in frames, a power of two from 256 to 65536
  static Result<std::unique_ptr<RingInput>, std::string_view> create(std::uint32_t capacityFrames);
  // the capacity for an output that reads burstFrames a callback (or period): at least two of them and 120 ms, as a
  // power of two. one burst or 40 ms underran on the stick's aaudio (s5 device half), so it is never a constant
  [[nodiscard]] static std::uint32_t capacityFor(std::size_t burstFrames);
  ~RingInput() override;

  RingInput(const RingInput&) = delete;
  RingInput& operator=(const RingInput&) = delete;

  [[nodiscard]] int ringFd() const { return ringFd_; }
  [[nodiscard]] int eventFd() const { return eventFd_; }
  [[nodiscard]] std::uint32_t capacity() const { return mask_ + 1; }

  // whether the session holds the speaker; closed is the default
  void setOpen(bool open);
  [[nodiscard]] bool isOpen() const { return open_.load(std::memory_order_acquire); }
  // the session's gain, 0..1, ramped over rampMs from the gain it has; muted holds it at 0 until unmuted
  void setGain(float gain, std::uint32_t rampMs);
  void setMuted(bool muted);
  // frames a flushed ring must hold before it plays again; half the capacity by default, at most the capacity
  void setRefillTarget(std::uint32_t frames);
  // the longest a refill holds: a writer that queues less than the target (ao_cadence stops at its own
  // --ao-cadence-buffer, 40 ms unless told) is played once this much silence went out, never held for good
  static constexpr std::uint32_t kMaxRefillHoldMs = 200;

  std::size_t read(std::span<float> stereo, std::int64_t heardNs) override;

  // a ring pump's read instead of the mixer's (a group leader reads the ring into its feed, audiod's RingPump): what
  // the writer queued, up to stereo's frames, as it is: no refill hold, no gain (the device's own mixer applies its
  // duck and mute to what the group plays), nothing published. flushed says the writer flushed since the last read,
  // and what it queued before is skipped. 0 while closed; never waits. one reader at a time: the mixer's slot or a
  // pump, handed over with a happens-before (the slot cleared, the pump joined)
  std::size_t drain(std::span<float> stereo, bool& flushed);
  // that the frame at pos is heard at heardNs (CLOCK_MONOTONIC), published as read publishes its own position: a
  // pump knows when the group hears what it read, and a position it holds (frozen) until then
  void publishHeard(std::uint64_t pos, std::int64_t heardNs);
  // frames read or skipped by a flush so far
  [[nodiscard]] std::uint64_t readPosition() const;

  [[nodiscard]] RingStatus status() const;

 private:
  RingInput(int ringFd, int eventFd, cadence_ring_header* header, std::size_t bytes);
  void publish(std::int64_t heardNs);

  int ringFd_;
  int eventFd_;
  cadence_ring_header* h_;
  std::size_t bytes_;
  float* data_;
  std::uint32_t mask_;

  std::atomic<bool> open_{false};
  std::atomic<float> gain_{1.0f};
  std::atomic<std::uint32_t> rampMs_{0};
  std::atomic<bool> muted_{false};
  // bumped whenever the gain or the mute changes, so the playback thread retargets its ramp once
  std::atomic<std::uint32_t> gainSerial_{0};
  std::atomic<std::uint32_t> refillTarget_;

  // the playback thread's
  dsp::GainRamp ramp_;
  std::uint32_t seenGainSerial_ = 0;
  // a flush emptied the ring and it has not refilled yet, and the silence handed out since
  bool refilling_ = false;
  std::size_t heldFrames_ = 0;
  // the playback thread writes them, any thread reads them
  std::atomic<std::uint64_t> framesRead_{0};
  std::atomic<std::uint64_t> reads_{0};
  std::atomic<std::uint64_t> emptyReads_{0};
  std::atomic<std::uint64_t> heldReads_{0};
  std::atomic<std::uint64_t> refillTimeouts_{0};
  std::atomic<bool> refillingNow_{false};
};

// a writer of a ring, as ao_cadence is: for tests and the fake screen plane. one thread
class RingWriter {
 public:
  // maps the ring by its fd, which stays the caller's; nullptr when it is not a ring
  static std::unique_ptr<RingWriter> attach(int ringFd);
  ~RingWriter();

  RingWriter(const RingWriter&) = delete;
  RingWriter& operator=(const RingWriter&) = delete;

  // queues what fits of interleaved stereo frames and returns how many frames it took
  std::size_t write(std::span<const float> stereo);
  // a seek: nothing queued before it is heard after it
  void flush();
  void setState(std::uint32_t state);
  // the reader's timestamp: when the frame at pos is heard; false until it published one
  bool timestamp(std::uint64_t& pos, std::int64_t& heardNs) const;
  [[nodiscard]] std::uint64_t written() const;

 private:
  RingWriter(cadence_ring_header* header, std::size_t bytes);

  cadence_ring_header* h_;
  std::size_t bytes_;
  float* data_;
  std::uint32_t mask_;
};

}  // namespace cadence
