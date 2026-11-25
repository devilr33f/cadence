#include "app/routes/system/reel.h"

#include <catch2/catch_test_macros.hpp>

using namespace cadence::screen::routes;

TEST_CASE("reel: the pace shortens as cards wait, and hurries while the reply is spoken") {
  const auto one = reel::pace(1, false);
  const auto two = reel::pace(2, false);
  const auto three = reel::pace(3, false);
  // one waiting: the current card reads for a few seconds
  CHECK(one.dwellMs >= 2500);
  CHECK(two.dwellMs < one.dwellMs);
  CHECK(three.dwellMs < two.dwellMs);
  CHECK(two.slideMs < one.slideMs);
  CHECK(three.slideMs < two.slideMs);
  // a slide is always a slide, never a cut, and never sluggish
  for (std::size_t w = 0; w < 8; ++w) {
    for (bool h : {false, true}) {
      const auto p = reel::pace(w, h);
      CHECK(p.slideMs >= 160);
      CHECK(p.slideMs <= 450);
      CHECK(p.dwellMs >= 500);
    }
  }
  CHECK(reel::pace(1, true).dwellMs < one.dwellMs);
}

TEST_CASE("reel: the first card shows at once, the next after its dwell, the last stays") {
  reel::Reel r;
  r.push("a", 0);
  CHECK(r.current() == "a");
  CHECK(r.dueMs() == -1);
  r.push("b", 100);
  CHECK(r.current() == "a");
  CHECK_FALSE(r.tick(1000));
  CHECK(r.tick(reel::pace(1, false).dwellMs));
  CHECK(r.current() == "b");
  CHECK(r.history().front() == "a");
  // nothing waits: b stays however long
  CHECK_FALSE(r.tick(1'000'000));
  CHECK(r.current() == "b");
  // a card already held is not pushed again
  r.push("a", 2'000'000);
  CHECK(r.waiting().empty());
}

TEST_CASE("reel: a burst never leaves the reel more than a couple of cards behind") {
  reel::Reel r;
  std::int64_t now = 0;
  r.push("c0", now);
  for (int i = 1; i <= 9; ++i) {
    now += 50;
    r.push("c" + std::to_string(i), now);
    CHECK(r.waiting().size() <= reel::kMaxLag);
  }
  // the newest is among the waiting; the passed-over ones are history, behind the one really shown before
  CHECK(r.waiting().back() == "c9");
  CHECK(r.history().size() == 7);
  CHECK(r.current() == "c0");
  // two waiting: a short dwell, then one waiting: the longer one
  const auto d2 = r.dueMs() - r.shownMs();
  CHECK(d2 == reel::pace(2, false).dwellMs);
  CHECK(r.tick(r.dueMs()));
  CHECK(r.current() == "c8");
  // the card stepped back behind c8 is c0, the one that was shown, not one passed over
  CHECK(r.history().front() == "c0");
  CHECK(r.slideMs() == reel::pace(1, false).slideMs);
  CHECK(r.tick(r.dueMs()));
  CHECK(r.current() == "c9");
  CHECK(r.waiting().empty());
  // the whole burst caught up within two dwells of the last push
  CHECK(r.shownMs() - now <= reel::pace(2, false).dwellMs + reel::pace(1, false).dwellMs);
}

TEST_CASE("reel: hurry while speaking reaches the newest sooner") {
  reel::Reel calm, quick;
  for (auto* r : {&calm, &quick}) {
    r->push("a", 0);
    r->push("b", 0);
  }
  quick.hurry(true);
  CHECK(quick.dueMs() < calm.dueMs());
}

TEST_CASE("reel: a cited card comes to the front; a removed one gives way") {
  reel::Reel r;
  r.push("a", 0);
  r.push("b", 0);
  r.tick(r.dueMs());
  REQUIRE(r.current() == "b");
  CHECK(r.front("a", 10'000));
  CHECK(r.current() == "a");
  CHECK(r.history().front() == "b");
  CHECK_FALSE(r.front("nope", 10'000));
  // a cited card stays: what waited passes into the history
  r.push("c", 10'100);
  r.push("d", 10'100);
  CHECK(r.front("a", 10'200));
  CHECK(r.waiting().empty());
  CHECK_FALSE(r.tick(1'000'000));
  CHECK(r.current() == "a");
  // a removed current gives way to the card shown before it, not one passed over
  r.remove("a");
  CHECK(r.current() == "b");
  r.clear();
  CHECK(r.empty());
}
