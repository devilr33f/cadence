#include "app/routes/preview/fake-devices.h"
#include "app/routes/system/tests/support.h"

#include <cstring>

using namespace cadence::screen;
using namespace cadence::screen::routes;
using namespace cadence::screen::routes::systest;

namespace {

cards::PagePtr devicesPage() { return cards::decodePage(fake::devicesPage()); }

std::string cardOf(Rml::Element* e) {
  for (auto* p = e; p != nullptr; p = p->GetParentNode()) {
    if (p->IsClassSet("ccard")) return p->GetAttribute<Rml::String>("data-card", "");
  }
  return {};
}

}  // namespace

TEST_CASE("devices.media: subscribed while the link is up, its list the indicator's, again after a drop") {
  auto& h = harness();
  auto& f = fresh(h);
  CHECK(f.link().last("devices.media") == nullptr);
  f.online();
  const auto* feed = f.link().last("devices.media");
  REQUIRE(feed != nullptr);
  CHECK(feed->subscribe);
  CHECK(f.system().topBar().find("devices-ind") == std::string::npos);
  f.answer("devices.media", "ok", fake::devicesMedia().dump());
  REQUIRE(f.system().devices().items().size() == 3);
  CHECK(nearby::sameRoom(f.system().devices().items()).size() == 2);
  const auto bar = f.system().topBar();
  CHECK(bar.find("devices-ind") != std::string::npos);
  CHECK(bar.find(">3<") != std::string::npos);
  // an equal list changes nothing; a broken one keeps the list
  const auto rev = f.system().devices().revision();
  f.event("devices.media", nullptr, fake::devicesMedia().dump());
  CHECK(f.system().devices().revision() == rev);
  f.event("devices.media", nullptr, "{\"items\": 7}");
  CHECK(f.system().devices().items().size() == 3);
  // nothing plays elsewhere: no indicator
  f.event("devices.media", nullptr, "{\"items\": []}");
  CHECK(f.system().devices().items().empty());
  CHECK(f.system().topBar().find("devices-ind") == std::string::npos);
  // the link drops and comes back: subscribed again
  f.deliver(syspreview::linkStatus(cadence_screen_v1_LinkState_LINK_STATE_OFFLINE));
  CHECK(f.link().count("devices.media") == 1);
  f.online();
  CHECK(f.link().count("devices.media") == 2);
  (void)h;
}

TEST_CASE("route DEVICES opens the quick panel on its device's card; BACK closes it and cancels the panel") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  f.deliver(syspreview::route(cadence_v2_ScreenRoute_SCREEN_ROUTE_DEVICES, "living-pickle"));
  h.step(100);
  REQUIRE(f.system().devices().isOpen());
  const auto* panel = f.link().last("devices.panel");
  REQUIRE(panel != nullptr);
  CHECK(panel->subscribe);
  CHECK(panel->json == "{\"focus\":\"living-pickle\"}");
  const std::string panelId = panel->id;
  static auto page = devicesPage();
  REQUIRE(page != nullptr);
  f.answer("devices.panel", "ok", "{}", page.get());
  h.step(300);
  CHECK(f.system().route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_DEVICES);
  CHECK(inDoc(focused(h), "devices"));
  CHECK(cardOf(focused(h)) == "devices:living-pickle");
  const auto ctx = f.system().router().computeContext();
  CHECK(ctx.route == cadence_v2_ScreenRoute_SCREEN_ROUTE_DEVICES);
  CHECK(ctx.pageRef == "living-pickle");
  // the same page again: focus stays where it is
  h.press(cadence_screen_v1_Key_KEY_DOWN);
  auto* was = focused(h);
  f.event("devices.panel", page.get());
  h.step(200);
  CHECK(focused(h) == was);
  // BACK: closed, the subscription cancelled on the server
  h.press(cadence_screen_v1_Key_KEY_BACK);
  h.step(400);
  CHECK_FALSE(f.system().devices().isOpen());
  const auto* cancel = f.link().last("cancel");
  REQUIRE(cancel != nullptr);
  CHECK(cancel->id == panelId);
  CHECK(f.system().route() != cadence_v2_ScreenRoute_SCREEN_ROUTE_DEVICES);
  CHECK_FALSE(inDoc(focused(h), "devices"));
}

TEST_CASE("the panel: a card that goes redraws the page with focus kept on its card, actions go as card.action") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  f.system().devices().open("bedroom-midi");
  static auto page = devicesPage();
  f.answer("devices.panel", "ok", "{}", page.get());
  h.step(300);
  REQUIRE(cardOf(focused(h)) == "devices:bedroom-midi");
  // the kitchen stopped: its card goes, focus stays on the bedroom's
  cadence_cards_v1_Page fewer = *page;
  fewer.cards = page->cards + 1;
  fewer.cards_count = page->cards_count - 1;
  f.event("devices.panel", &fewer);
  h.step(300);
  CHECK(f.system().devices().cards().count() == 2);
  CHECK(cardOf(focused(h)) == "devices:bedroom-midi");
  // a transport button acts through card.action, under the panel's own prefix
  auto* card = focused(h);
  while (card != nullptr && !card->IsClassSet("ccard")) card = card->GetParentNode();
  REQUIRE(card != nullptr);
  auto* button = card->QuerySelector(".cactions .btn.f");
  REQUIRE(button != nullptr);
  h.ui().focus().focus(button);
  h.press(cadence_screen_v1_Key_KEY_OK);
  h.step(100);
  const auto* act = f.link().last("card.action");
  REQUIRE(act != nullptr);
  CHECK(act->id.starts_with("dev:"));
  CHECK(act->json.find("\"cardId\":\"devices:bedroom-midi\"") != std::string::npos);
  f.system().devices().close();
  h.step(400);
}

TEST_CASE("the devices and tasks panels never show together") {
  auto& h = harness();
  auto& f = fresh(h);
  f.online();
  f.system().tasks().openList();
  h.step(300);
  REQUIRE(f.system().tasks().isOpen());
  f.system().devices().open();
  h.step(300);
  CHECK_FALSE(f.system().tasks().isOpen());
  CHECK(f.system().devices().isOpen());
  CHECK(f.system().route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_DEVICES);
  f.system().tasks().openList();
  h.step(300);
  CHECK_FALSE(f.system().devices().isOpen());
  CHECK(f.system().route() == cadence_v2_ScreenRoute_SCREEN_ROUTE_TASKS);
  f.system().tasks().close();
  h.step(400);
}
