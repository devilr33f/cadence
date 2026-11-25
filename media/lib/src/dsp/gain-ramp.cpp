#include "dsp/gain-ramp.h"

#include "dsp/format.h"

#include <algorithm>

namespace cadence::dsp {

namespace {

// written so that nan also becomes silence
float clamped(float gain) { return gain >= 0.0f ? std::min(gain, 1.0f) : 0.0f; }

}  // namespace

GainRamp::GainRamp(float gain) : gain_(clamped(gain)), target_(gain_) {}

void GainRamp::setTarget(float gain, std::size_t rampFrames) {
  target_ = clamped(gain);
  if (rampFrames == 0 || target_ == gain_) {
    gain_ = target_;
    left_ = 0;
    step_ = 0.0f;
    return;
  }
  left_ = rampFrames;
  step_ = (target_ - gain_) / static_cast<float>(rampFrames);
}

void GainRamp::apply(std::span<float> stereo) {
  const std::size_t frames = stereo.size() / kStereo;
  std::size_t i = 0;
  for (; i < frames && left_ > 0; ++i) {
    gain_ += step_;
    if (--left_ == 0)
      gain_ = target_;
    stereo[2 * i] *= gain_;
    stereo[2 * i + 1] *= gain_;
  }
  if (gain_ == 1.0f)
    return;
  for (; i < frames; ++i) {
    stereo[2 * i] *= gain_;
    stereo[2 * i + 1] *= gain_;
  }
}

}  // namespace cadence::dsp
