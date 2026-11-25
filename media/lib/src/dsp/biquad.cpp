#include "dsp/biquad.h"

#include <cmath>
#include <numbers>

namespace cadence::dsp {

// robert bristow-johnson, "cookbook formulae for audio eq biquad filter coefficients" (the w3c audio eq cookbook)
BiquadCoefficients designBiquad(FilterType type, double freqHz, double gainDb, double q, double rateHz) {
  const double w0 = 2 * std::numbers::pi * freqHz / rateHz;
  const double cosW = std::cos(w0);
  const double alpha = std::sin(w0) / (2 * q);
  const double a = std::pow(10.0, gainDb / 40);
  const double shelf = 2 * std::sqrt(a) * alpha;

  double b0 = 1;
  double b1 = 0;
  double b2 = 0;
  double a0 = 1;
  double a1 = 0;
  double a2 = 0;
  switch (type) {
    case FilterType::Peaking:
      b0 = 1 + alpha * a;
      b1 = -2 * cosW;
      b2 = 1 - alpha * a;
      a0 = 1 + alpha / a;
      a1 = -2 * cosW;
      a2 = 1 - alpha / a;
      break;
    case FilterType::LowShelf:
      b0 = a * ((a + 1) - (a - 1) * cosW + shelf);
      b1 = 2 * a * ((a - 1) - (a + 1) * cosW);
      b2 = a * ((a + 1) - (a - 1) * cosW - shelf);
      a0 = (a + 1) + (a - 1) * cosW + shelf;
      a1 = -2 * ((a - 1) + (a + 1) * cosW);
      a2 = (a + 1) + (a - 1) * cosW - shelf;
      break;
    case FilterType::HighShelf:
      b0 = a * ((a + 1) + (a - 1) * cosW + shelf);
      b1 = -2 * a * ((a - 1) + (a + 1) * cosW);
      b2 = a * ((a + 1) + (a - 1) * cosW - shelf);
      a0 = (a + 1) - (a - 1) * cosW + shelf;
      a1 = 2 * ((a - 1) - (a + 1) * cosW);
      a2 = (a + 1) - (a - 1) * cosW - shelf;
      break;
    case FilterType::LowPass:
      b0 = (1 - cosW) / 2;
      b1 = 1 - cosW;
      b2 = (1 - cosW) / 2;
      a0 = 1 + alpha;
      a1 = -2 * cosW;
      a2 = 1 - alpha;
      break;
    case FilterType::HighPass:
      b0 = (1 + cosW) / 2;
      b1 = -(1 + cosW);
      b2 = (1 + cosW) / 2;
      a0 = 1 + alpha;
      a1 = -2 * cosW;
      a2 = 1 - alpha;
      break;
    case FilterType::Unspecified:
      break;
  }
  return {.b0 = b0 / a0, .b1 = b1 / a0, .b2 = b2 / a0, .a1 = a1 / a0, .a2 = a2 / a0};
}

}  // namespace cadence::dsp
