#pragma once

#include "sync/calibration/estimate.h"

#include <cstdint>
#include <string>
#include <vector>

namespace cadence::sync::calibration {

enum class ResultCode : std::uint8_t { Ok, Incomplete, Inconsistent, TooFew };
[[nodiscard]] const char* token(ResultCode code);

enum class MemberCode : std::uint8_t { Ok, NoSelf, Isolated, Unstable };
[[nodiscard]] const char* token(MemberCode code);

struct MemberOffset {
  std::string id;
  MemberCode code = MemberCode::Ok;
  double offsetNs = 0;
  double confidence = 0;
  double spreadNs = 0;
  std::uint32_t rounds = 0;
  double ci95Ns = 0;
};

struct Solution {
  ResultCode code = ResultCode::Ok;
  std::vector<MemberOffset> members;
  double residualNs = 0;
};

struct SolverConfig {
  double maxResidualNs = 1'000'000;
};

[[nodiscard]] Solution solve(const Plan& plan, const std::vector<std::vector<Heard>>& heard,
                             const SolverConfig& config = {});
// v2 prefers each pair's opposite in-recording differences and weights it by its robust variance
[[nodiscard]] Solution solveV2(const Plan& plan, const std::vector<std::vector<HeardV2>>& heard,
                               const SolverConfig& config = {});

}  // namespace cadence::sync::calibration
