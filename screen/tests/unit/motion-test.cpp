#include "app/routes/home/feed.h"
#include "app/routes/home/hero.h"
#include "ui/core/motion.h"

#include <catch2/catch_test_macros.hpp>

using namespace cadence::screen;

TEST_CASE("the eases run 0 to 1, symmetric in and out, clamped") {
  CHECK(ui::motion::easeInOut(0) == 0);
  CHECK(ui::motion::easeInOut(1) == 1);
  CHECK(ui::motion::easeInOut(0.5) == 0.5);
  CHECK(ui::motion::easeInOut(-1) == 0);
  CHECK(ui::motion::easeInOut(2) == 1);
  // slow out of the start and into the end
  CHECK(ui::motion::easeInOut(0.1) < 0.1);
  CHECK(ui::motion::easeInOut(0.9) > 0.9);
  CHECK(ui::motion::easeOut(0.5) > 0.5);
  double last = -1;
  for (int i = 0; i <= 100; ++i) {
    const double v = ui::motion::easeInOut(i / 100.0);
    CHECK(v >= last);
    last = v;
  }
}

TEST_CASE("a cross-fade's outgoing frame eases from whole to gone over its time") {
  ui::motion::Fade f;
  CHECK_FALSE(f.running(0));
  CHECK(f.opacity(0) == 0);
  f.start(1000, 220);
  CHECK(f.running(1000));
  CHECK(f.opacity(1000) == 1);
  CHECK(f.opacity(1110) == 0.5);
  CHECK(f.opacity(1050) > 0.9);
  CHECK(f.opacity(1200) < 0.1);
  CHECK(f.running(1219));
  CHECK_FALSE(f.running(1220));
  CHECK(f.opacity(1220) == 0);
  CHECK(f.opacity(5000) == 0);
  f.stop();
  CHECK_FALSE(f.running(1100));
  // a zero duration still ends
  f.start(0, 0);
  CHECK_FALSE(f.running(1));
}

TEST_CASE("the feed puts every focused row's top at the anchor under the hero") {
  CHECK(routes::feed::scrollFor(800, 500) == 300);
  for (const float top : {700.0f, 1300.0f, 2400.0f}) CHECK(top - routes::feed::scrollFor(top, 500) == 500);
  // the first rows never pull the column down past its top
  CHECK(routes::feed::scrollFor(400, 500) == 0);
}

TEST_CASE("rows above the focused one step aside, the rest show") {
  using routes::feed::hiddenAbove;
  CHECK(hiddenAbove(0, 2));
  CHECK(hiddenAbove(1, 2));
  CHECK_FALSE(hiddenAbove(2, 2));
  CHECK_FALSE(hiddenAbove(3, 2));
  // focus outside the feed (the hero): every row shows
  CHECK_FALSE(hiddenAbove(0, -1));
}

TEST_CASE("the hero: a focused card's item in any row, else the film behind, else the buttons' item") {
  using routes::hero::Show;
  using routes::hero::Source;
  using routes::hero::showFor;
  // start: the item to continue
  CHECK((showFor("", "", "cont") == Show{Source::Default, "cont"}));
  // a card in the first row, the third, any: that card
  CHECK((showFor("card-1", "", "cont") == Show{Source::Card, "card-1"}));
  CHECK((showFor("card-30", "", "cont") == Show{Source::Card, "card-30"}));
  // back on the hero's buttons (no card): the item they act on again, never the last card
  CHECK((showFor("", "", "cont") == Show{Source::Default, "cont"}));
  // a film playing on behind: the hero offers it while no card holds focus, a card still wins
  CHECK((showFor("", "film", "cont") == Show{Source::Play, "film"}));
  CHECK((showFor("card-1", "film", "cont") == Show{Source::Card, "card-1"}));
  // nothing loaded yet: the placeholder
  CHECK(showFor("", "", "") == Show{});
}
