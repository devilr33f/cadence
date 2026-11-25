#include "dsp/limiter.h"

#include <algorithm>
#include <cmath>
#include <numeric>

namespace cadence::dsp {

namespace {

// 100 ms
const double kRelease = std::exp(-1.0 / (0.1 * kRateHz));

float finite(float x) { return std::isfinite(x) ? x : 0.0f; }

}  // namespace

Limiter::Limiter() {
  held_.fill(1.0);
  heldSum_ = static_cast<double>(kWindow);
}

void Limiter::reset() { *this = Limiter(); }

double Limiter::hold(double need) {
  // at most one entry leaves the window per frame
  if (minCount_ > 0 && frame_ - minima_[minHead_].frame >= kWindow) {
    minHead_ = (minHead_ + 1) % kWindow;
    --minCount_;
  }
  while (minCount_ > 0 && minima_[(minHead_ + minCount_ - 1) % kWindow].gain >= need)
    --minCount_;
  minima_[(minHead_ + minCount_) % kWindow] = {need, frame_};
  ++minCount_;
  ++frame_;
  return minima_[minHead_].gain;
}

double Limiter::average(double held) {
  heldSum_ += held - held_[heldPos_];
  held_[heldPos_] = held;
  heldPos_ = (heldPos_ + 1) % kWindow;
  // a fresh sum once per window keeps the running sum's rounding from drifting
  if (heldPos_ == 0)
    heldSum_ = std::accumulate(held_.begin(), held_.end(), 0.0);
  return heldSum_ / static_cast<double>(kWindow);
}

void Limiter::process(std::span<float> stereo) {
  const std::size_t frames = stereo.size() / kStereo;
  for (std::size_t frame = 0; frame < frames; ++frame) {
    float& left = stereo[frame * kStereo];
    float& right = stereo[frame * kStereo + 1];
    const float inLeft = finite(left);
    const float inRight = finite(right);

    const float peak = std::max(std::abs(inLeft), std::abs(inRight));
    const double need = peak > kLimiterCeiling ? kLimiterCeiling / static_cast<double>(peak) : 1.0;
    release_ = need < release_ ? need : need + (release_ - need) * kRelease;
    const double gain = average(hold(release_));

    const double outLeft = delay_[delayPos_] * gain;
    const double outRight = delay_[delayPos_ + 1] * gain;
    delay_[delayPos_] = inLeft;
    delay_[delayPos_ + 1] = inRight;
    delayPos_ = (delayPos_ + kStereo) % delay_.size();

    // the average's rounding may land an ulp over the ceiling
    left = static_cast<float>(std::clamp(outLeft, -double{kLimiterCeiling}, double{kLimiterCeiling}));
    right = static_cast<float>(std::clamp(outRight, -double{kLimiterCeiling}, double{kLimiterCeiling}));
  }
}

}  // namespace cadence::dsp
