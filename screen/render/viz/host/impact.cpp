#include "impact.h"

#include <algorithm>
#include <cmath>
#include <numbers>

namespace cadence::render::viz {

namespace {
constexpr std::int64_t kBlock = 256;
}  // namespace

void Impact::push(std::span<const float> mono) {
  // two poles at 170 Hz. a kick's attack comes through quickly; a hat does not. kept in fixed 256-sample audio
  // blocks (~5.33 ms at 48 kHz) so detection runs strictly in audio time, completely independent of render fps
  const double a = 1.0 - std::exp(-2.0 * std::numbers::pi * 170.0 / rate_);
  for (const float s : mono) {
    const double x = static_cast<double>(s);
    y_ += a * (x - y_);
    y2_ += a * (y_ - y2_);
    partial_ = std::max(partial_, std::abs(y2_));
    ++fed_;
    if (fed_ % kBlock == 0) {
      blocks_.push_back({fed_, partial_});
      partial_ = 0;
    }
  }
}

void Impact::step_block(double block_peak, double dt_b, double loud, double level_gain, bool& frame_hit) {
  ring_[ring_pos_] = block_peak;
  ring_pos_ = (ring_pos_ + 1) % ring_.size();
  double peak_abs = 0;
  for (const double v : ring_) peak_abs = std::max(peak_abs, v);

  // the floor is the standing bass. it drops at once and climbs slowly, so a kick — a rise over that bass — stays a
  // rise for the whole hit instead of being eaten sample by sample
  if (peak_abs < floor_) floor_ = peak_abs;
  else floor_ += (peak_abs - floor_) * (1.0 - std::exp(-dt_b / 0.35));
  const double over = std::max(0.0, peak_abs - floor_);

  // compare against a fast (~120 ms) baseline in audio time: a low kick crests over 50-90 ms through the 170 Hz
  // filter, while a slow bass swell lets fast_ catch up and stays rejected
  const double rise = std::max(0.0, peak_abs - fast_);
  if (peak_abs < fast_) fast_ = peak_abs;
  else fast_ += (peak_abs - fast_) * (1.0 - std::exp(-dt_b / 0.12));
  const bool sharp = rise > 0.35 * std::max(over, 1e-9);

  since_ += dt_b;
  pulse_ *= static_cast<float>(std::pow(0.08, dt_b / 0.11));

  // finalize peak_ once per kick after its 100 ms crest window closes so peak_ measures the true kick crest
  if (crest_open_ && since_ >= 0.10) {
    crest_open_ = false;
    if (hits_ == 1) {
      peak_ = crest_over_ * 0.45;
    } else {
      const double share = std::clamp(crest_over_ / std::max(peak_, 1e-9), 0.0, 1.2);
      if (crest_over_ >= peak_) peak_ += (std::min(crest_over_, 2.2 * peak_) - peak_) * 0.3;
      // the first hit out of silence rises from floor_=0, much taller than kicks over bass: let the first few hits
      // calibrate peak_ down quickly, then only let near-full kicks pull it down so breakdowns never drag it
      else if (hits_ <= 4) peak_ += (crest_over_ - peak_) * 0.75;
      else if (share > 0.72) peak_ += (crest_over_ - peak_) * 0.12;
    }
  }

  // a tick under ~45% of the usual kick is not a beat, and must not drag peak_ down to itself
  const bool above = hits_ > 0 && over > 0.45 * std::max(peak_, 1e-9);
  if (!above) fired_in_crest_ = false;
  const bool attack = hits_ == 0 ? (sharp && over > 0.008 && over > 0.30 * std::max(peak_abs, 1e-9) && since_ > 0.05)
                                 : (sharp && above && !fired_in_crest_ && since_ > 0.18);

  constexpr double kLoudKick = 0.28;
  const double clamped_loud = std::clamp(loud, 0.0, 1.0);
  const double clamped_gain = std::clamp(level_gain, 0.01, 1.0);
  if (attack && loud >= 0.2) {
    fired_in_crest_ = true;
    ++hits_;
    since_ = 0;
    crest_over_ = over;
    crest_open_ = true;
    if (hits_ == 1) peak_ = over * 0.55;
    frame_hit = true;
    const double strength_ref = std::max(peak_, kLoudKick * clamped_gain);
    const double strength_share = std::clamp(over / std::max(strength_ref, 1e-9), 0.0, 1.0);
    const double shaped = hits_ == 1 ? std::min(0.15, strength_share) : std::pow(strength_share, 0.65);
    pulse_ = std::max(pulse_, static_cast<float>(std::clamp(shaped * clamped_loud, 0.0, 1.0)));
  } else if (crest_open_ && hits_ > 1 && loud >= 0.2 && over > crest_over_) {
    // kick body still climbing within 100 ms of its leading-edge trigger: lift pulse_ to the new crest
    crest_over_ = over;
    const double strength_ref = std::max(peak_, kLoudKick * clamped_gain);
    const double strength_share = std::clamp(over / std::max(strength_ref, 1e-9), 0.0, 1.0);
    const double shaped = std::pow(strength_share, 0.65);
    pulse_ = std::max(pulse_, static_cast<float>(std::clamp(shaped * clamped_loud, 0.0, 1.0)));
  }
}

void Impact::reset_track() {
  peak_ = 0;
  since_ = 1;
  fired_in_crest_ = false;
  crest_over_ = 0;
  crest_open_ = false;
  hits_ = 0;
}

void Impact::update(std::int64_t heard, double dt, double loud, double level_gain) {
  dt = std::clamp(dt, 0.0, 0.1);
  const double dt_b = static_cast<double>(kBlock) / rate_;
  bool any = false;
  bool frame_hit = false;
  float frame_peak_pulse = 0;
  while (!blocks_.empty() && blocks_.front().end <= heard) {
    step_block(blocks_.front().peak, dt_b, loud, level_gain, frame_hit);
    frame_peak_pulse = std::max(frame_peak_pulse, pulse_);
    blocks_.pop_front();
    any = true;
  }
  if (!any) {
    if (heard >= fed_ && partial_ > 0) {
      step_block(partial_, dt, loud, level_gain, frame_hit);
      frame_peak_pulse = std::max(frame_peak_pulse, pulse_);
      partial_ = 0;
      any = true;
    } else {
      since_ += dt;
      pulse_ *= static_cast<float>(std::pow(0.08, dt / 0.11));
      frame_pulse_ = pulse_;
      hit_ = false;
      return;
    }
  }
  hit_ = frame_hit;
  // report the peak pulse reached during this frame without feeding it back into pulse_'s audio-time decay
  frame_pulse_ = std::max(pulse_, frame_peak_pulse);
}

}  // namespace cadence::render::viz
