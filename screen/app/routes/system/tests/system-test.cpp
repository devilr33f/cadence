#include "app/routes/system/tests/support.h"

#include "ui/core/budget.h"

using namespace cadence::screen;
using namespace cadence::screen::routes;
using namespace cadence::screen::routes::systest;

TEST_CASE("settings switch the language live: the page and the open drawer are built again in it") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  f.deliver(syspreview::route(cadence_v2_ScreenRoute_SCREEN_ROUTE_SETTINGS));
  h.step(100);
  REQUIRE(f.system().onSettings());
  auto title = [&] {
    auto* t = h.ui().page()->QuerySelector(".stitle-l");
    return t != nullptr ? t->GetInnerRML() : std::string();
  };
  CHECK(title() == "Настройки");
  h.ui().focus().focus(h.ui().page()->GetElementById("set-lang"));
  h.press(cadence_screen_v1_Key_KEY_OK);
  h.step(100);
  CHECK(h.translator().language() == cadence::i18n::Lang::En);
  CHECK(title() == "Settings");
  // focus stays on the language row
  CHECK(focusedId(h) == "set-lang");
  h.press(cadence_screen_v1_Key_KEY_OK);
  h.step(100);
  CHECK(h.translator().language() == cadence::i18n::Lang::Ru);
  CHECK(title() == "Настройки");

  // «Микрофон»: room and microphone; BACK one level up
  h.ui().focus().focus(h.ui().page()->GetElementById("set-mic"));
  h.press(cadence_screen_v1_Key_KEY_OK);
  h.step(100);
  CHECK(f.system().onSettings(true));
  h.press(cadence_screen_v1_Key_KEY_BACK);
  h.step(100);
  CHECK(f.system().onSettings());
  h.press(cadence_screen_v1_Key_KEY_BACK);
  h.step(100);
  CHECK(f.system().top() == cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
}

TEST_CASE("a language switch with the drawer open keeps its turn and its focus") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
  f.answer("assistant.invoke", "ok", "{\"turnId\":\"turn-1\"}");
  Turn t;
  t.state = cadence_v2_AssistantState_ASSISTANT_STATE_LISTENING;
  f.deliver(t.envelope());
  h.step(100);
  h.translator().set_language(cadence::i18n::Lang::En);
  h.step(100);
  CHECK(f.system().drawer().isOpen());
  CHECK(f.system().drawer().focused());
  CHECK(focusedId(h) == "dbtn");
  auto* title = h.ui().documentNamed("drawer")->QuerySelector(".dname");
  REQUIRE(title != nullptr);
  CHECK(title->GetInnerRML().find("Assistant") != std::string::npos);
  h.translator().set_language(cadence::i18n::Lang::Ru);
  h.step(100);
}

TEST_CASE("the pseudo-locale: the drawer, the claim and settings wrap no single-line slot") {
  auto& h = harness();
  auto& f = fresh(h);
  h.translator().set_language(cadence::i18n::Lang::Pseudo);
  f.online();
  h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
  f.answer("assistant.invoke", "ok", "{\"turnId\":\"turn-1\"}");
  Turn t;
  t.state = cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP;
  t.reply = "Включил.";
  t.cards = {"card_light_living.binpb"};
  f.deliver(t.envelope());
  h.step(400);
  auto l = ui::analyseLayers(*h.ui().context());
  CHECK(l.overflows == 0);
  for (const auto& o : l.overflowing) UNSCOPED_INFO(o);
  f.system().drawer().close();
  f.deliver(syspreview::route(cadence_v2_ScreenRoute_SCREEN_ROUTE_SETTINGS));
  h.step(300);
  l = ui::analyseLayers(*h.ui().context());
  CHECK(l.overflows == 0);
  h.translator().set_language(cadence::i18n::Lang::Ru);
  h.step(50);
}

TEST_CASE("status and screen context name these routes and the drawer, debounced") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  const int before = f.link().count("screen_context");
  f.deliver(syspreview::route(cadence_v2_ScreenRoute_SCREEN_ROUTE_SETTINGS));
  h.step(100);
  app::UiStatus s;
  f.system().status(s);
  CHECK(s.route == cadence_v2_ScreenRoute_SCREEN_ROUTE_SETTINGS);
  CHECK_FALSE(s.drawerOpen);
  // nothing before 500 ms, one context after
  CHECK(f.link().count("screen_context") == before);
  h.step(500);
  CHECK(f.link().count("screen_context") == before + 1);
  const auto* c = f.link().last("screen_context");
  REQUIRE(c != nullptr);
  CHECK(c->route == cadence_v2_ScreenRoute_SCREEN_ROUTE_SETTINGS);
  // the mic button: ready opens the assistant, none opens room and microphone
  h.press(cadence_screen_v1_Key_KEY_BACK);
  f.system().device().mic = Mic::None;
  ui::Activation a;
  a.action = "mic:open";
  auto* any = h.ui().page()->GetElementById("films-0");
  a.element = any;
  // through the ui's activation path
  h.ui().focus().focus(any);
  any->SetAttribute("data-action", "mic:open");
  h.press(cadence_screen_v1_Key_KEY_OK);
  h.step(100);
  CHECK(f.system().onSettings(true));
}

TEST_CASE("airplay: the pin covers everything and BACK does not leave it; mirroring shows the badge and asks on BACK") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  cadence_screen_v1_Envelope pin = cadence_screen_v1_Envelope_init_zero;
  pin.which_body = cadence_screen_v1_Envelope_airplay_pin_tag;
  pin.body.airplay_pin.active = true;
  copyTo(pin.body.airplay_pin.pin, "4821");
  pin.body.airplay_pin.expires_at_ms = f.epoch() + 60000;
  f.deliver(pin);
  h.step(50);
  CHECK(f.system().airplay().pinShown());
  CHECK(f.system().route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_AIRPLAY);
  h.press(cadence_screen_v1_Key_KEY_BACK);
  CHECK(f.system().airplay().pinShown());
  pin.body.airplay_pin.active = false;
  f.deliver(pin);
  CHECK_FALSE(f.system().airplay().pinShown());

  cadence_screen_v1_Envelope m = cadence_screen_v1_Envelope_init_zero;
  m.which_body = cadence_screen_v1_Envelope_mirror_start_tag;
  copyTo(m.body.mirror_start.session_id, "m-1");
  copyTo(m.body.mirror_start.client_name, "iPhone");
  f.deliver(m);
  h.step(50);
  CHECK(f.system().airplay().mirroring());
  auto* badge = h.ui().documentNamed("airplay")->GetElementById("apbadge");
  REQUIRE(badge != nullptr);
  CHECK_FALSE(badge->IsClassSet("hidden"));
  h.press(cadence_screen_v1_Key_KEY_BACK);
  h.step(400);
  CHECK(h.ui().levelOpen());
  // the confirm's focus is on the safe choice
  CHECK(focusedId(h) == "dialog-cancel");
  h.press(cadence_screen_v1_Key_KEY_BACK);
  h.step(300);
  CHECK_FALSE(h.ui().levelOpen());
  cadence_screen_v1_Envelope stop = cadence_screen_v1_Envelope_init_zero;
  stop.which_body = cadence_screen_v1_Envelope_mirror_stop_tag;
  copyTo(stop.body.mirror_stop.session_id, "m-1");
  f.deliver(stop);
  CHECK_FALSE(f.system().airplay().mirroring());
}

namespace {

cadence_screen_v1_Envelope mirrorStart(const char* id) {
  cadence_screen_v1_Envelope m = cadence_screen_v1_Envelope_init_zero;
  m.which_body = cadence_screen_v1_Envelope_mirror_start_tag;
  copyTo(m.body.mirror_start.session_id, id);
  copyTo(m.body.mirror_start.client_name, "iPhone");
  return m;
}

cadence_screen_v1_Envelope commandResult(const std::string& id, const char* code) {
  cadence_screen_v1_Envelope r = cadence_screen_v1_Envelope_init_zero;
  r.which_body = cadence_screen_v1_Envelope_command_result_tag;
  copyTo(r.body.command_result.request_id, id);
  copyTo(r.body.command_result.code, code);
  return r;
}

// BACK over the mirror, → to the confirm, OK
void confirmStop(ui::preview::Harness& h) {
  h.press(cadence_screen_v1_Key_KEY_BACK);
  h.step(400);
  REQUIRE(h.ui().levelOpen());
  h.press(cadence_screen_v1_Key_KEY_RIGHT);
  REQUIRE(focusedId(h) == "dialog-confirm");
  h.press(cadence_screen_v1_Key_KEY_OK);
  h.step(300);
  CHECK_FALSE(h.ui().levelOpen());
}

bool unavailableShown(ui::preview::Harness& h) {
  auto* host = h.ui().overlay()->GetElementById("toasts");
  return host != nullptr && host->GetInnerRML().find("Сейчас недоступно") != std::string::npos;
}

}  // namespace

TEST_CASE("airplay: confirming the stop asks cadenced to end that mirror; only a failure shows a toast") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  h.ui().clearToasts();
  f.deliver(mirrorStart("airplay-2"));
  h.step(50);
  REQUIRE(f.system().airplay().mirroring());
  h.ui().clearToasts();
  confirmStop(h);
  const auto* sent = f.link().last("mirror_stop");
  REQUIRE(sent != nullptr);
  CHECK(sent->json == "airplay-2");
  CHECK_FALSE(sent->id.empty());
  // cadenced's ok: no toast, and the badge stays until audiod's end comes back as MirrorStop
  f.deliver(commandResult(sent->id, ""));
  h.step(50);
  CHECK_FALSE(unavailableShown(h));
  CHECK(f.system().airplay().mirroring());
  cadence_screen_v1_Envelope stop = cadence_screen_v1_Envelope_init_zero;
  stop.which_body = cadence_screen_v1_Envelope_mirror_stop_tag;
  copyTo(stop.body.mirror_stop.session_id, "airplay-2");
  copyTo(stop.body.mirror_stop.reason, "stopped");
  f.deliver(stop);
  h.step(50);
  CHECK_FALSE(f.system().airplay().mirroring());
  CHECK(h.ui().documentNamed("airplay")->GetElementById("apbadge")->IsClassSet("hidden"));

  // refused (the mirror was no longer audiod's session): the toast
  f.deliver(mirrorStart("airplay-3"));
  h.step(50);
  h.ui().clearToasts();
  confirmStop(h);
  const auto* refused = f.link().last("mirror_stop");
  REQUIRE(refused != nullptr);
  CHECK(refused->json == "airplay-3");
  // an answer to another request is not this one's
  f.deliver(commandResult("other", "failed"));
  h.step(50);
  CHECK_FALSE(unavailableShown(h));
  f.deliver(commandResult(refused->id, "no_session"));
  h.step(50);
  CHECK(unavailableShown(h));

  // no link: the toast at once
  h.ui().clearToasts();
  f.link().up = false;
  confirmStop(h);
  h.step(50);
  CHECK(unavailableShown(h));
  f.link().up = true;
  copyTo(stop.body.mirror_stop.session_id, "airplay-3");
  f.deliver(stop);
  h.ui().clearToasts();
}

namespace {

cadence_screen_v1_Envelope screenConfig(std::string& json) {
  cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
  e.which_body = cadence_screen_v1_Envelope_capability_config_tag;
  copyTo(e.body.capability_config.capability, "display.screen");
  e.body.capability_config.json = json.data();
  return e;
}

Json lastArgs(Fixture& f, std::string_view method) {
  const auto* s = f.link().last(method);
  return s != nullptr ? Json::parse(s->json) : Json();
}

}  // namespace

TEST_CASE("settings round trip: a change shows at once, goes out as settings.set, the config sync says it again; a refusal puts it back") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  auto& local = f.system().layer().state().local;
  local = LocalSettings{};
  f.system().device().followUpS = 6;
  f.deliver(syspreview::route(cadence_v2_ScreenRoute_SCREEN_ROUTE_SETTINGS));
  h.step(100);
  REQUIRE(f.system().onSettings());
  const auto before = f.link().count("settings.set");

  // subtitles' size: m -> l, written as display.screen's own key
  h.ui().focus().focus(h.ui().page()->GetElementById("set-sub-size"));
  h.press(cadence_screen_v1_Key_KEY_OK);
  h.step(100);
  CHECK(local.subtitleSize == "l");
  CHECK(f.link().count("settings.set") == before + 1);
  CHECK(lastArgs(f, "settings.set") == Json::parse(R"({"screen":{"subtitles":{"size":"l"}}})"));
  CHECK(focusedId(h) == "set-sub-size");
  f.answer("settings.set", "ok", R"({"applied":["screen.subtitles"]})");
  h.step(50);
  CHECK(local.subtitleSize == "l");

  // the follow-up window: 6 s -> 8 s, as the device's own setting
  h.ui().focus().focus(h.ui().page()->GetElementById("set-follow"));
  h.press(cadence_screen_v1_Key_KEY_OK);
  h.step(100);
  CHECK(lastArgs(f, "settings.set") == Json::parse(R"({"followUpWindowMs":8000})"));
  CHECK(f.system().device().followUpS == 8);

  // overscan refused: back to what it was, and the page says so
  h.ui().focus().focus(h.ui().page()->GetElementById("set-overscan"));
  h.press(cadence_screen_v1_Key_KEY_OK);
  h.step(100);
  CHECK(local.overscan == 2.5);
  CHECK(lastArgs(f, "settings.set") == Json::parse(R"({"screen":{"overscan":2.5}})"));
  f.answer("settings.set", "invalid", R"({"code":"invalid"})");
  h.step(100);
  CHECK(local.overscan == 0);

  // the language: switched live, and back when refused
  h.ui().focus().focus(h.ui().page()->GetElementById("set-lang"));
  h.press(cadence_screen_v1_Key_KEY_OK);
  h.step(100);
  CHECK(h.translator().language() == cadence::i18n::Lang::En);
  CHECK(lastArgs(f, "settings.set") == Json::parse(R"({"language":"en"})"));
  f.answer("settings.set", "unavailable", R"({"code":"unavailable"})");
  h.step(100);
  CHECK(h.translator().language() == cadence::i18n::Lang::Ru);

  // the server's config is the page's: a write from the dashboard (or the sync of this one) shows here
  std::string cfg = R"({"subtitles":{"size":"s","style":"outline"},"overscan":5,"autoSkipOpenings":false,"reducedMotion":true})";
  f.deliver(screenConfig(cfg));
  h.step(100);
  CHECK(local.subtitleSize == "s");
  CHECK(local.subtitleStyle == "outline");
  CHECK(local.overscan == 5);
  CHECK_FALSE(local.autoSkip);
  CHECK(local.reducedMotion);
  std::string back = R"({"reducedMotion":false})";
  f.deliver(screenConfig(back));
  h.step(50);
  CHECK_FALSE(local.reducedMotion);
}

TEST_CASE("the microphone choice goes after each Ready, and room and microphone changes it") {
  auto& h = harness();
  auto& f = fresh(h);
  f.system().layer().state().mic = MicPick{};
  f.online();
  const auto* first = f.link().last("mic_choice");
  REQUIRE(first != nullptr);
  CHECK(first->code == std::to_string(cadence_v2_MicSourceKind_MIC_SOURCE_KIND_UNSPECIFIED));
  Settings::openRoom(f.system().router());
  h.step(100);
  REQUIRE(f.system().onSettings(true));
  h.ui().focus().focus(h.ui().page()->GetElementById("room-usb"));
  h.press(cadence_screen_v1_Key_KEY_OK);
  h.step(100);
  const auto* local = f.link().last("mic_choice");
  REQUIRE(local != nullptr);
  CHECK(local->code == std::to_string(cadence_v2_MicSourceKind_MIC_SOURCE_KIND_LOCAL));
  CHECK(f.system().device().mic == Mic::Local);
  CHECK(h.ui().page()->GetElementById("room-usb")->QuerySelector(".trail") != nullptr);
  // a new link (cadenced restarted): the choice again after its Ready
  const auto sent = f.link().count("mic_choice");
  f.online();
  CHECK(f.link().count("mic_choice") == sent + 1);
  CHECK(f.link().last("mic_choice")->code == std::to_string(cadence_v2_MicSourceKind_MIC_SOURCE_KIND_LOCAL));
  h.ui().focus().focus(h.ui().page()->GetElementById("room-speaker"));
  h.press(cadence_screen_v1_Key_KEY_OK);
  h.step(100);
  CHECK(f.link().last("mic_choice")->code == std::to_string(cadence_v2_MicSourceKind_MIC_SOURCE_KIND_SPEAKER));
  CHECK(f.link().last("mic_choice")->json.empty());
  h.press(cadence_screen_v1_Key_KEY_BACK);
  h.step(100);
}

TEST_CASE("a settings write outlives its page: BACK right after a change does not cancel it on the server") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  auto& local = f.system().layer().state().local;
  local = LocalSettings{};
  f.deliver(syspreview::route(cadence_v2_ScreenRoute_SCREEN_ROUTE_SETTINGS));
  h.step(100);
  REQUIRE(f.system().onSettings());
  h.ui().focus().focus(h.ui().page()->GetElementById("set-sub-style"));
  h.press(cadence_screen_v1_Key_KEY_OK);
  h.step(50);
  const auto* write = f.link().last("settings.set");
  REQUIRE(write != nullptr);
  const std::string id = write->id;
  h.press(cadence_screen_v1_Key_KEY_BACK);
  h.step(100);
  CHECK_FALSE(f.system().onSettings());
  for (const auto& s : f.link().sent) CHECK_FALSE((s.method == "cancel" && s.id == id));
  // a refusal after the page went still puts the value back
  f.answer("settings.set", "invalid", R"({"code":"invalid"})");
  h.step(50);
  CHECK(local.subtitleStyle == "box");
}
