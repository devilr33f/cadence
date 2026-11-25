#pragma once

#include "fft.h"

#include <array>
#include <cstddef>
#include <cstdint>
#include <span>
#include <vector>

namespace cadence::render::viz {

// the frames of a PcmSnapshot a channel (audiod's tap: the last 512 of the mix as heard, about 60 a second)
inline constexpr std::size_t kSnapshotFrames = 512;
// a VizFrame's bands (audiod's 16, 0-255 each)
inline constexpr std::size_t kBands = 16;
inline constexpr std::size_t kMaxBars = 128;

// a PcmSnapshot's samples (interleaved little-endian f32) as floats, whatever the host's byte order; a trailing
// partial sample is dropped
std::vector<float> decode_samples(std::span<const std::uint8_t> bytes);

struct AnalyzerConfig {
  std::size_t bars = 32;
  double min_hz = 50;
  double max_hz = 16000;
  // dBFS mapped to 0 and 1 (a full-scale sine reads 0)
  double floor_db = -60;
  double ceil_db = 0;
  // a bar falls at most this many heights a second (it rises at once)
  double fall_per_s = 1.5;
  // a peak holds this long, then falls with this acceleration (heights / s²)
  double peak_hold_s = 0.5;
  double peak_gravity = 3.0;
  std::size_t scope_points = 256;
};

// the classic visualizer's state from the tap: a spectrum of log-spaced bars (hann window, fft of the snapshot's
// mono mix) smoothed as winamp's are, falling peaks, and the oscilloscope's points. the screen feeds it each snapshot
// and steps it each frame it draws; the bands of a VizFrame stand in when the tap sends no pcm
class Analyzer {
 public:
  explicit Analyzer(AnalyzerConfig c = {});

  // one snapshot: interleaved samples of channels channels at sample_rate
  void push_pcm(std::span<const float> interleaved, std::uint32_t channels, std::uint32_t sample_rate);
  // one VizFrame: its 16 bands spread over the bars
  void push_bands(std::span<const std::uint8_t> bands);
  // silence: the target levels drop to 0 (paused, or the tap stopped)
  void silence();

  // advance the smoothing by dt seconds; reduced motion jumps bars and peaks to their targets
  void step(double dt, bool reduced_motion = false);

  [[nodiscard]] const AnalyzerConfig& config() const { return c_; }
  // 0..1 each, after step
  [[nodiscard]] std::span<const double> bars() const { return {bars_.data(), c_.bars}; }
  [[nodiscard]] std::span<const double> peaks() const { return {peaks_.data(), c_.bars}; }
  // -1..1 each, the latest snapshot's mono mix resampled to scope_points
  [[nodiscard]] std::span<const double> scope() const { return scope_; }
  // anything still moving: a caller that sees false can stop redrawing
  [[nodiscard]] bool moving() const;

 private:
  struct Range {
    std::size_t first, end;
  };

  void set_ranges(std::uint32_t sample_rate);

  AnalyzerConfig c_;
  Fft<kSnapshotFrames> fft_;
  std::array<double, kSnapshotFrames> window_{};
  std::uint32_t rate_ = 0;
  std::vector<Range> ranges_;

  std::array<double, kMaxBars> target_{};
  std::array<double, kMaxBars> bars_{};
  std::array<double, kMaxBars> peaks_{};
  std::array<double, kMaxBars> peak_age_{};
  std::array<double, kMaxBars> peak_speed_{};
  std::vector<double> scope_;
};

}  // namespace cadence::render::viz
