#include "dsp/eq.h"

#include <algorithm>
#include <cmath>
#include <numbers>

namespace cadence::dsp {

namespace {

bool known(FilterType type) {
  switch (type) {
    case FilterType::Peaking:
    case FilterType::LowShelf:
    case FilterType::HighShelf:
    case FilterType::LowPass:
    case FilterType::HighPass:
      return true;
    case FilterType::Unspecified:
      break;
  }
  return false;
}

bool shelf(FilterType type) { return type == FilterType::LowShelf || type == FilterType::HighShelf; }

// the cookbook's slope s = 1: the steepest shelf whose gain still never overshoots
constexpr float kMaxShelfQ = static_cast<float>(std::numbers::sqrt2 / 2);

}  // namespace

Eq::Eq(std::size_t channels) : channels_(channels) {
  for (auto& chain : chains_)
    chain.states.resize(channels * kStages);
}

void Eq::addLayer(Design& design, const EqSettings& layer, std::size_t limit) {
  const float preampDb = std::isnan(layer.preampDb) ? 0.0f : std::clamp(layer.preampDb, -12.0f, 12.0f);
  design.gain *= std::pow(10.0, preampDb / 20.0);
  for (std::size_t i = 0; i < std::min(limit, layer.bands.size()); ++i) {
    const auto& band = layer.bands[i];
    if (!known(band.type) || std::isnan(band.freqHz) || std::isnan(band.gainDb) || std::isnan(band.q))
      continue;
    const float maxQ = shelf(band.type) ? kMaxShelfQ : 10.0f;
    design.stages[design.count++] =
        designBiquad(band.type, std::clamp(band.freqHz, 20.0f, 20000.0f), std::clamp(band.gainDb, -12.0f, 12.0f),
                     std::clamp(band.q, 0.1f, maxQ));
  }
}

void Eq::set(const EqSettings& profile, const EqSettings& user) {
  const std::lock_guard lock(setMutex_);
  Design& design = slots_[back_];
  design = Design{};
  addLayer(design, profile, kProfileBands);
  addLayer(design, user, kUserBands);
  back_ = middle_.exchange(back_ | kFresh, std::memory_order_acq_rel) & kIndex;
}

void Eq::pickUp() {
  if (fadeLeft_ > 0 || (middle_.load(std::memory_order_relaxed) & kFresh) == 0)
    return;
  front_ = middle_.exchange(front_, std::memory_order_acq_rel) & kIndex;
  current_ ^= 1;
  Chain& chain = chains_[current_];
  chain.design = slots_[front_];
  for (auto& state : chain.states)
    state.reset();
  fadeLeft_ = kEqFadeFrames;
}

void Eq::reset() {
  for (auto& chain : chains_)
    for (auto& state : chain.states)
      state.reset();
}

double Eq::run(Chain& chain, std::size_t channel, double x) {
  double y = x * chain.design.gain;
  BiquadState* states = chain.states.data() + channel * kStages;
  for (std::size_t i = 0; i < chain.design.count; ++i)
    y = states[i].process(chain.design.stages[i], y);
  return y;
}

void Eq::process(std::span<float> interleaved) {
  if (channels_ == 0)
    return;
  const std::size_t frames = interleaved.size() / channels_;
  // a decoder's nan or inf would otherwise stay in the filters' state for good
  for (float& sample : interleaved.first(frames * channels_))
    if (!std::isfinite(sample))
      sample = 0;

  pickUp();
  Chain& next = chains_[current_];
  Chain& previous = chains_[current_ ^ 1];
  if (fadeLeft_ == 0 && next.design.count == 0 && next.design.gain == 1.0)
    return;

  for (std::size_t frame = 0; frame < frames; ++frame) {
    const bool fading = fadeLeft_ > 0;
    const double weight =
        fading ? static_cast<double>(kEqFadeFrames - fadeLeft_ + 1) / static_cast<double>(kEqFadeFrames) : 1.0;
    for (std::size_t channel = 0; channel < channels_; ++channel) {
      float& sample = interleaved[frame * channels_ + channel];
      const double x = sample;
      double y = run(next, channel, x);
      if (fading) {
        const double old = run(previous, channel, x);
        y = old + weight * (y - old);
      }
      sample = static_cast<float>(y);
    }
    if (fading)
      --fadeLeft_;
  }
}

}  // namespace cadence::dsp
