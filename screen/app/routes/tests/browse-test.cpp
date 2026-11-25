// i1's routes on the fake catalog: the focus walk of design r2 section 18.7 by the d-pad, the picker skipped with a
// remembered voiceover, resolving's BACK and failure, the episodes grid's focus, home's providers, the screen context
#include "app/routes/core/methods.h"
#include "app/routes/resolving/resolving.h"
#include "app/routes/search/search.h"
#include "app/routes/tests/rig.h"
#include "ui/core/focus.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>

using namespace cadence::screen;
using namespace cadence::screen::routes;
namespace rpc = cadence::screen::routes::rpc;
using routes::test::Rig;

namespace {

constexpr auto kUp = cadence_screen_v1_Key_KEY_UP;
constexpr auto kDown = cadence_screen_v1_Key_KEY_DOWN;
constexpr auto kLeft = cadence_screen_v1_Key_KEY_LEFT;
constexpr auto kRight = cadence_screen_v1_Key_KEY_RIGHT;

bool inside(Rml::Element* e, const std::string& id) {
  for (auto* p = e; p != nullptr; p = p->GetParentNode()) {
    if (p->GetId() == id) return true;
  }
  return false;
}

class Background final : public Service {
 public:
  void attach(Router&, ui::Ui&, app::Outbox&) override {}
  [[nodiscard]] std::optional<BackgroundPlay> background() const override { return play; }
  bool foreground() override {
    if (!play) return false;
    foregrounds++;
    return true;
  }

  std::optional<BackgroundPlay> play;
  int foregrounds = 0;
};

}  // namespace

TEST_CASE("home: background audio has a focused mini-player; video keeps its existing hero") {
  Rig r;
  auto service = std::make_unique<Background>();
  auto* background = service.get();
  background->play = BackgroundPlay{"music-1", "ref:track:1", "Северное сияние", "Полярные огни",
                                    "img:fakeart00000000000000042", true, false};
  r.router().addService(std::move(service));
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  r.h.step(600);

  auto* chip = r.byId("home-now-playing");
  REQUIRE(chip != nullptr);
  CHECK(r.focusedId() == "home-now-playing");
  CHECK(chip->GetInnerRML().find("Северное сияние") != std::string::npos);
  CHECK(chip->GetInnerRML().find("Полярные огни") != std::string::npos);
  CHECK(chip->QuerySelector(".hnp-img") != nullptr);
  CHECK_FALSE(chip->IsClassSet("paused"));
  r.ok();
  CHECK(background->foregrounds == 1);

  // a sender's pause and a new track redraw the chip, never the hero or its focus
  const auto hero = r.byId("home-hero")->GetInnerRML();
  background->play->paused = true;
  background->play->title = "Следующая песня";
  r.h.step(100);
  chip = r.byId("home-now-playing");
  REQUIRE(chip != nullptr);
  CHECK(chip->IsClassSet("paused"));
  CHECK(r.focusedId() == "home-now-playing");
  CHECK(r.byId("home-hero")->GetInnerRML() == hero);

  background->play.reset();
  r.h.step(100);
  CHECK(r.byId("home-now-playing") == nullptr);

  background->play = BackgroundPlay{"video-1", "ref:film:1", "Тихая гавань", "", "", false, false};
  r.h.step(100);
  CHECK(r.byId("home-now-playing") == nullptr);
  CHECK(r.focusedId() == "hero-play");
  CHECK(r.ui().page()->IsClassSet("over-video"));
  r.ok();
  CHECK(background->foregrounds == 2);
}

TEST_CASE("the focus walk: home → search → title → picker → resolving → playing, by the d-pad") {
  Rig r;
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  r.h.step(600);
  // 1. home, focus on the hero's «Продолжить»; ↑ to the top bar's search; OK
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  REQUIRE(r.focusedId() == "hero-play");
  r.press(kUp);
  REQUIRE(r.focusedId() == "home-search");
  r.ok();
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_SEARCH);
  r.h.step(100);
  // 2. the keyboard, focus on «а»; type «мон»: → ×6 to «ё», ↓ to «м», OK; ↓ to «у», ← ×5 to «о», OK; ← to «н», OK
  REQUIRE(r.focusedAction() == "key:а");
  r.press(kRight, 6);
  r.press(kDown);
  REQUIRE(r.focusedAction() == "key:м");
  r.ok();
  r.press(kDown);
  r.press(kLeft, 5);
  REQUIRE(r.focusedAction() == "key:о");
  r.ok();
  // results start at the second letter
  r.h.step(400);
  REQUIRE(r.server().last(rpc::kCatalogSearch) != nullptr);
  REQUIRE(r.server().last(rpc::kCatalogSearch)->args["query"] == "мо");
  r.press(kLeft);
  REQUIRE(r.focusedAction() == "key:н");
  r.ok();
  r.h.step(1500);
  const auto* last = r.server().last(rpc::kCatalogSearch);
  REQUIRE(last->args["query"] == "мон");
  REQUIRE(last->subscribe);
  // the earlier search was cancelled when the query grew
  REQUIRE(!r.server().cancels.empty());
  // 3. → out of the keyboard's right column into the results: the keyboard collapses, focus on the first title group
  r.press(kRight, 6);
  REQUIRE(r.focusedAction() == "key:у");
  r.press(kRight);
  r.h.step(300);
  REQUIRE(inside(r.focused(), "srow-anime"));
  REQUIRE(r.byId("s-root")->IsClassSet("collapsed"));
  REQUIRE(r.focused()->GetAttribute<Rml::String>("data-ref", "") == "ref-tg-1");
  // OK: the ghost hand-off (400 ms), then the title page
  r.ok();
  r.h.step(900);
  // 4. the title page, focus on its primary button; first time (no continue point here: «Продолжить: серия 13»)
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_TITLE);
  REQUIRE(r.focusedId() == "t-play");
  // 5. OK opens the picker (nothing remembered), focus on the default row's best quality
  r.ok();
  r.h.step(300);
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_PICKER);
  REQUIRE(r.focusedAction() == "q:vo-anilibria|1080");
  // 6. ↓ to «AniDUB · ОЗВУЧКА», its 1080 (nearest in x); OK
  r.press(kDown);
  REQUIRE(r.focusedAction() == "q:vo-anidub|1080");
  r.ok();
  r.h.step(200);
  // 7. resolving: the steps tick; BACK returns to 6 with focus restored on the chip
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_RESOLVING);
  const auto* resolve = r.server().last(rpc::kMediaPlay);
  REQUIRE(resolve != nullptr);
  REQUIRE(resolve->args["choices"]["voiceover"] == "vo-anidub");
  REQUIRE(resolve->args["choices"]["quality"] == "1080");
  REQUIRE(resolve->args["episode"] == "13");
  const auto resolveId = resolve->id;
  r.back();
  r.h.step(100);
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_PICKER);
  REQUIRE(std::find(r.server().cancels.begin(), r.server().cancels.end(), resolveId) != r.server().cancels.end());
  REQUIRE(r.focusedAction() == "q:vo-anidub|1080");
  // OK again: this time it plays
  r.ok();
  r.h.step(1200);
  // 8. playing: the server played it (media.play), and cadenced's MediaPlay names the session
  REQUIRE(r.server().plays.size() == 1);
  const auto& play = r.server().plays.front();
  REQUIRE(play.url == "http://fake.invalid/play/13");
  REQUIRE(play.itemRef == "ref-v-aniliberty");
  REQUIRE(play.episode == "13");
  REQUIRE(play.startMs == 600000);
  REQUIRE(play.subtitles == 1);
  REQUIRE(play.skips == 1);
  // the player reports the session: resolving gives the screen back (no player route registered in i1)
  player::PlayerState st;
  st.sessionId = play.sessionId;
  st.state = cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING;
  r.router().playerState(st);
  // the picture is up (no player here waits): the hand-over's motion, then the screen goes back
  r.h.step(100);
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_RESOLVING);
  r.h.step(800);
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_PICKER);
}

TEST_CASE("a remembered voiceover skips the picker: OK on «Продолжить» plays straight away") {
  fake::Script s;
  s.remembered = true;
  Rig r(s);
  RouteArgs a;
  a.ref = "ref-tg-1";
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_TITLE, a);
  r.h.step(400);
  REQUIRE(r.focusedId() == "t-play");
  r.ok();
  r.h.step(100);
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_RESOLVING);
  REQUIRE(r.server().last(rpc::kMediaPlay)->args["choices"]["voiceover"] == "vo-anilibria");
  // BACK from resolving returns to the title page with focus on the button that started it
  r.back();
  r.h.step(100);
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_TITLE);
  REQUIRE(r.focusedId() == "t-play");
}

TEST_CASE("a failed resolve says why and offers another voiceover or source, focused") {
  fake::Script s;
  s.resolve = "no_seeders";
  Rig r(s);
  RouteArgs a;
  a.ref = "ref-v-aniliberty";
  a.episode = "13";
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_PICKER, a);
  r.h.step(300);
  for (int i = 0; i < 7 && !r.focusedId().starts_with("pt-"); ++i) {
    r.press(kDown);
    UNSCOPED_INFO(r.focusedId() << " " << r.focusedAction());
  }
  REQUIRE(r.focusedId().starts_with("pt-"));
  r.ok();
  r.h.step(1200);
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_RESOLVING);
  REQUIRE(r.focusedId() == "r-other");
  REQUIRE(r.byId("r-retry") != nullptr);
  REQUIRE(r.byId("r-back") != nullptr);
  // the torrent's resolve went with the torrent's choice
  REQUIRE(r.server().last(rpc::kMediaPlay)->args["choices"].contains("torrent"));
  r.ok();
  r.h.step(100);
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_PICKER);
}

TEST_CASE("episodes: focus starts on the next unwatched, ranges over 50, a fractional ordinal between its neighbours") {
  fake::Script s;
  s.episodes = 120;
  Rig r(s);
  RouteArgs a;
  a.ref = "ref-v-aniliberty";
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_EPISODES, a);
  r.h.step(400);
  REQUIRE(r.focusedAction() == "ep:13");
  Rml::ElementList chips;
  r.ui().page()->QuerySelectorAll(chips, ".ranges .chip");
  REQUIRE(chips.size() == 3);
  // 5.5 sits between 5 and 6, its label as given
  Rml::ElementList cards;
  r.ui().page()->QuerySelectorAll(cards, ".epgrid .card");
  std::vector<std::string> order;
  for (auto* c : cards) order.push_back(c->GetAttribute<Rml::String>("data-action", ""));
  const auto five = std::find(order.begin(), order.end(), "ep:5");
  REQUIRE(five != order.end());
  REQUIRE(*(five + 1) == "ep:5.5");
  REQUIRE(*(five + 2) == "ep:6");
  // a watched episode is dimmed with its word
  REQUIRE(r.byId(catalog::idOf("ep", "3"))->IsClassSet("watched"));
  // the second range, then back: the first remembers its focus
  r.ui().focus().focus(chips[1]);
  r.ok();
  r.h.step(100);
  REQUIRE(r.byId(catalog::idOf("ep", "13")) == nullptr);
  REQUIRE(r.byId(catalog::idOf("ep", "60")) != nullptr);
}

TEST_CASE("home: providers load on their own, a down one is a strip with retry, an unconfigured one is left out") {
  Rig r;
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  // before any answer: each provider's header with a skeleton
  r.h.step(20);
  REQUIRE(r.ui().page()->QuerySelector(".skel") != nullptr);
  r.h.step(600);
  REQUIRE(r.byId("prov-torrserver")->QuerySelector(".card.has-ref") != nullptr);
  REQUIRE(r.byId("prov-torrplayer")->QuerySelector(".card.has-ref") != nullptr);
  REQUIRE(r.byId("prov-ytmusic")->QuerySelector(".row.down") != nullptr);
  REQUIRE(r.byId("prov-spotify")->GetNumChildren() == 0);
  // the hero shows the item to resume
  REQUIRE(r.byId("hero-play")->GetAttribute<Rml::String>("data-action", "") == "hero:play");
  // ↓ into the rows collapses the hero; BACK there returns to it
  r.press(kDown);
  REQUIRE(ui::Focus::ancestor(r.focused(), "row") != nullptr);
  REQUIRE(r.ui().page()->QuerySelector(".hero")->IsClassSet("collapsed"));
  r.back();
  REQUIRE(r.focusedId() == "hero-play");
  // BACK on the hero does nothing (home is the root)
  r.back();
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  REQUIRE(r.focusedId() == "hero-play");
}

TEST_CASE("the screen context: debounced 500 ms, refs only, at most 24 visible, sent at a turn's start") {
  Rig r;
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  r.h.step(300);
  const auto before = r.server().contexts.size();
  // focus moves in a burst: one context half a second after the last
  r.press(kDown);
  r.press(kRight);
  r.press(kRight);
  r.h.step(200);
  REQUIRE(r.server().contexts.size() == before);
  r.h.step(500);
  REQUIRE(r.server().contexts.size() == before + 1);
  const auto c = r.server().contexts.back();
  REQUIRE(c.route == cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  REQUIRE(std::string(c.focused_ref).starts_with("ref-"));
  REQUIRE(c.visible_refs_count > 0);
  REQUIRE(c.visible_refs_count <= 24);
  for (pb_size_t i = 0; i < c.visible_refs_count; ++i) REQUIRE(std::string(c.visible_refs[i]).starts_with("ref-"));
  // a turn starts: the context goes at once, its revision one higher
  ipc::OutEnvelope turn(cadence_screen_v1_Envelope_assistant_turn_tag);
  ipc::copyField(turn->body.assistant_turn.turn_id, "turn-1");
  r.router().message(turn.get());
  REQUIRE(r.server().contexts.size() == before + 2);
  REQUIRE(r.server().contexts.back().revision == c.revision + 1);
  // search adds its query; the title page its ref and the selection the next play would take
  RouteArgs q;
  q.query = "лун";
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_SEARCH, q);
  r.h.step(2000);
  REQUIRE(r.server().contexts.back().route == cadence_v2_ScreenRoute_SCREEN_ROUTE_SEARCH);
  REQUIRE(r.server().contextQueries.back() == "лун");
  RouteArgs t;
  t.ref = "ref-tg-1";
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_TITLE, t);
  r.h.step(800);
  const auto& tc = r.server().contexts.back();
  REQUIRE(tc.route == cadence_v2_ScreenRoute_SCREEN_ROUTE_TITLE);
  REQUIRE(std::string(tc.page_ref) == "ref-tg-1");
  REQUIRE(tc.has_selection);
  REQUIRE(std::string(tc.selection.source) == "aniliberty");
  REQUIRE(std::string(tc.selection.voiceover) == "vo-anilibria");
  REQUIRE(std::string(tc.selection.episode) == "13");
}

TEST_CASE("cadenced's Route opens a route with its query, BACK pops, home pops to the root") {
  Rig r;
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  r.h.step(200);
  ipc::OutEnvelope m(cadence_screen_v1_Envelope_route_tag);
  m->body.route.route = cadence_v2_ScreenRoute_SCREEN_ROUTE_SEARCH;
  m->body.route.query = m.keep("лунный");
  r.router().message(m.get());
  r.h.step(1300);
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_SEARCH);
  // «найди …» opens search with the query filled in and results already streaming
  REQUIRE(r.server().last(rpc::kCatalogSearch)->args["query"] == "лунный");
  ipc::OutEnvelope home(cadence_screen_v1_Envelope_route_tag);
  home->body.route.route = cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME;
  r.router().message(home.get());
  r.h.step(100);
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  REQUIRE(r.router().depth() == 1);
}

TEST_CASE("resolving: the play is the server's (media.play), and the player's report of its session hands over") {
  fake::Script s;
  s.remembered = true;
  Rig r(s);
  RouteArgs a;
  a.ref = "ref-tg-1";
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_TITLE, a);
  r.h.step(400);
  r.ok();
  r.h.step(1200);
  // nothing resolved or played on the screen itself: media.play went, and cadenced's MediaPlay came back
  REQUIRE(r.server().last(rpc::kCatalogResolve) == nullptr);
  REQUIRE(r.server().last(rpc::kMediaPlay) != nullptr);
  REQUIRE(r.server().plays.size() == 1);
  REQUIRE(r.server().plays.front().sessionId == "cad-1");
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_RESOLVING);
  // another session (what played before) does not hand over; the one media.play named does
  player::PlayerState other;
  other.sessionId = "music-1";
  other.state = cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING;
  r.router().playerState(other);
  r.h.step(100);
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_RESOLVING);
  player::PlayerState st;
  st.sessionId = "cad-1";
  st.state = cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING;
  r.router().playerState(st);
  r.h.step(800);
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_TITLE);
  // the encoded MediaPlay stays within screen/v1's bounds
  ipc::OutEnvelope out(cadence_screen_v1_Envelope_media_play_tag);
  REQUIRE(fake::playEnvelope(fake::playable("ref-v-1", "13"), "cad-9", out));
  std::vector<std::uint8_t> frame;
  REQUIRE(ipc::encodeEnvelope(out.get(), frame));
}

TEST_CASE("resolving: every YouTube stage has words, a pick and a live availability countdown") {
  fake::Script s;
  s.remembered = true;
  // media.play stays unanswered: stages come while the server resolves, and its answer ends them
  s.resolve = "hang";
  Rig r(s);
  RouteArgs a;
  a.ref = "ref-tg-1";
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_TITLE, a);
  r.h.step(400);
  r.ok();
  r.h.step(1200);
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_RESOLVING);
  auto* route = dynamic_cast<ResolvingRoute*>(r.router().top());
  REQUIRE(route != nullptr);
  route->event(Json{{"stage", "cookies"}, {"elapsedMs", 5}});
  route->event(Json{{"stage", "extract"}, {"elapsedMs", 40}});
  route->event(Json{{"stage", "challenge"}, {"elapsedMs", 120}});
  route->event(Json{{"stage", "formats"}, {"elapsedMs", 6400}});
  route->event(Json{{"stage", "pick"}, {"elapsedMs", 6450}, {"height", 1080}, {"vcodec", "h264"}, {"audioTracks", 1}});
  route->event(Json{{"stage", "wait_available"}, {"elapsedMs", 6500}, {"remainingMs", 2000}});
  r.h.step(100);
  Rml::ElementList steps;
  r.byId("r-steps")->QuerySelectorAll(steps, ".rstep");
  REQUIRE(steps.size() == 6);
  std::string text;
  for (auto* step : steps) text += step->GetInnerRML();
  for (const auto& word : {"Готовлю сессию", "Спрашиваю YouTube", "Решаю задачу YouTube", "Читаю форматы", "1080p", "h264"}) {
    CHECK(text.find(word) != std::string::npos);
  }
  CHECK(text.find("со звуковой дорожкой") != std::string::npos);
  REQUIRE(r.byId("r-wait") != nullptr);
  CHECK(r.byId("r-wait")->GetInnerRML().find("2 с") != std::string::npos);
  r.h.step(1100);
  CHECK(r.byId("r-wait")->GetInnerRML().find("1 с") != std::string::npos);
  route->event(Json{{"stage", "relay"}, {"elapsedMs", 8500}});
  r.h.step(50);
  CHECK(r.byId("r-steps")->GetInnerRML().find("Загружаю поток") != std::string::npos);
  CHECK(r.byId("r-steps")->GetInnerRML().find("wait_available") == std::string::npos);
}

TEST_CASE("episodes opened from the title page with its episode: focus on it after everything settled") {
  fake::Script s;
  s.episodes = 120;
  Rig r(s);
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  r.h.step(300);
  RouteArgs t;
  t.ref = "ref-tg-1";
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_TITLE, t);
  r.h.step(200);
  RouteArgs a;
  a.ref = "ref-v-aniliberty";
  a.episode = "13";
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_EPISODES, a);
  r.h.step(100);
  UNSCOPED_INFO("at 100: " << r.focusedAction());
  r.h.step(1400);
  REQUIRE(r.focusedAction() == "ep:13");
}

TEST_CASE("a provider that was down is asked again once cadenced or the server is back") {
  fake::Script s;
  s.down = {"torrplayer"};
  Rig r(s);
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  r.h.step(400);
  REQUIRE(r.byId("prov-torrplayer")->QuerySelector(".row.down") != nullptr);
  r.server().script.down.clear();
  ipc::OutEnvelope online(cadence_screen_v1_Envelope_link_status_tag);
  online->body.link_status.state = cadence_screen_v1_LinkState_LINK_STATE_ONLINE;
  r.router().message(online.get());
  r.h.step(400);
  REQUIRE(r.byId("prov-torrplayer")->QuerySelector(".card.has-ref") != nullptr);
}

namespace {

std::size_t browses(Rig& r) {
  std::size_t n = 0;
  for (const auto& c : r.server().calls) if (c.method == rpc::kCatalogBrowse) n++;
  return n;
}

RouteArgs refreshEvery(int ms) {
  RouteArgs a;
  a.data = Json{{"refreshMs", ms}};
  return a;
}

}  // namespace

TEST_CASE("home asks every provider again while on top: rows change in place, focus and scroll kept") {
  fake::Script s;
  // torrplayer is not among catalogSources yet
  s.absent = {"torrplayer", "spotify"};
  Rig r(s);
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME, refreshEvery(2000));
  r.h.step(600);
  REQUIRE(r.byId("prov-torrplayer")->GetNumChildren() == 0);
  // into the library row, three cards along
  r.press(kDown, 2);
  r.press(kRight, 3);
  auto* row = ui::Focus::ancestor(r.focused(), "row");
  REQUIRE(row != nullptr);
  REQUIRE(inside(r.focused(), "prov-torrserver"));
  const auto rowId = row->GetId();
  const auto focusedId = r.focusedId();
  const auto scroll = row->QuerySelector(".track")->GetAttribute<Rml::String>("data-scroll", "");
  REQUIRE(!scroll.empty());
  auto* firstCard = row->QuerySelector(".card.has-ref");
  const auto before = browses(r);
  // the same answers: a refresh changes nothing on screen
  r.h.step(2600);
  REQUIRE(browses(r) >= before + 4);
  REQUIRE(row->QuerySelector(".card.has-ref") == firstCard);
  REQUIRE(r.byId("prov-torrplayer")->GetNumChildren() == 0);
  // the household adds torrplayer: its rows join below, the focused row untouched
  r.server().script.absent = {"spotify"};
  r.h.step(2600);
  REQUIRE(r.byId("prov-torrplayer")->QuerySelector(".card.has-ref") != nullptr);
  REQUIRE(row->QuerySelector(".card.has-ref") == firstCard);
  REQUIRE(row->QuerySelector(".track")->GetAttribute<Rml::String>("data-scroll", "") == scroll);
  REQUIRE(r.focusedId() == focusedId);
  // torrserver's library gains a film at its head
  auto ts = fake::homeSections("torrserver");
  auto added = ts["sections"][1]["items"][0];
  added["ref"] = "ref-film-new";
  added["title"] = "Новинка";
  ts["sections"][1]["items"].insert(ts["sections"][1]["items"].begin(), added);
  r.server().script.home["torrserver"] = ts;
  r.h.step(2600);
  // no skeleton came back: a refresh is quiet
  REQUIRE(r.ui().page()->QuerySelector(".skel") == nullptr);
  REQUIRE(r.byId(catalog::idOf(rowId, "ref-film-new")) != nullptr);
  // the same card keeps focus in the same row (now a place further along); the hero stays collapsed
  REQUIRE(r.focusedId() == focusedId);
  REQUIRE(ui::Focus::ancestor(r.focused(), "row")->GetId() == rowId);
  REQUIRE(r.ui().page()->QuerySelector(".hero")->IsClassSet("collapsed"));
  // a refresh that answers torrplayer gone takes its rows out, focus untouched
  r.server().script.absent = {"torrplayer", "spotify"};
  r.h.step(2600);
  REQUIRE(r.byId("prov-torrplayer")->GetNumChildren() == 0);
  REQUIRE(r.focusedId() == focusedId);
}

TEST_CASE("home's refresh waits while focus moves, and runs on coming back to home") {
  Rig r;
  r.router().open(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME, refreshEvery(2000));
  r.h.step(600);
  r.press(kDown, 2);
  const auto before = browses(r);
  // scrolling along the row for 4 s: nothing is asked
  for (int i = 0; i < 16; ++i) {
    r.press(i % 2 == 0 ? kRight : kLeft);
    r.h.step(190);
  }
  REQUIRE(browses(r) == before);
  // focus rests: the refresh goes out
  r.h.step(1600);
  REQUIRE(browses(r) > before);
  // a film added while home was under the search page shows once home is back on top
  r.h.step(600);
  r.press(kUp, 3);
  REQUIRE(r.focusedId() == "home-search");
  r.ok();
  r.h.step(200);
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_SEARCH);
  auto ts = fake::homeSections("torrserver");
  auto added = ts["sections"][1]["items"][0];
  added["ref"] = "ref-film-back";
  ts["sections"][1]["items"].push_back(added);
  r.server().script.home["torrserver"] = ts;
  r.back();
  r.h.step(1800);
  REQUIRE(r.route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  REQUIRE(r.focusedId() == "home-search");
  bool found = false;
  Rml::ElementList cards;
  r.byId("prov-torrserver")->QuerySelectorAll(cards, ".card.has-ref");
  for (auto* c : cards) found = found || c->GetAttribute<Rml::String>("data-ref", "") == "ref-film-back";
  // the library row draws its first ten: the new one is the ninth
  REQUIRE(found);
}
