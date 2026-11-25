#include "dsp/viz.h"

#include <algorithm>
#include <cmath>
#include <numbers>

namespace cadence::dsp {

namespace {

constexpr std::size_t kSize = Fft1024::kSize;
constexpr double kLowHz = 60;
constexpr double kHighHz = 16000;
constexpr double kFloorDb = -60;
constexpr double kBinHz = kRateHz / static_cast<double>(kSize);

// a full-scale sine's power over the positive bins through the periodic hann window (sum of w^2 = 3N/8) is 3N^2/32
const double kFullScale = std::sqrt(3.0 / 32.0) * static_cast<double>(kSize);

std::size_t nearestBin(double hz) { return static_cast<std::size_t>(std::lround(hz / kBinHz)); }

}  // namespace

VizBands::VizBands() {
  for (std::size_t n = 0; n < kSize; ++n)
    window_[n] = static_cast<float>(
        0.5 * (1 - std::cos(2 * std::numbers::pi * static_cast<double>(n) / static_cast<double>(kSize))));

  // bin 0 is dc
  std::size_t end = 1;
  for (std::size_t band = 0; band < kVizBands; ++band) {
    const auto edge = [](std::size_t k) {
      return kLowHz * std::pow(kHighHz / kLowHz, static_cast<double>(k) / static_cast<double>(kVizBands));
    };
    const std::size_t first = std::max(nearestBin(edge(band)), end);
    end = std::max(nearestBin(edge(band + 1)), first + 1);
    ranges_[band] = {first, end};
  }
}

void VizBands::push(std::span<const float> mono) {
  for (const float sample : mono) {
    ring_[ringPos_] = sample;
    ringPos_ = (ringPos_ + 1) % kSize;
    if (++sinceFrame_ == kVizHop) {
      sinceFrame_ = 0;
      analyze();
    }
  }
}

VizFrame VizBands::bands() {
  ready_ = false;
  return bands_;
}

void VizBands::analyze() {
  // ringPos_ is the oldest sample
  for (std::size_t n = 0; n < kSize; ++n)
    spectrum_[n] = {ring_[(ringPos_ + n) % kSize] * window_[n], 0.0f};
  fft_.forward(spectrum_);

  for (std::size_t band = 0; band < kVizBands; ++band) {
    double power = 0;
    for (std::size_t bin = ranges_[band].first; bin < ranges_[band].end; ++bin)
      power += std::norm(std::complex<double>(spectrum_[bin]));
    const double level = std::sqrt(power) / kFullScale;
    double scaled = 0;
    if (level > 0)
      scaled = std::clamp(255 * (20 * std::log10(level) - kFloorDb) / -kFloorDb, 0.0, 255.0);
    bands_[band] = static_cast<std::uint8_t>(std::lround(scaled));
  }
  ready_ = true;
}

}  // namespace cadence::dsp
