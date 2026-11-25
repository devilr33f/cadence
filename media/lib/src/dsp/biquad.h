#pragma once

#include "dsp/format.h"

#include <cmath>

namespace cadence::dsp {

// numbered as cadence.audiod.v1.EqType, so the ipc value maps over as is
enum class FilterType : int {
  Unspecified = 0,
  Peaking = 1,
  LowShelf = 2,
  HighShelf = 3,
  LowPass = 4,
  HighPass = 5,
};

// normalized by a0
struct BiquadCoefficients {
  double b0 = 1;
  double b1 = 0;
  double b2 = 0;
  double a1 = 0;
  double a2 = 0;
};

// rbj audio eq cookbook. gain only shapes peaking and shelves; for shelves q is the shelf's own q (0.707 is the
// cookbook's slope s = 1, above which a shelf overshoots, so the eq caps it there). an unspecified or unknown type
// designs the identity
BiquadCoefficients designBiquad(FilterType type, double freqHz, double gainDb, double q, double rateHz = kRateHz);

// one channel's state, transposed direct form ii. callers hand it finite samples only: a nan or inf would stay in the
// state for good
class BiquadState {
 public:
  double process(const BiquadCoefficients& c, double x) {
    const double y = c.b0 * x + z1_;
    z1_ = flush(c.b1 * x - c.a1 * y + z2_);
    z2_ = flush(c.b2 * x - c.a2 * y);
    return y;
  }

  void reset() { z1_ = z2_ = 0; }

 private:
  // a decaying tail would otherwise end in denormals, which are slow on most cpus
  static double flush(double v) { return std::abs(v) < 1e-20 ? 0.0 : v; }

  double z1_ = 0;
  double z2_ = 0;
};

}  // namespace cadence::dsp
