// a fake server's streamed search, frame by frame through screen/v1's codec (as the link carries them): results
// fill rows by kind, a row never reorders under focus, a merged title group changes only its meta line, and a row
// of an earlier kind that arrives after focus went into the results joins below
#include "app/routes/core/methods.h"
#include "app/routes/search/search.h"
#include "ipc/codec.h"
#include "app/routes/tests/rig.h"

#include <catch2/catch_test_macros.hpp>

using namespace cadence::screen;
using namespace cadence::screen::routes;
namespace rpc = cadence::screen::routes::rpc;
using routes::test::Rig;

namespace {

// encoded and decoded again, as the link would: what the router gets is what crossed the wire
void deliver(Router& router, ipc::OutEnvelope& out) {
  std::vector<std::uint8_t> frame;
  REQUIRE(ipc::encodeEnvelope(out.get(), frame));
  std::string error;
  auto decoded = ipc::decodeEnvelope(frame, &error);
  REQUIRE(decoded);
  router.message(*decoded);
}

void event(Router& router, const std::string& id, std::uint32_t seq, const Json& json, bool last = false) {
  ipc::OutEnvelope out(cadence_screen_v1_Envelope_rpc_event_tag);
  ipc::copyField(out->body.rpc_event.id, id);
  out->body.rpc_event.seq = seq;
  out->body.rpc_event.json = out.keep(json.dump());
  out->body.rpc_event.last = last;
  deliver(router, out);
}

Json item(const std::string& ref, const std::string& provider, const std::string& kind, int sources = 1) {
  Json it{{"ref", ref}, {"provider", provider}, {"kind", kind}, {"title", "Название " + ref}, {"playable", kind != "title_group"},
          {"hasChildren", kind == "title_group"}};
  if (kind == "title_group") it["show"] = {{"year", 2023}, {"format", "TV"}, {"sourcesWithIt", sources}};
  return it;
}

std::vector<std::string> rowOrder(Rig& r) {
  std::vector<std::string> out;
  Rml::ElementList rows;
  r.ui().page()->QuerySelectorAll(rows, "#s-rows .row");
  for (auto* e : rows) out.push_back(e->GetId());
  return out;
}

}  // namespace

TEST_CASE("streamed search over the link fills rows without reordering the focused one") {
  fake::Script s;
  // the test streams the events itself
  s.searchTimesMs = {};
  Rig r(s);
  RouteArgs a;
  a.query = "лун";
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_SEARCH, a);
  r.h.step(100);
  const auto* call = r.server().last(rpc::kCatalogSearch);
  REQUIRE(call != nullptr);
  REQUIRE(call->subscribe);
  const auto id = call->id;
  // the fake answered with its pending sources alone; the test streams the rest
  std::uint32_t seq = 0;

  // youtube answers first: its row is the only one
  event(r.router(), id, ++seq,
        Json{{"provider", "ytmusic"}, {"items", {item("y1", "ytmusic", "video"), item("y2", "ytmusic", "video"), item("y3", "ytmusic", "video")}}});
  r.h.step(50);
  REQUIRE(r.byId("srow-youtube") != nullptr);

  // focus into the youtube row's second card
  auto* card = r.byId(catalog::idOf("s", "y2"));
  REQUIRE(card != nullptr);
  r.ui().focus().focus(card);
  r.h.step(300);
  const auto before = r.ui().focus().rect(card);
  const auto rowsBefore = rowOrder(r);

  // anime (a kind ranked above youtube) arrives now: it joins below, never above the focused row
  event(r.router(), id, ++seq,
        Json{{"provider", "torrplayer"}, {"source", "aniliberty"}, {"items", {item("t1", "torrplayer", "title_group", 2)}}});
  // more youtube: appended at the row's end
  event(r.router(), id, ++seq, Json{{"provider", "ytmusic"}, {"items", {item("y4", "ytmusic", "video")}}});
  // another source returns the same title group: merged in place, only its meta line changes
  event(r.router(), id, ++seq,
        Json{{"provider", "torrplayer"}, {"source", "anime365"}, {"updated", {item("t1", "torrplayer", "title_group", 5)}}});
  event(r.router(), id, ++seq, Json{{"done", true}}, true);
  r.h.step(300);

  REQUIRE(r.focused() == card);
  const auto after = r.ui().focus().rect(card);
  REQUIRE(after.x == before.x);
  REQUIRE(after.y == before.y);
  const auto rows = rowOrder(r);
  REQUIRE(rows.size() == 2);
  REQUIRE(rows[0] == rowsBefore[0]);
  REQUIRE(rows[1] == "srow-anime");
  // the youtube row's order: y1 y2 y3 y4
  Rml::ElementList cards;
  r.byId("srow-youtube")->QuerySelectorAll(cards, ".card");
  REQUIRE(cards.size() == 4);
  REQUIRE(cards[3]->GetAttribute<Rml::String>("data-ref", "") == "y4");
  // the merged group's second meta line names five sources
  auto* group = r.byId(catalog::idOf("s", "t1"));
  Rml::ElementList metas;
  group->QuerySelectorAll(metas, ".meta");
  REQUIRE(metas.size() == 2);
  REQUIRE(metas[1]->GetInnerRML().find("5") != std::string::npos);
  // the strip collapsed to its summary line
  REQUIRE(r.byId("s-sum") != nullptr);
}

TEST_CASE("events out of order wait for the gap; a cancelled call hears nothing more") {
  std::vector<cadence_screen_v1_Envelope> sent;
  std::vector<std::string> seen;
  RpcClient c([&](const cadence_screen_v1_Envelope& e) {
    sent.push_back(e);
    return true;
  });
  const auto id = c.call("catalog.search", Json{{"query", "x"}},
                         {[&](const std::string& status, const Json&) { seen.push_back("response:" + status); },
                          [&](const Json& j, bool last) { seen.push_back(j.value("n", "") + (last ? "!" : "")); }},
                         true);
  REQUIRE(!id.empty());
  REQUIRE(sent.back().which_body == cadence_screen_v1_Envelope_rpc_request_tag);
  REQUIRE(sent.back().body.rpc_request.subscribe);
  ipc::OutEnvelope e2(cadence_screen_v1_Envelope_rpc_event_tag);
  ipc::copyField(e2->body.rpc_event.id, id);
  e2->body.rpc_event.seq = 2;
  e2->body.rpc_event.json = e2.keep(R"({"n":"two"})");
  // an event before the response, out of order: only the sequence gap waits
  REQUIRE(c.event(e2->body.rpc_event));
  REQUIRE(seen.empty());
  ipc::OutEnvelope e1(cadence_screen_v1_Envelope_rpc_event_tag);
  ipc::copyField(e1->body.rpc_event.id, id);
  e1->body.rpc_event.seq = 1;
  e1->body.rpc_event.json = e1.keep(R"({"n":"one"})");
  c.event(e1->body.rpc_event);
  REQUIRE(seen == std::vector<std::string>{"one", "two"});
  ipc::OutEnvelope resp(cadence_screen_v1_Envelope_rpc_response_tag);
  ipc::copyField(resp->body.rpc_response.id, id);
  ipc::copyField(resp->body.rpc_response.status, "ok");
  REQUIRE(c.response(resp->body.rpc_response));
  REQUIRE(seen == std::vector<std::string>{"one", "two", "response:ok"});
  // cancelled: RpcCancel goes out, later frames are not ours
  c.cancel(id);
  REQUIRE(sent.back().which_body == cadence_screen_v1_Envelope_rpc_cancel_tag);
  e1->body.rpc_event.seq = 3;
  REQUIRE_FALSE(c.event(e1->body.rpc_event));
  // the link down: an open call ends as unavailable
  std::string status;
  c.call("catalog.item", Json{{"ref", "r"}}, {[&](const std::string& s, const Json&) { status = s; }, nullptr});
  c.failAll();
  REQUIRE(status == "unavailable");
  REQUIRE(c.open() == 0);
}

TEST_CASE("a call the server never answers ends as a timeout, and the server is told to drop it") {
  std::vector<cadence_screen_v1_Envelope> sent;
  RpcClient c([&](const cadence_screen_v1_Envelope& e) {
    sent.push_back(e);
    return true;
  });
  std::string status;
  Json answer;
  // sent before the clock's first tick: counted from that tick, not from zero
  c.call("catalog.browse", Json{{"path", "torrplayer"}}, {[&](const std::string& s, const Json& j) { status = s, answer = j; }, nullptr});
  c.expire(1'000'000);
  REQUIRE(status.empty());
  c.expire(1'000'000 + RpcClient::kNoAnswerMs - 1);
  REQUIRE(status.empty());
  c.expire(1'000'000 + RpcClient::kNoAnswerMs);
  REQUIRE(status == "unavailable");
  CHECK(answer.value("code", "") == "timeout");
  CHECK(answer.value("waitedMs", 0) == RpcClient::kNoAnswerMs);
  CHECK(sent.back().which_body == cadence_screen_v1_Envelope_rpc_cancel_tag);
  CHECK(c.open() == 0);
  // an answered subscription is the server's to end, however long it streams
  const auto id = c.call("catalog.search", Json{{"query", "x"}}, {[](const std::string&, const Json&) {}, [](const Json&, bool) {}}, true);
  ipc::OutEnvelope resp(cadence_screen_v1_Envelope_rpc_response_tag);
  ipc::copyField(resp->body.rpc_response.id, id);
  ipc::copyField(resp->body.rpc_response.status, "ok");
  REQUIRE(c.response(resp->body.rpc_response));
  c.expire(10'000'000);
  CHECK(c.open() == 1);
}

TEST_CASE("the context publisher: trailing 500 ms, one revision more each send, a flush at once") {
  std::vector<std::uint64_t> revisions;
  ContextPublisher p([&](const cadence_screen_v1_Envelope& e) {
    revisions.push_back(e.body.screen_context.revision);
    return true;
  });
  ScreenContextData d;
  d.route = cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME;
  p.set(d, 0);
  p.update(400);
  d.focusedRef = "a";
  p.set(d, 400);
  p.update(800);
  REQUIRE(revisions.empty());
  p.update(900);
  REQUIRE(revisions == std::vector<std::uint64_t>{1});
  // the same context again sends nothing
  p.set(d, 1000);
  p.update(2000);
  REQUIRE(revisions.size() == 1);
  p.flush();
  REQUIRE(revisions == std::vector<std::uint64_t>{1, 2});
  // 30 visible refs: 24 go
  for (int i = 0; i < 30; ++i) d.visibleRefs.push_back("r" + std::to_string(i));
  std::size_t count = 0;
  ContextPublisher q([&](const cadence_screen_v1_Envelope& e) {
    count = e.body.screen_context.visible_refs_count;
    return true;
  });
  q.set(d, 0);
  q.flush();
  REQUIRE(count == 24);
}
