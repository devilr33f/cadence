#pragma once

#include "dsp/fft.h"

#include <array>
#include <complex>
#include <cstddef>
#include <cstdint>
#include <span>
#include <vector>

namespace cadence::kws {

// the values the keyword frontend builds its features from; the frame geometry and the band count fix the model's
// input, so they are KwsFrontend's constants
struct FrontendParams {
  // bin k of the fft sits at k * sampleRateHz / KwsFrontend::kFftSize
  double sampleRateHz = 0;
  // an int16 sample s becomes s / sampleScale, in float
  float sampleScale = 0;
  // the outer edges of the triangles, which are spaced evenly between them on the htk mel scale
  double melLowHz = 0;
  double melHighHz = 0;
  // a band's power below it counts as it, so the features' floor is 10 log10(powerFloor) dB
  float powerFloor = 0;

  // the vk capsula mini's stock frontend: 16 kHz, s / 32767, 0 to 8000 Hz, a floor of 1e-10 (-100 dB)
  static FrontendParams defaultCapsula();
};

// the keyword model's features from 16 kHz mono int16 audio, computed as the vk capsula mini's stock does: frames of
// 640 samples every 160 over the continuous stream (no centring or padding, so the first frame ends at sample 640),
// each through a periodic hamming window into the first 640 points of a zero-padded 2048-point fft, its power
// spectrum (no 1/N) through 40 triangles on the htk mel scale that peak at 1 (no area normalisation), and 10 log10
// of each band's power. float throughout, as the stock. the window and the triangles are computed in double at
// construction, from FrontendParams alone. a push allocates nothing once the frames it completes fit
class KwsFrontend {
 public:
  static constexpr std::size_t kFrameLength = 640;
  static constexpr std::size_t kFrameStep = 160;
  static constexpr std::size_t kFftSize = 2048;
  static constexpr std::size_t kMels = 40;
  using Frame = std::array<float, kMels>;

  explicit KwsFrontend(const FrontendParams& params);

  // the frames these samples complete, oldest first: valid until the next push or reset
  std::span<const Frame> push(std::span<const std::int16_t> pcm16k);
  // forgets the samples kept for the next frame, so the next one ends 640 samples into what follows
  void reset();

 private:
  struct Band {
    std::size_t firstBin = 0;
    std::size_t bins = 0;
    std::size_t weightsAt = 0;
  };

  void frame(Frame& features);

  float sampleScale_;
  float powerFloor_;
  dsp::Fft<kFftSize> fft_;
  std::array<float, kFrameLength> window_{};
  std::array<Band, kMels> bands_{};
  // the triangles' nonzero weights, band after band
  std::vector<float> weights_;

  // the last kFrameLength samples, scaled; once full, history_[historyAt_] is the oldest
  std::array<float, kFrameLength> history_{};
  std::size_t historyAt_ = 0;
  std::size_t untilFrame_ = kFrameLength;

  std::array<std::complex<float>, kFftSize> spectrum_{};
  std::array<float, kFftSize / 2 + 1> power_{};
  std::vector<Frame> frames_;
};

}  // namespace cadence::kws
