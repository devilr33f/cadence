#pragma once

#include <cstddef>
#include <span>

namespace cadence::dsp {

// a gain that moves linearly to its target over a ramp, per frame on interleaved stereo: the server's duck of the mix
// and a session's own gain. the target is clamped to 0..1 (nan is 0), so it never raises what it is applied to
// (rule 7); a new target ramps from the gain it finds, so a change mid-ramp turns around without a jump
class GainRamp {
 public:
  explicit GainRamp(float gain = 1.0f);

  // between blocks; rampFrames 0 jumps
  void setTarget(float gain, std::size_t rampFrames);
  // whole frames only; a trailing partial frame is left as is
  void apply(std::span<float> stereo);

  [[nodiscard]] float gain() const { return gain_; }
  [[nodiscard]] float target() const { return target_; }
  // at its target: apply is a plain multiply, or nothing at unity
  [[nodiscard]] bool settled() const { return left_ == 0; }

 private:
  float gain_;
  float target_;
  float step_ = 0.0f;
  std::size_t left_ = 0;
};

}  // namespace cadence::dsp
