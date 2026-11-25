#pragma once

#include "app/routes/system/reel.h"
#include "ui/cards/host.h"
#include "ui/core/ui.h"

#include <RmlUi/Core/Element.h>

#include <cstdint>
#include <map>
#include <memory>
#include <set>
#include <string>
#include <string_view>

namespace cadence::screen::routes {

// the showcase: the assistant's content cards (found posts, links, media) on the left of the screen while the
// drawer talks on the right. a deck: the current card in front, the two before it stepped back behind it (smaller,
// dimmer, peeking above), the rest gone; a new card rises in from below. what moves is each slot's transform and
// opacity, by a transition as long as the reel's pace says (reel.h), so a burst of results speeds the deck up and the
// last one settles. it lives in the drawer's document and goes with it; its own card host keeps the cards' actions
class Showcase {
 public:
  Showcase(ui::Ui& ui, ui::cards::CardHost::Sender send);
  ~Showcase();
  Showcase(const Showcase&) = delete;
  Showcase& operator=(const Showcase&) = delete;

  // the drawer's document was (re)built: the showcase's box and the deck in it
  void attach(Rml::Element* box, Rml::Element* deck);
  // a content card (ui::cards::isContent); false when an equal or newer revision is held
  bool show(const cadence_cards_v1_Card& card);
  [[nodiscard]] bool has(std::string_view cardId) const;
  // the drawer closed: nothing stays
  void clear();
  // the reply is being spoken: the newest comes up sooner
  void hurry(bool on) { reel_.hurry(on); }
  // a reply's [n] mark names a card: it comes to the front and its numbered source lights; n 0 lights none
  void cite(std::string_view cardId, std::uint32_t n);
  // focus is in the deck: the reel holds still
  void hold(bool on) { held_ = on; }
  // advances the reel and places the slots
  void tick(std::int64_t nowMs);

  [[nodiscard]] ui::cards::CardHost& host() { return *host_; }
  [[nodiscard]] const reel::Reel& reel() const { return reel_; }
  // the current card's element (null when none)
  [[nodiscard]] Rml::Element* currentCard() const;
  [[nodiscard]] Rml::Element* box() const { return box_.get(); }

 private:
  Rml::Element* slot(std::string_view cardId);
  void place(std::int64_t nowMs);

  ui::Ui& ui_;
  std::unique_ptr<ui::cards::CardHost> host_;
  reel::Reel reel_;
  Rml::ObserverPtr<Rml::Element> box_, deck_;
  // each card's slot in the deck, by card id
  std::map<std::string, Rml::ObserverPtr<Rml::Element>, std::less<>> slots_;
  // the slot classes and transition last set, so a frame changes only what moved
  std::map<std::string, std::string, std::less<>> placed_;
  // slots made since the last frame: they stay below one frame before they move
  std::set<std::string, std::less<>> fresh_;
  std::int64_t placedShownMs_ = -1;
  bool held_ = false;
  bool dirty_ = false;
};

}  // namespace cadence::screen::routes
