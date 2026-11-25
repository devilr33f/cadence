#include "kws/frontend.h"

#include <algorithm>
#include <cmath>
#include <numbers>

namespace cadence::kws {
namespace {

constexpr std::size_t kBins = KwsFrontend::kFftSize / 2 + 1;
// a push of the stock's 1280-sample fragment completes at most 8 frames
constexpr std::size_t kFramesReserved = 8;

double hzToMel(double hz) { return 2595 * std::log10(1 + hz / 700); }

double melToHz(double mel) { return 700 * (std::pow(10.0, mel / 2595) - 1); }

}  // namespace

FrontendParams FrontendParams::defaultCapsula() {
  return {.sampleRateHz = 16000, .sampleScale = 32767, .melLowHz = 0, .melHighHz = 8000, .powerFloor = 1e-10f};
}

KwsFrontend::KwsFrontend(const FrontendParams& params)
    : sampleScale_(params.sampleScale), powerFloor_(params.powerFloor) {
  for (std::size_t n = 0; n < kFrameLength; ++n)
    window_[n] = static_cast<float>(
        0.54 - 0.46 * std::cos(2 * std::numbers::pi * static_cast<double>(n) / static_cast<double>(kFrameLength)));

  std::array<double, kMels + 2> edges{};
  const double low = hzToMel(params.melLowHz);
  const double high = hzToMel(params.melHighHz);
  for (std::size_t j = 0; j < edges.size(); ++j)
    edges[j] = melToHz(low + (high - low) * static_cast<double>(j) / static_cast<double>(kMels + 1));

  for (std::size_t band = 0; band < kMels; ++band) {
    bands_[band].weightsAt = weights_.size();
    for (std::size_t k = 0; k < kBins; ++k) {
      const double hz = static_cast<double>(k) * params.sampleRateHz / static_cast<double>(kFftSize);
      const double rise = (hz - edges[band]) / (edges[band + 1] - edges[band]);
      const double fall = (edges[band + 2] - hz) / (edges[band + 2] - edges[band + 1]);
      const double weight = std::max(0.0, std::min(rise, fall));
      // a triangle's bins are contiguous
      if (weight > 0) {
        if (bands_[band].bins == 0)
          bands_[band].firstBin = k;
        weights_.push_back(static_cast<float>(weight));
        ++bands_[band].bins;
      } else if (bands_[band].bins > 0) {
        break;
      }
    }
  }
  frames_.reserve(kFramesReserved);
}

std::span<const KwsFrontend::Frame> KwsFrontend::push(std::span<const std::int16_t> pcm16k) {
  frames_.clear();
  for (const std::int16_t sample : pcm16k) {
    history_[historyAt_] = static_cast<float>(sample) / sampleScale_;
    historyAt_ = (historyAt_ + 1) % kFrameLength;
    if (--untilFrame_ == 0) {
      frame(frames_.emplace_back());
      untilFrame_ = kFrameStep;
    }
  }
  return frames_;
}

void KwsFrontend::reset() {
  historyAt_ = 0;
  untilFrame_ = kFrameLength;
  frames_.clear();
}

void KwsFrontend::frame(Frame& features) {
  // historyAt_ is the oldest sample
  for (std::size_t n = 0; n < kFrameLength; ++n)
    spectrum_[n] = {history_[(historyAt_ + n) % kFrameLength] * window_[n], 0.0f};
  std::fill(spectrum_.begin() + static_cast<std::ptrdiff_t>(kFrameLength), spectrum_.end(), std::complex<float>{});
  fft_.forward(spectrum_);
  for (std::size_t k = 0; k < kBins; ++k)
    power_[k] = spectrum_[k].real() * spectrum_[k].real() + spectrum_[k].imag() * spectrum_[k].imag();

  for (std::size_t band = 0; band < kMels; ++band) {
    const Band& b = bands_[band];
    float energy = 0;
    for (std::size_t i = 0; i < b.bins; ++i)
      energy += power_[b.firstBin + i] * weights_[b.weightsAt + i];
    features[band] = 10 * std::log10(std::max(energy, powerFloor_));
  }
}

}  // namespace cadence::kws
