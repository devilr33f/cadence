// the tap's GroupClock from a playout's heard frame (multiroom P4): it runs on from the frame's due time while the
// frame is recent, and stands at its end once nothing newer is heard
#include "audiod/group-clock.h"

#include <catch2/catch_test_macros.hpp>

using namespace cadence;
using namespace cadence::audiod;

TEST_CASE("group clock: none before a frame is heard, running on from the frame heard last, stopped after it") {
  CHECK_FALSE(groupClockAt(std::nullopt, 1'000));
  sync::PlacedFrame f;
  f.epoch = 3;
  f.mediaMs = 61'000;
  f.dueMonoNs = 5'000'000'000;
  // a frame due later is not heard yet
  CHECK_FALSE(groupClockAt(f, f.dueMonoNs - 1));
  const auto running = groupClockAt(f, f.dueMonoNs + 12'000'000);
  REQUIRE(running);
  CHECK(running->playing);
  CHECK(running->epoch == 3);
  CHECK(running->mediaNs == 61'012'000'000);
  CHECK(running->monoNs == f.dueMonoNs + 12'000'000);
  // a lost frame or two after it still runs on
  CHECK(groupClockAt(f, f.dueMonoNs + kGroupClockRunOnNs)->playing);
  const auto stopped = groupClockAt(f, f.dueMonoNs + kGroupClockRunOnNs + 1);
  REQUIRE(stopped);
  CHECK_FALSE(stopped->playing);
  CHECK(stopped->mediaNs == 61'020'000'000);
}
