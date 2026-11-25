#include "ui/cards/timeline.h"

#include <catch2/catch_test_macros.hpp>

using namespace cadence::screen::ui::cards;

TEST_CASE("a timeline's time: m:ss, h:mm:ss past an hour, value over max") {
  CHECK(clockText(0) == "0:00");
  CHECK(clockText(83) == "1:23");
  CHECK(clockText(83.9) == "1:23");
  CHECK(clockText(599) == "9:59");
  CHECK(clockText(3599) == "59:59");
  CHECK(clockText(3600) == "1:00:00");
  CHECK(clockText(3723) == "1:02:03");
  CHECK(clockText(-5) == "0:00");
  CHECK(timelineText(83, 225) == "1:23 / 3:45");
  CHECK(timelineText(61, 3723) == "1:01 / 1:02:03");
}

TEST_CASE("a timeline runs from its anchor at its rate, within its bounds") {
  Timeline t;
  t.value = 83;
  t.max = 225;
  t.anchorAtMs = 1'000'000;
  t.rate = 1;
  CHECK(t.at(1'000'000) == 83);
  CHECK(t.at(1'002'500) == 85.5);
  CHECK(t.running(1'002'500));
  // a clock behind the anchor never runs it backwards
  CHECK(t.at(999'000) == 83);
  // past its end it stands at max and runs no more
  CHECK(t.at(1'000'000 + 200'000) == 225);
  CHECK_FALSE(t.running(1'000'000 + 200'000));
  // twice the speed
  t.rate = 2;
  CHECK(t.at(1'001'000) == 85);
}

TEST_CASE("a paused timeline, or one with no anchor, stands") {
  Timeline t;
  t.value = 61;
  t.max = 215;
  t.anchorAtMs = 1'000'000;
  t.rate = 0;
  CHECK(t.at(1'060'000) == 61);
  CHECK_FALSE(t.running(1'060'000));
  t.rate = 1;
  t.anchorAtMs = 0;
  CHECK(t.at(1'060'000) == 61);
  CHECK_FALSE(t.running(1'060'000));
  // a value past max (a session a moment past its length) is drawn at max
  t.value = 230;
  CHECK(t.at(0) == 215);
}

TEST_CASE("moved by the remote it stops at the choice; a refusal sends it running again") {
  Timeline t;
  t.value = 83;
  t.max = 225;
  t.anchorAtMs = 1'000'000;
  t.rate = 1;
  t.hold(150);
  CHECK(t.at(1'000'000) == 150);
  CHECK(t.at(1'030'000) == 150);
  CHECK_FALSE(t.running(1'030'000));
  t.hold(400);
  CHECK(t.at(1'030'000) == 225);
  t.hold(-10);
  CHECK(t.at(1'030'000) == 0);
  t.release();
  CHECK(t.at(1'030'000) == 113);
  CHECK(t.running(1'030'000));
}
