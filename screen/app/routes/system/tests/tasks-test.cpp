#include "app/routes/system/tests/support.h"

#include <cstring>

using namespace cadence::screen;
using namespace cadence::screen::routes;
using namespace cadence::screen::routes::systest;

namespace {

std::unique_ptr<syspreview::PageOf> approval() {
  std::vector<cards::CardPtr> c;
  c.push_back(syspreview::loadCard("card_task_running.binpb"));
  c.push_back(syspreview::loadCard("card_hermes_approval.binpb"));
  return std::make_unique<syspreview::PageOf>(std::move(c));
}

std::string label(Rml::Element* e) {
  if (e == nullptr) return {};
  // a button's word is its last span (an icon may come first)
  Rml::ElementList spans;
  e->QuerySelectorAll(spans, "span");
  return spans.empty() ? e->GetInnerRML() : spans.back()->GetInnerRML();
}

}  // namespace

TEST_CASE("the indicator counts what runs and what waits from the subscribed tasks list") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  const auto* list = f.link().last("tasks.list");
  REQUIRE(list != nullptr);
  CHECK(list->subscribe);
  static auto page = syspreview::loadPage("page_tasks.binpb");
  f.answer("tasks.list", "ok", "{}", page.get());
  CHECK(f.system().tasks().active() >= 1);
  CHECK(f.system().tasks().waiting() >= 1);
  const auto bar = f.system().topBar();
  CHECK(bar.find("tasks-ind") != std::string::npos);
  CHECK(bar.find("tdot") != std::string::npos);
  // OK on it opens the tasks panel
  f.system().tasks().openList();
  h.step(400);
  CHECK(f.system().tasks().isOpen());
  CHECK(f.system().route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_TASKS);
  CHECK(inDoc(focused(h), "tasks"));
}

TEST_CASE("the indicator takes the server's typed counts (tasks_summary) over the list's tones, a late one dropped") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  auto summary = [&](std::uint32_t running, std::uint32_t waiting, std::uint64_t revision) {
    cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
    e.which_body = cadence_screen_v1_Envelope_tasks_summary_tag;
    e.body.tasks_summary.running = running;
    e.body.tasks_summary.waiting = waiting;
    e.body.tasks_summary.finished = 1;
    e.body.tasks_summary.revision = revision;
    f.deliver(e);
  };
  summary(2, 0, 5);
  CHECK(f.system().tasks().active() == 2);
  CHECK(f.system().tasks().waiting() == 0);
  CHECK(f.system().tasks().finished() == 1);
  CHECK(f.system().topBar().find("tdot") == std::string::npos);
  // the list's tones no longer count once the server's summary came
  static auto page = syspreview::loadPage("page_tasks.binpb");
  f.answer("tasks.list", "ok", "{}", page.get());
  CHECK(f.system().tasks().active() == 2);
  summary(1, 1, 6);
  CHECK(f.system().tasks().active() == 2);
  CHECK(f.system().tasks().waiting() == 1);
  CHECK(f.system().topBar().find("tdot") != std::string::npos);
  // an older revision arriving late changes nothing
  summary(0, 0, 4);
  CHECK(f.system().tasks().active() == 2);
  summary(0, 0, 7);
  CHECK(f.system().tasks().active() == 0);
  CHECK(f.system().topBar().find("tasks-ind") == std::string::npos);
  (void)h;
}

TEST_CASE("the approval walk: a toast over a film, ↓ into it, the task, approve, confirm on the safe choice") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  h.ui().focus().focus(h.ui().page()->GetElementById("films-3"));
  // a film: nothing below the focused row
  h.ui().toast(ui::ToastSpec{"t-1", ui::ToastTone::Warning, "Hermes ждёт ответа", "task", "task:ab12", "[[common.open]]", 60000});
  h.step(300);
  // the toast never takes focus by itself
  CHECK(focusedId(h) == "films-3");
  CHECK_FALSE(h.ui().toastFocused());
  h.press(cadence_screen_v1_Key_KEY_DOWN);
  REQUIRE(h.ui().toastFocused());
  // OK: the task's panel, subscribed
  h.press(cadence_screen_v1_Key_KEY_OK);
  CHECK_FALSE(h.ui().toastFocused());
  const auto* item = f.link().last("tasks.item");
  REQUIRE(item != nullptr);
  CHECK(item->subscribe);
  CHECK(item->json == "{\"taskId\":\"ab12\"}");
  auto p = approval();
  f.answer("tasks.item", "ok", "{}", &p->page);
  h.step(300);
  CHECK(f.system().tasks().inTask());
  CHECK(f.system().route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_TASK);
  // focus opens on «Запретить», the safe choice
  auto* deny = focused(h);
  REQUIRE(deny != nullptr);
  CHECK(deny->IsClassSet("btn"));
  CHECK_FALSE(deny->IsClassSet("primary"));
  CHECK(label(deny).find("Запретить") != std::string::npos);
  // → to «Разрешить один раз» (the fixture's order), OK: the confirm, focus on its cancel
  h.press(cadence_screen_v1_Key_KEY_RIGHT);
  // a long label wraps the actions onto a second line in the 640 dp column
  if (label(focused(h)).find("Разрешить") == std::string::npos) h.press(cadence_screen_v1_Key_KEY_DOWN);
  auto* allow = focused(h);
  REQUIRE(allow != nullptr);
  CHECK(label(allow).find("Разрешить") != std::string::npos);
  const int before = f.link().count("card.action");
  h.press(cadence_screen_v1_Key_KEY_OK);
  h.step(400);
  REQUIRE(h.ui().levelOpen());
  CHECK(focusedId(h) == "dialog-cancel");
  CHECK(f.link().count("card.action") == before);
  // → confirm, OK: the action goes out from the panel's host
  h.press(cadence_screen_v1_Key_KEY_RIGHT);
  CHECK(focusedId(h) == "dialog-confirm");
  h.press(cadence_screen_v1_Key_KEY_OK);
  h.step(300);
  CHECK_FALSE(h.ui().levelOpen());
  const auto* act = f.link().last("card.action");
  REQUIRE(act != nullptr);
  CHECK(act->id.starts_with("tsk:"));
  CHECK(act->json.find("\"cardId\":\"task.9f2a\"") != std::string::npos);
  CHECK(act->json.find("\"actionId\":\"a:k:allow\"") != std::string::npos);
  // the context names the task
  h.step(600);
  const auto* ctx = f.link().last("screen_context");
  REQUIRE(ctx != nullptr);
  CHECK(ctx->route == cadence_v2_ScreenRoute_SCREEN_ROUTE_TASK);
  CHECK(ctx->pageRef == "ab12");
  // BACK: the list; BACK again: closed
  h.press(cadence_screen_v1_Key_KEY_BACK);
  CHECK(f.system().tasks().isOpen());
  CHECK_FALSE(f.system().tasks().inTask());
  h.press(cadence_screen_v1_Key_KEY_BACK);
  CHECK_FALSE(f.system().tasks().isOpen());
}

TEST_CASE("a list card's «Открыть» answers its panel; BACK comes back to that card") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  static auto list = syspreview::loadPage("page_tasks.binpb");
  f.answer("tasks.list", "ok", "{}", list.get());
  f.system().tasks().openList();
  h.step(300);
  // the second card's button
  Rml::ElementList buttons;
  h.ui().documentNamed("tasks")->QuerySelectorAll(buttons, ".ccard .btn.f");
  REQUIRE(buttons.size() >= 2);
  h.ui().focus().focus(buttons[1]);
  Rml::Element* card = buttons[1];
  while (card != nullptr && !card->IsClassSet("ccard")) card = card->GetParentNode();
  REQUIRE(card != nullptr);
  const auto cardId = card->GetAttribute<Rml::String>("data-card", "");
  h.press(cadence_screen_v1_Key_KEY_OK);
  const auto* act = f.link().last("card.action");
  REQUIRE(act != nullptr);
  // the server answers the task's page as the typed card action
  auto p = approval();
  cadence_cards_v1_CardActionResult result = cadence_cards_v1_CardActionResult_init_zero;
  result.which_result = cadence_cards_v1_CardActionResult_page_tag;
  result.result.page = &p->page;
  cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
  e.which_body = cadence_screen_v1_Envelope_rpc_response_tag;
  copyTo(e.body.rpc_response.id, act->id);
  copyTo(e.body.rpc_response.status, "ok");
  e.body.rpc_response.card_action = &result;
  f.deliver(e);
  h.step(300);
  CHECK(f.system().tasks().inTask());
  // its revisions come through cards.watch
  CHECK(f.link().last("cards.watch") != nullptr);
  h.press(cadence_screen_v1_Key_KEY_BACK);
  h.step(300);
  CHECK_FALSE(f.system().tasks().inTask());
  Rml::Element* now = focused(h);
  while (now != nullptr && !now->IsClassSet("ccard")) now = now->GetParentNode();
  REQUIRE(now != nullptr);
  CHECK(now->GetAttribute<Rml::String>("data-card", "") == cardId);
}

TEST_CASE("the live tail updates in place: focus and scroll stay, and «новое ↓» says there is more until ↓ reaches it") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  std::string lines;
  for (int i = 1; i <= 20; ++i) lines += "строка " + std::to_string(i) + "\n";
  auto r1 = syspreview::tailCard("task-ab12-now", 1, lines);
  auto r2 = syspreview::tailCard("task-ab12-now", 2, lines + "строка 21\nстрока 22\n");
  syspreview::PageOf p1({});
  p1.cards.push_back(r1->card);
  p1.page.cards = p1.cards.data();
  p1.page.cards_count = 1;
  syspreview::PageOf p2({});
  p2.cards.push_back(r2->card);
  p2.page.cards = p2.cards.data();
  p2.page.cards_count = 1;
  auto& tasks = f.system().tasks();
  tasks.showTask(p1.page, false);
  h.step(200);
  auto* tail = h.ui().documentNamed("tasks")->QuerySelector(".clong.tail");
  REQUIRE(tail != nullptr);
  // a tail starts at its end
  INFO("scroll " << tail->GetScrollTop() << " height " << tail->GetScrollHeight() << " client " << tail->GetClientHeight());
  CHECK(tail->GetScrollTop() > 0);
  // following: a new revision keeps it at the end, no pill
  tasks.showTask(p1.page, false);
  h.step(100);
  CHECK_FALSE(tasks.newBelow());
  // the reader scrolls up; new lines arrive below
  h.ui().focus().focus(tail);
  tail->SetScrollTop(0);
  h.step(50);
  tasks.showTask(p2.page, false);
  h.step(100);
  auto* tail2 = h.ui().documentNamed("tasks")->QuerySelector(".clong.tail");
  REQUIRE(tail2 != nullptr);
  CHECK(tail2->GetScrollTop() < 1.0f);
  CHECK(tasks.newBelow());
  // focus stayed on the tail (its new element)
  CHECK(focused(h) == tail2);
  // ↓ to the end resumes following: the pill goes
  tail2->SetScrollTop(tail2->GetScrollHeight());
  h.step(50);
  CHECK_FALSE(tasks.newBelow());
}
