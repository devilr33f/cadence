#pragma once

#include "playback/jitter-buffer.h"
#include "trace/trace-queue.h"

#include <array>
#include <cstddef>
#include <cstdint>
#include <optional>

namespace cadence {

// the playback thread's voice marks (AlsaPlayer::traceTo): a begun response is followed from the period that read
// its first sample to the write that hands that sample to the device. fixed storage, so nothing allocates
class VoiceMarks {
 public:
  explicit VoiceMarks(const trace::Tracer& tracer) : tracer_(tracer) {}

  // a period read from the mixer, kept until it is known to be current
  void read(const JitterTrace& voice, bool underrun, bool drained) {
    if (!tracer_.active())
      return;
    pending_ = voice;
    underrun_ = underrun;
    drained_ = drained;
  }

  // the kept period goes to the device: its first samples sit latency frames after where it starts (written)
  void processed(std::uint64_t written, std::uint64_t latency) {
    if (!tracer_.active())
      return;
    for (std::size_t index = 0; index < pending_.starts; ++index) {
      if (count_ < firsts_.size())
        firsts_[count_++] = {written + pending_.startOffsets[index] + latency, pending_.startIds[index]};
      playing_ = pending_.startIds[index];
    }
    if (underrun_)
      tracer_.mark(trace::Stage::Underrun, trace::Target::Stream, pending_.underrun.view(), {{"device", 0}});
    else if (drained_ && pending_.starts == 0)
      playing_ = {};
    pending_.starts = 0;
    underrun_ = false;
    drained_ = false;
  }

  // the device's audio was taken back: what was not written of a begun response is gone
  void discarded() { count_ = 0; }

  // a write left written frames in the device in all, unplayed of them not yet heard (none: not known)
  void wrote(std::uint64_t written, std::optional<std::uint64_t> unplayed, int rate) {
    for (std::size_t index = 0; index < count_;) {
      auto& first = firsts_[index];
      if (written < first.position) {
        ++index;
        continue;
      }
      // the device holds unplayed frames, the last (written - position) of them the response's own
      const auto behind = written - first.position;
      const auto ahead = unplayed && *unplayed > behind ? *unplayed - behind : 0;
      tracer_.mark(trace::Stage::FirstSamplePlayed, trace::Target::Stream, first.id.view(),
                   {{"buffered_ms", static_cast<double>(ahead) * 1000.0 / rate}});
      first = firsts_[--count_];
    }
  }

  // the device recovered from an underrun while a response played
  void recovered() {
    if (!playing_.empty())
      tracer_.mark(trace::Stage::Underrun, trace::Target::Stream, playing_.view(), {{"device", 1}});
  }

 private:
  struct First {
    std::uint64_t position = 0;
    trace::Id id;
  };

  const trace::Tracer& tracer_;
  JitterTrace pending_;
  bool underrun_ = false;
  bool drained_ = false;
  std::array<First, 4> firsts_{};
  std::size_t count_ = 0;
  trace::Id playing_;
};

}  // namespace cadence
