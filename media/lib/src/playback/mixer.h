#pragma once

#include "playback/jitter-buffer.h"

#include <array>
#include <atomic>
#include <cstddef>
#include <cstdint>
#include <memory>
#include <mutex>
#include <optional>
#include <span>
#include <vector>

namespace cadence {

// sources in priority order, highest first: the device's replies, then the earcons and alarm tones mixed over
// them
enum class MixerSource : std::size_t { Voice, Tone };
inline constexpr std::size_t kMixerSources = 2;

// a tone ends as soon as it is queued, so it plays at once whatever the target; this holds a few seconds of
// them, an alarm's 2 s ring period and a spoken prompt's 3 s (kMaxPromptSamples) included
inline constexpr JitterBufferConfig kToneQueue{.targetSamples = 0, .maxSamples = 192000, .retiredIds = 64};

// what a ring loops when it is more than one fixed period (a sequence with a streamed url): fill writes exactly
// out.size() samples, silence where it has none yet. it runs on the playback thread under the mixer's lock, so it
// never blocks
class LoopFeed {
 public:
  virtual ~LoopFeed() = default;
  virtual void fill(std::span<std::int16_t> out) = 0;
};

struct MixerRead {
  // the most samples any source gave, at the front of the output; the rest is silence
  std::size_t samples = 0;
  // each source's, by MixerSource
  std::array<std::size_t, kMixerSources> sources{};
  // the voice source's: the status counters stay the voice queue's
  bool underrun = false;
  bool drained = false;
  // the watched voice response's last-sample boundary in this read's output (see JitterRead::marker)
  std::optional<std::size_t> marker;
  // the voice source's begun and starved responses (see JitterTrace)
  JitterTrace voiceTrace;
  // sum of the generations of the sources' queues this read came from and of the loop's stops
  std::uint64_t generation = 0;
};

// one jitter buffer per source, summed into one 48 kHz mono stream with a gain each; the software volume is the
// pcm backend's. any thread may call anything: the sources are JitterBuffers, a gain is one atomic float so a
// racing read sees the old or the new value whole, and one mutex guards read's scratch buffers and the loop.
class Mixer {
 public:
  explicit Mixer(JitterBufferConfig voice = {});

  JitterBuffer& source(MixerSource source);
  [[nodiscard]] const JitterBuffer& source(MixerSource source) const;
  // clamped to 0..1, nan is 0
  void setGain(MixerSource source, float gain);
  // plays period over and over with the tone source's gain, mixed over its queue, until stopLoop: an alarm's or a
  // timer's ring. gapless across reads, and counted as tone samples, so it ducks the music for as long as it loops,
  // its silent stretches included. a new period replaces the one looping, whose audio already read plays out; an
  // empty one loops nothing
  void loop(std::span<const std::int16_t> period);
  // loops what feed gives instead, as loop does a period; null loops nothing. the mixer holds the feed until the loop
  // stops or is replaced
  void loopFeed(std::shared_ptr<LoopFeed> feed);
  // true when a period looped. the generation then changes, so the player takes the loop back from the device
  bool stopLoop();
  // fills the whole output: each source's samples times its gain, summed and clamped to the int16 range
  MixerRead read(std::span<std::int16_t> mono);
  // changes whenever any source's does, or a loop stops, so the player knows audio it already read may be stale
  [[nodiscard]] std::uint64_t generation() const;

 private:
  struct Slot {
    explicit Slot(JitterBufferConfig config) : buffer(config) {}

    JitterBuffer buffer;
    std::atomic<float> gain{1.0f};
  };

  std::array<Slot, kMixerSources> slots_;

  // scratch for read, so it allocates only when the output grows; the loop is guarded by the same mutex
  std::mutex readMutex_;
  std::vector<std::int16_t> chunk_;
  std::vector<std::int32_t> sum_;
  std::vector<std::int16_t> loop_;
  std::shared_ptr<LoopFeed> feed_;
  std::vector<std::int16_t> feedChunk_;
  // the next sample of the loop
  std::size_t loopAt_ = 0;
  // stopLoop's share of the generation, moved under the mutex so a read and a stop never interleave
  std::atomic<std::uint64_t> loopStops_{0};
};

}  // namespace cadence
