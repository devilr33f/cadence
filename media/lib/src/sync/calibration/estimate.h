#pragma once

#include "sync/calibration/detect.h"
#include "sync/calibration/plan.h"

#include <cstddef>
#include <cstdint>
#include <functional>
#include <span>
#include <vector>

namespace cadence::sync::calibration {

struct ReadStamp {
  std::uint64_t endFrame = 0;
  std::int64_t monoNs = 0;
};

struct SampleClock {
  double originNs = 0;
  double nsPerFrame = 0;
  [[nodiscard]] double monoAt(double frame) const { return originNs + frame * nsPerFrame; }
  [[nodiscard]] double frameAt(double monoNs) const { return (monoNs - originNs) / nsPerFrame; }
};

[[nodiscard]] SampleClock fitSampleClock(std::span<const ReadStamp> stamps, double nominalRate,
                                         double maxPpm = 1000);

struct Heard {
  ArrivalCode code = ArrivalCode::Weak;
  std::int64_t tNs = 0;
  double confidence = 0;
  double spreadNs = 0;
  std::uint32_t rounds = 0;
};

struct HeardV2 {
  ArrivalCodeV2 code = ArrivalCodeV2::Weak;
  std::int64_t tNs = 0;
  double confidence = 0;
  double spreadNs = 0;
  std::uint32_t rounds = 0;
  double sigmaNs = 0;
  double ci95Ns = 0;
  // this source's arrival less this recorder's own, in the same recording and round
  double relativeNs = 0;
  double relativeSpreadNs = 0;
  bool hasRelative = false;
};

struct AnalysisConfig {
  DetectorConfig detector;
  // legacy two-pass settings
  std::int64_t narrowNs = 2'000'000;
  std::int64_t gateNs = 1'000'000;
  double maxSpreadNs = 500'000;
  // v2: direct-path clustering, robust confidence and microphone cap
  double toleranceNs = 2'000'000;
  double clusterNs = 250'000;
  double minEnvelopeFraction = 0.25;
  std::size_t maxMicChannels = 3;
  double identityMarginDb = 3;
};

// legacy chirp analysis, kept byte-for-byte in behavior for existing peers
[[nodiscard]] std::vector<Heard> analyse(const Plan& plan, std::span<const std::int16_t> interleaved,
                                         std::size_t channels, double rate, const SampleClock& clock,
                                         const std::function<double(double localMonoNs)>& toLeader,
                                         const AnalysisConfig& config = {});
[[nodiscard]] std::vector<Heard> analyse(const Plan& plan, std::span<const float> pcm, double rate,
                                         const SampleClock& clock,
                                         const std::function<double(double localMonoNs)>& toLeader,
                                         const AnalysisConfig& config = {});
// coded analysis; selfIndex is this recorder in the plan and enables in-recording differencing
[[nodiscard]] std::vector<HeardV2> analyse(const Plan& plan, std::span<const std::int16_t> interleaved,
                                           std::size_t channels, double rate, std::size_t selfIndex,
                                           const SampleClock& clock,
                                           const std::function<double(double localMonoNs)>& toLeader,
                                           const AnalysisConfig& config = {});
[[nodiscard]] std::vector<HeardV2> analyse(const Plan& plan, std::span<const float> pcm, double rate,
                                           std::size_t selfIndex, const SampleClock& clock,
                                           const std::function<double(double localMonoNs)>& toLeader,
                                           const AnalysisConfig& config = {});

}  // namespace cadence::sync::calibration
