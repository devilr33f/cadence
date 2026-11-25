#include "app/routes/system/chat.h"
#include "app/routes/system/drawer.h"
#include "app/routes/system/tests/support.h"

#include <catch2/matchers/catch_matchers_floating_point.hpp>

#include <algorithm>
#include <string>
#include <vector>

using namespace cadence::screen;
using namespace cadence::screen::routes;
using namespace cadence::screen::routes::systest;
using Catch::Matchers::WithinAbs;

namespace {

std::vector<std::string> keys(const std::vector<chat::Item>& items) {
  std::vector<std::string> out;
  for (const auto& i : items) out.push_back(i.key);
  return out;
}

chat::LiveView speakingView() {
  chat::LiveView v;
  v.reply = true;
  return v;
}

void answerInvoke(Fixture& f) { f.answer("assistant.invoke", "ok", "{\"turnId\":\"turn-1\",\"micDeviceId\":\"speaker-1\"}"); }

// the chat's items in the drawer's dom, oldest first: their keys
std::vector<std::string> domKeys(ui::preview::Harness& h) {
  std::vector<std::string> out;
  auto* list = h.ui().documentNamed("drawer")->GetElementById("dmsgs");
  for (int i = 0; i < list->GetNumChildren(); ++i) out.push_back(list->GetChild(i)->GetAttribute<Rml::String>("data-key", ""));
  return out;
}

Rml::Element* item(ui::preview::Harness& h, const std::string& key) {
  auto* list = h.ui().documentNamed("drawer")->GetElementById("dmsgs");
  for (int i = 0; i < list->GetNumChildren(); ++i) {
    if (list->GetChild(i)->GetAttribute<Rml::String>("data-key", "") == key) return list->GetChild(i);
  }
  return nullptr;
}

std::string prop(Rml::Element* e, const char* name) {
  const auto* p = e != nullptr ? e->GetLocalProperty(name) : nullptr;
  return p != nullptr ? p->ToString() : "";
}

double opacityOf(Rml::Element* e) {
  const auto s = prop(e, "opacity");
  return s.empty() ? 1.0 : std::stod(s);
}

}  // namespace

TEST_CASE("chat: a round reads user, steps, cards where their tool ran, the reply, then its sources") {
  chat::Log log;
  auto& r = log.next("turn-1");
  r.stable = "Включи свет";
  r.steps = {{"s1", "Свет", cadence_v2_StepStatus_STEP_STATUS_DONE}};
  // a tool's card before the reply showed, a source after it
  log.place("ha.light", false);
  r.reply = "Включил [1].";
  r.cited = true;
  log.place("web.w7", true);
  const auto k = "r" + std::to_string(log.live().key);
  CHECK(keys(log.items(speakingView())) ==
        std::vector<std::string>{k + "-u", k + "-s", k + "-c-ha.light", k + "-r", k + "-src", k + "-c-web.w7"});
  // before the reply sounds it is not there; a state block (a failed turn) comes last
  chat::LiveView thinking;
  thinking.state = true;
  CHECK(keys(log.items(thinking)) == std::vector<std::string>{k + "-u", k + "-s", k + "-c-ha.light", k + "-c-web.w7", k + "-st"});
  // listening with nothing heard yet: the bubble's caption alone
  chat::Log fresh;
  chat::LiveView listening;
  listening.listening = true;
  CHECK(keys(fresh.items(listening)) == std::vector<std::string>{"r" + std::to_string(fresh.live().key) + "-u"});
  CHECK(fresh.items(chat::LiveView{}).empty());
}

TEST_CASE("chat: while the assistant thinks a pending reply sits where the reply will come, until it does") {
  chat::Log log;
  auto& r = log.next("turn-1");
  r.stable = "Найди аниме";
  r.steps = {{"s1", "Ищу в каталоге", cadence_v2_StepStatus_STEP_STATUS_DONE}};
  log.place("catalog.found", false);
  const auto k = "r" + std::to_string(log.live().key);
  chat::LiveView thinking;
  thinking.thinking = true;
  CHECK(keys(log.items(thinking)) == std::vector<std::string>{k + "-u", k + "-s", k + "-c-catalog.found", k + "-t"});
  // the reply replaces it once it sounds; an older round never keeps one
  r.reply = "Нашёл.";
  thinking.reply = true;
  CHECK(keys(log.items(thinking)) == std::vector<std::string>{k + "-u", k + "-s", k + "-c-catalog.found", k + "-r"});
  log.next("turn-2").stable = "А второй сезон";
  chat::LiveView next;
  next.thinking = true;
  const auto k2 = "r" + std::to_string(log.live().key);
  const auto items = log.items(next);
  CHECK(keys(items).back() == k2 + "-t");
  CHECK(std::count_if(items.begin(), items.end(), [](const chat::Item& i) { return i.kind == chat::Kind::Thinking; }) == 1);
}

TEST_CASE("chat: a step whose card stands in the chat is that card; one whose card went left points to it") {
  chat::Log log;
  auto& r = log.next("turn-1");
  r.stable = "Включи музыку";
  r.steps = {{"c1", "Ищу музыку · 4 результата", cadence_v2_StepStatus_STEP_STATUS_DONE},
             {"c2", "Загружаю «Toxicity»", cadence_v2_StepStatus_STEP_STATUS_RUNNING}};
  r.steps[0].cardId = "call-search";
  r.steps[1].cardId = "call-play";
  log.ref("call-search", "<div/>");
  log.place("call-play", false);
  const auto k = "r" + std::to_string(log.live().key);
  log.placeSteps([](std::string_view id) { return id == "call-play"; }, [](std::string_view id) { return id == "call-search"; });
  CHECK(log.live().steps[0].left);
  CHECK_FALSE(log.live().steps[0].inCard);
  CHECK(log.live().steps[1].inCard);
  // the search's line stands for its showcase line; the play's card stands for its line
  CHECK(keys(log.items(chat::LiveView{})) == std::vector<std::string>{k + "-u", k + "-s", k + "-c-call-play"});
  // a showcase line whose card the showcase let go (a new turn, the turn's media) goes with it
  log.ref("gone-card", "<div/>");
  log.placeSteps([](std::string_view id) { return id == "call-play"; }, [](std::string_view id) { return id == "call-search"; });
  CHECK(log.live().refs.empty());
  // every step drawn as its card: no steps block at all
  log.live().steps.erase(log.live().steps.begin());
  log.placeSteps([](std::string_view) { return true; }, [](std::string_view) { return false; });
  CHECK(keys(log.items(chat::LiveView{})) == std::vector<std::string>{k + "-u", k + "-c-call-play"});
}

TEST_CASE("chat: a new turn keeps the last as history, newest last; an empty one is reused; at most 8 rounds") {
  chat::Log log;
  log.next("turn-1").stable = "раз";
  const auto first = log.live().key;
  log.next("turn-2");
  REQUIRE(log.rounds().size() == 2);
  CHECK(log.rounds()[0].stable == "раз");
  CHECK(log.age(0) == 1);
  CHECK(log.age(1) == 0);
  // nothing said in turn 2: turn 3 reuses its round, under a key of its own
  const auto second = log.live().key;
  log.next("turn-3");
  CHECK(log.rounds().size() == 2);
  CHECK(log.live().turnId == "turn-3");
  CHECK(log.live().key == second);
  CHECK(first != second);
  for (int i = 0; i < 12; ++i) log.next("t" + std::to_string(i)).reply = "ok";
  CHECK(log.rounds().size() == chat::kMaxRounds);
  // the oldest left the top: the newest are kept, in order
  CHECK(log.live().turnId == "t11");
  CHECK(log.rounds().front().turnId == "t" + std::to_string(12 - chat::kMaxRounds));
  log.clear();
  CHECK(log.rounds().size() == 1);
  CHECK(log.live().empty());
}

TEST_CASE("chat: a card shown again in a later round moves down to it; one the host let go leaves") {
  chat::Log log;
  log.next("turn-1").stable = "Включи свет";
  log.place("ha.light", false);
  log.next("turn-2").stable = "Ярче";
  CHECK(log.roundOf("ha.light") == 0u);
  log.place("ha.light", true);
  CHECK(log.roundOf("ha.light") == 1u);
  CHECK(log.rounds()[0].cards.empty());
  const auto items = log.items(speakingView());
  REQUIRE(!items.empty());
  CHECK(items.back().cardId == "ha.light");
  CHECK(items.back().round == 1);
  log.prune([](std::string_view id) { return id != "ha.light"; });
  CHECK_FALSE(log.roundOf("ha.light").has_value());
}

TEST_CASE("chat: each older round is smaller and dimmer; the live question settles once the answer shows") {
  double scale = 2, opacity = 2;
  for (std::size_t age = 0; age < 6; ++age) {
    const auto l = chat::look(age, chat::Kind::Reply, false);
    CHECK(l.scale <= scale);
    CHECK(l.opacity <= opacity);
    CHECK(l.scale >= 0.6);
    CHECK(l.opacity >= 0.5);
    scale = l.scale;
    opacity = l.opacity;
  }
  CHECK(chat::look(0, chat::Kind::Reply, false).scale == 1);
  CHECK(chat::look(1, chat::Kind::User, false).scale < 1);
  // the live round's own words step back as the reply comes in; its cards and reply stay whole
  CHECK(chat::look(0, chat::Kind::User, true).scale < 1);
  CHECK(chat::look(0, chat::Kind::User, true).scale > chat::look(1, chat::Kind::User, false).scale);
  CHECK(chat::look(0, chat::Kind::Card, true).scale == 1);
  CHECK(chat::look(0, chat::Kind::Reply, true).opacity == 1);
  // past the third round they keep the last step
  CHECK(chat::look(3, chat::Kind::Reply, false).scale == chat::look(7, chat::Kind::Reply, false).scale);
}

TEST_CASE("chat: the stack keeps the newest on its place and closes the room older items give up") {
  // three items 100, 50 and 80 high, 20 apart
  const std::vector<chat::Box> boxes{{0, 100}, {120, 50}, {190, 80}};
  const std::vector<chat::Look> whole{{1, 1}, {1, 1}, {1, 1}};
  const auto same = chat::bottoms(boxes, whole, 0);
  CHECK_THAT(same[0], WithinAbs(100, 1e-9));
  CHECK_THAT(same[1], WithinAbs(170, 1e-9));
  CHECK_THAT(same[2], WithinAbs(270, 1e-9));
  // the two older ones at half size: each visual gap is its item's share, the newest does not move
  const std::vector<chat::Look> aged{{0.5, 1}, {0.5, 1}, {1, 1}};
  const auto b = chat::bottoms(boxes, aged, 0);
  CHECK_THAT(b[2], WithinAbs(270, 1e-9));
  const double top1 = b[1] - 0.5 * 50;
  CHECK_THAT(b[1], WithinAbs(190 - 20 * 0.5, 1e-9));
  CHECK_THAT(top1 - b[0], WithinAbs(20 * 0.5, 1e-9));
  // the history scrolled back moves everything down alike
  const auto off = chat::bottoms(boxes, aged, 40);
  for (std::size_t i = 0; i < 3; ++i) CHECK_THAT(off[i] - b[i], WithinAbs(40, 1e-9));
}

TEST_CASE("chat: a tween eases cubic-out and a retarget mid-way starts from where it is") {
  chat::Tween t;
  t.snap(0);
  t.retarget(100, 1000, 280);
  CHECK(t.at(1000) == 0);
  CHECK(t.at(1140) > 50);  // cubic-out: most of the way at half time
  CHECK(t.at(1280) == 100);
  CHECK(t.done(1280));
  t.snap(0);
  t.retarget(100, 0, 280);
  const double mid = t.at(100);
  t.retarget(-50, 100, 280);
  CHECK(t.at(100) == mid);
  CHECK(t.at(380) == -50);
  // a zero duration (reduced motion) is there at once
  t.retarget(10, 400, 0);
  CHECK(t.at(400) == 10);
}

TEST_CASE("chat: a partial transcript's kept words keep their birth, new and changed ones are born now") {
  chat::Fade f;
  f.update("Включи свет", 100);
  CHECK(f.born(0) == 100);
  f.update("Включи свет в гости", 200);
  CHECK(f.born(0) == 100);
  CHECK(f.born(1) == 100);
  CHECK(f.born(2) == 200);
  CHECK(f.born(3) == 200);
  // the recognizer grew the last word: it keeps its birth
  f.update("Включи свет в гостиной", 300);
  CHECK(f.born(3) == 200);
  // it changed its mind about a word: that word and the rest are new
  f.update("Включи свет на кухне", 400);
  CHECK(f.born(1) == 100);
  CHECK(f.born(2) == 400);
  CHECK(f.born(3) == 400);
  CHECK(chat::sharedWords("a b c", "a b d") == 2);

  // the markup: stable then unstable, the young words in spans of their birth, every space inside a span
  chat::Fade g;
  g.update("один два", 1000);
  g.update("один два три четыре", 1200);
  const auto m = chat::wordsMarkup("один два три", "четыре", g, 1250, 280);
  CHECK(m == "<span class=\"stable fresh\" data-born=\"1000\">один два </span>"
             "<span class=\"stable fresh\" data-born=\"1200\">три </span>"
             "<span class=\"unstable fresh\" data-born=\"1200\">четыре</span>");
  // once faded, plain
  CHECK(chat::wordsMarkup("один два три", "четыре", g, 2000, 280) ==
        "<span class=\"stable\">один два три </span><span class=\"unstable\">четыре</span>");
  CHECK(chat::wordsMarkup("", "", g, 2000, 280).empty());
}

TEST_CASE("chat: the reply's newest revealed words sit in a span that fades in, the rest unseen") {
  const std::string m = replyMarkup("один два три", 9, false, 5, 777);
  CHECK(m == "один <span class=\"fresh\" data-born=\"777\">два </span><span class=\"unseen\">три</span>");
  CHECK(replyMarkup("один два три", 9, false) == "один два <span class=\"unseen\">три</span>");
  // a marker inside the fresh words fades with them
  const std::string c = replyMarkup("да [1] нет", 10, true, 3, 5);
  CHECK(c.find("<span class=\"fresh\" data-born=\"5\"><span class=\"cmark f\"") != std::string::npos);
}

TEST_CASE("chat: a follow-up keeps the first turn above, smaller and dimmer; the new bubble rises in from below") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
  answerInvoke(f);
  Turn t;
  t.state = cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP;
  t.stable = "Включи свет";
  t.reply = "Включил.";
  t.cards = {"card_light_living.binpb"};
  t.followUpMs = 60000;
  f.deliver(t.envelope());
  h.step(600);
  auto& d = f.system().drawer();
  REQUIRE(d.log().rounds().size() == 1);
  const auto r1 = "r" + std::to_string(d.log().live().key);
  // the card arrived with the reply: under it
  CHECK(domKeys(h) == std::vector<std::string>{r1 + "-u", r1 + "-r", r1 + "-c-ha.light.living"});
  CHECK(item(h, r1 + "-c-ha.light.living")->QuerySelector(".ccard") != nullptr);
  // settled: the question a step back, the reply and the card whole
  CHECK(prop(item(h, r1 + "-u"), "transform").find("scale(0.94") != std::string::npos);
  CHECK(prop(item(h, r1 + "-r"), "transform").find("scale") == std::string::npos);

  // the follow-up: a new turn, listening, its words arriving as partials
  Turn u;
  u.id = "turn-2";
  u.state = cadence_v2_AssistantState_ASSISTANT_STATE_LISTENING;
  u.stable = "А теперь";
  f.deliver(u.envelope());
  h.step(16);
  REQUIRE(d.log().rounds().size() == 2);
  const auto r2 = "r" + std::to_string(d.log().live().key);
  auto* bubble = item(h, r2 + "-u");
  REQUIRE(bubble != nullptr);
  CHECK(domKeys(h).back() == r2 + "-u");
  // mid-rise: below its place and still faint
  CHECK(opacityOf(bubble) < 0.6);
  CHECK(prop(bubble, "transform").find("translateY(") != std::string::npos);
  h.step(400);
  CHECK(opacityOf(bubble) == 1);
  CHECK(prop(bubble, "transform") == "none");
  // the first round is history now: smaller and dimmer
  auto* old = item(h, r1 + "-r");
  CHECK(prop(old, "transform").find("scale(0.9") != std::string::npos);
  CHECK(opacityOf(old) < 0.85);
  // and still legible
  CHECK(opacityOf(old) > 0.7);

  // a partial grows the same bubble; the new words fade in and the kept ones stay
  u.revision = 2;
  u.stable = "А теперь сделай";
  u.unstable = "потеплее";
  f.deliver(u.envelope());
  h.step(32);
  CHECK(item(h, r2 + "-u") == bubble);
  Rml::ElementList freshWords;
  bubble->QuerySelectorAll(freshWords, ".fresh");
  REQUIRE(!freshWords.empty());
  CHECK(freshWords.front()->GetInnerRML().find("сделай") != std::string::npos);
  CHECK(bubble->GetInnerRML().find("А теперь") < bubble->GetInnerRML().find("сделай"));
  h.step(400);
  freshWords.clear();
  bubble->QuerySelectorAll(freshWords, ".fresh");
  CHECK(freshWords.empty());

  // the turn's newer revision of the light moves the card down into this round
  u.revision = 3;
  u.state = cadence_v2_AssistantState_ASSISTANT_STATE_SPEAKING;
  u.reply = "Сделал.";
  u.cards = {"card_light_living_rev2.binpb"};
  f.deliver(u.envelope());
  h.step(600);
  CHECK(item(h, r1 + "-c-ha.light.living") == nullptr);
  REQUIRE(item(h, r2 + "-c-ha.light.living") != nullptr);
  CHECK(domKeys(h).back() == r2 + "-c-ha.light.living");
  CHECK(d.cards().count() == 1);

  // closing forgets the conversation
  h.press(cadence_screen_v1_Key_KEY_BACK);
  h.press(cadence_screen_v1_Key_KEY_BACK);
  h.step(400);
  CHECK_FALSE(d.isOpen());
  CHECK(d.log().rounds().size() == 1);
  CHECK(d.log().live().empty());
  CHECK(d.chat().size() == 0);
}

TEST_CASE("chat: with reduced motion a new bubble is in place at once and only fades") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  h.ui().setReducedMotion(true);
  h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
  answerInvoke(f);
  Turn t;
  t.state = cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP;
  t.stable = "Который час";
  t.reply = "Девять.";
  t.followUpMs = 60000;
  f.deliver(t.envelope());
  h.step(300);
  Turn u;
  u.id = "turn-2";
  u.state = cadence_v2_AssistantState_ASSISTANT_STATE_LISTENING;
  u.stable = "А завтра";
  f.deliver(u.envelope());
  h.step(16);
  auto& d = f.system().drawer();
  auto* bubble = item(h, "r" + std::to_string(d.log().live().key) + "-u");
  REQUIRE(bubble != nullptr);
  // no rise: its transform is its place already; the words carry no fade
  CHECK(prop(bubble, "transform") == "none");
  CHECK(opacityOf(bubble) < 1);
  Rml::ElementList freshWords;
  bubble->QuerySelectorAll(freshWords, ".fresh");
  CHECK(freshWords.empty());
  // the older round shrank at once
  auto* old = item(h, "r" + std::to_string(d.log().rounds()[0].key) + "-r");
  INFO(prop(old, "transform"));
  CHECK(prop(old, "transform").find("scale(0.9,") != std::string::npos);
  h.step(200);
  CHECK(opacityOf(bubble) == 1);
  h.ui().setReducedMotion(false);
}

TEST_CASE("chat: focus on an older round's card wakes that round whole and the history moves down to show it") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  h.press(cadence_screen_v1_Key_KEY_ASSISTANT);
  answerInvoke(f);
  Turn t;
  t.state = cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP;
  t.stable = "Включи свет";
  t.reply = "Включил.";
  t.cards = {"card_light_living.binpb"};
  t.followUpMs = 60000;
  f.deliver(t.envelope());
  h.step(300);
  auto& d = f.system().drawer();
  const auto r1 = "r" + std::to_string(d.log().live().key);
  // the follow-up listens: «Стоп» takes the focus the first round's toggle had
  Turn u;
  u.id = "turn-2";
  u.state = cadence_v2_AssistantState_ASSISTANT_STATE_LISTENING;
  u.stable = "Какая погода";
  f.deliver(u.envelope());
  h.step(600);
  CHECK(focusedId(h) == "dbtn");
  auto* card = item(h, r1 + "-c-ha.light.living");
  REQUIRE(card != nullptr);
  INFO(prop(card, "transform"));
  CHECK(prop(card, "transform").find("scale(0.9,") != std::string::npos);
  // the first round's toggle takes focus: its round is whole again
  Rml::ElementList toggles;
  card->QuerySelectorAll(toggles, "[data-kind=toggle]");
  REQUIRE(!toggles.empty());
  h.ui().focus().focus(toggles.front());
  h.step(600);
  CHECK(prop(card, "transform").find("scale(0.") == std::string::npos);
  CHECK(opacityOf(card) == 1);
}
