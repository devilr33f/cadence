#include "audio/wake-gate.h"

#include <utility>

namespace cadence {

bool WakeGate::confirm() {
  const auto rate = static_cast<std::uint64_t>(sampleRate_);
  if (rate == 0 || (streamFrames_ - warmupFrom_) * 1000 / rate < warmupMs_)
    return false;
  if (lastWakeFrame_) {
    const auto elapsedMs = (streamFrames_ - *lastWakeFrame_) * 1000 / rate;
    if (elapsedMs < kWakeRefractoryMs)
      return false;
  }
  lastWakeFrame_ = streamFrames_;
  pending_ = true;
  return true;
}

bool WakeGate::take() { return std::exchange(pending_, false); }

}  // namespace cadence
