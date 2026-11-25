#include "onset.h"

#include <algorithm>
#include <cmath>
#include <limits>

namespace cadence::render::viz {

namespace {

constexpr double kShort = 0.167;
constexpr double kLong = 2.0;
constexpr double kRatio = 1.32;
constexpr double kFloor = 0.0004;
constexpr double kRefractory = 0.25;

}  // namespace

void OnsetTracker::update(double e, double dt) {
  dt = std::clamp(dt, 0.0, 0.5);
  t_ += dt;
  hist_.push_back({t_, e});
  while (!hist_.empty() && hist_.front().t < t_ - kLong) hist_.pop_front();
  double s = 0, l = 0;
  std::size_t ns = 0;
  for (const auto& h : hist_) {
    l += h.e;
    if (h.t > t_ - kShort) {
      s += h.e;
      ++ns;
    }
  }
  const double short_mean = ns > 0 ? s / static_cast<double>(ns) : 0;
  const double long_mean = l / static_cast<double>(hist_.size());
  // the long window must have filled a little before anything counts as over it
  const bool primed = hist_.size() >= 8 && hist_.back().t - hist_.front().t > kShort * 2;
  onset_ = primed && short_mean > long_mean * kRatio + kFloor && (last_ < 0 || t_ - last_ > kRefractory);
  if (!onset_) {
    pulse_ *= static_cast<float>(std::pow(0.9, dt * 60));
    return;
  }
  if (last_ >= 0) {
    const double interval = t_ - last_;
    if (interval > 0.24 && interval < 1.6) {
      intervals_[interval_pos_] = interval;
      interval_pos_ = (interval_pos_ + 1) % intervals_.size();
      interval_count_ = std::min(interval_count_ + 1, intervals_.size());
      if (interval_count_ >= 4) {
        std::array<double, 12> w;
        w.fill(std::numeric_limits<double>::infinity());
        const std::size_t count = std::min(interval_count_, w.size());
        std::copy_n(intervals_.begin(), count, w.begin());
        // the fixed-size sort leaves the sentinels behind the real intervals
        std::sort(w.begin(), w.end());
        const double median = w[count / 2];
        double sum = 0;
        std::size_t k = 0;
        for (std::size_t i = 0; i < count; ++i) {
          if (std::abs(w[i] - median) < median * 0.35) {
            sum += w[i];
            ++k;
          }
        }
        if (k >= 4) {
          const double inst = 60.0 / (sum / static_cast<double>(k));
          bpm_ = bpm_ <= 0 ? inst : bpm_ * 0.8 + inst * 0.2;
        }
      }
    }
  }
  last_ = t_;
  ++count_;
  pulse_ = 1;
  std::rotate(times_.begin(), times_.begin() + 1, times_.end());
  times_.back() = t_;
}

std::array<float, 8> OnsetTracker::ages() const {
  std::array<float, 8> out{};
  for (std::size_t i = 0; i < 8; ++i) {
    const double a = times_[i] < 0 ? -1 : t_ - times_[i];
    out[i] = a < 0 || a > 3.5 ? -1.0f : static_cast<float>(a);
  }
  return out;
}

}  // namespace cadence::render::viz
