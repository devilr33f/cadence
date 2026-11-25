#pragma once

#include <cstdint>
#include <optional>

namespace cadence {

// confirmations closer than this in stream time belong to the same utterance
inline constexpr std::uint64_t kWakeRefractoryMs = 1200;
// confirmations this soon after the gate is built or re-armed come from a capture-start transient
inline constexpr std::uint64_t kWakeWarmupMs = 3000;

// turns spotter confirmations into one wake per utterance, timed by captured frames, not the wall clock
class WakeGate {
 public:
  WakeGate() = default;
  explicit WakeGate(int sampleRate, std::uint64_t warmupMs = kWakeWarmupMs)
      : sampleRate_(sampleRate), warmupMs_(warmupMs) {}

  void advance(std::uint64_t frames) { streamFrames_ += frames; }
  // starts the warm-up again from the current stream time, as after an unmute
  void rearm() { warmupFrom_ = streamFrames_; }
  // true when it was accepted: past the warm-up and clear of the last accepted one
  bool confirm();
  // true once per accepted confirmation
  bool take();

 private:
  int sampleRate_ = 0;
  std::uint64_t warmupMs_ = kWakeWarmupMs;
  std::uint64_t streamFrames_ = 0;
  std::uint64_t warmupFrom_ = 0;
  std::optional<std::uint64_t> lastWakeFrame_;
  bool pending_ = false;
};

}  // namespace cadence
