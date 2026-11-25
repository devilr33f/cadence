// the playback routes on stream b's harness (sdl offscreen, llvmpipe) with a fake shell and player: the osd's keys
// (design §12's table), skips, up next and auto-continue, the voiceover switch keeping the position, item actions'
// optimistic step and rollback, the side sheets, audio mode's lean-back, idle and its first key, the screen context
#include "app/routes/panels/panel.h"
#include "app/routes/player/cache-info.h"
#include "app/routes/player/switcher.h"
#include "app/routes/preview/playback-pages.h"
#include "ipc/codec.h"
#include "ui/preview/harness.h"

#include <catch2/catch_test_macros.hpp>
#include <nlohmann/json.hpp>

#include <array>
#include <memory>

using namespace cadence::screen;
using namespace cadence::screen::routes;
using namespace cadence::screen::routes::playback;

namespace {

constexpr std::uint64_t kMin = 60000;

struct Env {
  ui::preview::Harness h{{960, 540, cadence::i18n::Lang::Ru}};
  Rig* g = nullptr;
  explicit Env(std::function<void(Router&)> prepare = {}, ui::preview::HarnessOptions options = {}) : h(options) {
    REQUIRE(h.ok());
    h.ui().setPage([] { return std::string("<div/>"); });
    h.step(32);
    g = &start(h.ui(), std::move(prepare));
    h.step(32);
  }
  ~Env() { stop(); }
  PlaybackRoutes& r() { return *g->playback; }
  std::string focused() {
    auto* f = h.ui().focus().current();
    return f != nullptr ? f->GetId() : std::string();
  }
  void key(cadence_screen_v1_Key k) { h.press(k); }
  void episode(std::uint64_t at, cadence_v2_PlaybackState st = cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING) {
    auto s = fakeEpisode();
    g->state(fakeState(s, at, st));
    g->play(s);
    h.step(100);
    g->answer("catalog.item", fakeDetailJson());
    g->answer("catalog.options", fakeOptionsJson());
    h.step(300);
  }
  const player::Command* lastCommand() {
    auto& c = g->fakePlayer.commands;
    return c.empty() ? nullptr : &c.back();
  }
  bool hasCall(const std::string& method) { return g->called(method); }
  Json lastArgs(const std::string& method) { return g->lastArgs(method); }
};

std::array<float, 4> geometry(Rml::Element* el) {
  REQUIRE(el != nullptr);
  const auto at = el->GetAbsoluteOffset(Rml::BoxArea::Border);
  const auto size = el->GetBox().GetSize(Rml::BoxArea::Border);
  return {at.x, at.y, size.x, size.y};
}

void checkRightEdge(Rml::Element* el, int viewportWidth) {
  REQUIRE(el != nullptr);
  const auto box = geometry(el);
  const float safe = 96.0f * static_cast<float>(viewportWidth) / 1920.0f;
  CHECK(box[0] >= safe);
  CHECK(box[0] + box[2] <= static_cast<float>(viewportWidth) - safe);
}

void bareCast(Env& e) {
  cadence_screen_v1_Envelope m = cadence_screen_v1_Envelope_init_zero;
  m.which_body = cadence_screen_v1_Envelope_now_playing_tag;
  auto& n = m.body.now_playing;
  ipc::copyField(n.session_id, "airplay-bare");
  n.source = cadence_v2_MediaSource_MEDIA_SOURCE_AIRPLAY;
  n.state = cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING;
  e.r().message(m);
}

void checkStableAudio(Env& e) {
  e.h.step(500);
  auto* root = e.r().context().el("audio");
  REQUIRE(root != nullptr);
  auto* art = e.r().context().el("a-art");
  auto* col = root->QuerySelector(".acol");
  auto* title = root->QuerySelector(".atitle");
  auto* progress = e.r().context().el("a-progress");
  const auto lean = std::array{geometry(art), geometry(col), geometry(title), geometry(progress)};
  e.key(cadence_screen_v1_Key_KEY_OK);
  e.h.step(500);
  CHECK(std::array{geometry(art), geometry(col), geometry(title), geometry(progress)} == lean);
  CHECK(e.r().context().el("a-controls")->GetProperty<float>("opacity") == 1.0f);
  CHECK(e.r().context().el("a-controls")->IsVisible(true));
  e.key(cadence_screen_v1_Key_KEY_BACK);
  e.h.step(500);
  CHECK(std::array{geometry(art), geometry(col), geometry(title), geometry(progress)} == lean);
  CHECK(e.r().context().el("a-controls")->GetProperty<float>("opacity") == 0.0f);
  CHECK_FALSE(e.r().context().el("a-controls")->IsVisible(true));
}

}  // namespace

TEST_CASE("osd: focus opens on play/pause, rows by ↑ ↓ with their memory, ← → in a row, the scrubber seeks") {
  Env e;
  e.episode(7 * kMin + 12000);
  REQUIRE(e.r().shownRoute() == cadence_v2_ScreenRoute_SCREEN_ROUTE_PLAYER);
  CHECK(e.r().playerPage().osd().shown);
  CHECK(e.focused() == "o-play");
  e.key(cadence_screen_v1_Key_KEY_RIGHT);
  CHECK(e.focused() == "o-fwd10");
  e.key(cadence_screen_v1_Key_KEY_UP);
  CHECK(e.focused() == "o-scrub");
  // ← on the scrubber seeks 10 s back
  e.key(cadence_screen_v1_Key_KEY_LEFT);
  REQUIRE(e.lastCommand() != nullptr);
  CHECK(e.lastCommand()->action == player::CommandAction::Seek);
  // relative, from where playback is (never a stale base); the bar holds the target at once
  CHECK(e.lastCommand()->relative);
  CHECK(e.lastCommand()->deltaMs == -10000);
  CHECK(e.r().context().position() == 7 * kMin + 2000);
  // ↓ ↓: transport (its column kept), then the details strip, which lifts the rows
  e.key(cadence_screen_v1_Key_KEY_DOWN);
  CHECK(e.focused() == "o-fwd10");
  e.key(cadence_screen_v1_Key_KEY_DOWN);
  CHECK(e.focused().starts_with("o-act-"));
  CHECK(e.r().context().el("o-body")->IsClassSet("lifted"));
  e.key(cadence_screen_v1_Key_KEY_UP);
  CHECK(e.focused() == "o-fwd10");
  // to the chips at the right of the transport row, no wrap past the last
  for (int i = 0; i < 8; ++i) e.key(cadence_screen_v1_Key_KEY_RIGHT);
  CHECK(e.focused() == "o-quality");
  // BACK hides the osd; BACK again leaves the player with the film playing on behind home, never stopping it
  e.key(cadence_screen_v1_Key_KEY_BACK);
  CHECK_FALSE(e.r().playerPage().osd().shown);
  const auto commands = e.g->fakePlayer.commands.size();
  e.key(cadence_screen_v1_Key_KEY_BACK);
  CHECK(e.g->fakePlayer.commands.size() == commands);
  CHECK_FALSE(e.r().active());
  CHECK(e.r().shownRoute() == cadence_v2_ScreenRoute_SCREEN_ROUTE_UNSPECIFIED);
  const auto behind = e.r().background();
  REQUIRE(behind);
  CHECK_FALSE(behind->audio);
  // the remote still controls the film without bringing its page back
  e.key(cadence_screen_v1_Key_KEY_PLAY_PAUSE);
  REQUIRE(e.lastCommand() != nullptr);
  CHECK(e.lastCommand()->action == player::CommandAction::Pause);
  CHECK_FALSE(e.r().active());
}

TEST_CASE("background audio: local music stays available and the media key uses the player's own transport") {
  Env e;
  auto s = fakeTrack();
  e.g->state(fakeState(s, 95000, cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING));
  e.g->play(s);
  e.h.step(100);
  e.r().go(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  e.h.step(100);

  const auto behind = e.r().background();
  REQUIRE(behind);
  CHECK(behind->audio);
  CHECK(behind->title == "Северное сияние");
  CHECK(behind->artist == "Полярные огни");
  CHECK_FALSE(behind->paused);
  e.key(cadence_screen_v1_Key_KEY_PLAY_PAUSE);
  REQUIRE(e.lastCommand() != nullptr);
  CHECK(e.lastCommand()->action == player::CommandAction::Pause);

  e.g->state(fakeState(s, 97000, cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED));
  e.h.step(100);
  REQUIRE(e.r().background());
  CHECK(e.r().background()->paused);
  e.key(cadence_screen_v1_Key_KEY_PLAY_PAUSE);
  CHECK(e.lastCommand()->action == player::CommandAction::Resume);
  CHECK(e.r().foreground());
  CHECK(e.r().shownRoute() == cadence_v2_ScreenRoute_SCREEN_ROUTE_AUDIO);
}

TEST_CASE("osd hidden: OK pauses and shows the osd for 3 s, ← → seek with the compact bar, ↓ opens on the details") {
  Env e;
  e.episode(12 * kMin);
  e.r().playerPage().hide();
  e.key(cadence_screen_v1_Key_KEY_RIGHT);
  CHECK(e.lastCommand()->action == player::CommandAction::Seek);
  CHECK(e.lastCommand()->relative);
  CHECK(e.lastCommand()->deltaMs == 10000);
  CHECK(e.r().context().position() == 12 * kMin + 10000);
  CHECK_FALSE(e.r().context().el("compact")->IsClassSet("gone"));
  CHECK_FALSE(e.r().playerPage().osd().shown);
  e.h.step(2100);
  CHECK(e.r().context().el("compact")->IsClassSet("gone"));
  e.key(cadence_screen_v1_Key_KEY_OK);
  CHECK(e.lastCommand()->action == player::CommandAction::Pause);
  CHECK(e.r().playerPage().osd().shown);
  CHECK(e.focused() == "o-play");
  // playing on: it hides 3 s later
  e.h.step(3100);
  CHECK_FALSE(e.r().playerPage().osd().shown);
  e.key(cadence_screen_v1_Key_KEY_DOWN);
  CHECK(e.r().playerPage().osd().shown);
  CHECK(e.r().playerPage().osd().row == OsdState::Details);
  // auto-hide: never while paused
  e.g->state(fakeState(fakeEpisode(), 12 * kMin, cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED));
  e.h.step(6000);
  CHECK(e.r().playerPage().osd().shown);
  e.g->state(fakeState(fakeEpisode(), 12 * kMin, cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING));
  e.h.step(5100);
  CHECK_FALSE(e.r().playerPage().osd().shown);
}

TEST_CASE("skips: the pill takes the first OK with the osd hidden") {
  Env e;
  e.episode(3 * kMin + 40000);
  e.r().playerPage().hide();
  e.h.step(300);
  auto* pill = e.r().context().el("skip");
  REQUIRE(pill != nullptr);
  CHECK_FALSE(pill->IsClassSet("hidden"));
  CHECK(e.focused() == "skip");
  e.key(cadence_screen_v1_Key_KEY_OK);
  CHECK(e.lastCommand()->action == player::CommandAction::Seek);
  CHECK(e.lastCommand()->positionMs == 4 * kMin + 30000);
  CHECK(pill->IsClassSet("hidden"));
}

TEST_CASE("skips: auto-skip jumps over the segment, and OK within 3 s goes back") {
  Env a;
  a.r().configure(R"({"player":{"autoSkip":true}})");
  a.episode(3 * kMin + 10000);
  a.h.step(300);
  CHECK(a.lastCommand()->positionMs == 4 * kMin + 30000);
  a.r().playerPage().hide();
  a.key(cadence_screen_v1_Key_KEY_OK);
  CHECK(a.lastCommand()->positionMs == 3 * kMin + 10000);
}

TEST_CASE("up next: in the credits it counts down 10 s and plays the next episode with the same voiceover") {
  Env e;
  e.episode(21 * kMin + 10000);
  e.r().playerPage().hide();
  e.h.step(500);
  // the voiceover has it
  e.g->answer("catalog.options", fakeOptionsJson());
  CHECK_FALSE(e.r().context().el("upnext")->IsClassSet("hidden"));
  CHECK(e.focused() == "un-watch");
  e.h.step(10000);
  // the server plays it after this session (media.play, reason continue): nothing resolves or plays here
  REQUIRE(e.hasCall("media.play"));
  CHECK_FALSE(e.hasCall("catalog.resolve"));
  const auto args = e.lastArgs("media.play");
  CHECK(args["episode"] == "6");
  CHECK(args["choices"]["voiceover"] == "vo-alb");
  CHECK(args["from"] == "start");
  CHECK(args["reason"] == "continue");
  CHECK(args["replaces"] == "video-1");
  CHECK(e.g->fakePlayer.plays.empty());
  // cadenced's MediaPlay of the server's new session starts it
  auto next = fakeEpisode();
  next.sessionId = "video-2";
  next.episode = "6";
  e.g->play(next);
  CHECK(e.g->answer("media.play", R"({"sessionId":"video-2","episode":"6"})"));
  CHECK(e.r().context().session->episode == "6");
  CHECK(e.r().context().session->sessionId == "video-2");
}

TEST_CASE("up next: BACK cancels it and the credits play on") {
  Env c;
  c.episode(21 * kMin + 10000);
  c.r().playerPage().hide();
  c.h.step(500);
  c.key(cadence_screen_v1_Key_KEY_BACK);
  CHECK(c.r().context().el("upnext")->IsClassSet("hidden"));
  c.h.step(11000);
  CHECK_FALSE(c.hasCall("media.play"));
  CHECK(c.r().active());
}

TEST_CASE("switcher: another voiceover resolves from where it was and plays at the position kept") {
  Env e;
  e.episode(14 * kMin + 52000);
  e.r().playerPage().show(OsdState::Transport);
  auto* vo = e.r().context().el("o-vo");
  REQUIRE(vo != nullptr);
  e.h.ui().focus().focus(vo);
  e.key(cadence_screen_v1_Key_KEY_OK);
  REQUIRE(e.r().sheet().isOpen());
  CHECK(e.r().shownRoute() == cadence_v2_ScreenRoute_SCREEN_ROUTE_PICKER);
  e.g->answer("catalog.options", fakeOptionsJson());
  e.h.step(100);
  CHECK(e.focused() == "sw-0");
  e.key(cadence_screen_v1_Key_KEY_DOWN);
  CHECK(e.focused() == "sw-1");
  e.key(cadence_screen_v1_Key_KEY_OK);
  // the server plays the switch after this session at the position kept (media.play, reason switch)
  const auto args = e.lastArgs("media.play");
  CHECK(args["choices"]["voiceover"] == "vo-dub");
  CHECK(args["startMs"] == 14 * kMin + 52000);
  CHECK(args["reason"] == "switch");
  CHECK(args["replaces"] == "video-1");
  CHECK(e.r().sheet().isOpen());
  auto next = fakeEpisode();
  next.sessionId = "video-2";
  e.g->play(next);
  CHECK(e.g->answer("media.play", R"({"sessionId":"video-2"})"));
  CHECK(e.g->fakePlayer.plays.empty());
  CHECK_FALSE(e.r().sheet().isOpen());
  CHECK(e.r().context().session->voiceover == "AniDUB");
}

TEST_CASE("switcher: a refused switch puts the choices back in the sheet") {
  Env e;
  e.episode(14 * kMin + 52000);
  e.r().playerPage().show(OsdState::Transport);
  e.h.ui().focus().focus(e.r().context().el("o-vo"));
  e.key(cadence_screen_v1_Key_KEY_OK);
  e.g->answer("catalog.options", fakeOptionsJson());
  e.h.step(100);
  e.key(cadence_screen_v1_Key_KEY_DOWN);
  e.key(cadence_screen_v1_Key_KEY_OK);
  REQUIRE(e.hasCall("media.play"));
  CHECK(e.g->answer("media.play", R"({"code":"unplayable"})", "refused"));
  e.h.step(100);
  CHECK(e.r().sheet().isOpen());
  CHECK(e.r().context().el("sw-1") != nullptr);
  CHECK(e.r().context().session->sessionId == "video-1");
}

TEST_CASE("item actions: like and dislike flip together at once; a refusal puts both back") {
  Env e;
  auto s = fakeVideo();
  e.g->state(fakeState(s, 4 * kMin, cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING));
  e.g->play(s);
  e.h.step(100);
  // the channel chip, then like, dislike, subscribe, queue
  e.r().playerPage().show(OsdState::Details, 2);
  CHECK(e.focused() == "o-act-1");
  e.key(cadence_screen_v1_Key_KEY_OK);
  auto& acts = e.r().context().session->itemActions;
  CHECK_FALSE(acts[0].toggled);
  CHECK(acts[1].toggled);
  CHECK(e.lastArgs("catalog.act")["action"] == "a-dislike");
  e.g->answer("catalog.act", R"({"code":"refused"})", "refused");
  CHECK(acts[0].toggled);
  CHECK_FALSE(acts[1].toggled);
  CHECK(e.focused() == "o-act-1");
}

TEST_CASE("panels: a page of cards in the side sheet; BACK closes it, focus back on its chip, the osd shown") {
  Env e;
  auto s = fakeVideo();
  e.g->state(fakeState(s, 4 * kMin, cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING));
  e.g->play(s);
  e.h.step(100);
  e.r().playerPage().show(OsdState::Details, 6);
  CHECK(e.focused() == "o-panel-1");
  e.key(cadence_screen_v1_Key_KEY_OK);
  REQUIRE(e.r().sheet().isOpen());
  CHECK(e.r().shownRoute() == cadence_v2_ScreenRoute_SCREEN_ROUTE_PANEL);
  CHECK(e.lastArgs("catalog.panel")["panel"] == "p-comments");
  REQUIRE(e.g->answerPage("catalog.panel", "page_comments.binpb"));
  e.h.step(300);
  // focus inside the sheet, on its page's first focusable
  auto* f = e.h.ui().focus().current();
  REQUIRE(f != nullptr);
  CHECK(ui::Focus::ancestor(f, "side") != nullptr);
  // ← at the sheet's edge does nothing: it is modal
  e.key(cadence_screen_v1_Key_KEY_LEFT);
  CHECK(e.r().sheet().isOpen());
  e.key(cadence_screen_v1_Key_KEY_BACK);
  CHECK_FALSE(e.r().sheet().isOpen());
  CHECK(e.focused() == "o-panel-1");
  CHECK(e.r().playerPage().osd().shown);
}

TEST_CASE("audio: lean-back until a key, which only shows the controls; 8 s back; 2 min to the ambient sleeve") {
  Env e;
  auto s = fakeTrack();
  e.g->state(fakeState(s, 95000, cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING));
  e.g->play(s);
  e.h.step(100);
  // the classic family dims to ambient; the host's families never do (below)
  e.r().audioPage().setVisualizer(VizFamily::Classic);
  CHECK(e.r().shownRoute() == cadence_v2_ScreenRoute_SCREEN_ROUTE_AUDIO);
  CHECK(e.r().audioPage().state() == AudioPage::State::Lean);
  // the first OK is consumed: no pause
  e.key(cadence_screen_v1_Key_KEY_OK);
  CHECK(e.r().audioPage().state() == AudioPage::State::Controls);
  CHECK(e.g->fakePlayer.commands.empty());
  CHECK(e.focused() == "a-play");
  e.key(cadence_screen_v1_Key_KEY_OK);
  REQUIRE(e.lastCommand() != nullptr);
  CHECK(e.lastCommand()->action == player::CommandAction::Pause);
  e.h.step(8100);
  CHECK(e.r().audioPage().state() == AudioPage::State::Lean);
  e.h.step(120100);
  CHECK(e.r().audioPage().state() == AudioPage::State::Ambient);
  const auto before = e.g->fakePlayer.commands.size();
  e.key(cadence_screen_v1_Key_KEY_RIGHT);
  CHECK(e.r().audioPage().state() == AudioPage::State::Controls);
  CHECK(e.g->fakePlayer.commands.size() == before);
}

TEST_CASE("audio: the drive family keeps the scene up and puts the title in its shape while lean") {
  Env e;
  auto s = fakeTrack();
  e.g->state(fakeState(s, 95000, cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING));
  e.g->play(s);
  e.h.step(100);
  e.r().audioPage().setVisualizer(VizFamily::Drive);
  e.h.step(100);
  auto* root = e.r().context().el("audio");
  REQUIRE(root != nullptr);
  CHECK(root->IsClassSet("drive"));
  REQUIRE(e.r().context().el("a-drive") != nullptr);
  e.h.step(125000);
  CHECK(e.r().audioPage().state() == AudioPage::State::Lean);
  e.key(cadence_screen_v1_Key_KEY_OK);
  CHECK(e.r().audioPage().state() == AudioPage::State::Controls);
  CHECK(root->IsClassSet("drive"));
  CHECK_FALSE(root->IsClassSet("lean"));
}

TEST_CASE("audio: the controls fit the safe area at 1080p and 720p, with the visualizer pill naming only its mode") {
  for (const auto& [width, height] : {std::pair{1920, 1080}, std::pair{1280, 720}}) {
    INFO(std::to_string(width) + "x" + std::to_string(height));
    Env e({}, {width, height, cadence::i18n::Lang::Ru});
    auto s = fakeTrack();
    // the owner's row: three transport buttons, then queue, the view and the visualizer
    s.itemActions.clear();
    s.panels.clear();
    s.views = {View::Audio, View::Visualizer};
    s.view = View::Audio;
    e.g->state(fakeState(s, 95000, cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING));
    e.g->play(s);
    e.h.step(100);
    e.r().audioPage().setVisualizer(VizFamily::Classic);
    e.key(cadence_screen_v1_Key_KEY_OK);
    e.h.step(500);
    for (const char* id : {"a-prev", "a-play", "a-next", "a-queue", "a-view", "a-viz"})
      checkRightEdge(e.r().context().el(id), width);
    auto* viz = e.r().context().el("a-viz");
    REQUIRE(viz != nullptr);
    auto* label = viz->QuerySelector("span:last-child");
    REQUIRE(label != nullptr);
    CHECK(label->GetInnerRML() == "классика");
  }
}

TEST_CASE("audio: up next is a provider-sized badge and the cover stays over the milkdrop underlay") {
  Env e;
  auto s = fakeTrack();
  s.upNext->title = "Hostile Illusion — A Very Long Next Track Title";
  e.g->state(fakeState(s, 95000, cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING));
  e.g->play(s);
  e.h.step(300);
  e.r().audioPage().setVisualizer(VizFamily::Classic);
  e.h.step(300);
  auto* next = e.r().context().el("a-upnext");
  REQUIRE(next != nullptr);
  CHECK(next->IsClassSet("badge"));
  REQUIRE(next->QuerySelector(".icon") != nullptr);
  auto* provider = e.r().context().el("audio")->QuerySelector(".asource .badge");
  REQUIRE(provider != nullptr);
  CHECK(geometry(next)[3] == geometry(provider)[3]);
  auto* art = e.r().context().el("a-art");
  REQUIRE(art != nullptr);
  CHECK(art->IsVisible(true));
  e.r().audioPage().setVisualizer(VizFamily::Scenes);
  e.h.step(300);
  CHECK(e.r().context().el("audio")->IsClassSet("viz"));
  CHECK(art->IsVisible(true));
}

TEST_CASE("audio: controls never move local music, youtube music, or an airplay cast with or without art and metadata") {
  for (const int variant : {0, 1, 2, 3}) {
    INFO("variant " << variant);
    Env e;
    if (variant <= 1) {
      auto s = fakeTrack();
      if (variant == 0) {
        s.itemRef.clear();
        s.providerId.clear();
        s.providerName.clear();
        s.itemActions.clear();
        s.panels.clear();
        s.upNext.reset();
      }
      e.g->state(fakeState(s, 95000, cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING));
      e.g->play(s);
    } else if (variant == 2) {
      e.g->cast("airplay-full", cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING, 94000);
    } else {
      bareCast(e);
    }
    REQUIRE(e.r().shownRoute() == cadence_v2_ScreenRoute_SCREEN_ROUTE_AUDIO);
    checkStableAudio(e);
  }
}

TEST_CASE("ambient: idle after idleAfterS, aerial muted through the player, the first key only wakes") {
  Env e;
  e.r().configure(R"({"ambient":{"idleAfterS":5,"sleepAfterS":0}})");
  e.h.step(5100);
  CHECK(e.r().shownRoute() == cadence_v2_ScreenRoute_SCREEN_ROUTE_AMBIENT);
  REQUIRE(e.hasCall("ambient.next"));
  e.g->answer("ambient.next", R"({"kind":"video","id":"a","url":"https://sylvan.apple.com/a.mov","captions":[{"atS":0,"text":"Исландия"}]})");
  REQUIRE(e.g->fakePlayer.plays.size() == 1);
  CHECK(e.g->fakePlayer.plays[0].purpose == cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_AMBIENT);
  // a key wakes and is swallowed, down and up
  e.key(cadence_screen_v1_Key_KEY_OK);
  CHECK_FALSE(e.r().active());
  CHECK(e.g->fakePlayer.commands.back().action == player::CommandAction::Stop);
}

TEST_CASE("ambient: over a paused film idle plays no clip, the first key puts the film back, a toast yields") {
  Env f;
  f.r().configure(R"({"ambient":{"idleAfterS":5}})");
  f.episode(9 * kMin, cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED);
  f.r().playerPage().show(OsdState::Transport);
  f.key(cadence_screen_v1_Key_KEY_RIGHT);
  const auto was = f.focused();
  f.h.step(5200);
  REQUIRE(f.r().shownRoute() == cadence_v2_ScreenRoute_SCREEN_ROUTE_AMBIENT);
  f.g->answer("ambient.next", R"({"kind":"video","id":"a","url":"https://sylvan.apple.com/a.mov"})");
  // one player: a clip never replaces the paused film
  CHECK(f.g->fakePlayer.plays.empty());
  f.key(cadence_screen_v1_Key_KEY_DOWN);
  CHECK(f.r().shownRoute() == cadence_v2_ScreenRoute_SCREEN_ROUTE_PLAYER);
  // a toast while idle yields at once
  f.h.step(5200);
  REQUIRE(f.r().shownRoute() == cadence_v2_ScreenRoute_SCREEN_ROUTE_AMBIENT);
  cadence_screen_v1_Envelope t = cadence_screen_v1_Envelope_init_zero;
  t.which_body = cadence_screen_v1_Envelope_toast_tag;
  f.g->router->message(t);
  CHECK(f.r().shownRoute() == cadence_v2_ScreenRoute_SCREEN_ROUTE_PLAYER);
  (void)was;
}

// owner: aerial never played after a youtube video was stopped: the stopped session (not ended) kept the clip away
TEST_CASE("ambient: a stopped film leaves idle to the clip") {
  Env f;
  f.r().configure(R"({"ambient":{"idleAfterS":5,"sleepAfterS":0}})");
  f.episode(9 * kMin, cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE);
  f.h.step(5200);
  REQUIRE(f.r().shownRoute() == cadence_v2_ScreenRoute_SCREEN_ROUTE_AMBIENT);
  REQUIRE(f.hasCall("ambient.next"));
  f.g->answer("ambient.next", R"({"kind":"video","id":"a","url":"https://sylvan.apple.com/a.mov"})");
  REQUIRE(f.g->fakePlayer.plays.size() == 1);
  CHECK(f.g->fakePlayer.plays[0].purpose == cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_AMBIENT);
}

TEST_CASE("screen context: the player's ref, position and tracks, debounced 500 ms, never a title") {
  Env e;
  e.episode(7 * kMin);
  e.h.step(600);
  REQUIRE_FALSE(e.g->outbox.contexts.empty());
  const auto c = e.g->outbox.contexts.back();
  CHECK(c.route == cadence_v2_ScreenRoute_SCREEN_ROUTE_PLAYER);
  CHECK(c.pageRef == "ref:title:1");
  CHECK(c.hasPlayer);
  CHECK(c.itemRef == "ref:title:1");
  CHECK(c.positionMs == 7 * kMin);
  CHECK(e.g->outbox.contexts.back().tracks == "v1* a1* a2 s1* s2 ");
  const auto n = e.g->outbox.contexts.size();
  // a seek in a burst of keys: one context after they settle, with the new position
  e.key(cadence_screen_v1_Key_KEY_UP);
  e.key(cadence_screen_v1_Key_KEY_RIGHT);
  e.key(cadence_screen_v1_Key_KEY_RIGHT);
  e.h.step(200);
  CHECK(e.g->outbox.contexts.size() == n);
  e.h.step(400);
  CHECK(e.g->outbox.contexts.size() == n + 1);
  CHECK(e.g->outbox.contexts.back().revision > c.revision);
  CHECK(e.g->outbox.contexts.back().positionMs == 7 * kMin + 20000);
}

TEST_CASE("cache: how far ahead and how fast between the times, and the spinner while the player waits") {
  Env e;
  e.episode(7 * kMin);
  auto& ctx = e.r().context();
  auto* line = ctx.el("o-cache");
  REQUIRE(line != nullptr);
  // a player that says nothing of its cache shows nothing (the previews' fakes)
  CHECK(line->IsClassSet("hidden"));
  auto st = e.g->fakePlayer.playerState;
  st.bufferedMs = 83000;
  e.g->state(st);
  e.g->fakePlayer.speed = 4'200'000;
  e.h.step(300);
  CHECK_FALSE(line->IsClassSet("hidden"));
  const std::string text = line->GetInnerRML();
  CHECK(text.find("1:23") != std::string::npos);
  CHECK(text.find("4,2") != std::string::npos);
  // paused, yet waiting for a seek: the spinner after 1 s, with the speed
  st.state = cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED;
  e.g->state(st);
  e.g->fakePlayer.waits = true;
  e.h.step(300);
  auto* buf = ctx.el("buffering");
  REQUIRE(buf != nullptr);
  CHECK(buf->IsClassSet("hidden"));
  e.h.step(1000);
  CHECK_FALSE(buf->IsClassSet("hidden"));
  CHECK(ctx.el("buf-speed")->GetInnerRML().find("МБ/с") != std::string::npos);
  e.g->fakePlayer.waits = false;
  e.g->fakePlayer.speed.reset();
  e.h.step(300);
  CHECK(buf->IsClassSet("hidden"));
  CHECK(line->IsClassSet("hidden"));
}

TEST_CASE("cache: speeds in the language's units") {
  using cadence::i18n::Lang;
  CHECK(speedText(Lang::En, 0).empty());
  CHECK(speedText(Lang::En, 300) == "1\xc2\xa0KB/s");
  CHECK(speedText(Lang::En, 850'400) == "850\xc2\xa0KB/s");
  CHECK(speedText(Lang::Ru, 4'249'000) == "4,2\xc2\xa0МБ/с");
  CHECK(speedText(Lang::En, 12'000'000) == "12\xc2\xa0MB/s");
  CHECK(cacheLine(Lang::En, 500, std::nullopt).empty());
  CHECK(cacheLine(Lang::En, 3'723'000, 0) == "[[player.osd.buffer_ahead|time=1:02:03]]");
  CHECK(cacheLine(Lang::En, 0, 2'000'000) == "2\xc2\xa0MB/s");
}

TEST_CASE("subtitles: drawn by the screen, one ink box a line, escaped, above the title block while the osd shows") {
  Env e;
  e.episode(7 * kMin);
  e.r().subtitle("<b>Фонари</b>\n[[x]]");
  auto* subs = e.r().context().el("subs");
  REQUIRE(subs != nullptr);
  const std::string rml = subs->GetInnerRML();
  CHECK(rml.find("&lt;b&gt;") != std::string::npos);
  CHECK(rml.find("[[x]]") == std::string::npos);
  CHECK(subs->IsClassSet("raised"));
  e.r().playerPage().hide();
  CHECK_FALSE(subs->IsClassSet("raised"));
}

TEST_CASE("audio: the tap's bands animate the background model, not rml bars; paused it settles") {
  Env e;
  auto s = fakeTrack();
  e.g->state(fakeState(s, 95000, cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING));
  e.g->play(s);
  e.r().audioPage().setVisualizer(VizFamily::Classic);
  CHECK(e.r().context().el("a-bars") == nullptr);
  std::array<std::uint8_t, 16> b{};
  b.fill(255);
  e.r().bands(b);
  e.h.step(33);
  e.h.step(16);
  CHECK(e.r().audioPage().barHeights()[3] > BarSmoother::kMinDp);
  // the controls showing change nothing of the background model
  e.key(cadence_screen_v1_Key_KEY_OK);
  e.h.step(16);
  CHECK(e.r().audioPage().barHeights()[3] > BarSmoother::kMinDp);
  e.g->state(fakeState(s, 95000, cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED));
  for (int i = 0; i < 90; ++i) e.h.step(16);
  CHECK(e.r().audioPage().barHeights()[3] == BarSmoother::kMinDp);
}

namespace {

// i3's drawer as the router sees it: a layer whose status says whether it is open
struct FakeDrawer final : Layer {
  explicit FakeDrawer(bool& open) : open_(open) {}
  void status(app::UiStatus& s) const override { s.drawerOpen = open_; }
  bool& open_;
};

}  // namespace

TEST_CASE("drawer: the osd hides while the assistant's drawer is open, and the remote is the drawer's") {
  bool open = false;
  Env e([&open](Router& r) {
    r.addLayer([&open](Services&, app::Outbox&) -> std::unique_ptr<Layer> { return std::make_unique<FakeDrawer>(open); });
  });
  e.episode(7 * kMin);
  REQUIRE(e.r().playerPage().osd().shown);
  open = true;
  e.h.step(100);
  CHECK_FALSE(e.r().playerPage().osd().shown);
  // keys go past the player (to the drawer's own filter on the device): no seek, no osd
  const auto before = e.g->fakePlayer.commands.size();
  e.key(cadence_screen_v1_Key_KEY_LEFT);
  e.key(cadence_screen_v1_Key_KEY_DOWN);
  CHECK(e.g->fakePlayer.commands.size() == before);
  CHECK_FALSE(e.r().playerPage().osd().shown);
  open = false;
  e.key(cadence_screen_v1_Key_KEY_UP);
  CHECK(e.r().playerPage().osd().shown);
}

namespace {

// the server's fields of a session as cadenced relays them (screen/v1 field 36), encoded and decoded as the link
// carries them: what the routes get is what crossed the wire
void streamState(Rig& g, const std::string& sessionId, const std::vector<std::string>& upNext, bool liked) {
  ipc::OutEnvelope out(cadence_screen_v1_Envelope_media_session_state_tag);
  auto& st = out->body.media_session_state;
  ipc::copyField(st.session_id, sessionId);
  for (const char* a : {"next", "previous"}) ipc::copyField(st.actions[st.actions_count++], a);
  st.item_actions = out.keepArray<cadence_v2_MediaItemAction>(1);
  st.item_actions_count = 1;
  ipc::copyField(st.item_actions[0].id, "srv-like");
  st.item_actions[0].kind = cadence_v2_MediaItemActionKind_MEDIA_ITEM_ACTION_KIND_LIKE;
  st.item_actions[0].label = out.keep("Нравится");
  st.item_actions[0].toggled = liked;
  st.item_actions[0].enabled = true;
  st.up_next = out.keepArray<cadence_v2_MediaQueueItem>(upNext.size());
  for (const auto& ref : upNext) {
    auto& q = st.up_next[st.up_next_count++];
    ipc::copyField(q.ref, ref);
    q.title = out.keep("Следующее " + ref);
    q.duration_ms = 200000;
  }
  st.has_queue_position = true;
  st.queue_position = 3;
  st.panels = out.keepArray<cadence_v2_MediaPanel>(1);
  st.panels_count = 1;
  ipc::copyField(st.panels[0].id, "srv-desc");
  st.panels[0].kind = cadence_v2_MediaPanelKind_MEDIA_PANEL_KIND_DESCRIPTION;
  st.panels[0].title = out.keep("Описание");
  std::vector<std::uint8_t> frame;
  REQUIRE(ipc::encodeEnvelope(out.get(), frame));
  std::string error;
  auto decoded = ipc::decodeEnvelope(frame, &error);
  REQUIRE(decoded);
  g.router->message(*decoded);
}

}  // namespace

TEST_CASE("session state: a streamed media_session_state updates up next and the actions without moving focus") {
  Env e;
  auto s = fakeVideo();
  e.g->state(fakeState(s, 4 * kMin, cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING));
  e.g->play(s);
  e.h.step(100);
  e.r().playerPage().show(OsdState::Transport);
  e.h.step(100);
  REQUIRE(e.focused() == "o-play");
  CHECK(e.r().context().el("o-next") == nullptr);
  streamState(*e.g, s.sessionId, {"q:7", "q:8"}, false);
  e.h.step(100);
  // the server's up next, item actions, panels and transport are the session's; focus stayed on play/pause though
  // previous now stands before it
  const auto& sess = *e.r().context().session;
  REQUIRE(sess.serverFields);
  REQUIRE(sess.upNext);
  CHECK(sess.upNext->ref == "q:7");
  CHECK(sess.serverUpNext.size() == 2);
  CHECK(sess.queuePosition == 3u);
  REQUIRE(sess.itemActions.size() == 1);
  CHECK(sess.itemActions[0].id == "srv-like");
  REQUIRE(sess.panels.size() == 1);
  CHECK(sess.panels[0].id == "srv-desc");
  CHECK(e.r().context().el("o-prev") != nullptr);
  CHECK(e.r().context().el("o-next") != nullptr);
  CHECK(e.focused() == "o-play");
  // a later state replaces it whole, focus still kept
  streamState(*e.g, s.sessionId, {"q:9"}, true);
  e.h.step(100);
  CHECK(e.r().context().session->upNext->ref == "q:9");
  CHECK(e.r().context().session->itemActions[0].toggled);
  CHECK(e.focused() == "o-play");
  // the catalog's detail no longer overrides what the server sent
  e.g->answer("catalog.item", fakeDetailJson());
  CHECK(e.r().context().session->itemActions.size() == 1);
  // next is the server's transport
  e.h.ui().focus().focus(e.r().context().el("o-next"));
  e.key(cadence_screen_v1_Key_KEY_OK);
  const auto next = e.lastArgs("media.control");
  CHECK(next["sessionId"] == s.sessionId);
  CHECK(next["action"] == "next");
  CHECK(e.g->fakePlayer.plays.empty());
}

TEST_CASE("session state: fields that come before their play are kept for it; item actions are media.item_action") {
  Env e;
  auto s = fakeVideo();
  streamState(*e.g, s.sessionId, {"q:1"}, false);
  e.g->state(fakeState(s, 4 * kMin, cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING));
  e.g->play(s);
  e.h.step(100);
  REQUIRE(e.r().context().session->serverFields);
  CHECK(e.r().context().session->upNext->ref == "q:1");
  e.r().playerPage().show(OsdState::Details);
  e.h.ui().focus().focus(e.r().context().el("o-act-0"));
  e.key(cadence_screen_v1_Key_KEY_OK);
  CHECK(e.r().context().session->itemActions[0].toggled);
  const auto args = e.lastArgs("media.item_action");
  CHECK(args["sessionId"] == s.sessionId);
  CHECK(args["actionId"] == "srv-like");
  CHECK_FALSE(e.hasCall("catalog.act"));
  // a refusal puts it back
  CHECK(e.g->answer("media.item_action", R"({"code":"not_supported"})", "unavailable"));
  CHECK_FALSE(e.r().context().session->itemActions[0].toggled);
}

TEST_CASE("queue: the server's page (media.queue), and OK on one of its items is media.control's jump") {
  Env e;
  auto s = fakeTrack();
  e.g->state(fakeState(s, 95000, cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING));
  e.g->play(s);
  e.h.step(100);
  streamState(*e.g, s.sessionId, {"mq:4", "mq:5"}, false);
  e.r().go(cadence_v2_ScreenRoute_SCREEN_ROUTE_QUEUE);
  e.h.step(100);
  REQUIRE(e.r().sheet().isOpen());
  CHECK(e.lastArgs("media.queue")["sessionId"] == s.sessionId);
  CHECK(e.g->answer("media.queue",
                    R"({"items":[{"ref":"mq:4","title":"Четыре"},{"ref":"mq:5","title":"Пять"},{"ref":"mq:6","title":"Шесть"}],"position":3})"));
  e.h.step(100);
  e.h.ui().focus().focus(e.r().context().el("q-n2"));
  e.key(cadence_screen_v1_Key_KEY_OK);
  const auto jump = e.lastArgs("media.control");
  CHECK(jump["action"] == "jump");
  CHECK(jump["ref"] == "mq:6");
  CHECK(e.g->answer("media.control", "{}"));
  CHECK_FALSE(e.r().sheet().isOpen());
}

TEST_CASE("audio queue: the full queue gets a focusable sidebar whose rows jump") {
  Env e;
  auto s = fakeTrack();
  e.g->state(fakeState(s, 95000, cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING));
  e.g->play(s);
  e.h.step(100);
  REQUIRE(e.r().context().el("a-queue") == nullptr);
  CHECK(e.lastArgs("media.queue")["all"] == true);
  const auto before = geometry(e.r().context().el("a-art"));
  CHECK(e.g->answer(
      "media.queue",
      R"({"name":"Evening","artworkUrl":"https://cadence.example/v2/images/AbCdEfGhIjKlMnOpQrStUvWx","total":3,"position":0,"items":[{"ref":"mq:0","title":"One","subtitle":"Artist","durationMs":181000},{"ref":"mq:1","title":"Two","subtitle":"Artist","durationMs":182000},{"ref":"mq:2","title":"Three","subtitle":"Artist","durationMs":183000}]})"));
  e.h.step(100);
  auto* sidebar = e.r().context().el("a-queue");
  REQUIRE(sidebar != nullptr);
  CHECK(e.r().context().el("audio")->IsClassSet("queued"));
  CHECK(e.r().context().el("aq-item-0")->IsClassSet("current"));
  CHECK(geometry(e.r().context().el("a-art")) != before);
  e.key(cadence_screen_v1_Key_KEY_OK);
  e.h.ui().focus().focus(e.r().context().el("aq-item-0"));
  e.key(cadence_screen_v1_Key_KEY_DOWN);
  CHECK(e.focused() == "aq-item-1");
  e.h.ui().focus().focus(e.r().context().el("aq-item-2"));
  e.key(cadence_screen_v1_Key_KEY_OK);
  const auto jump = e.lastArgs("media.control");
  CHECK(jump["sessionId"] == s.sessionId);
  CHECK(jump["action"] == "jump");
  CHECK(jump["ref"] == "mq:2");
}

TEST_CASE("audio queue: no queue leaves the audio page's geometry alone") {
  Env plain;
  auto single = fakeTrack();
  plain.g->state(fakeState(single, 95000, cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING));
  plain.g->play(single);
  plain.h.step(100);
  const auto stable = geometry(plain.r().context().el("a-art"));
  CHECK(plain.g->answer("media.queue", R"({"code":"no_queue"})", "not_found"));
  plain.h.step(100);
  CHECK(plain.r().context().el("a-queue") == nullptr);
  CHECK_FALSE(plain.r().context().el("audio")->IsClassSet("queued"));
  CHECK(geometry(plain.r().context().el("a-art")) == stable);
}

TEST_CASE("a cast: it displaces the film and opens audio mode, its controls go to cadenced, BACK keeps it behind home") {
  Env e;
  e.episode(12 * kMin);
  REQUIRE(e.r().shownRoute() == cadence_v2_ScreenRoute_SCREEN_ROUTE_PLAYER);
  e.g->fakePlayer.playerState.state = cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING;
  const auto before = e.g->fakePlayer.commands.size();

  // the phone casts: audio mode replaces the player, which is paused (whatever became of audiod's own pause)
  e.g->cast("ms-1", cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING, 94000);
  e.h.step(300);
  CHECK(e.r().shownRoute() == cadence_v2_ScreenRoute_SCREEN_ROUTE_AUDIO);
  REQUIRE(e.g->fakePlayer.commands.size() == before + 1);
  CHECK(e.lastCommand()->action == player::CommandAction::Pause);
  REQUIRE(e.r().context().session);
  CHECK(e.r().context().session->device);
  CHECK(e.r().context().session->sender == "iPhone Маши");
  CHECK(e.r().context().session->art == "img:8c73c45694719f0b7718292a");
  CHECK(e.r().context().position() == 94000);
  // the film's own reports say nothing of the cast now
  e.g->state(fakeState(fakeEpisode(), 12 * kMin, cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED));
  CHECK(e.r().context().playing());

  // a key shows the controls: play/pause alone (airplay takes no skips), no queue; OK pauses through cadenced
  e.key(cadence_screen_v1_Key_KEY_OK);
  CHECK(e.focused() == "a-play");
  CHECK(e.r().context().el("a-prev") == nullptr);
  CHECK(e.r().context().el("a-next") == nullptr);
  CHECK(e.r().context().el("a-queue") == nullptr);
  e.key(cadence_screen_v1_Key_KEY_OK);
  REQUIRE(e.g->outbox.castCommands.size() == 1);
  CHECK(std::string(e.g->outbox.castCommands[0].session_id) == "ms-1");
  CHECK(e.g->outbox.castCommands[0].action == cadence_screen_v1_MediaAction_MEDIA_ACTION_PAUSE);
  CHECK(e.g->fakePlayer.commands.size() == before + 1);
  e.g->cast("ms-1", cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED, 95000);
  e.h.step(100);
  CHECK_FALSE(e.r().context().playing());
  CHECK(e.r().shownRoute() == cadence_v2_ScreenRoute_SCREEN_ROUTE_AUDIO);

  // a new track in the same cast stays on the page
  e.g->cast("ms-1", cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING, 0, "next song");
  e.h.step(100);
  CHECK(e.r().shownRoute() == cadence_v2_ScreenRoute_SCREEN_ROUTE_AUDIO);
  CHECK(e.r().context().session->title == "next song");

  // BACK twice: the controls hide, then home with the cast behind it, which home can bring back
  e.key(cadence_screen_v1_Key_KEY_BACK);
  e.key(cadence_screen_v1_Key_KEY_BACK);
  e.h.step(100);
  CHECK_FALSE(e.r().active());
  const auto behind = e.r().background();
  REQUIRE(behind);
  CHECK(behind->audio);
  CHECK(behind->itemRef.empty());
  // the remote's key reaches the cast too, without reopening audio mode
  e.key(cadence_screen_v1_Key_KEY_PLAY_PAUSE);
  REQUIRE(e.g->outbox.castCommands.size() == 2);
  CHECK(e.g->outbox.castCommands[1].action == cadence_screen_v1_MediaAction_MEDIA_ACTION_PAUSE);
  CHECK_FALSE(e.r().active());
  e.g->cast("ms-1", cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED, 95000, "next song");
  e.h.step(100);
  REQUIRE(e.r().background());
  CHECK(e.r().background()->paused);
  e.key(cadence_screen_v1_Key_KEY_PLAY_PAUSE);
  REQUIRE(e.g->outbox.castCommands.size() == 3);
  CHECK(e.g->outbox.castCommands[2].action == cadence_screen_v1_MediaAction_MEDIA_ACTION_RESUME);
  CHECK(e.r().foreground());
  CHECK(e.r().shownRoute() == cadence_v2_ScreenRoute_SCREEN_ROUTE_AUDIO);

  // the sender ends it: its page goes and nothing resumes the film
  e.g->cast("", cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE, 0);
  e.h.step(100);
  CHECK_FALSE(e.r().active());
  CHECK_FALSE(e.r().context().session);
  CHECK(e.g->fakePlayer.commands.size() == before + 1);
}

TEST_CASE("a cast pause still reaches the visualizer when the same update changes the track") {
  Env e;
  e.g->cast("ms-1", cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING, 94000, "first song");
  e.r().audioPage().setVisualizer(VizFamily::Scenes);
  e.h.step(100);
  REQUIRE(e.r().context().playing());
  REQUIRE(e.r().audioPage().viz()->frameReady());

  // the sender's flush and its next dmap can arrive together: the look changes, but the state must not be skipped
  e.g->cast("ms-1", cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED, 95000, "next song");
  e.h.step(100);
  CHECK(e.r().context().session->title == "next song");
  CHECK_FALSE(e.r().context().playing());
  CHECK(e.r().context().state.rate == 0);
  CHECK_FALSE(e.r().audioPage().viz()->frameReady());
}

TEST_CASE("a spotify connect cast offers its skips, which go to cadenced") {
  Env e;
  e.g->cast("ms-2", cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING, 1000, "Ночной паром",
            cadence_v2_MediaSource_MEDIA_SOURCE_SOLOIST);
  e.h.step(300);
  REQUIRE(e.r().shownRoute() == cadence_v2_ScreenRoute_SCREEN_ROUTE_AUDIO);
  CHECK(e.r().context().session->providerName == "Spotify");
  e.key(cadence_screen_v1_Key_KEY_OK);
  CHECK(e.r().context().el("a-prev") != nullptr);
  e.key(cadence_screen_v1_Key_KEY_RIGHT);
  CHECK(e.focused() == "a-next");
  e.key(cadence_screen_v1_Key_KEY_OK);
  REQUIRE(e.g->outbox.castCommands.size() == 1);
  CHECK(e.g->outbox.castCommands[0].action == cadence_screen_v1_MediaAction_MEDIA_ACTION_NEXT);
}

TEST_CASE("a follower screen: next is its leader's through the server alone, and it counts no up next down") {
  Env e;
  auto s = fakeEpisode();
  // a multiroom follower's play (P4): its picture follows the group's, and its play names no actions of its own
  s.follow = true;
  s.actions.clear();
  e.g->state(fakeState(s, 21 * kMin + 10000, cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING));
  e.g->play(s);
  e.h.step(100);
  e.g->answer("catalog.item", fakeDetailJson());
  e.g->answer("catalog.options", fakeOptionsJson());
  e.h.step(300);
  e.r().playerPage().hide();
  e.h.step(500);
  // the catalog's next episode is none of its own: in the credits no countdown, and nothing plays after it
  CHECK(e.r().context().el("upnext")->IsClassSet("hidden"));
  e.h.step(11000);
  CHECK_FALSE(e.hasCall("media.play"));
  e.r().playerPage().show(OsdState::Transport);
  e.h.step(100);
  CHECK(e.r().context().el("o-next") == nullptr);
  // the server's fields of its session (its leader's next, previous and up next) offer the transport
  streamState(*e.g, s.sessionId, {"ce:6"}, false);
  e.h.step(100);
  CHECK(e.r().context().el("o-prev") != nullptr);
  REQUIRE(e.r().context().el("o-next") != nullptr);
  CHECK(e.r().context().el("upnext")->IsClassSet("hidden"));
  e.h.ui().focus().focus(e.r().context().el("o-next"));
  e.key(cadence_screen_v1_Key_KEY_OK);
  const auto next = e.lastArgs("media.control");
  CHECK(next["sessionId"] == s.sessionId);
  CHECK(next["action"] == "next");
  CHECK_FALSE(e.hasCall("media.play"));
  CHECK(e.g->fakePlayer.plays.empty());
}
