#include "sync/calibration/probe.h"

#include "sync/calibration.h"

#include <algorithm>
#include <cmath>
#include <numbers>

namespace cadence::sync::calibration {

namespace {

using Bits = std::array<int, kGoldChips>;

// fibonacci form of the primitive polynomial: bit n+10 is the xor of its nonzero lower terms at n
Bits mSequence(bool second) {
  Bits out{};
  std::array<int, 10> state{};
  state[0] = 1;
  for (std::size_t n = 0; n < out.size(); ++n) {
    out[n] = state[0];
    const int next = second ? state[8] ^ state[3] ^ state[2] ^ state[0] : state[3] ^ state[0];
    for (std::size_t k = 0; k + 1 < state.size(); ++k)
      state[k] = state[k + 1];
    state.back() = next;
  }
  return out;
}

double sinc(double x) { return std::abs(x) < 1e-12 ? 1.0 : std::sin(std::numbers::pi * x) / (std::numbers::pi * x); }

// raised cosine, eight chip periods either side:
// p(u) = sinc(u/T) cos(pi*r*u/T) / (1 - (2*r*u/T)^2), r=0.25, zero for |u| > 4T.
// at |u|=2T the removable denominator singularity is (pi/4) sinc(u/T).
double raisedCosine(double chips) {
  if (std::abs(chips) > static_cast<double>(kGoldPulseChips))
    return 0;
  if (std::abs(std::abs(chips) - 2.0) < 1e-10)
    return std::numbers::pi / 4.0 * sinc(chips);
  return sinc(chips) * std::cos(std::numbers::pi * kGoldRollOff * chips) /
         (1.0 - std::pow(2.0 * kGoldRollOff * chips, 2));
}

}  // namespace

std::array<std::int8_t, kGoldChips> goldCode(std::uint32_t code) {
  static const Bits first = mSequence(false);
  static const Bits second = mSequence(true);
  const auto shift = static_cast<std::size_t>(code % kGoldChips);
  std::array<std::int8_t, kGoldChips> out{};
  for (std::size_t n = 0; n < out.size(); ++n)
    out[n] = static_cast<std::int8_t>(1 - 2 * (first[n] ^ second[(n + shift) % kGoldChips]));
  return out;
}

ProbeShape chirpProbe(const ChirpShape& shape) {
  ProbeShape out;
  out.kind = ProbeKind::Chirp;
  out.bandLoHz = shape.f0Hz;
  out.bandHiHz = shape.f1Hz;
  out.durationS = shape.durationS;
  out.fadeS = shape.fadeS;
  out.amplitude = shape.amplitude;
  return out;
}

ProbeShape ultrasonicProbe() {
  ProbeShape out;
  out.bandLoHz = 18000;
  out.bandHiHz = 20500;
  // the code rides 30 db under full scale on the music input, so the device's own volume is still the ceiling (rule 7)
  out.amplitude = std::pow(10.0, -30.0 / 20.0);
  return out;
}

bool ultrasonic(const ProbeShape& shape) { return shape.kind == ProbeKind::Gold && shape.bandLoHz >= 16000; }

double probeDuration(const ProbeShape& shape) {
  if (shape.kind == ProbeKind::Chirp)
    return shape.durationS;
  const double chipRate = shape.bandHiHz - shape.bandLoHz;
  return chipRate > 0 ? (static_cast<double>(kGoldChips) + 2.0 * kGoldPulseChips) / chipRate : 0;
}

double probeAt(const ProbeShape& shape, std::uint32_t code, double tS) {
  if (shape.kind == ProbeKind::Chirp) {
    const ChirpShape chirp{shape.bandLoHz, shape.bandHiHz, shape.durationS, shape.fadeS, shape.amplitude};
    return chirpAt(chirp, tS);
  }
  const double duration = probeDuration(shape);
  const double chipRate = shape.bandHiHz - shape.bandLoHz;
  if (tS < 0 || tS >= duration || chipRate <= 0)
    return 0;
  const auto chips = goldCode(code);
  const double chipS = 1.0 / chipRate;
  const double at = tS / chipS - static_cast<double>(kGoldPulseChips);
  constexpr auto kPulse = static_cast<std::int64_t>(kGoldPulseChips);
  const auto first = std::max<std::int64_t>(0, static_cast<std::int64_t>(std::floor(at)) - kPulse);
  const auto last = std::min<std::int64_t>(static_cast<std::int64_t>(kGoldChips) - 1,
                                           static_cast<std::int64_t>(std::ceil(at)) + kPulse);
  double baseband = 0;
  for (auto k = first; k <= last; ++k)
    baseband += static_cast<double>(chips[static_cast<std::size_t>(k)]) * raisedCosine(at - static_cast<double>(k));
  double fade = 1;
  if (shape.fadeS > 0) {
    const double edge = std::min(tS, duration - tS);
    if (edge < shape.fadeS)
      fade = 0.5 - 0.5 * std::cos(std::numbers::pi * edge / shape.fadeS);
  }
  // the carrier is global, not restarted per chip: both sides of a correlation see the same passband waveform
  return shape.amplitude * fade * baseband *
         std::cos(2.0 * std::numbers::pi * (shape.bandLoHz + shape.bandHiHz) / 2.0 * tS);
}

std::vector<float> probeSamples(const ProbeShape& shape, std::uint32_t code, double rate) {
  const auto count = static_cast<std::size_t>(std::ceil(probeDuration(shape) * rate));
  std::vector<float> out(count);
  for (std::size_t n = 0; n < count; ++n)
    out[n] = static_cast<float>(probeAt(shape, code, static_cast<double>(n) / rate));
  return out;
}

ProbeTable::ProbeTable(const ProbeShape& shape, std::uint32_t code)
    : shape_(shape), code_(code), durationS_(probeDuration(shape)) {
  const double chipRate = shape.bandHiHz - shape.bandLoHz;
  if (shape.kind != ProbeKind::Gold || chipRate <= 0 || durationS_ <= 0)
    return;
  carrierHz_ = (shape.bandLoHz + shape.bandHiHz) / 2.0;
  // an in-band probe's chips come faster: eight points a chip keep its table at about the output's own rate
  stepS_ = 1.0 / (chipRate * (chipRate <= 3000 ? 16.0 : 8.0));
  const auto points = static_cast<std::size_t>(std::ceil(durationS_ / stepS_)) + 2;
  envelope_.resize(points);
  // the envelope is probeAt's sum with its carrier left out, so the table and probeAt agree by construction
  const auto chips = goldCode(code);
  const double chipS = 1.0 / chipRate;
  constexpr auto kPulse = static_cast<std::int64_t>(kGoldPulseChips);
  for (std::size_t i = 0; i < points; ++i) {
    const double t = static_cast<double>(i) * stepS_;
    if (t >= durationS_)
      break;
    const double at = t / chipS - static_cast<double>(kGoldPulseChips);
    const auto first = std::max<std::int64_t>(0, static_cast<std::int64_t>(std::floor(at)) - kPulse);
    const auto last = std::min<std::int64_t>(static_cast<std::int64_t>(kGoldChips) - 1,
                                             static_cast<std::int64_t>(std::ceil(at)) + kPulse);
    double baseband = 0;
    for (auto k = first; k <= last; ++k)
      baseband += static_cast<double>(chips[static_cast<std::size_t>(k)]) * raisedCosine(at - static_cast<double>(k));
    double fade = 1;
    if (shape.fadeS > 0) {
      const double edge = std::min(t, durationS_ - t);
      if (edge < shape.fadeS)
        fade = 0.5 - 0.5 * std::cos(std::numbers::pi * edge / shape.fadeS);
    }
    envelope_[i] = static_cast<float>(shape.amplitude * fade * baseband);
  }
}

double ProbeTable::at(double tS) const {
  if (envelope_.empty())
    return probeAt(shape_, code_, tS);
  if (tS < 0 || tS >= durationS_)
    return 0;
  const double x = tS / stepS_;
  const auto i = static_cast<std::size_t>(x);
  if (i + 1 >= envelope_.size())
    return 0;
  const double f = x - static_cast<double>(i);
  const double envelope = envelope_[i] + (envelope_[i + 1] - envelope_[i]) * f;
  return envelope * std::cos(2.0 * std::numbers::pi * carrierHz_ * tS);
}

}  // namespace cadence::sync::calibration
