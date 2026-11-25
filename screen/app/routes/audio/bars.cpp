#include "app/routes/audio/bars.h"

#include <algorithm>
#include <cmath>

namespace cadence::screen::routes {

namespace {

constexpr double kAttackTauS = 0.012;
// a falling bar accelerates at this many heights a second squared, from rest
constexpr double kGravity = 9.0;
constexpr double kBounceTauS = 0.11;
constexpr double kEpsilonDp = 0.02;
// the window shown under the recent peak, in the bands' 0..1 (60 db): 27 db; the peak falls 9 db a second
constexpr double kSpan = 0.45;
constexpr double kPeakFall = 0.15;
constexpr double kPeakFloor = 0.3;
// nothing under -45 dbfs shows, however quiet the track: the window never reaches into noise
constexpr double kNoise = 0.25;

}  // namespace

BarSmoother::BarSmoother() {
  h_.fill(kMinDp);
  peak_.fill(kPeakFloor);
}

void BarSmoother::bands(const std::array<std::uint8_t, 16>& b, std::int64_t nowMs) {
  const double dt = lastBandsMs_ < 0 ? 1.0 / 30 : std::clamp(static_cast<double>(nowMs - lastBandsMs_) / 1000.0, 0.005, 0.25);
  lastBandsMs_ = nowMs;
  double loudest = 0;
  for (std::size_t i = 0; i < target_.size(); ++i) loudest = std::max(loudest, b[i] / 255.0);
  peakAll_ = std::max({loudest, peakAll_ - kPeakFall * dt, kPeakFloor});
  for (std::size_t i = 0; i < target_.size(); ++i) {
    const double v = b[i] / 255.0;
    peak_[i] = std::max({v, peak_[i] - kPeakFall * dt, kPeakFloor});
    // each bar against its own peak, never more than 12 db under the loudest band's (treble moves, noise does not)
    const double top = std::max(peak_[i], peakAll_ - 0.2);
    const double bottom = std::max(top - kSpan, kNoise);
    const double x = std::clamp((v - bottom) / std::max(top - bottom, 0.05), 0.0, 1.0);
    target_[i] = kMinDp + (kMaxDp - kMinDp) * std::pow(x, 1.5);
  }
  // the kick: the low bands' rise against its recent average
  double flux = 0;
  for (std::size_t i = 0; i < low_.size(); ++i) {
    flux += std::max(0.0, b[i] / 255.0 - low_[i]);
    low_[i] = b[i] / 255.0;
  }
  const bool kick = flux > 0.12 && flux > 2.2 * fluxAvg_ && nowMs - lastKickMs_ >= 180;
  fluxAvg_ += (flux - fluxAvg_) * (1 - std::exp(-dt / 0.8));
  if (kick && !reduced_) {
    lastKickMs_ = nowMs;
    bounce_ = 1;
  }
}

double BarSmoother::want(std::size_t i) const {
  if (paused_) return kMinDp;
  // the bounce lifts the bass most, the treble a little
  const double lift = bounce_ * (kMaxDp - kMinDp) * (0.18 - 0.1 * static_cast<double>(i) / kBars);
  return std::min(kMaxDp, target_[i] + lift);
}

bool BarSmoother::step(std::int64_t nowMs) {
  if (lastMs_ >= 0 && nowMs - lastMs_ < intervalMs()) return false;
  const double dt = lastMs_ < 0 ? 1.0 / 60.0 : static_cast<double>(nowMs - lastMs_) / 1000.0;
  lastMs_ = nowMs;
  bounce_ = reduced_ ? 0 : bounce_ * std::exp(-dt / kBounceTauS);
  if (bounce_ < 0.01) bounce_ = 0;
  bool moved = false;
  for (std::size_t i = 0; i < h_.size(); ++i) {
    const double w = want(i);
    double next = w;
    if (!reduced_) {
      if (w >= h_[i]) {
        next = h_[i] + (w - h_[i]) * (1.0 - std::exp(-dt / kAttackTauS));
        v_[i] = 0;
      } else {
        v_[i] += kGravity * (kMaxDp - kMinDp) * dt;
        next = std::max(w, h_[i] - v_[i] * dt);
        if (next == w) v_[i] = 0;
      }
      if (std::fabs(next - w) < kEpsilonDp) next = w;
    }
    if (next != h_[i]) moved = true;
    h_[i] = next;
  }
  return moved;
}

bool BarSmoother::active(std::int64_t nowMs) const {
  (void)nowMs;
  // reduced motion moves only when a frame arrives, and settles once when paused
  if (reduced_) {
    if (!paused_) return false;
    return std::any_of(h_.begin(), h_.end(), [](double h) { return h > kMinDp; });
  }
  if (bounce_ > 0) return true;
  for (std::size_t i = 0; i < h_.size(); ++i) {
    if (std::fabs(h_[i] - want(i)) >= kEpsilonDp) return true;
  }
  return false;
}

BarLayout barLayout(render::Rect screen) {
  BarLayout out;
  if (screen.width < 64 || screen.height < 36) return out;
  const int margin = screen.width / 24;
  const int available = screen.width - 2 * margin;
  const int nominalPitch = available / BarSmoother::kBars;
  int width = std::max(1, nominalPitch * 3 / 5);
  int gap = std::max(1, nominalPitch - width);
  int rowWidth = BarSmoother::kBars * width + (BarSmoother::kBars - 1) * gap;
  // an odd spare pixel would make the outer margins unequal; shortening every gap flips the row's parity
  if ((rowWidth & 1) != (available & 1) && gap > 1) {
    --gap;
    rowWidth -= BarSmoother::kBars - 1;
  }
  out.firstX = screen.x + margin + (available - rowWidth) / 2;
  out.pitch = width + gap;
  out.barWidth = width;
  out.gap = gap;
  out.maxHeight = screen.height * 2 / 9;
  out.baseline = screen.y + screen.height - screen.height / 12;
  out.band = render::Rect{screen.x, out.baseline - out.maxHeight, screen.width, out.maxHeight};
  return out;
}

render::Rect barRect(const BarLayout& layout, int index, double heightDp) {
  const double scale = static_cast<double>(layout.maxHeight) / BarSmoother::kMaxDp;
  const int height = std::clamp(static_cast<int>(std::lround(heightDp * scale)), 0, layout.maxHeight);
  return render::Rect{layout.firstX + index * layout.pitch, layout.baseline - height, layout.barWidth, height};
}

}  // namespace cadence::screen::routes
