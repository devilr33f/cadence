// live tracking's six-cycle reducer, smoothing and bounded, server-owned corrections
#include "sync/calibration.h"

#include <catch2/catch_test_macros.hpp>

#include <cmath>
#include <string>
#include <vector>

using namespace cadence::sync::calibration;

namespace {

struct Scene {
  Plan plan;
  std::vector<std::vector<HeardV2>> heard;
};

Scene scene(double aMs, double bMs) {
  Scene out;
  out.plan = makeProbePlan({"a", "b"}, 1'000'000'000, {}, {}, kSlotSpacingNs, 1);
  out.plan.probe = ultrasonicProbe();
  const double delays[] = {aMs * 1e6, bMs * 1e6};
  out.heard.assign(2, std::vector<HeardV2>(2));
  for (std::size_t recorder = 0; recorder < 2; ++recorder) {
    for (std::size_t source = 0; source < 2; ++source) {
      auto& h = out.heard[recorder][source];
      h.code = ArrivalCodeV2::Ok;
      h.tNs = out.plan.slot(0, source) + static_cast<std::int64_t>(delays[source]);
      h.confidence = 1;
      h.spreadNs = h.sigmaNs = 10'000;
      h.ci95Ns = 20'000;
      h.rounds = 1;
      if (source != recorder) {
        h.hasRelative = true;
        h.relativeNs = delays[source] - delays[recorder];
        h.relativeSpreadNs = 10'000;
      }
    }
  }
  return out;
}

const TrackingMember* member(const TrackingReport& report, const std::string& id) {
  for (const auto& m : report.members)
    if (m.id == id)
      return &m;
  return nullptr;
}

}  // namespace

TEST_CASE("tracking window: a two ms step is smoothed, then corrected once after three windows") {
  TrackingConfig config;
  config.deadbandNs = 250'000;
  TrackingWindow tracker(config);
  auto initial = scene(20, 20);
  CHECK(tracker.add(initial.plan, initial.heard, 2'000'000'000).state == "tracking");
  CHECK(tracker.add(initial.plan, initial.heard, 3'000'000'000).state == "tracking");

  auto changed = scene(20, 22);
  tracker.add(changed.plan, changed.heard, 4'000'000'000);
  tracker.add(changed.plan, changed.heard, 5'000'000'000);
  const auto third = tracker.add(changed.plan, changed.heard, 6'000'000'000);
  REQUIRE(third.state == "tracking");
  const auto* b = member(third, "b");
  REQUIRE(b != nullptr);
  WARN("offset " << b->offsetUs << " ci " << b->ci95Us << " cycles " << b->cycles << " correction "
                << b->correctionUs);
  CHECK(std::abs(b->offsetUs - 1000) < 200);
  CHECK(b->ci95Us < 125);
  CHECK(b->correctionUs < 0);
  CHECK(b->correctionUs >= -5000);
  CHECK(b->correctionId == 1);
  CHECK(b->cycles == 5);
  CHECK(member(third, "a")->correctionUs == 0);
  CHECK(member(third, "a")->correctionId == 0);

  const auto held = tracker.add(changed.plan, changed.heard, 7'000'000'000);
  CHECK(member(held, "b")->correctionUs == 0);
  CHECK(member(held, "b")->correctionId == 1);
  tracker.add(changed.plan, changed.heard, 8'000'000'000);
  auto movedAgain = scene(20, 23);
  tracker.add(movedAgain.plan, movedAgain.heard, 9'000'000'000);
  tracker.add(movedAgain.plan, movedAgain.heard, 66'000'000'000);
  const auto next = tracker.add(movedAgain.plan, movedAgain.heard, 67'000'000'000);
  CHECK(member(next, "b")->correctionUs < 0);
  CHECK(member(next, "b")->correctionId == 2);
}

TEST_CASE("tracking window: uncertainty blocks a correction and a large offset needs calibration") {
  TrackingConfig config;
  config.deadbandNs = 250'000;
  TrackingWindow tracker(config);
  auto noisy = scene(20, 22);
  for (auto& recorder : noisy.heard)
    for (auto& h : recorder)
      h.relativeSpreadNs = h.spreadNs = h.sigmaNs = 500'000;
  for (int cycle = 0; cycle < 4; ++cycle) {
    const auto report = tracker.add(noisy.plan, noisy.heard, 2'000'000'000 + cycle * 1'000'000'000);
    CHECK(member(report, "b")->correctionUs == 0);
  }

  auto large = scene(20, 42);
  const auto report = tracker.add(large.plan, large.heard, 70'000'000'000);
  CHECK(report.state == "needs_calibration");
  CHECK(member(report, "b")->correctionUs == 0);
}
