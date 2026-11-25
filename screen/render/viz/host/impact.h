#pragma once

#include <algorithm>
#include <array>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <deque>
#include <span>

namespace cadence::render::viz {

// 0 stays 0 and 1 stays 1. the middle falls away, so a slight kick (the opening) is a short mark and a strong
// drum stays tall
inline float exponential_strength(float s) {
  s = std::clamp(s, 0.0f, 1.0f);
  constexpr float k = 4.0f;
  return (std::exp(k * s) - 1.0f) / (std::exp(k) - 1.0f);
}

// the beat the picture hits on. not the spectral-flux grid: that hears a hat as easily as a kick, because most of
// the spectrum is the top, so the ticks around the beat were punching and the beat one hears was not (the owner).
// this is only the kick's own attack, the rise of the low band against the kicks that came before it
class Impact {
 public:
  // every new sample, in order. the stream may run ahead of what is heard; update() uses only the samples up to
  // `heard` (the same index the beat grid is fed on)
  void set_rate(double rate) { rate_ = rate > 0 ? rate : 48000; }
  void push(std::span<const float> mono);
  // loud is the music against the track's own level (0 in a fade); level_gain scales a loud master's full kick
  // down for quieter masters so pre-drop build-up kicks stay smaller than the drop's real kicks
  void update(std::int64_t heard, double dt, double loud, double level_gain = 1.0);
  // clears per-track kick calibration on a metadata track switch while keeping the audio stream position intact
  void reset_track();

  [[nodiscard]] bool hit() const { return hit_; }
  // 1 on a usual kick, small on a weaker one, gone in ~0.11 s
  [[nodiscard]] float pulse() const { return frame_pulse_; }

 private:
  void step_block(double block_peak, double dt_b, double loud, double level_gain, bool& frame_hit);

  double rate_ = 48000;
  double y_ = 0, y2_ = 0;
  double partial_ = 0;
  std::int64_t fed_ = 0;
  struct Block {
    std::int64_t end = 0;
    double peak = 0;
  };
  std::deque<Block> blocks_;
  // fixed 6-block (~32 ms at 48 kHz) sliding peak in audio time: detector runs strictly per audio block, never FPS
  std::array<double, 6> ring_{};
  std::size_t ring_pos_ = 0;
  double floor_ = 0;
  // fast baseline (~120 ms in audio time) for attack sharpness
  double fast_ = 0;
  double peak_ = 0;
  double since_ = 1;
  float pulse_ = 0;
  // peak of pulse_ during the current render frame (reported by pulse() without feeding back into pulse_'s decay)
  float frame_pulse_ = 0;
  bool hit_ = false;
  bool fired_in_crest_ = false;
  // crest tracking: update peak_ once per kick when the 100 ms crest window closes
  double crest_over_ = 0;
  bool crest_open_ = false;
  int hits_ = 0;
};

}  // namespace cadence::render::viz
