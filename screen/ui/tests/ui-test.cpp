#include "ui/core/rml.h"
#include "ui/core/shell.h"
#include "ui/images/binder.h"
#include "ui/images/image-cache.h"
#include "ui/preview/harness.h"
#include "ui/rml/sprites.h"

#include <RmlUi/Core/ElementDocument.h>

#include <catch2/catch_test_macros.hpp>

#include <cmath>
#include <cstdlib>
#include <cstring>
#include <memory>
#include <string>

using namespace cadence::screen;
using namespace cadence::screen::ui;
using preview::Harness;

namespace {

Harness& harness() {
  static Harness h({960, 540, cadence::i18n::Lang::Ru});
  REQUIRE(h.ok());
  return h;
}

preview::Page page(std::string name, std::function<std::string()> body) {
  preview::Page p;
  p.name = std::move(name);
  p.body = std::move(body);
  return p;
}

rml::Row row(const std::string& id, int n) {
  rml::Row r;
  r.id = id;
  r.title = id;
  for (int i = 0; i < n; ++i) {
    rml::Card c;
    c.id = id + "-" + std::to_string(i);
    c.title = "t" + std::to_string(i);
    c.action = "open";
    r.cards.push_back(c);
  }
  return r;
}

std::string focused(Harness& h) {
  auto* e = h.ui().focus().current();
  return e != nullptr ? e->GetId() : "";
}

}  // namespace

TEST_CASE("a row keeps its column: down and up come back to the item it had") {
  auto& h = harness();
  h.load(page("rows", [] { return "<div class=\"safe\"><div class=\"vscroll\">" + rml::row(row("a", 8)) + rml::row(row("b", 8)) + "</div></div>"; }));
  h.ui().focus().focus(h.ui().page()->GetElementById("a-0"));
  for (int i = 0; i < 4; ++i) h.press(cadence_screen_v1_Key_KEY_RIGHT);
  CHECK(focused(h) == "a-4");
  h.press(cadence_screen_v1_Key_KEY_DOWN);
  const auto entered = focused(h);
  CHECK(entered.starts_with("b-"));
  h.press(cadence_screen_v1_Key_KEY_RIGHT);
  h.press(cadence_screen_v1_Key_KEY_UP);
  CHECK(focused(h) == "a-4");
  // the row scrolled so focus sits in slot 2
  auto* track = h.ui().page()->GetElementById("a")->QuerySelector(".track");
  CHECK(track->GetAttribute<float>("data-scroll", 0) > 0);
}

TEST_CASE("no horizontal wrap: the row's end nudges and keeps focus") {
  auto& h = harness();
  h.load(page("end", [] { return "<div class=\"safe\">" + rml::row(row("a", 2)) + "</div>"; }));
  h.ui().focus().focus(h.ui().page()->GetElementById("a-1"));
  h.press(cadence_screen_v1_Key_KEY_RIGHT);
  CHECK(focused(h) == "a-1");
  h.press(cadence_screen_v1_Key_KEY_LEFT);
  h.press(cadence_screen_v1_Key_KEY_LEFT);
  CHECK(focused(h) == "a-0");
}

TEST_CASE("a removed focused element hands focus to its next sibling") {
  auto& h = harness();
  h.load(page("remove", [] { return "<div class=\"safe\">" + rml::row(row("a", 4)) + "</div>"; }));
  auto* e = h.ui().page()->GetElementById("a-1");
  h.ui().focus().focus(e);
  e->GetParentNode()->RemoveChild(e);
  h.step(32);
  CHECK(focused(h) == "a-2");
}

TEST_CASE("long-press OK opens the actions sheet, BACK returns focus to the item") {
  auto& h = harness();
  h.load(page("long", [] {
    auto r = row("a", 3);
    r.cards[1].actions = "info|Info|info;queue|Queue|queue_add";
    return "<div class=\"safe\">" + rml::row(r) + "</div>";
  }));
  h.ui().focus().focus(h.ui().page()->GetElementById("a-1"));
  h.key(cadence_screen_v1_Key_KEY_OK, cadence_screen_v1_KeyAction_KEY_ACTION_DOWN);
  h.step(300);
  // the hold arc is showing by now
  CHECK(h.ui().page()->GetElementById("a-1")->IsClassSet("holding"));
  h.key(cadence_screen_v1_Key_KEY_OK, cadence_screen_v1_KeyAction_KEY_ACTION_LONG);
  h.key(cadence_screen_v1_Key_KEY_OK, cadence_screen_v1_KeyAction_KEY_ACTION_UP);
  h.step(400);
  CHECK(h.ui().levelOpen());
  CHECK(h.ui().focus().current()->GetId() == "sheet-first");
  h.press(cadence_screen_v1_Key_KEY_BACK);
  h.step(300);
  CHECK_FALSE(h.ui().levelOpen());
  CHECK(focused(h) == "a-1");
}

TEST_CASE("a dialog starts on the safe choice") {
  auto& h = harness();
  h.load(page("dialog", [] { return "<div class=\"safe\">" + rml::row(row("a", 3)) + "</div>"; }));
  bool confirmed = false;
  h.ui().dialog({"Q", "B", {}, {}, [&] { confirmed = true; }, nullptr});
  h.step(400);
  CHECK(h.ui().focus().current()->GetId() == "dialog-cancel");
  h.press(cadence_screen_v1_Key_KEY_RIGHT);
  CHECK(h.ui().focus().current()->GetId() == "dialog-confirm");
  h.press(cadence_screen_v1_Key_KEY_OK);
  CHECK(confirmed);
}

TEST_CASE("a card slider sends one action 600 ms after its last key, with its value") {
  auto& h = harness();
  preview::Page p;
  p.name = "slider";
  p.body = [] { return "<div class=\"safe\"><div id=\"cards\" class=\"cstack\"/></div>"; };
  h.load(p);
  for (const auto& page : preview::pages()) {
    if (page.name == "card-card_light_living_r2") {
      h.load(page);
      break;
    }
  }
  Rml::ElementList sliders;
  h.ui().page()->QuerySelectorAll(sliders, ".slider");
  REQUIRE(sliders.size() == 1);
  h.ui().focus().focus(sliders[0]);
  const double before = sliders[0]->GetAttribute<double>("data-value", 0);
  h.press(cadence_screen_v1_Key_KEY_RIGHT);
  h.press(cadence_screen_v1_Key_KEY_RIGHT);
  h.step(300);
  CHECK(h.sent().empty());
  h.step(400);
  REQUIRE(h.sent().size() == 1);
  const double step = sliders[0]->GetAttribute<double>("data-step", 1);
  CHECK(h.sent()[0].json.find("\"value\":" + rml::sliderLabel(before + 2 * step, "")) != std::string::npos);
}

TEST_CASE("the language switches live: open documents are built again") {
  auto& h = harness();
  h.load(page("lang", [] { return "<div class=\"safe\"><div id=\"word\">[[common.retry]]</div></div>"; }));
  CHECK(h.ui().page()->GetElementById("word")->GetInnerRML() == "Повторить");
  h.translator().set_language(cadence::i18n::Lang::En);
  h.step(32);
  CHECK(h.ui().page()->GetElementById("word")->GetInnerRML() == "Retry");
  h.translator().set_language(cadence::i18n::Lang::Ru);
  h.step(32);
}

TEST_CASE("a spinner turns every frame while it shows, and the skeleton pulses at rest") {
  auto& h = harness();
  h.load(page("spin", [] { return "<div class=\"safe\">" + rml::spinner() + rml::skeletonCard(rml::CardKind::Poster) + "</div>"; }));
  Rml::ElementList spinners;
  h.ui().page()->QuerySelectorAll(spinners, ".spinner");
  REQUIRE(spinners.size() == 1);
  const auto a = h.capture();
  const auto turn = [&] {
    const auto* t = spinners[0]->GetProperty("transform");
    return t != nullptr ? t->ToString() : std::string();
  };
  const auto turnA = turn();
  h.step(32);
  const auto turnB = turn();
  const auto b = h.capture();
  INFO(turnA << " -> " << turnB);
  CHECK(turnA != turnB);
  CHECK(preview::diffFraction(a, b) > 0);
  // the loop wants the next frame a frame away, not the old 100 ms step
  const auto due = h.ui().nextUpdateMs(h.nowMs());
  CHECK(due >= 0);
  CHECK(due <= 16);
}

TEST_CASE("a toast's typed action is the button's activation, never parsed from an id") {
  cadence_screen_v1_Toast t = cadence_screen_v1_Toast_init_zero;
  std::strncpy(t.id, "t-2", sizeof t.id - 1);
  std::string text = "Hermes ждёт ответа";
  t.text = text.data();
  t.has_action = true;
  std::string label = "Открыть";
  std::strncpy(t.action.id, "open", sizeof t.action.id - 1);
  t.action.label = label.data();
  t.action.kind = cadence_screen_v1_ToastActionKind_TOAST_ACTION_KIND_OPEN;
  t.action.route = cadence_v2_ScreenRoute_SCREEN_ROUTE_TASK;
  std::strncpy(t.action.ref, "ab12", sizeof t.action.ref - 1);
  auto spec = toastSpecOf(t);
  CHECK(spec.actionId == "task:ab12");
  CHECK(spec.actionLabel == "Открыть");

  t.action.kind = cadence_screen_v1_ToastActionKind_TOAST_ACTION_KIND_RPC;
  std::strncpy(t.action.id, "retry", sizeof t.action.id - 1);
  CHECK(toastSpecOf(t).actionId == "retry");

  // an open of a route a toast does not open: no button
  t.action.kind = cadence_screen_v1_ToastActionKind_TOAST_ACTION_KIND_OPEN;
  t.action.route = cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME;
  CHECK(toastSpecOf(t).actionId.empty());

  // an older cadenced's untyped action
  t.has_action = false;
  std::strncpy(t.action_id, "legacy", sizeof t.action_id - 1);
  CHECK(toastSpecOf(t).actionId == "legacy");
}

// airplay mirroring decodes onto the video SurfaceView under the gl surface: an opaque page over it hid the phone's
// picture (only the ui showed)
TEST_CASE("see-through hides the page, also one loaded meanwhile, and shows it again") {
  auto& h = harness();
  h.load(page("st", [] { return "<div class=\"safe\"><div id=\"x\">x</div></div>"; }));
  h.ui().setSeeThrough(true);
  h.step(32);
  CHECK_FALSE(h.ui().page()->IsVisible());
  h.load(page("st2", [] { return "<div class=\"safe\"><div id=\"y\">y</div></div>"; }));
  CHECK_FALSE(h.ui().page()->IsVisible());
  h.ui().setSeeThrough(false);
  h.step(32);
  CHECK(h.ui().page()->IsVisible());
}

TEST_CASE("a document change no transition follows still owes a frame: new children, a property, an attribute") {
  // a preview's new text and backdrop under a host already shown started no transition and stayed off screen until
  // the next key; the ui now notices any change its documents make
  auto& h = harness();
  h.load(page("owed", [] { return "<div class=\"safe\"><div id=\"host\"><div>a</div></div></div>"; }));
  const auto settle = [&] {
    (void)h.capture();
    h.step(32);
  };
  settle();
  REQUIRE_FALSE(h.ui().dirty());
  auto* host = h.ui().page()->GetElementById("host");
  REQUIRE(host != nullptr);
  host->SetInnerRML("<div>b</div>");
  h.step(16);
  CHECK(h.ui().dirty());
  settle();
  REQUIRE_FALSE(h.ui().dirty());
  host->SetProperty("opacity", "0.5");
  h.step(16);
  CHECK(h.ui().dirty());
  settle();
  REQUIRE_FALSE(h.ui().dirty());
  host->SetAttribute("data-x", "1");
  h.step(16);
  CHECK(h.ui().dirty());
  // and nothing at rest: an unchanged page owes no frame
  settle();
  h.step(500);
  CHECK_FALSE(h.ui().dirty());
}

namespace {
// any valid id is the same 2 x 2 png
struct OnePng final : images::Fetcher {
  bool fetch(const std::string&, std::string& bytes, std::string&) override {
    static const char kPng[] =
        "\x89\x50\x4e\x47\x0d\x0a\x1a\x0a\x00\x00\x00\x0d\x49\x48\x44\x52\x00\x00\x00\x02\x00\x00\x00\x02\x08\x06\x00"
        "\x00\x00\x72\xb6\x0d\x24\x00\x00\x00\x11\x49\x44\x41\x54\x78\x9c\x63\xf8\xcf\xc0\xf0\x1f\x84\x19\x60\x0c\x00"
        "\x47\xca\x07\xf9\x67\x59\x6e\xb7\x00\x00\x00\x00\x49\x45\x4e\x44\xae\x42\x60\x82";
    bytes.assign(kPng, sizeof kPng - 1);
    return true;
  }
};
}  // namespace

TEST_CASE("decoded images go on screen within the upload budget a pass, at least one, the rest the next passes") {
  auto& h = harness();
  h.load(page("budget", [] {
    std::string s = "<div class=\"safe\">";
    for (int i = 0; i < 3; ++i) {
      s += "<div class=\"netimg\" style=\"width: 40dp; height: 40dp;\" data-img=\"aaaaaaaaaaaaaaaaaaaaaaa" +
           std::to_string(i) + "\"/>";
    }
    return s + "</div>";
  }));
  images::CacheOptions o;
  o.inlineDecode = true;
  images::ImageCache cache(std::make_shared<OnePng>(), o);
  // a budget of one byte: one image a pass
  images::ImageBinder binder(cache, 1);
  const auto pass = [&] {
    binder.poll();
    return binder.scan(h.ui().page());
  };
  CHECK(pass());
  CHECK(binder.bound() == 1);
  CHECK(binder.deferred() == 2);
  CHECK(pass());
  CHECK(binder.bound() == 2);
  CHECK(pass());
  CHECK(binder.bound() == 3);
  CHECK(binder.deferred() == 0);
  // a resident image costs nothing: rebinding it is not held back
  CHECK_FALSE(pass());
}

namespace {
// the glow's alpha at a pixel, 0-255
int alphaAt(const Pixels& p, double x, double y) {
  const auto i = (static_cast<std::size_t>(y) * static_cast<std::size_t>(p.width) + static_cast<std::size_t>(x)) * 4 + 3;
  return p.rgba[i];
}
}  // namespace

TEST_CASE("a round ring's glow follows its rounding: as strong at the corner as along the edge, clear inside") {
  for (const int r : {40, 48, 60}) {
    const auto g = generateSprite("gen:glow:" + std::to_string(r));
    REQUIRE(g);
    const int size = 2 * (32 + r) + 16;
    REQUIRE(g->width == size);
    REQUIRE(g->height == size);
    // the ring's corner circle is centred 32 + r in from the sprite's corner; 8 dp out from the ring along the
    // edge and along the corner's diagonal
    const double c = 32 + r, out = 8, d = (r + out) / std::sqrt(2.0);
    const int edge = alphaAt(*g, 32 - out, size / 2.0);
    const int corner = alphaAt(*g, c - d, c - d);
    INFO("radius " << r << ": edge " << edge << ", corner " << corner);
    CHECK(edge > 40);
    CHECK(std::abs(edge - corner) <= 12);
    // just inside the round corner nothing tints what the ring holds
    const double in = (r - 4) / std::sqrt(2.0);
    CHECK(alphaAt(*g, c - in, c - in) == 0);
  }
  // the cards' glow is unchanged, and a radius out of range is no sprite
  const auto cards = generateSprite("gen:glow");
  REQUIRE(cards);
  CHECK(cards->width == 160);
  CHECK_FALSE(generateSprite("gen:glow:2"));
  CHECK_FALSE(generateSprite("gen:glow:97"));
  CHECK_FALSE(generateSprite("gen:glow:x"));
}
