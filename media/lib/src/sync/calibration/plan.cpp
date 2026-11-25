#include "sync/calibration/plan.h"

#include <algorithm>
#include <cmath>

namespace cadence::sync::calibration {

std::int64_t Plan::windowFromNs() const {
  if (slotNs.empty())
    return 0;
  if (probe.kind == ProbeKind::Chirp)
    return *std::min_element(slotNs.begin(), slotNs.end()) - searchBeforeNs - 50'000'000;
  auto from = std::int64_t{0};
  bool any = false;
  for (std::size_t r = 0; r < rounds; ++r)
    for (std::size_t i = 0; i < members.size(); ++i) {
      if (skipped(i))
        continue;
      const auto value = sourceWindowFromNs(r, i);
      from = any ? std::min(from, value) : value;
      any = true;
    }
  if (!any)
    return 0;
  from -= 50'000'000;
  if (hasUltrasonic())
    from = std::min(from, ultrasonicPlan().windowFromNs());
  return from;
}

std::int64_t Plan::windowToNs() const {
  if (slotNs.empty())
    return 0;
  if (probe.kind == ProbeKind::Chirp)
    return *std::max_element(slotNs.begin(), slotNs.end()) + searchAfterNs +
           static_cast<std::int64_t>(std::ceil(chirp.durationS * 1e9)) + 50'000'000;
  auto to = std::int64_t{0};
  bool any = false;
  for (std::size_t r = 0; r < rounds; ++r)
    for (std::size_t i = 0; i < members.size(); ++i) {
      if (skipped(i))
        continue;
      const auto value = sourceWindowToNs(r, i);
      to = any ? std::max(to, value) : value;
      any = true;
    }
  if (!any)
    return 0;
  to += static_cast<std::int64_t>(std::ceil(probeDuration(probe) * 1e9)) + 50'000'000;
  if (hasUltrasonic())
    to = std::max(to, ultrasonicPlan().windowToNs());
  return to;
}

Plan Plan::ultrasonicPlan() const {
  Plan out;
  if (!hasUltrasonic())
    return out;
  out.members = members;
  out.rounds = 1;
  out.slotNs = ultrasonicSlotNs;
  out.probe = *ultrasonic;
  out.probeCodes = ultrasonicCodes;
  out.priorNs = priorNs;
  out.priorSpanNs = priorSpanNs;
  out.toleranceNs = toleranceNs;
  out.sourceSkipped.resize(members.size());
  for (std::size_t i = 0; i < members.size(); ++i)
    out.sourceSkipped[i] = ultrasonicSlotNs[i] == 0 || skipped(i);
  return out;
}

namespace {

Plan layout(std::vector<std::string> members, std::int64_t startNs, std::int64_t spacingNs, std::uint32_t rounds) {
  Plan plan;
  plan.members = std::move(members);
  const auto n = std::max<std::size_t>(plan.members.size(), 1);
  plan.rounds = static_cast<std::uint32_t>(std::clamp<std::size_t>(rounds, 1, std::max<std::size_t>(kMaxSlots / n, 1)));
  for (std::size_t k = 0; k < plan.rounds * plan.members.size(); ++k) {
    plan.slotNs.push_back(startNs + static_cast<std::int64_t>(k) * spacingNs);
    plan.probeCodes.push_back(static_cast<std::uint32_t>(k % n));
  }
  return plan;
}

}  // namespace

Plan makePlan(std::vector<std::string> members, std::int64_t startNs, std::int64_t spacingNs, std::uint32_t rounds) {
  auto plan = layout(std::move(members), startNs, spacingNs, rounds);
  plan.probe = chirpProbe(plan.chirp);
  return plan;
}

Plan makeProbePlan(std::vector<std::string> members, std::int64_t startNs, std::vector<std::int64_t> priorNs,
                   std::vector<std::int64_t> priorSpanNs, std::int64_t spacingNs, std::uint32_t rounds) {
  const auto count = members.size();
  priorNs.resize(count, 0);
  priorSpanNs.resize(count, 150'000'000);
  auto widest = std::int64_t{0};
  for (auto& span : priorSpanNs) {
    span = std::max<std::int64_t>(span, 0);
    widest = std::max(widest, 2 * span + 40'000'000);
  }
  const ProbeShape probe;
  const auto needed = widest + static_cast<std::int64_t>(std::ceil(probeDuration(probe) * 1e9)) + 100'000'000;
  auto plan = layout(std::move(members), startNs, std::max(spacingNs, needed), rounds);
  plan.probe = probe;
  plan.priorNs = std::move(priorNs);
  plan.priorSpanNs = std::move(priorSpanNs);
  return plan;
}

void addUltrasonicRound(Plan& plan, std::int64_t startNs, std::int64_t spacingNs,
                        std::vector<bool> skipped) {
  if (plan.probe.kind != ProbeKind::Gold || plan.members.empty())
    return;
  auto round = makeProbePlan(plan.members, startNs, plan.priorNs, plan.priorSpanNs, spacingNs, 1);
  plan.ultrasonic = ultrasonicProbe();
  plan.ultrasonicSlotNs = std::move(round.slotNs);
  plan.ultrasonicCodes = std::move(round.probeCodes);
  skipped.resize(plan.members.size(), false);
  for (std::size_t i = 0; i < plan.members.size(); ++i)
    if (skipped[i])
      plan.ultrasonicSlotNs[i] = 0;
}

}  // namespace cadence::sync::calibration
