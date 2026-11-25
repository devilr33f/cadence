#include "analyzer.h"

#include <algorithm>
#include <bit>
#include <cmath>
#include <cstring>
#include <numbers>

namespace cadence::render::viz {

std::vector<float> decode_samples(std::span<const std::uint8_t> bytes) {
  std::vector<float> out(bytes.size() / 4);
  for (std::size_t i = 0; i < out.size(); ++i) {
    const std::uint32_t u = std::uint32_t{bytes[4 * i]} | std::uint32_t{bytes[4 * i + 1]} << 8U |
                            std::uint32_t{bytes[4 * i + 2]} << 16U | std::uint32_t{bytes[4 * i + 3]} << 24U;
    out[i] = std::bit_cast<float>(u);
  }
  return out;
}

Analyzer::Analyzer(AnalyzerConfig c) : c_(c) {
  c_.bars = std::clamp<std::size_t>(c_.bars, 1, kMaxBars);
  c_.scope_points = std::max<std::size_t>(c_.scope_points, 2);
  for (std::size_t n = 0; n < kSnapshotFrames; ++n) {
    window_[n] = 0.5 - 0.5 * std::cos(2.0 * std::numbers::pi * static_cast<double>(n) /
                                      static_cast<double>(kSnapshotFrames - 1));
  }
  scope_.assign(c_.scope_points, 0.0);
}

// log-spaced edges from min_hz to max_hz (capped at nyquist), each bar at least one bin and the bins in order
void Analyzer::set_ranges(std::uint32_t sample_rate) {
  rate_ = sample_rate;
  ranges_.clear();
  const double bin_hz = static_cast<double>(sample_rate) / static_cast<double>(kSnapshotFrames);
  const std::size_t last_bin = kSnapshotFrames / 2;
  const double hi = std::min(c_.max_hz, static_cast<double>(sample_rate) / 2.0);
  const double lo = std::min(c_.min_hz, hi / 2.0);
  std::size_t prev = 1;
  for (std::size_t b = 0; b < c_.bars; ++b) {
    const double edge = lo * std::pow(hi / lo, static_cast<double>(b + 1) / static_cast<double>(c_.bars));
    auto end = static_cast<std::size_t>(std::lround(edge / bin_hz));
    end = std::clamp(end, prev + 1, last_bin + 1);
    if (prev >= last_bin + 1) {
      ranges_.push_back({last_bin, last_bin + 1});
      continue;
    }
    ranges_.push_back({prev, end});
    prev = end;
  }
}

void Analyzer::push_pcm(std::span<const float> interleaved, std::uint32_t channels, std::uint32_t sample_rate) {
  if (channels == 0 || sample_rate == 0) return;
  if (sample_rate != rate_) set_ranges(sample_rate);
  const std::size_t frames = std::min(interleaved.size() / channels, kSnapshotFrames);
  // the mono mix, right-aligned in the window (the newest frames last), zero-padded in front
  std::array<double, kSnapshotFrames> mono{};
  const std::size_t pad = kSnapshotFrames - frames;
  for (std::size_t f = 0; f < frames; ++f) {
    double s = 0;
    for (std::size_t ch = 0; ch < channels; ++ch) s += static_cast<double>(interleaved[f * channels + ch]);
    s /= static_cast<double>(channels);
    mono[pad + f] = std::isfinite(s) ? std::clamp(s, -1.0, 1.0) : 0.0;
  }

  std::array<std::complex<double>, kSnapshotFrames> x{};
  double window_sum = 0;
  for (std::size_t n = 0; n < kSnapshotFrames; ++n) {
    x[n] = mono[n] * window_[n];
    window_sum += window_[n];
  }
  fft_.forward(x);
  // a full-scale sine's bin reads amplitude 1: |X| * 2 / sum(window)
  const double scale = 2.0 / window_sum;
  for (std::size_t b = 0; b < c_.bars; ++b) {
    double power = 0;
    for (std::size_t k = ranges_[b].first; k < ranges_[b].end; ++k) power += std::norm(x[k]);
    const double amp = std::sqrt(power) * scale;
    const double db = amp > 0 ? 20.0 * std::log10(amp) : c_.floor_db;
    target_[b] = std::clamp((db - c_.floor_db) / (c_.ceil_db - c_.floor_db), 0.0, 1.0);
  }

  // the scope: the window's newest frames, nearest-sample resampled
  for (std::size_t i = 0; i < c_.scope_points; ++i) {
    const std::size_t at = frames == 0 ? 0 : pad + i * frames / c_.scope_points;
    scope_[i] = frames == 0 ? 0.0 : mono[at];
  }
}

void Analyzer::push_bands(std::span<const std::uint8_t> bands) {
  if (bands.empty()) return;
  for (std::size_t b = 0; b < c_.bars; ++b) {
    const std::size_t i = b * bands.size() / c_.bars;
    target_[b] = static_cast<double>(bands[i]) / 255.0;
  }
}

void Analyzer::silence() {
  std::fill(target_.begin(), target_.end(), 0.0);
  std::fill(scope_.begin(), scope_.end(), 0.0);
}

void Analyzer::step(double dt, bool reduced_motion) {
  dt = std::max(dt, 0.0);
  for (std::size_t b = 0; b < c_.bars; ++b) {
    if (reduced_motion) {
      bars_[b] = target_[b];
      peaks_[b] = target_[b];
      peak_age_[b] = 0;
      peak_speed_[b] = 0;
      continue;
    }
    bars_[b] = target_[b] >= bars_[b] ? target_[b] : std::max(target_[b], bars_[b] - c_.fall_per_s * dt);
    if (bars_[b] >= peaks_[b]) {
      peaks_[b] = bars_[b];
      peak_age_[b] = 0;
      peak_speed_[b] = 0;
      continue;
    }
    peak_age_[b] += dt;
    if (peak_age_[b] > c_.peak_hold_s) {
      peak_speed_[b] += c_.peak_gravity * dt;
      peaks_[b] = std::max(bars_[b], peaks_[b] - peak_speed_[b] * dt);
    }
  }
}

bool Analyzer::moving() const {
  for (std::size_t b = 0; b < c_.bars; ++b) {
    if (bars_[b] > 0 || peaks_[b] > 0 || target_[b] > 0) return true;
  }
  return std::any_of(scope_.begin(), scope_.end(), [](double s) { return s != 0; });
}

}  // namespace cadence::render::viz
