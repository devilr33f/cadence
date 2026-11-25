#pragma once

#include "../fft.h"

#include <algorithm>
#include <array>
#include <cstddef>
#include <cstdint>
#include <deque>
#include <span>
#include <vector>

namespace cadence::render::viz {

// the beat as heard (the owner: the picture's beats did not line up with the song's; the kick band's flux fired on a
// phonk track's held 808 at random and missed most of the pulse). the standard way: an onset strength envelope (the
// rise of a log band spectrum, averaged over bands), the tempo from its autocorrelation (weighted toward 120 bpm),
// and beats on that grid, each moved onto the envelope's peak near where the grid expects it. without a tempo that
// holds, the envelope's own peaks stand in.
//
// the audio is fed in order and whole, ahead of what is heard (the screen's timeline holds 150-450 ms of it), so a
// beat is confirmed against its peak before it sounds; a beat's place is a frame index on the fed stream, and the
// consumer asks for the beats up to the frame heard now
class BeatGrid {
 public:
  static constexpr std::size_t kWindow = 1024;
  static constexpr std::size_t kBands = 40;

  explicit BeatGrid(std::uint32_t rate = 48000);

  // the next frames of the stream, mono, in order
  void feed(std::span<const float> mono);
  // frames fed so far: the index of the next one
  [[nodiscard]] std::int64_t fed() const { return fed_; }

  struct Beat {
    std::int64_t frame = 0;
    // its strength, 0..1: the kick and the snare against the usual beat. a hat's rise barely counts — the beats
    // one hears were coming out smaller than the ticks around them
    float strength = 0;
  };
  // the beats at frames up to `heard` (inclusive) not taken before, oldest first
  std::vector<Beat> take(std::int64_t heard);

  // the song's loud level against a loud master's, as an amplitude (BeatTracker's level_gain): the usual beat's
  // linear rise scales with it, so a quieter master's beats are its own size
  void set_level_gain(double g) { level_gain_ = std::clamp(g, 0.01, 1.0); }

  // the tempo, 0 while none holds; how sure, 0..1
  [[nodiscard]] double bpm() const { return period_ > 0 && confidence_ > 0.15 ? 60.0 * rate_ / (period_ * static_cast<double>(hop_)) : 0; }
  [[nodiscard]] double confidence() const { return confidence_; }
  // 0 at the last beat taken, rising to 1 at the next one expected (0 while no tempo holds)
  [[nodiscard]] double phase(std::int64_t heard) const;

 private:
  void hop();
  void estimate_tempo();
  void anchor();
  void place_beats();
  [[nodiscard]] double env(std::int64_t h) const;
  [[nodiscard]] double lin(std::int64_t h) const;
  [[nodiscard]] double body(std::int64_t h) const;
  void emit(std::int64_t h, double strength_log, double strength_lin, double strength_body, bool hit);

  std::uint32_t rate_;
  std::size_t hop_;
  Fft<kWindow> fft_;
  std::array<double, kWindow> window_{};
  std::array<std::size_t, kBands + 1> edges_{};
  std::vector<float> ring_ = std::vector<float>(kWindow, 0.0f);
  std::size_t ring_pos_ = 0, since_hop_ = 0;
  std::int64_t fed_ = 0;
  std::array<double, kBands> prev_db_{};
  std::vector<double> prev_mag_ = std::vector<double>(kWindow / 2, 0.0);
  bool primed_ = false;

  // the envelopes by hop: log (timing, tempo), linear (the whole spectrum) and body (kick and snare, weighted)
  static constexpr std::size_t kHistory = 1024;
  std::vector<double> env_ = std::vector<double>(kHistory, 0.0);
  std::vector<double> lin_ = std::vector<double>(kHistory, 0.0);
  std::vector<double> body_ = std::vector<double>(kHistory, 0.0);
  std::int64_t hops_ = 0;
  std::int64_t last_tempo_ = 0;
  // hops of near silence in a row, and the hop the music (re)started at
  std::int64_t silent_ = 0, first_hop_ = 0;

  // the grid, in hops: the period, how sure, and the next beat it expects
  double period_ = 0;
  double confidence_ = 0;
  double next_ = -1;
  int disagree_ = 0;
  // the last beat the grid found a hit for, in hops (-1: the last one had none)
  std::int64_t last_hit_ = -1;
  double candidate_ = 0;
  // the usual beat: its envelope peak and linear rise (up fast, down slowly)
  // where they start, and come back to after silence: a loud master's beat (its envelope peak, and its linear rise
  // on the tap's pre-volume pcm)
  static constexpr double kPriorEnv = 6.0, kPriorLin = 2.0, kPriorBody = 0.35;
  double ref_env_ = kPriorEnv, ref_lin_ = kPriorLin, ref_body_ = kPriorBody;
  double level_gain_ = 1;
  int refs_ = 0;
  std::int64_t last_beat_hop_ = -1000;
  std::int64_t last_beat_frame_ = -1;
  // the fallback's adaptive threshold
  double mean_ = 0, var_ = 0;

  std::deque<Beat> beats_;
};

}  // namespace cadence::render::viz
