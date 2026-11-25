#pragma once

#include "dsp/biquad.h"
#include "dsp/format.h"

#include <array>
#include <cstddef>
#include <span>

namespace cadence::dsp {

enum class DuckMode { Duck, Muffle };

// v1's music duck, per sample on interleaved stereo: while active the gain ramps linearly to 0.15 over 200 ms;
// once inactive it holds the duck 0.5 s more, then ramps back to 1 over 500 ms. each ramp starts from the gain it
// finds, so an activation during the hold or the ramp up turns around without a jump. muffle also low-passes the
// music, the cutoff following the gain from 20 kHz (gain 1) down to 1 kHz (gain 0.15), log-interpolated.
class DuckEnvelope {
 public:
  explicit DuckEnvelope(DuckMode mode);

  // between blocks; the gain carries on, and the low-pass of a switch to muffle starts from silence
  void setMode(DuckMode mode);
  // once per block, before apply
  void setActive(bool active);
  // whole frames only; a trailing partial frame is left as is. a nan or inf sample is taken as silence
  void apply(std::span<float> stereo);

 private:
  void turn(bool on);
  double step();
  void retune();

  DuckMode mode_;
  bool active_ = false;
  // the duck is on while active and during the hold after it
  bool on_ = false;
  std::size_t holdLeft_ = 0;
  double from_ = 1;
  std::size_t elapsed_ = 0;
  double gain_ = 1;

  BiquadCoefficients lowPass_;
  double tunedGain_ = -1;
  std::size_t untilRetune_ = 0;
  std::array<BiquadState, kStereo> states_{};
};

}  // namespace cadence::dsp
