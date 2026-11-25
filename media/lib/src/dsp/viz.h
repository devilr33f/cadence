#pragma once

#include "dsp/fft.h"
#include "dsp/format.h"

#include <array>
#include <complex>
#include <cstddef>
#include <cstdint>
#include <span>

namespace cadence::dsp {

inline constexpr std::size_t kVizBands = 16;
// 1/30 s
inline constexpr std::size_t kVizHop = kRate / 30;

using VizFrame = std::array<std::uint8_t, kVizBands>;

// spectrum bands for the lights from mono 48 kHz samples. every kVizHop samples it takes the last 1024 through a
// hann window and the fft into 16 bands with log-spaced edges from 60 Hz to 16 kHz, each at least one bin. a band
// is the rms level of its bins (the root of their summed power, by parseval the band's own rms), scaled so that a
// full-scale sine reads 0 dBFS, and mapped from -60..0 dBFS to 0..255.
class VizBands {
 public:
  VizBands();

  void push(std::span<const float> mono);
  // a frame came since the last bands()
  [[nodiscard]] bool ready() const { return ready_; }
  // hands the latest frame over and clears ready(); a push spanning several frames keeps only the last
  VizFrame bands();

 private:
  struct BinRange {
    std::size_t first;
    std::size_t end;
  };

  void analyze();

  Fft1024 fft_;
  std::array<float, Fft1024::kSize> window_{};
  std::array<BinRange, kVizBands> ranges_{};

  std::array<float, Fft1024::kSize> ring_{};
  std::size_t ringPos_ = 0;
  std::size_t sinceFrame_ = 0;

  std::array<std::complex<float>, Fft1024::kSize> spectrum_{};
  VizFrame bands_{};
  bool ready_ = false;
};

}  // namespace cadence::dsp
