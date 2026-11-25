#pragma once

#include <algorithm>
#include <cmath>
#include <cstddef>
#include <numbers>
#include <span>
#include <vector>

namespace cadence::test_support {

// amplitude * sin(2 pi hz n / rate), the same on each of the interleaved channels
inline std::vector<float> sine(double hz, double amplitude, std::size_t frames, std::size_t channels = 1,
                               double rate = 48000) {
  std::vector<float> samples(frames * channels);
  for (std::size_t i = 0; i < frames; ++i) {
    const double value = amplitude * std::sin(2 * std::numbers::pi * hz * static_cast<double>(i) / rate);
    std::fill_n(samples.begin() + static_cast<std::ptrdiff_t>(i * channels), channels, static_cast<float>(value));
  }
  return samples;
}

// rms of one channel over frames [first, first + frames)
inline double rms(std::span<const float> samples, std::size_t channels, std::size_t channel, std::size_t first,
                  std::size_t frames) {
  double sum = 0;
  for (std::size_t i = first; i < first + frames; ++i) {
    const double value = samples[i * channels + channel];
    sum += value * value;
  }
  return std::sqrt(sum / static_cast<double>(frames));
}

inline double db(double ratio) { return 20 * std::log10(ratio); }

// the largest sample-to-sample step of one channel over frames [first, first + frames)
inline double maxStep(std::span<const float> samples, std::size_t channels, std::size_t channel, std::size_t first,
                      std::size_t frames) {
  double step = 0;
  for (std::size_t i = std::max<std::size_t>(first, 1); i < first + frames; ++i)
    step = std::max(step, std::abs(static_cast<double>(samples[i * channels + channel]) -
                                   static_cast<double>(samples[(i - 1) * channels + channel])));
  return step;
}

// the amplitude of one frequency in one channel over frames [first, first + frames), from a single dft bin: exact
// for a sine with a whole number of cycles in the window, and blind to any other such sine
template <typename Samples>
double amplitudeAt(const Samples& samples, std::size_t channels, std::size_t channel, std::size_t first,
                   std::size_t frames, double hz, double rate = 48000) {
  double re = 0;
  double im = 0;
  for (std::size_t i = 0; i < frames; ++i) {
    const double value = static_cast<double>(samples[(first + i) * channels + channel]);
    const double phase = 2 * std::numbers::pi * hz * static_cast<double>(i) / rate;
    re += value * std::cos(phase);
    im -= value * std::sin(phase);
  }
  return 2 * std::sqrt(re * re + im * im) / static_cast<double>(frames);
}

// calls process on consecutive blocks of at most block frames, the way the audio thread hands them over
template <typename Process>
void inBlocks(std::span<float> samples, std::size_t channels, std::size_t block, Process process) {
  for (std::size_t first = 0; first < samples.size(); first += block * channels)
    process(samples.subspan(first, std::min(block * channels, samples.size() - first)));
}

}  // namespace cadence::test_support
