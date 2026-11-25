#pragma once

#include "sync/calibration/fft.h"
#include "sync/calibration/probe.h"

#include <complex>
#include <cstddef>
#include <cstdint>
#include <map>
#include <span>
#include <vector>

namespace cadence::sync::calibration {

enum class ArrivalCode : std::uint8_t { Ok, NotCovered, Weak, Unstable };
[[nodiscard]] const char* token(ArrivalCode code);

// separate from the legacy wire enum: its exhaustive adapters remain valid for legacy peers
enum class ArrivalCodeV2 : std::uint8_t { Ok, NotCovered, Weak, Unstable, Missed, Unsupported };
[[nodiscard]] const char* token(ArrivalCodeV2 code);

struct DetectorConfig {
  double minPsrDb = 15;
  // legacy single-peak detection: the earliest peak at least this fraction of the highest
  double earliestFraction = 0.5;
  double zeroConfidenceDb = 10;
  double fullConfidenceDb = 40;
};

struct Decimated {
  std::vector<float> pcm;
  double rate = 0;
  // input frames represented by one output frame (three for 48 khz)
  double stride = 1;
};

// 32 and 48 khz become 16 khz through a zero-phase windowed sinc; 44.1 khz deliberately stays at its rate rather
// than paying for a fractional resampler, and rates at or below 16 khz are returned unchanged
[[nodiscard]] Decimated decimate(std::span<const float> pcm, double rate);

// analytic correlation, with each reference spectrum cached per transform size
class Correlator {
 public:
  explicit Correlator(std::vector<float> ref);
  [[nodiscard]] std::size_t refSize() const { return ref_.size(); }
  [[nodiscard]] std::vector<std::complex<float>> operator()(std::span<const float> signal);

 private:
  std::vector<float> ref_;
  Fft fft_;
  std::map<std::size_t, std::vector<std::complex<float>>> spectra_;
};

struct Baseband {
  std::vector<std::complex<float>> pcm;
  double rate = 0;
  double stride = 1;
};

// complex down-conversion at the probe's carrier, low-pass and decimation to 8 khz; only 48 khz can hear the band
[[nodiscard]] Baseband downconvert(std::span<const float> pcm, double rate, double carrierHz);

// a complex baseband correlation; unlike Correlator it adds no analytic transform of its own
class ComplexCorrelator {
 public:
  explicit ComplexCorrelator(std::vector<std::complex<float>> ref);
  [[nodiscard]] std::size_t refSize() const { return ref_.size(); }
  [[nodiscard]] std::vector<std::complex<float>> operator()(std::span<const std::complex<float>> signal);

 private:
  std::vector<std::complex<float>> ref_;
  Fft fft_;
  std::map<std::size_t, std::vector<std::complex<float>>> spectra_;
};

[[nodiscard]] std::vector<std::complex<float>> basebandReference(const ProbeShape& shape, std::uint32_t code);

[[nodiscard]] std::complex<float> interpolate(std::span<const std::complex<float>> corr, double lag);

struct Peak {
  double lag = 0;
  double envelope = 0;
  double psrDb = 0;
};

// every local envelope peak over minPsrDb in [from,to), sinc-refined; sigma<=0 takes the window's rayleigh floor
[[nodiscard]] std::vector<Peak> findCandidates(std::span<const std::complex<float>> corr, double from, double to,
                                               double sigma = 0, const DetectorConfig& config = {});
// all correlations are of one window against the plan's codes, in source order; another code within 1 ms of the
// candidate and less than 3 db below it means the candidate is leakage. coverLags optionally rejects a cross-correlation
// sidelobe anywhere under another code's stronger probe; empty correlations are codes not evaluated for this window
[[nodiscard]] bool passesIdentity(std::span<const std::vector<std::complex<float>>> correlations,
                                  std::size_t expected, const Peak& candidate, double radiusLags,
                                  double marginDb = 3, double coverLags = 0);

}  // namespace cadence::sync::calibration
