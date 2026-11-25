#pragma once

#include "dsp/format.h"

#include <array>
#include <cstddef>
#include <span>

namespace cadence::dsp {

// -1 dBFS
inline constexpr float kLimiterCeiling = 0.891f;

// a stereo-linked peak limiter on interleaved stereo with a 5 ms look-ahead.
//
// each frame needs the gain min(1, ceiling / peak). that need recovers exponentially (100 ms) once it lets go, a
// running minimum over the look-ahead window holds it, and a moving average over the same window smooths it, so
// the gain ramps down linearly over the look-ahead and reaches each peak's need exactly when the peak leaves the
// delay line: the output never passes the ceiling and a signal below it passes at unity.
class Limiter {
 public:
  Limiter();

  // whole frames only; a trailing partial frame is left as is. a non-finite sample counts as silence
  void process(std::span<float> stereo);
  // forgets the delayed audio and the gain it held, as if new
  void reset();
  [[nodiscard]] static constexpr std::size_t latencyFrames() { return kLookahead; }

 private:
  // 5 ms
  static constexpr std::size_t kLookahead = framesFor(5);
  static constexpr std::size_t kWindow = kLookahead + 1;

  struct Held {
    double gain;
    std::size_t frame;
  };

  double hold(double need);
  double average(double held);

  // the input, kLookahead frames back
  std::array<float, kLookahead * kStereo> delay_{};
  std::size_t delayPos_ = 0;

  double release_ = 1;

  // ascending running minimum over the last kWindow frames, a ring of kWindow entries
  std::array<Held, kWindow> minima_{};
  std::size_t minHead_ = 0;
  std::size_t minCount_ = 0;
  std::size_t frame_ = 0;

  std::array<double, kWindow> held_{};
  std::size_t heldPos_ = 0;
  double heldSum_ = 0;
};

}  // namespace cadence::dsp
