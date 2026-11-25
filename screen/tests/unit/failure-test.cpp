#include "app/routes/core/failure.h"

#include <catch2/catch_test_macros.hpp>

using namespace cadence::screen::routes;
using nlohmann::json;

TEST_CASE("a refusal shows what failed as the server said it") {
  const auto c = failure::of("catalog.browse", "unavailable",
                             json{{"code", "unavailable"}, {"failure", "failed"}, {"provider", "torrplayer"}, {"source", "anime365"},
                                  {"httpStatus", 502}},
                             1200);
  CHECK(failure::sourceLine(c) == "torrplayer · anime365");
  CHECK(failure::reasonLine(c) == "failed · HTTP 502");
  CHECK(failure::detailLine(c) == "catalog.browse → unavailable/failed");
}

TEST_CASE("a timeout says how long the screen waited") {
  const auto c = failure::of("catalog.item", "unavailable", json{{"code", "unavailable"}, {"failure", "timeout"}}, 10200);
  CHECK(failure::reasonLine(c) == "timeout · 10.2 s");
  // the route's own provider when the answer named none
  CHECK(failure::sourceLine(c, "torrserver") == "torrserver");
  // a local timeout (no answer at all) is the status alone
  CHECK(failure::reasonLine(failure::of("media.panel", "timeout", json::object(), 8000)) == "timeout · 8.0 s");
  // the wait unknown: no time
  CHECK(failure::reasonLine(failure::of("catalog.item", "unavailable", json{{"failure", "timeout"}}, -1)) == "timeout");
}

TEST_CASE("a resolve stream's refusal reads its status and its refused body") {
  const auto c = failure::of("media.play", "", json{{"status", "unavailable"}, {"refused", {{"code", "unavailable"}, {"failure", "no_seeders"}}}}, 500);
  CHECK(c.status == "unavailable");
  CHECK(failure::reasonLine(c) == "no_seeders");
  CHECK(failure::detailLine(c) == "media.play → unavailable/no_seeders");
}

TEST_CASE("only codes reach the screen (rule 8): anything else is dropped") {
  const auto c = failure::of("catalog.item", "unavailable",
                             json{{"code", "http://host/x?token=s3cret"}, {"failure", "Connection refused"}, {"provider", "torrplayer"},
                                  {"source", std::string(60, 'a')}, {"httpStatus", 99999}},
                             0);
  CHECK(c.code.empty());
  CHECK(c.failure.empty());
  CHECK(c.source.empty());
  CHECK(c.httpStatus == 0);
  CHECK(failure::reasonLine(c) == "unavailable");
  CHECK(failure::token("no_link") == "no_link");
  CHECK(failure::token("Не") == "");
}

namespace {

failure::Call call(std::string failure, int http = 0, std::string status = "unavailable", std::int64_t waited = -1) {
  failure::Call c;
  c.method = "catalog.item";
  c.status = std::move(status);
  c.code = "unavailable";
  c.failure = std::move(failure);
  c.httpStatus = http;
  c.waitedMs = waited;
  c.provider = "torrplayer";
  return c;
}

}  // namespace

TEST_CASE("plain words, table-driven: the reason and the next step") {
  using failure::Words;
  using failure::wordsFor;
  CHECK((wordsFor(call("timeout", 0, "unavailable", 10200)) ==
         Words{"[[error.reason.timeout|seconds=10]]", "[[error.hint.running|provider=torrplayer]]"}));
  CHECK((wordsFor(call("timeout")) == Words{"[[error.reason.timeout_unknown]]", "[[error.hint.running|provider=torrplayer]]"}));
  // a wait under a second still says one
  CHECK(wordsFor(call("timeout", 0, "unavailable", 300)).reason == "[[error.reason.timeout|seconds=1]]");
  CHECK((wordsFor(call("auth", 401)) == Words{"[[error.reason.auth]]", "[[error.hint.token]]"}));
  CHECK((wordsFor(call("failed", 403)) == Words{"[[error.reason.auth]]", "[[error.hint.token]]"}));
  CHECK((wordsFor(call("", 0, "not_found")) == Words{"[[error.reason.not_found]]", "[[error.hint.other_source]]"}));
  CHECK((wordsFor(call("failed", 404)) == Words{"[[error.reason.not_found]]", "[[error.hint.other_source]]"}));
  CHECK((wordsFor(call("failed", 502)) == Words{"[[error.reason.server|status=502]]", ""}));
  CHECK((wordsFor(call("no_seeders")) == Words{"[[error.reason.no_seeders]]", "[[error.hint.other_release]]"}));
  CHECK((wordsFor(call("unavailable")) == Words{"[[error.reason.unavailable]]", "[[error.hint.running|provider=torrplayer]]"}));
  CHECK((wordsFor(call("rate_limited", 429)) == Words{"[[error.reason.rate_limited]]", "[[error.hint.wait]]"}));
  CHECK((wordsFor(call("no_link")) == Words{"[[error.reason.no_link]]", "[[error.hint.network]]"}));
  // a provider from the route when the answer named none; none at all: no «запущен» hint
  auto anon = call("timeout", 0, "unavailable", 3000);
  anon.provider.clear();
  CHECK(wordsFor(anon, "torrserver").hint == "[[error.hint.running|provider=torrserver]]");
  CHECK(wordsFor(anon).hint.empty());
  // a code no rule knows: no words, the card shows the code alone
  failure::Call odd;
  odd.method = "catalog.act";
  odd.status = "invalid";
  odd.code = "invalid";
  CHECK((wordsFor(odd) == Words{}));
}

TEST_CASE("a player failure suggests another release only for a torrent source") {
  using namespace cadence::screen::routes::failure;
  Call c;
  c.method = "player";
  c.status = "unplayable";
  c.code = "unplayable";
  c.failure = "network";
  c.provider = "torrserver";
  CHECK((wordsFor(c) == Words{"[[error.reason.player]]", "[[error.hint.other_release]]"}));
  // youtube music has no releases or voiceovers to pick
  c.provider = "yt";
  CHECK((wordsFor(c) == Words{"[[error.reason.player]]", ""}));
  c.provider.clear();
  CHECK((wordsFor(c, "yt-dlp") == Words{"[[error.reason.player]]", ""}));
  CHECK((wordsFor(c, "torrplayer") == Words{"[[error.reason.player]]", "[[error.hint.other_release]]"}));
}
