#pragma once

#include "sync/calibration/detect.h"
#include "sync/calibration/estimate.h"
#include "sync/calibration/fft.h"
#include "sync/calibration/plan.h"
#include "sync/calibration/probe.h"
#include "sync/calibration/solve.h"
#include "sync/calibration/track.h"

#include <complex>
#include <cstddef>
#include <cstdint>
#include <functional>
#include <optional>
#include <span>
#include <string>
#include <vector>

// acoustic calibration of a multiroom group. everything here is pure: no clock, no thread, no audio kept past a call
// (rule 8). the legacy chirp api remains for existing peers; coded plans add identity and in-recording differences.
namespace cadence::sync::calibration {

[[nodiscard]] ChirpShape legacyChirp();
inline constexpr std::uint32_t kMaxGainDb = 14;
[[nodiscard]] ChirpShape withGain(ChirpShape shape, std::uint32_t gainDb);
[[nodiscard]] double chirpAt(const ChirpShape& shape, double tS);
[[nodiscard]] std::vector<float> chirpSamples(const ChirpShape& shape, double rate);

// the legacy double transform and analytic correlation remain for wire-compatible chirp analysis
void fft(std::vector<std::complex<double>>& data, bool inverse);
[[nodiscard]] std::vector<std::complex<double>> correlate(std::span<const float> signal, std::span<const float> ref);
[[nodiscard]] std::complex<double> interpolate(std::span<const std::complex<double>> corr, double lag);

struct Arrival {
  ArrivalCode code = ArrivalCode::Weak;
  double lag = 0;
  double psrDb = 0;
  double confidence = 0;
};

[[nodiscard]] Arrival findArrival(std::span<const std::complex<double>> corr, double from, double to,
                                  const DetectorConfig& config = {}, std::optional<double> sigma = std::nullopt);

}  // namespace cadence::sync::calibration
