#pragma once

#include <cstdint>

// how a group's playout corrects where its audio lands: the error is heard minus due (positive: late) before each
// frame is pushed, exact to a sample while the music input keeps up (the playhead formula). small errors are steered
// out through the input's resampler, gapless; past cutNs the frame is cut (frames dropped or silence put before it,
// fade-spliced); past rejoinNs the playout rejoins the stream.
//
// the steering is a pi loop on the error. the spec's gain (~10 ppm per 100 us) alone leaves a crystal's drift as a
// standing error of drift / gain (50 ppm: 500 us) and takes 10 s to settle; a group must hold 100 us for a stereo
// pair, so the proportional gain is 2 ppm per us and an integral term, critically damped (a double pole at 1/s), takes
// the drift itself, so the standing error is 0: an 80 ppm crystal peaks at ~30 us and is gone in a few s. a 48 kHz trim moves in steps of ~21 ppm (dsp::Resampler's
// denominator), which the integral dithers between
namespace cadence::sync {

struct GroupPlacerConfig {
  // under it the proportional term rests (the integral still runs). 0: the spec's 50 us band left the loop a pure
  // integral inside it, which swung +-50 us with a 6 s period on an 80 ppm dac; the error is measured to a fraction of
  // a sample (the resampler's pending output), so it needs no band against noise
  std::int64_t deadBandNs = 0;
  double ppmPerUs = 2.0;
  // ppm per us of error per second: kp^2 / 4, critically damped
  double integralPpmPerUsS = 1.0;
  std::int32_t maxPpm = 500;
  std::int64_t cutNs = 5'000'000;
  std::int64_t rejoinNs = 200'000'000;
  // the smoothing of the reported error, per frame
  double smoothing = 0.1;
};

struct GroupPlacement {
  enum class Action : std::uint8_t { Steer, Cut, Rejoin };
  Action action = Action::Steer;
  // the rate from here, ppm faster (positive) or slower
  std::int32_t steerPpm = 0;
  // a cut: how late (positive: drop that much) or early (negative: that much silence first)
  std::int64_t cutNs = 0;
};

// one thread
class GroupPlacer {
 public:
  explicit GroupPlacer(GroupPlacerConfig config = {}) : config_(config) {}

  // errorNs: heard minus due of the frame about to be pushed; dtNs: since the last call (a frame, 20 ms)
  GroupPlacement place(std::int64_t errorNs, std::int64_t dtNs);
  void reset();

  [[nodiscard]] std::int32_t steerPpm() const { return steer_; }
  [[nodiscard]] std::int64_t smoothedErrorNs() const { return static_cast<std::int64_t>(smoothed_); }
  [[nodiscard]] std::uint64_t cuts() const { return cuts_; }

 private:
  GroupPlacerConfig config_;
  double integral_ = 0;
  double smoothed_ = 0;
  std::int32_t steer_ = 0;
  std::uint64_t cuts_ = 0;
};

}  // namespace cadence::sync
