// a member that has recorded but is still analysing gets its own bounded answer wait. explicit leader timestamps keep
// the old deadline, the extension and the timeout code testable without sleeping through either
#include "sync/calibration-run.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <cstdint>
#include <string>

using namespace cadence::sync;

namespace {

cadence_sync_v1_CalibrateProgress recorded(std::uint32_t id, std::uint32_t budgetMs) {
  cadence_sync_v1_CalibrateProgress progress = cadence_sync_v1_CalibrateProgress_init_zero;
  progress.id = id;
  progress.stage = cadence_sync_v1_CalibrateStage_CALIBRATE_STAGE_RECORDED;
  progress.analysis_budget_ms = budgetMs;
  return progress;
}

void answered(CalibrationCollector& collector, const std::string& member, std::uint32_t id, std::int64_t atNs) {
  collector.add(member, refusal(id, "no_capture"), atNs);
}

const CalibrationMemberOutcome& member(const CalibrationOutcome& outcome, const std::string& id) {
  const auto found = std::find_if(outcome.members.begin(), outcome.members.end(),
                                  [&](const CalibrationMemberOutcome& m) { return m.id == id; });
  REQUIRE(found != outcome.members.end());
  return *found;
}

}  // namespace

TEST_CASE("a recorded member's analysis may answer after the old three-second wait") {
  auto plan = calibration::makePlan({"leader", "slow"}, 0, calibration::kSlotSpacingNs, 1);
  CalibrationCollector collector(7, plan, {});
  REQUIRE(collector.recorded("slow", recorded(7, 12000)));
  answered(collector, "leader", 7, plan.windowToNs());
  CHECK(collector.deadlineNs() == plan.windowToNs() + 12'000'000'000);

  answered(collector, "slow", 7, plan.windowToNs() + 4'000'000'000);
  REQUIRE(collector.complete());
  const auto outcome = collector.finish();
  CHECK(member(outcome, "slow").code == "no_capture");
  CHECK(member(outcome, "slow").answeredMs == 4000);
}

TEST_CASE("a recorded member that never answers is analysis_timeout, with a budget capped at thirty seconds") {
  auto plan = calibration::makePlan({"leader", "slow"}, 0, calibration::kSlotSpacingNs, 1);
  CalibrationCollector collector(8, plan, {});
  answered(collector, "leader", 8, plan.windowToNs());
  REQUIRE(collector.recorded("slow", recorded(8, 60000)));
  CHECK(collector.deadlineNs() == plan.windowToNs() + 30'000'000'000);
  CHECK(plan.windowToNs() + 3'000'000'000 < collector.deadlineNs());

  const auto outcome = collector.finish();
  CHECK(member(outcome, "slow").code == "analysis_timeout");
  CHECK(member(outcome, "slow").answeredMs == -1);
}

TEST_CASE("a member without progress keeps the old answer deadline and no_answer code") {
  auto plan = calibration::makePlan({"leader", "old"}, 0, calibration::kSlotSpacingNs, 1);
  CalibrationCollector collector(9, plan, {});
  answered(collector, "leader", 9, plan.windowToNs() - 2'000'000'000);
  CHECK(collector.deadlineNs() == plan.windowToNs() + kCalibrationAnswerNs);
  CHECK(member(collector.finish(), "leader").answeredMs == 0);

  const auto outcome = collector.finish();
  CHECK(member(outcome, "old").code == "no_answer");
  CHECK(member(outcome, "old").answeredMs == -1);
}

TEST_CASE("progress for another run or an unknown stage does not extend a collector") {
  auto plan = calibration::makePlan({"leader", "old"}, 0, calibration::kSlotSpacingNs, 1);
  CalibrationCollector collector(10, plan, {});
  CHECK_FALSE(collector.recorded("old", recorded(11, 20000)));
  auto unknown = recorded(10, 20000);
  unknown.stage = cadence_sync_v1_CalibrateStage_CALIBRATE_STAGE_UNSPECIFIED;
  CHECK_FALSE(collector.recorded("old", unknown));
  CHECK_FALSE(collector.recorded("other", recorded(10, 20000)));
  CHECK(collector.deadlineNs() == plan.windowToNs() + kCalibrationAnswerNs);
}

TEST_CASE("a calibration start says the leader takes progress, which an older leader's never does") {
  auto plan = calibration::makePlan({"leader", "slow"}, 0, calibration::kSlotSpacingNs, 1);
  CHECK(startOf(plan, 9, 1).accepts_progress);
  cadence_sync_v1_CalibrateStart old = cadence_sync_v1_CalibrateStart_init_zero;
  CHECK_FALSE(old.accepts_progress);
}
