#pragma once

#include "app/routes/core/router.h"
#include "app/routes/system/system.h"
#include "ui/images/image-cache.h"
#include "ui/cards/model.h"
#include "ui/preview/gallery.h"

#include <cstdint>
#include <deque>
#include <functional>
#include <memory>
#include <optional>
#include <string>
#include <vector>

// the routes' fakes for the gallery and the tests: a link that records what the screen sends, the system on the
// gallery's ui, and screen/v1 messages built from plain values (an assistant turn, a claim, a page of cards)
namespace cadence::screen::routes::syspreview {

// one message the screen sent, as the tests read it
struct Sent {
  pb_size_t body = 0;
  std::string id;
  std::string method;
  std::string json;
  bool subscribe = false;
  cadence_v2_ScreenRoute route = cadence_v2_ScreenRoute_SCREEN_ROUTE_UNSPECIFIED;
  bool drawerOpen = false;
  std::string pageRef;
  std::string code;
};

class FakeLink final : public app::Outbox {
 public:
  bool send(const cadence_screen_v1_Envelope& e) override;
  std::vector<Sent> sent;
  bool up = true;
  // the last request of a method ("" when none)
  [[nodiscard]] const Sent* last(std::string_view method) const;
  [[nodiscard]] int count(std::string_view method) const;
};

// the gallery's clock for the routes' countdowns: the claim's code expires 9:41 after the page's start
inline constexpr std::int64_t kEpochMs = 1794652800000;

// i3's part of the screen as the tests and the gallery reach it: the layer and the router's routes
class SystemView {
 public:
  explicit SystemView(Router& r) : router_(r) {}
  SystemLayer& layer() { return *systemLayer(); }
  Drawer& drawer() { return layer().drawer(); }
  Tasks& tasks() { return layer().tasks(); }
  Devices& devices() { return layer().devices(); }
  Airplay& airplay() { return layer().airplay(); }
  DeviceState& device() { return layer().device(); }
  [[nodiscard]] std::string topBar() { return layer().topBar(); }
  Router& router() { return router_; }
  // the enrollment route, null when it is not open
  Enroll* enroll() { return layer().enroll(); }
  [[nodiscard]] cadence_v2_ScreenRoute route() const { return router_.status().route; }
  void status(app::UiStatus& s) const { s = router_.status(); }
  [[nodiscard]] cadence_v2_ScreenRoute top() const {
    return router_.top() != nullptr ? router_.top()->id() : cadence_v2_ScreenRoute_SCREEN_ROUTE_UNSPECIFIED;
  }
  // settings on top (room: its room and microphone section)
  [[nodiscard]] bool onSettings(bool room = false) const {
    auto* s = dynamic_cast<Settings*>(router_.top());
    return s != nullptr && s->room() == room;
  }

 private:
  Router& router_;
};

class Fixture {
 public:
  // body: the page under the routes (home's stand-in, a film's)
  Fixture(ui::Ui& ui, std::function<std::string()> body);
  ~Fixture();

  SystemView& system() { return *view_; }
  FakeLink& link() { return link_; }
  ui::Ui& ui() { return ui_; }
  // a message as the link would hand it to the shell
  void deliver(const cadence_screen_v1_Envelope& e) { router_->message(e); }
  void online(const std::string& room = "Гостиная", const std::string& name = "Телевизор в гостиной");
  // an rpc answer to the last call of method, ok with json (and a page)
  void answer(std::string_view method, std::string_view status, std::string_view json,
              const cadence_cards_v1_Page* page = nullptr);
  // an event of the last call of method: a page, and json (devices.media's next list)
  void event(std::string_view method, const cadence_cards_v1_Page* page, std::string_view json = {});
  std::int64_t epoch() const;

 private:
  ui::Ui& ui_;
  FakeLink link_;
  std::int64_t startMs_;
  std::unique_ptr<ui::images::ImageCache> images_;
  std::unique_ptr<Router> router_;
  std::unique_ptr<SystemView> view_;
  std::shared_ptr<bool> alive_ = std::make_shared<bool>(true);
};

// the fixture of a ui in the gallery: made by a page's setup, gone before the next page's or the harness
Fixture& fixtureFor(ui::Ui& ui, std::function<std::string()> body);
void dropFixture();

// a card fixture (proto/fixtures/cards/v1/<file>)
cards::CardPtr loadCard(const std::string& file);
cards::PagePtr loadPage(const std::string& file);

// a page of cards by value (shallow: the cards stay owned by their pointers)
struct PageOf {
  std::vector<cards::CardPtr> owned;
  std::vector<cadence_cards_v1_Card> cards;
  cadence_cards_v1_Page page = cadence_cards_v1_Page_init_zero;
  std::string id = "page-1";
  explicit PageOf(std::vector<cards::CardPtr> c);
};

// a card built here: a long text that follows its tail (a task's «Сейчас»), or a head card
struct BuiltCard {
  std::deque<std::string> strings;
  std::deque<std::uint32_t> numbers;
  std::deque<bool> flags;
  std::deque<cadence_cards_v1_Tone> tones;
  std::deque<cadence_cards_v1_Node> nodes;
  std::deque<cadence_cards_v1_CardBlock> blocks;
  std::deque<cadence_cards_v1_LongText> texts;
  cadence_cards_v1_Card card = cadence_cards_v1_Card_init_zero;
};
std::unique_ptr<BuiltCard> tailCard(const std::string& id, std::uint32_t revision, const std::string& text);

// an assistant turn as the server sends it
struct Turn {
  std::string id = "turn-1";
  std::uint32_t revision = 1;
  cadence_v2_AssistantState state = cadence_v2_AssistantState_ASSISTANT_STATE_LISTENING;
  cadence_v2_Trigger trigger = cadence_v2_Trigger_TRIGGER_BUTTON;
  cadence_v2_MicSourceKind mic = cadence_v2_MicSourceKind_MIC_SOURCE_KIND_SPEAKER;
  std::string stable;
  std::string unstable;
  struct Step {
    std::string id, label;
    cadence_v2_StepStatus status;
  };
  std::vector<Step> steps;
  std::string reply;
  std::vector<cadence_v2_WordTiming> words;
  std::vector<std::pair<std::uint32_t, std::string>> citations;
  std::vector<std::string> cards;
  std::uint32_t followUpMs = 0;
  std::string error;
  std::optional<cadence_v2_TurnFailure> failure;
  bool conversationReset = false;

  // the envelope, valid while this turn lives and until the next call
  const cadence_screen_v1_Envelope& envelope();

 private:
  cadence_screen_v1_Envelope env_ = cadence_screen_v1_Envelope_init_zero;
  std::vector<cadence_v2_AssistantStep> steps_;
  std::vector<cadence_v2_Citation> cites_;
  std::vector<cards::CardPtr> owned_;
  std::vector<cadence_cards_v1_Card> cards_;
};

cadence_screen_v1_Envelope linkStatus(cadence_screen_v1_LinkState state, const char* code = "");
cadence_screen_v1_Envelope claim(cadence_screen_v1_ClaimPhase phase, const char* code, std::int64_t expiresAtMs,
                                 const char* errorCode = "");
cadence_screen_v1_Envelope route(cadence_v2_ScreenRoute r, const char* ref = "");
cadence_screen_v1_Envelope voiceLevel(const char* turnId, std::uint32_t mic, std::uint32_t output);

}  // namespace cadence::screen::routes::syspreview
