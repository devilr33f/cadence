#include "app/routes/system/tests/support.h"

#include "ui/core/budget.h"

#include <cstring>

using namespace cadence::screen;
using namespace cadence::screen::routes;
using namespace cadence::screen::routes::systest;

namespace {

void answerInvoke(Fixture& f) { f.answer("assistant.invoke", "ok", "{\"turnId\":\"turn-1\",\"micDeviceId\":\"speaker-1\"}"); }

Turn turn(cadence_v2_AssistantState s, std::uint32_t rev) {
  Turn t;
  t.state = s;
  t.revision = rev;
  t.stable = "Включи свет";
  return t;
}

bool open(Fixture& f) { return f.system().drawer().isOpen(); }

}  // namespace

TEST_CASE("G opens the drawer from anywhere, listening at once, with «Стоп» focused, and BACK puts focus back") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  h.ui().focus().focus(h.ui().page()->GetElementById("films-1"));
  h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
  REQUIRE(open(f));
  CHECK(f.system().drawer().focused());
  CHECK(focusedId(h) == "dbtn");
  CHECK(inDoc(focused(h), "drawer"));
  CHECK(f.system().drawer().state() == cadence_v2_AssistantState_ASSISTANT_STATE_INVOKED);
  // the press asks the server to open the room's microphone, and the screen context says so at once
  const auto* inv = f.link().last("assistant.invoke");
  REQUIRE(inv != nullptr);
  const auto* ctx = f.link().last("screen_context");
  REQUIRE(ctx != nullptr);
  CHECK(ctx->drawerOpen);
  app::UiStatus st;
  f.system().status(st);
  CHECK(st.drawerOpen);

  answerInvoke(f);
  Turn t = turn(cadence_v2_AssistantState_ASSISTANT_STATE_LISTENING, 1);
  f.deliver(t.envelope());
  h.step(100);
  CHECK(f.system().drawer().turnId() == "turn-1");
  CHECK(focusedId(h) == "dbtn");

  // BACK while listening cancels the turn and closes; focus goes back to what it was on
  h.press(cadence_screen_v1_Key_KEY_BACK);
  h.step(300);
  CHECK_FALSE(open(f));
  CHECK(f.link().last("assistant.stop") != nullptr);
  CHECK(focusedId(h) == "films-1");
  // the page behind never saw the BACK
  CHECK(h.ui().page()->GetElementById("films-1") != nullptr);
}

TEST_CASE("the key-opened walk: thinking cancels, speaking and follow-up keep focus out of cards, ↑ takes it in") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  h.ui().focus().focus(h.ui().page()->GetElementById("films-0"));
  h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
  answerInvoke(f);
  Turn t = turn(cadence_v2_AssistantState_ASSISTANT_STATE_THINKING, 2);
  t.steps = {{"s1", "Свет в гостиной", cadence_v2_StepStatus_STEP_STATUS_RUNNING}};
  f.deliver(t.envelope());
  h.step(100);
  // no buttons during a turn: the foot holds the focus quietly, back cancels
  CHECK(focusedId(h) == "dbtn");
  {
    Rml::ElementList buttons;
    h.ui().documentNamed("drawer")->GetElementById("dfootb")->QuerySelectorAll(buttons, "[data-action]");
    CHECK(buttons.empty());
  }
  t.state = cadence_v2_AssistantState_ASSISTANT_STATE_SPEAKING;
  t.revision = 3;
  t.reply = "Включил свет в гостиной.";
  t.cards = {"card_light_living.binpb"};
  f.deliver(t.envelope());
  h.step(100);
  CHECK(f.system().drawer().cards().count() == 1);
  // focus never moves into cards while speaking
  h.press(cadence_screen_v1_Key_KEY_UP);
  CHECK(focusedId(h) == "dbtn");
  t.state = cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP;
  t.revision = 4;
  t.followUpMs = 6000;
  f.deliver(t.envelope());
  h.step(100);
  // follow-up: the foot keeps focus quietly (no ring on a card unasked); ↑ takes it into the card
  CHECK(focusedId(h) == "dbtn");
  h.press(cadence_screen_v1_Key_KEY_UP);
  auto* toggle = focused(h);
  REQUIRE(toggle != nullptr);
  CHECK(toggle->GetAttribute<Rml::String>("data-kind", "") == "toggle");
  CHECK(inDoc(toggle, "drawer"));
  // OK on the toggle: a card action from the drawer's own host
  h.press(cadence_screen_v1_Key_KEY_OK);
  const auto* act = f.link().last("card.action");
  REQUIRE(act != nullptr);
  CHECK(act->id.starts_with("drw:"));
  CHECK(act->json.find("\"actionId\":\"a:7f3c\"") != std::string::npos);
  // the window shrinks; BACK in follow-up closes
  h.press(cadence_screen_v1_Key_KEY_BACK);
  h.step(300);
  CHECK_FALSE(open(f));
  CHECK(focusedId(h) == "films-0");
}

TEST_CASE("the wake-word walk: no focus, the page keeps its keys, ↓ takes focus in, BACK closes without acting behind") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  h.ui().focus().focus(h.ui().page()->GetElementById("films-1"));
  Turn t = turn(cadence_v2_AssistantState_ASSISTANT_STATE_LISTENING, 1);
  t.trigger = cadence_v2_Trigger_TRIGGER_WAKE;
  f.deliver(t.envelope());
  h.step(400);
  REQUIRE(open(f));
  CHECK_FALSE(f.system().drawer().focused());
  CHECK(focusedId(h) == "films-1");
  // no hint during a turn: the foot has no buttons to choose
  auto* hint = h.ui().documentNamed("drawer")->GetElementById("dhint");
  REQUIRE(hint != nullptr);
  CHECK(hint->IsClassSet("hidden"));
  // ←/→ still do what they did
  h.press(cadence_screen_v1_Key_KEY_RIGHT);
  CHECK(focusedId(h) == "films-2");
  // the first ↓: focus into the drawer, on the foot's quiet anchor with no card
  h.press(cadence_screen_v1_Key_KEY_DOWN);
  CHECK(f.system().drawer().focused());
  CHECK(focusedId(h) == "dbtn");
  CHECK(hint->IsClassSet("hidden"));
  h.press(cadence_screen_v1_Key_KEY_BACK);
  h.step(300);
  CHECK_FALSE(open(f));
  CHECK(focusedId(h) == "films-2");
}

TEST_CASE("a wake-word drawer with a card: G enters on the foot, as ↓ does; ↑ reaches the card's control") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  h.ui().focus().focus(h.ui().page()->GetElementById("films-0"));
  Turn t = turn(cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP, 1);
  t.trigger = cadence_v2_Trigger_TRIGGER_WAKE;
  t.reply = "Включил.";
  t.cards = {"card_light_living.binpb"};
  f.deliver(t.envelope());
  h.step(300);
  REQUIRE(open(f));
  CHECK(focusedId(h) == "films-0");
  // G takes focus in while the drawer has no focus (it does not start a new turn)
  const int invokes = f.link().count("assistant.invoke");
  h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
  CHECK(f.link().count("assistant.invoke") == invokes);
  // a card never takes focus on its own: the foot's button has it, ↑ enters the card
  CHECK(f.system().drawer().focused());
  CHECK(focusedId(h) == "dbtn");
  h.press(cadence_screen_v1_Key_KEY_UP);
  auto* e = focused(h);
  REQUIRE(e != nullptr);
  CHECK(e->GetAttribute<Rml::String>("data-kind", "") == "toggle");
  // G again inside the drawer: a new turn without closing
  h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
  CHECK(f.link().count("assistant.invoke") == invokes + 1);
  CHECK(open(f));
}

TEST_CASE("BACK while speaking stops speech first and closes second") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
  answerInvoke(f);
  Turn t = turn(cadence_v2_AssistantState_ASSISTANT_STATE_SPEAKING, 2);
  t.reply = "Сейчас двадцать два градуса, к вечеру похолодает до пятнадцати.";
  f.deliver(t.envelope());
  h.step(200);
  const int stops = f.link().count("assistant.stop");
  h.press(cadence_screen_v1_Key_KEY_BACK);
  CHECK(open(f));
  CHECK(f.link().count("assistant.stop") == stops + 1);
  h.press(cadence_screen_v1_Key_KEY_BACK);
  h.step(300);
  CHECK_FALSE(open(f));
}

TEST_CASE("the reply is revealed by the server's word timings, else 3 words a second") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
  answerInvoke(f);
  Turn t = turn(cadence_v2_AssistantState_ASSISTANT_STATE_SPEAKING, 2);
  // 6 words, no timings
  t.reply = "один два три четыре пять шесть";
  f.deliver(t.envelope());
  h.step(16);
  // the first word (and its space)
  CHECK(f.system().drawer().revealed() <= 5);
  h.step(700);
  // ~0.7 s: three words
  const auto mid = f.system().drawer().revealed();
  CHECK(mid >= 9);
  CHECK(mid < 20);
  h.step(2000);
  CHECK(f.system().drawer().revealed() == runeCount(t.reply));

  // with timings: the second word at 1 s
  Turn u = turn(cadence_v2_AssistantState_ASSISTANT_STATE_SPEAKING, 1);
  u.id = "turn-2";
  u.reply = "раз два";
  cadence_v2_WordTiming w1 = cadence_v2_WordTiming_init_zero;
  w1.text_start = 0;
  w1.text_end = 3;
  w1.start_ms = 0;
  cadence_v2_WordTiming w2 = cadence_v2_WordTiming_init_zero;
  w2.text_start = 4;
  w2.text_end = 7;
  w2.start_ms = 1000;
  u.words = {w1, w2};
  f.deliver(u.envelope());
  h.step(500);
  CHECK(f.system().drawer().revealed() == 3);
  h.step(700);
  CHECK(f.system().drawer().revealed() == 7);
}

TEST_CASE("the listening line follows the room microphone's level with a fast attack") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
  answerInvoke(f);
  Turn t = turn(cadence_v2_AssistantState_ASSISTANT_STATE_LISTENING, 1);
  f.deliver(t.envelope());
  h.step(100);
  CHECK(f.system().drawer().lineDp() < 6);
  f.deliver(syspreview::voiceLevel("turn-1", 255, 0));
  h.step(160);
  CHECK(f.system().drawer().lineDp() > 20);
  // release over 200 ms
  f.deliver(syspreview::voiceLevel("turn-1", 0, 0));
  h.step(64);
  CHECK(f.system().drawer().lineDp() > 10);
  h.step(1000);
  CHECK(f.system().drawer().lineDp() < 5);
}

TEST_CASE("no microphone, a speaker offline and no server each open their own state, with no listening line") {
  auto& h = harness();
  {
    auto& f = fresh(h);
    f.online();
    f.system().device().mic = Mic::None;
    h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
    CHECK(f.system().drawer().view() == Drawer::View::NoMic);
    CHECK(f.link().last("assistant.invoke") == nullptr);
    CHECK(f.system().drawer().lineDp() == 0);
    // «Как подключить»: a qr to the setup page
    h.press(cadence_screen_v1_Key_KEY_OK);
    CHECK(f.system().drawer().view() == Drawer::View::SetupQr);
  }
  {
    auto& f = fresh(h);
    f.online();
    h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
    f.answer("assistant.invoke", "unavailable", "{\"code\":\"mic_offline\"}");
    CHECK(f.system().drawer().view() == Drawer::View::MicOffline);
  }
  {
    auto& f = fresh(h);
    f.deliver(syspreview::linkStatus(cadence_screen_v1_LinkState_LINK_STATE_OFFLINE, "unreachable"));
    h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
    CHECK(f.system().drawer().view() == Drawer::View::Offline);
    CHECK(f.link().last("assistant.invoke") == nullptr);
    h.step(100);
    CHECK(f.system().drawer().lineDp() == 0);
    // a closed drawer keeps its press: the link coming up opens nothing
    h.press(cadence_screen_v1_Key_KEY_BACK);
    h.step(300);
    CHECK_FALSE(open(f));
    f.online();
    CHECK(f.link().last("assistant.invoke") == nullptr);
  }
}

// a cold start's press can outrun the link: offline at once, then the invoke goes out as the link arrives
TEST_CASE("a press with no link asks again when the link comes up") {
  auto& h = harness();
  auto& f = fresh(h);
  f.deliver(syspreview::linkStatus(cadence_screen_v1_LinkState_LINK_STATE_OFFLINE, "unreachable"));
  h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
  REQUIRE(f.system().drawer().view() == Drawer::View::Offline);
  CHECK(f.link().last("assistant.invoke") == nullptr);
  // ready and online: the drawer keeps its open state and asks now
  f.online();
  CHECK(open(f));
  const auto* inv = f.link().last("assistant.invoke");
  REQUIRE(inv != nullptr);
  CHECK(f.system().drawer().view() == Drawer::View::Turn);
  CHECK(f.system().drawer().state() == cadence_v2_AssistantState_ASSISTANT_STATE_INVOKED);
  // the answer arrives as any other
  answerInvoke(f);
  CHECK(f.system().drawer().turnId() == "turn-1");
}

TEST_CASE("nothing heard says so and closes after 4 s; resting closes after 20 s without a key") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
  answerInvoke(f);
  Turn t = turn(cadence_v2_AssistantState_ASSISTANT_STATE_LISTENING, 2);
  t.error = "not_heard";
  f.deliver(t.envelope());
  CHECK(f.system().drawer().view() == Drawer::View::NotHeard);
  h.step(3500);
  CHECK(open(f));
  h.step(800);
  CHECK_FALSE(open(f));

  h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
  answerInvoke(f);
  Turn r = turn(cadence_v2_AssistantState_ASSISTANT_STATE_RESTING, 3);
  r.id = "turn-3";
  r.cards = {"card_light_living.binpb"};
  f.deliver(r.envelope());
  h.step(19000);
  CHECK(open(f));
  h.step(1500);
  CHECK_FALSE(open(f));
  // privacy: nothing of the turn is left behind
  CHECK(f.system().drawer().turnId().empty());
  CHECK(f.system().drawer().cards().count() == 0);
}

TEST_CASE("a failed turn names the model timeout and keeps retry as its one action") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
  answerInvoke(f);
  Turn t = turn(cadence_v2_AssistantState_ASSISTANT_STATE_RESTING, 2);
  t.error = "timeout";
  cadence_v2_TurnFailure failure = cadence_v2_TurnFailure_init_zero;
  std::strcpy(failure.code, "timeout");
  std::strcpy(failure.stage, "llm");
  std::strcpy(failure.provider, "gateway-main");
  std::strcpy(failure.model, "vendor/model-x");
  failure.step = 1;
  failure.waited_ms = 10001;
  failure.limit_ms = 10000;
  t.failure = failure;
  f.deliver(t.envelope());

  CHECK(f.system().drawer().view() == Drawer::View::Failed);
  auto* doc = h.ui().documentNamed("drawer");
  REQUIRE(doc->QuerySelector(".dstate .sbody") != nullptr);
  CHECK(doc->QuerySelector(".dstate .sbody")->GetInnerRML().find("Модель не ответила за 10 с") != std::string::npos);
  Rml::ElementList codes;
  doc->QuerySelectorAll(codes, ".dstate .scode");
  REQUIRE(codes.size() == 2);
  CHECK(codes[0]->GetInnerRML() == "gateway-main · vendor/model-x");
  CHECK(codes[1]->GetInnerRML() == "llm · timeout · 10.0 s");
  Rml::ElementList buttons;
  doc->GetElementById("dfootb")->QuerySelectorAll(buttons, "[data-action]");
  REQUIRE(buttons.size() == 1);
  CHECK(buttons[0]->GetAttribute<Rml::String>("data-action", "") == "drawer:retry");
  CHECK(buttons[0]->GetInnerRML().find("Повторить") != std::string::npos);
}

TEST_CASE("citation markers: the sources go to the showcase, ↑ enters the reply, a marker lights and opens its source") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
  answerInvoke(f);
  Turn t = turn(cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP, 2);
  t.reply = "Третий сезон выйдет в октябре [1], он в списке премьер [2].";
  t.citations = {{1, "web.answer.w7"}, {2, "web.answer.w7"}};
  t.cards = {"card_web_answer.binpb"};
  t.followUpMs = 60000;
  f.deliver(t.envelope());
  h.step(200);
  // a content card is the showcase's, at its front (the reply cites it); the chat keeps a line of it
  auto& show = f.system().drawer().showcase();
  CHECK(show.has("web.answer.w7"));
  CHECK(show.reel().current() == "web.answer.w7");
  CHECK(f.system().drawer().cards().count() == 0);
  Rml::ElementList refs;
  h.ui().documentNamed("drawer")->QuerySelectorAll(refs, ".ci.ref .sref");
  CHECK(refs.size() == 1);
  // follow-up keeps focus on the foot; ↑ takes it into the reply's markers
  CHECK(focusedId(h) == "dbtn");
  h.press(cadence_screen_v1_Key_KEY_UP);
  auto* m = focused(h);
  REQUIRE(m != nullptr);
  REQUIRE(m->IsClassSet("cmark"));
  const auto n = m->GetAttribute<Rml::String>("data-cite", "");
  h.press(n == "1" ? cadence_screen_v1_Key_KEY_RIGHT : cadence_screen_v1_Key_KEY_LEFT);
  auto* m2 = focused(h);
  REQUIRE(m2 != nullptr);
  CHECK(m2->IsClassSet("cmark"));
  CHECK(m2->GetAttribute<Rml::String>("data-cite", "") != n);
  // the focused marker lights its numbered link in the showcase
  const auto n2 = m2->GetAttribute<Rml::String>("data-cite", "");
  Rml::ElementList lit;
  h.ui().documentNamed("drawer")->QuerySelectorAll(lit, ".sslot .clink .cite.on");
  REQUIRE(lit.size() == 1);
  CHECK(lit[0]->GetParentNode()->GetAttribute<Rml::String>("data-cite", "") == n2);
  // OK on a marker takes focus to that link; BACK brings it back to the drawer
  h.press(cadence_screen_v1_Key_KEY_OK);
  auto* link = focused(h);
  REQUIRE(link != nullptr);
  CHECK(link->IsClassSet("clink"));
  CHECK(link->GetAttribute<Rml::String>("data-cite", "") == n2);
  h.press(cadence_screen_v1_Key_KEY_BACK);
  h.step(100);
  CHECK(open(f));
  auto* back = focused(h);
  REQUIRE(back != nullptr);
  CHECK(back->IsClassSet("cmark"));
}

TEST_CASE("a reset turn fades the old chat and leaves its confirmation") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
  answerInvoke(f);
  Turn first = turn(cadence_v2_AssistantState_ASSISTANT_STATE_RESTING, 1);
  first.reply = "Первый ответ";
  f.deliver(first.envelope());
  Turn second = turn(cadence_v2_AssistantState_ASSISTANT_STATE_SPEAKING, 1);
  second.id = "turn-2";
  second.stable = "Ещё вопрос";
  second.reply = "Старый ответ";
  f.deliver(second.envelope());
  h.step(300);
  auto& d = f.system().drawer();
  REQUIRE(d.log().rounds().size() == 2);
  const std::string oldKey = "r" + std::to_string(d.log().rounds().front().key) + "-r";
  REQUIRE(d.chat().element(oldKey) != nullptr);

  // no reset button on the tv (the owner's call): a reset comes by voice or the dashboard, as a marked turn
  CHECK(h.ui().documentNamed("drawer")->GetElementById("dreset") == nullptr);

  second.revision = 2;
  second.state = cadence_v2_AssistantState_ASSISTANT_STATE_RESTING;
  second.stable.clear();
  second.reply = "Начинаем заново.";
  second.conversationReset = true;
  f.deliver(second.envelope());
  REQUIRE(d.log().rounds().size() == 1);
  REQUIRE(d.chat().element(oldKey) != nullptr);
  h.step(60);
  CHECK(d.chat().opacity(oldKey) < 1.0);
  h.step(200);
  CHECK(d.chat().element(oldKey) == nullptr);
  REQUIRE(d.log().rounds().size() == 1);
  const std::string newKey = "r" + std::to_string(d.log().live().key) + "-r";
  auto* line = d.chat().element(newKey);
  REQUIRE(line != nullptr);
  CHECK(line->GetInnerRML().find("Начинаем заново.") != std::string::npos);
  CHECK(line->GetInnerRML().find("Старый ответ") == std::string::npos);
}

TEST_CASE("long BACK opens the drawer, and the mic glyph shows while BACK is held") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  h.key(cadence_screen_v1_Key_KEY_BACK, cadence_screen_v1_KeyAction_KEY_ACTION_DOWN);
  auto* glyph = h.ui().documentNamed("drawer")->GetElementById("backhint");
  REQUIRE(glyph != nullptr);
  CHECK(glyph->IsClassSet("on"));
  h.key(cadence_screen_v1_Key_KEY_ASSISTANT, cadence_screen_v1_KeyAction_KEY_ACTION_LONG);
  CHECK_FALSE(glyph->IsClassSet("on"));
  CHECK(open(f));
  CHECK(f.link().last("assistant.invoke") != nullptr);
}

TEST_CASE("the drawer over a film stays within two full-screen translucent layers, and in xx nothing wraps") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
  answerInvoke(f);
  Turn t = turn(cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP, 2);
  t.reply = "Включил.";
  t.cards = {"card_light_living.binpb"};
  f.deliver(t.envelope());
  h.step(500);
  const auto l = ui::analyseLayers(*h.ui().context());
  CHECK(l.translucentLayers <= 2);
  // the tasks panel under the drawer gives up its scrim: still two at most
  static auto list = syspreview::loadPage("page_tasks.binpb");
  f.system().tasks().showList(*list);
  f.system().tasks().openList();
  f.system().tasks().setUnderDrawer(true);
  h.step(500);
  CHECK(ui::analyseLayers(*h.ui().context()).translucentLayers <= 2);
}

TEST_CASE("a press whose turn speaks with a card at once keeps focus on the foot; ↑ reaches the card") {
  // regression: the press entered the drawer before its turn had a foot button, and the card fallback it deferred
  // took focus from «Стоп» once the card arrived (the system gallery's drawer-speaking walk missed #dbtn)
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  f.system().drawer().invoke();
  answerInvoke(f);
  Turn t = turn(cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP, 1);
  t.reply = "Включил свет в гостиной.";
  t.cards = {"card_light_living.binpb"};
  t.followUpMs = 6000;
  f.deliver(t.envelope());
  h.step(600);
  REQUIRE(open(f));
  CHECK(focusedId(h) == "dbtn");
  h.press(cadence_screen_v1_Key_KEY_UP);
  auto* toggle = focused(h);
  REQUIRE(toggle != nullptr);
  CHECK(toggle->GetAttribute<Rml::String>("data-kind", "") == "toggle");
}
