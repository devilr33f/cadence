#include "ui/cards/host.h"
#include "ui/cards/model.h"
#include "ui/cards/render.h"
#include "ui/core/rml.h"
#include "ui/preview/gallery.h"

#include <catch2/catch_test_macros.hpp>

#include <fstream>
#include <iterator>

using namespace cadence::screen::ui;

namespace {

std::string fixture(const std::string& name) {
  std::ifstream in(std::string(CADENCE_SCREEN_CARD_FIXTURES) + "/" + name, std::ios::binary);
  return {std::istreambuf_iterator<char>(in), {}};
}

std::span<const std::uint8_t> bytes(const std::string& s) {
  return {reinterpret_cast<const std::uint8_t*>(s.data()), s.size()};
}

}  // namespace

TEST_CASE("every card fixture decodes, passes the device's limits and draws whole") {
  const auto files = preview::cardFixtures();
  REQUIRE(files.size() >= 22);
  for (const auto& f : files) {
    INFO(f);
    const auto data = fixture(f);
    if (f.starts_with("page_")) {
      auto p = cards::decodePage(bytes(data));
      REQUIRE(p);
      for (pb_size_t i = 0; i < p->cards_count; ++i) CHECK(cards::check(p->cards[i]).ok());
      CHECK(cards::renderPage(*p).find("[[cards.unavailable]]") == std::string::npos);
    } else {
      auto c = cards::decodeCard(bytes(data));
      REQUIRE(c);
      const auto v = cards::check(*c);
      INFO(v.reason);
      CHECK(v.ok());
      CHECK(v.focusables <= cards::kMaxFocusables);
      CHECK(cards::render(*c).find("[[cards.unavailable]]") == std::string::npos);
    }
  }
}

TEST_CASE("the device advertises every primitive and optional field (pkg/cards.Known)") {
  const auto f = cards::features();
  CHECK(f.size() == 28 + 7);
  CHECK(cards::knownFeature("slider.timeline"));
  CHECK(cards::knownFeature("slider"));
  CHECK(cards::knownFeature("post.quote"));
  CHECK_FALSE(cards::knownFeature("hologram"));
  CHECK(cards::versions() == std::vector<std::uint32_t>{1});
}

TEST_CASE("an unknown feature, a wrong version or a limit draws the fallback") {
  auto c = cards::decodeCard(bytes(fixture("card_light_living.binpb")));
  REQUIRE(c);
  static char feature[] = "hologram";
  static char* features[] = {feature};
  auto* saved = c->features;
  const auto count = c->features_count;
  c->features = features;
  c->features_count = 1;
  CHECK(cards::check(*c).reason == "feature");
  const auto rml = cards::render(*c);
  CHECK(rml.find("[[cards.unavailable]]") != std::string::npos);
  CHECK(rml.find("fallback") != std::string::npos);
  c->features = saved;
  c->features_count = count;

  std::uint32_t v2 = 2;
  auto* version = c->version;
  c->version = &v2;
  CHECK(cards::check(*c).reason == "version");
  c->version = version;
  CHECK(cards::check(*c).ok());
}

TEST_CASE("oversized bytes do not decode") {
  std::vector<std::uint8_t> big(cards::kMaxCardBytes + 1, 0);
  CHECK_FALSE(cards::decodeCard(big));
}

TEST_CASE("third-party text is escaped and never opens a key or a binding") {
  CHECK(rml::esc("<b>[[common.retry]]{{x}}&\"") ==
        "&lt;b&gt;[\xE2\x80\x8B[common.retry]]{\xE2\x80\x8B{x}}&amp;&quot;");
}

TEST_CASE("a countdown formats on the device") {
  CHECK(cards::countdownText(10'000, 0, cadence_cards_v1_CountdownFormat_COUNTDOWN_FORMAT_MM_SS) == "00:10");
  CHECK(cards::countdownText(3'725'000, 0, cadence_cards_v1_CountdownFormat_COUNTDOWN_FORMAT_HH_MM_SS) == "01:02:05");
  CHECK(cards::countdownText(0, 5'000, cadence_cards_v1_CountdownFormat_COUNTDOWN_FORMAT_MM_SS) == "00:00");
}

TEST_CASE("a card action's json is pkg/rpc's CardAction") {
  CHECK(cards::actionJson("ha.light", "", 3, "a:7f3d", "70") ==
        R"({"cardId":"ha.light","revision":3,"actionId":"a:7f3d","value":70})");
  CHECK(cards::actionJson("", "p1", 0, "a:next", "") == R"({"pageId":"p1","actionId":"a:next"})");
}

TEST_CASE("a timeline slider draws its time, runs from its anchor, and without an action is not focusable") {
  static std::uint32_t version = 1, revision = 1;
  static char id[] = "devices:kitchen", fallback[] = "kitchen: playing", title[] = "kitchen", label[] = "position";
  static double value = 83, lo = 0, hi = 225, step = 10, rate = 1;
  static std::int64_t anchor = 1'000'000;
  cadence_cards_v1_SliderTimeline timeline{&anchor, &rate};
  cadence_cards_v1_Slider slider{};
  slider.value = &value;
  slider.min = &lo;
  slider.max = &hi;
  slider.step = &step;
  slider.label = label;
  slider.timeline = &timeline;
  cadence_cards_v1_Node child{};
  child.which_kind = cadence_cards_v1_Node_slider_tag;
  child.kind.slider = &slider;
  cadence_cards_v1_CardBlock block{};
  block.title = title;
  block.children_count = 1;
  block.children = &child;
  cadence_cards_v1_Node root{};
  root.which_kind = cadence_cards_v1_Node_card_tag;
  root.kind.card = &block;
  cadence_cards_v1_Card card{};
  card.version = &version;
  card.id = id;
  card.revision = &revision;
  card.fallback_text = fallback;
  card.root = &root;

  const auto v = cards::check(card);
  INFO(v.reason);
  REQUIRE(v.ok());
  CHECK(v.focusables == 0);
  cards::RenderOptions o;
  o.nowEpochMs = anchor + 2'500;
  auto out = cards::render(card, o);
  CHECK(out.find("1:25 / 3:45") != std::string::npos);
  CHECK(out.find("class=\"slider timeline\"") != std::string::npos);
  CHECK(out.find("data-timeline=\"1\"") != std::string::npos);

  // with an action it takes focus; a position a moment past the length still draws, at its end
  static char act[] = "devices:kitchen:seek";
  cadence_cards_v1_Action action{};
  action.id = act;
  slider.action = &action;
  value = 230;
  CHECK(cards::check(card).focusables == 1);
  out = cards::render(card, o);
  CHECK(out.find("class=\"slider f timeline\"") != std::string::npos);
  CHECK(out.find("3:45 / 3:45") != std::string::npos);
  // a plain slider keeps its range
  slider.timeline = nullptr;
  CHECK(cards::check(card).reason == "range");
}
