#include "dsp/duck.h"

#include <algorithm>
#include <cmath>

namespace cadence::dsp {

namespace {

constexpr double kDucked = 0.15;
constexpr std::size_t kDown = framesFor(200);
constexpr std::size_t kHold = framesFor(500);
constexpr std::size_t kUp = framesFor(500);

constexpr double kOpenHz = 20000;
constexpr double kMuffledHz = 1000;
constexpr double kMuffleQ = 0.707;
constexpr std::size_t kRetuneFrames = 32;

double cutoffFor(double gain) {
  const double depth = std::clamp((1 - gain) / (1 - kDucked), 0.0, 1.0);
  return kOpenHz * std::pow(kMuffledHz / kOpenHz, depth);
}

}  // namespace

DuckEnvelope::DuckEnvelope(DuckMode mode) : mode_(mode) { retune(); }

void DuckEnvelope::setMode(DuckMode mode) {
  if (mode == mode_)
    return;
  mode_ = mode;
  for (auto& state : states_)
    state.reset();
  untilRetune_ = 0;
}

void DuckEnvelope::setActive(bool active) {
  if (active) {
    holdLeft_ = 0;
    turn(true);
  } else if (active_) {
    holdLeft_ = kHold;
  }
  active_ = active;
}

void DuckEnvelope::turn(bool on) {
  if (on == on_)
    return;
  from_ = gain_;
  on_ = on;
  elapsed_ = 0;
}

double DuckEnvelope::step() {
  if (on_ && !active_) {
    if (holdLeft_ == 0)
      turn(false);
    else
      --holdLeft_;
  }
  const double target = on_ ? kDucked : 1.0;
  const std::size_t length = on_ ? kDown : kUp;
  if (elapsed_ < length) {
    gain_ = from_ + (target - from_) * static_cast<double>(elapsed_) / static_cast<double>(length);
    ++elapsed_;
  } else {
    gain_ = target;
  }
  return gain_;
}

void DuckEnvelope::retune() {
  if (gain_ == tunedGain_)
    return;
  tunedGain_ = gain_;
  lowPass_ = designBiquad(FilterType::LowPass, cutoffFor(gain_), 0, kMuffleQ);
}

void DuckEnvelope::apply(std::span<float> stereo) {
  const std::size_t frames = stereo.size() / kStereo;
  for (std::size_t frame = 0; frame < frames; ++frame) {
    const double gain = step();
    if (mode_ == DuckMode::Muffle) {
      if (untilRetune_ == 0) {
        retune();
        untilRetune_ = kRetuneFrames;
      }
      --untilRetune_;
    }
    for (std::size_t channel = 0; channel < kStereo; ++channel) {
      float& sample = stereo[frame * kStereo + channel];
      // a decoder's nan or inf would otherwise stay in the low-pass state for good
      double x = std::isfinite(sample) ? sample : 0.0;
      if (mode_ == DuckMode::Muffle)
        x = states_[channel].process(lowPass_, x);
      sample = static_cast<float>(x * gain);
    }
  }
}

}  // namespace cadence::dsp
