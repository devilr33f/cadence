#include "sync/calibration/track.h"

#include <algorithm>
#include <cmath>

namespace cadence::sync::calibration {

namespace {

double median(std::vector<double> values) {
  if (values.empty())
    return 0;
  std::sort(values.begin(), values.end());
  const auto middle = values.size() / 2;
  return values.size() % 2 == 0 ? (values[middle - 1] + values[middle]) / 2 : values[middle];
}

double mad(std::span<const double> values, double centre) {
  std::vector<double> deviations;
  deviations.reserve(values.size());
  for (const auto value : values)
    deviations.push_back(std::abs(value - centre));
  return 1.4826 * median(std::move(deviations));
}

}  // namespace

TrackingReport TrackingWindow::add(const Plan& plan, const std::vector<std::vector<HeardV2>>& heard,
                                   std::int64_t nowNs) {
  if (plan.members.size() < 2 || heard.size() != plan.members.size())
    return {.state = "idle", .members = {}};
  auto current = solveV2(plan, heard);
  cycles_.push_back({plan, heard, current});
  while (cycles_.size() > std::max<std::size_t>(config_.windowCycles, 1))
    cycles_.pop_front();
  TrackingReport out;
  out.state = current.code == ResultCode::Ok ? "tracking" : "idle";
  for (std::size_t i = 0; i < current.members.size(); ++i) {
    const auto& measured = current.members[i];
    if (measured.code != MemberCode::Ok)
      continue;
    std::vector<double> offsets;
    for (const auto& cycle : cycles_) {
      if (i < cycle.solution.members.size() && cycle.solution.members[i].code == MemberCode::Ok)
        offsets.push_back(cycle.solution.members[i].offsetNs);
    }
    const auto centre = median(offsets);
    const auto [slowest, fastest] = std::ranges::minmax(offsets);
    // a persistent output-latency step is the signal, not uncertainty; between-cycle spread counts only once the
    // window agrees within the deadband again
    const double betweenCi = offsets.empty() || fastest - slowest > static_cast<double>(config_.deadbandNs)
                                 ? 0
                                 : 1.96 * mad(offsets, centre) / std::sqrt(static_cast<double>(offsets.size()));
    const auto ci95 = std::max(measured.ci95Ns, betweenCi);
    auto& smooth = smoothed_[measured.id];
    const auto alpha = std::clamp(config_.smoothingAlpha, 0.01, 1.0);
    if (!smooth.set) {
      smooth.offsetNs = measured.offsetNs;
      smooth.ci95Ns = ci95;
      smooth.set = true;
    } else {
      smooth.offsetNs += alpha * (measured.offsetNs - smooth.offsetNs);
      smooth.ci95Ns += alpha * (ci95 - smooth.ci95Ns);
    }
    TrackingMember member;
    member.id = measured.id;
    member.offsetUs = static_cast<std::int32_t>(std::llround(smooth.offsetNs / 1000.0));
    member.ci95Us = static_cast<std::uint32_t>(std::min(smooth.ci95Ns / 1000.0, 4e9));
    member.cycles = static_cast<std::uint32_t>(offsets.size());
    member.correctionId = correctionIds_[measured.id];
    const bool outside = std::abs(smooth.offsetNs) > static_cast<double>(config_.deadbandNs);
    const bool precise = smooth.ci95Ns < static_cast<double>(config_.deadbandNs) / 2.0;
    const bool bounded = std::abs(measured.offsetNs) <= static_cast<double>(config_.maxCorrectionNs);
    bool moved = false, selfValid = false;
    double currentSelfNs = smooth.selfNs;
    if (i < heard.size() && i < heard[i].size() && heard[i][i].code == ArrivalCodeV2::Ok) {
      currentSelfNs = static_cast<double>(heard[i][i].tNs - plan.slot(0, i));
      selfValid = true;
      if (!smooth.selfSet) {
        smooth.selfNs = currentSelfNs;
        smooth.selfSet = true;
      }
      moved = std::abs(currentSelfNs - smooth.selfNs) > static_cast<double>(config_.deadbandNs);
      // a centred member is the baseline for the next move; a mean shift caused by its neighbour moves no self-arrival
      if (!outside)
        smooth.selfNs = currentSelfNs;
    }
    if (!bounded)
      out.state = "needs_calibration";
    smooth.qualifying = current.code == ResultCode::Ok && outside && precise && bounded && moved ? smooth.qualifying + 1 : 0;
    const bool cooled = smooth.lastCorrectionNs == 0 || nowNs - smooth.lastCorrectionNs >= config_.correctionCooldownNs;
    if (smooth.qualifying >= config_.requiredWindows && cooled && bounded) {
      const auto correction = std::clamp(-smooth.offsetNs, -static_cast<double>(config_.maxCorrectionNs),
                                         static_cast<double>(config_.maxCorrectionNs));
      member.correctionUs = static_cast<std::int32_t>(std::llround(correction / 1000.0));
      member.correctionId = ++correctionIds_[measured.id];
      smooth.lastCorrectionNs = nowNs;
      smooth.qualifying = 0;
      if (selfValid)
        smooth.selfNs = currentSelfNs;
    }
    out.members.push_back(std::move(member));
  }
  return out;
}

void TrackingWindow::reset() {
  cycles_.clear();
  smoothed_.clear();
}

}  // namespace cadence::sync::calibration
