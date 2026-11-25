#pragma once

#include "dsp/biquad.h"
#include "dsp/format.h"

#include <array>
#include <atomic>
#include <cstddef>
#include <mutex>
#include <span>
#include <vector>

namespace cadence::dsp {

inline constexpr std::size_t kProfileBands = 16;
inline constexpr std::size_t kUserBands = 10;
// 20 ms
inline constexpr std::size_t kEqFadeFrames = framesFor(20);

struct EqBand {
  FilterType type = FilterType::Unspecified;
  float freqHz = 1000;
  float gainDb = 0;
  float q = 0.707f;
};

struct EqSettings {
  float preampDb = 0;
  std::vector<EqBand> bands;
};

// the board's speaker profile, then the user's eq, each with its preamp, on interleaved 48 kHz audio.
//
// set clamps (freq 20-20000 Hz, gain and preamp -12..12 dB, q 0.1-10, a shelf's q 0.1-0.707: above the cookbook's
// slope s = 1 a shelf overshoots its gain), keeps the first 16 profile and 10 user bands, and skips a band of an
// unknown type or with a nan value (a nan preamp is 0). process takes a nan or inf sample as silence, so it never
// sticks in the filters' state.
//
// set designs the cascade into one of
// three slots of a triple buffer and publishes it with one atomic exchange; process picks the latest up at the start
// of a block with another, copies it into its idle chain and crossfades the two chains linearly over 20 ms, both
// running. a set during a fade waits for the fade to end. so the audio thread never blocks or allocates; concurrent
// set calls serialize on a mutex the audio thread never takes.
class Eq {
 public:
  explicit Eq(std::size_t channels);

  Eq(const Eq&) = delete;
  Eq& operator=(const Eq&) = delete;

  void set(const EqSettings& profile, const EqSettings& user);
  // whole frames only; a trailing partial frame is left as is
  void process(std::span<float> interleaved);
  // forgets the audio held in the filters' state; on the audio thread, between blocks
  void reset();

 private:
  static constexpr std::size_t kStages = kProfileBands + kUserBands;
  static constexpr unsigned kIndex = 3;
  static constexpr unsigned kFresh = 4;

  struct Design {
    std::array<BiquadCoefficients, kStages> stages{};
    std::size_t count = 0;
    double gain = 1;
  };

  struct Chain {
    Design design;
    // kStages per channel
    std::vector<BiquadState> states;
  };

  static void addLayer(Design& design, const EqSettings& layer, std::size_t limit);
  void pickUp();
  double run(Chain& chain, std::size_t channel, double x);

  std::size_t channels_;

  std::mutex setMutex_;
  std::array<Design, 3> slots_{};
  unsigned back_ = 0;
  std::atomic<unsigned> middle_{1};
  unsigned front_ = 2;

  std::array<Chain, 2> chains_;
  std::size_t current_ = 0;
  std::size_t fadeLeft_ = 0;
};

}  // namespace cadence::dsp
