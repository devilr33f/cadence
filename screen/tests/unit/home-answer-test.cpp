#include "app/routes/home/answer.h"

#include <catch2/catch_test_macros.hpp>

using namespace cadence::screen::routes::home_answer;

TEST_CASE("a feed that does not exist for the household is no row, silently") {
  // ytmusic cannot browse: never asked again
  CHECK(outcomeOf("invalid", "unsupported") == Outcome::HideForGood);
  CHECK(outcomeOf("unknown_method", "") == Outcome::HideForGood);
  // a call the server finds wrong would only be refused again
  CHECK(outcomeOf("invalid", "invalid") == Outcome::HideForGood);
  // a provider not configured, a feed its instance lacks (torrplayer without the schedule endpoint)
  CHECK(outcomeOf("not_found", "not_found") == Outcome::Hide);
  // a ref the server forgot (its restart): the slow re-check renews it
  CHECK(outcomeOf("expired", "unknown_ref") == Outcome::Hide);
}

TEST_CASE("a failure the viewer can act on keeps the inline error row") {
  CHECK(outcomeOf("unavailable", "unavailable") == Outcome::Error);  // a timeout, the upstream's 5xx, the network
  CHECK(outcomeOf("refused", "sign_in") == Outcome::Error);          // auth
  CHECK(outcomeOf("internal", "internal") == Outcome::Error);
  CHECK(outcomeOf("timeout", "") == Outcome::Error);
  CHECK(outcomeOf("ok", "") == Outcome::Rows);
}
