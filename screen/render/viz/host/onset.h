#pragma once

#include <array>
#include <cstddef>
#include <cstdint>
#include <deque>

namespace cadence::render::viz {

// the visualizer's kick detector, as the poc's iteration 2 rebuilt it: the raw (never smoothed) bass energy over a
// short window (~167 ms) against an adaptive long one (~2 s), over 1.32 times it plus a floor against silence, with
// a 250 ms refractory, so a kick's attack is not smeared away. the tempo comes from the recent inter-onset intervals
// clustered around their median (±35 %, so a detector that lands a frame early or late on alternate kicks still averages to the true period), and holds once four of them agree. time is the caller's: dt per frame
class OnsetTracker {
 public:
  void update(double bass_energy, double dt);

  [[nodiscard]] bool onset() const { return onset_; }
  // 1 on an onset, decaying (0.9 every 1/60 s)
  [[nodiscard]] float pulse() const { return pulse_; }
  [[nodiscard]] std::uint32_t count() const { return count_; }
  // beats a minute, 0 until the tempo holds
  [[nodiscard]] float bpm() const { return static_cast<float>(bpm_); }
  // seconds since each of the last 8 onsets, newest last; -1 for none (or older than 3.5 s)
  [[nodiscard]] std::array<float, 8> ages() const;
  // a switch on a bar line: an onset that starts a bar of four once the tempo holds, else any onset
  [[nodiscard]] bool bar() const { return onset_ && (bpm_ <= 0 || count_ % 4 == 0); }

 private:
  struct Sample {
    double t;
    double e;
  };
  std::deque<Sample> hist_;
  double t_ = 0;
  double last_ = -1;
  bool onset_ = false;
  float pulse_ = 0;
  std::uint32_t count_ = 0;
  double bpm_ = 0;
  std::array<double, 12> intervals_{};
  std::size_t interval_pos_ = 0, interval_count_ = 0;
  std::array<double, 8> times_{-1, -1, -1, -1, -1, -1, -1, -1};
};

}  // namespace cadence::render::viz
