#pragma once

#include "app/routes/system/env.h"
#include "ui/cards/host.h"

#include <functional>
#include <string>
#include <string_view>

// what the right sheets of server cards share (the tasks panel, the devices panel): the card host's card.action
// calls under a prefix of their own, a page kept past its frame, focus held by card and index across a new
// revision, and the sheet's enter and leave (the layer's open, entering and leaving classes)
namespace cadence::screen::routes::sheet {

// a deep copy of a page the link handed over (its frame is gone after the message)
cards::PagePtr copyPage(const cadence_cards_v1_Page& page);

// the id of the card an element sits in, "" when none
std::string cardIdOf(Rml::Element* e);
// the card of that id under host, null when none
Rml::Element* findCard(Rml::Element* host, std::string_view cardId);

// a card host's sender: card.action requests whose rpc id carries prefix (its answers come back with it)
cards::CardHost::Sender actionSender(app::Outbox& out, std::string prefix);
// an rpc_response to one of those calls, handed to the host without its prefix: true when it was one
bool actionResponse(const cadence_screen_v1_Envelope& m, std::string_view prefix, cards::CardHost& host);

// where focus is within the cards: its card and its index among that card's focusables
struct FocusMark {
  std::string card;
  int index = -1;
};
FocusMark markFocus(Rml::Element* host, Rml::Element* focused);
// focus back on the marked element (or the card's last focusable when it lost some): true when focused
bool restoreFocus(ui::Ui& ui, Rml::Element* host, const FocusMark& mark);
// focus on a card's first focusable: true when it has one
bool focusCard(ui::Ui& ui, Rml::Element* card);
// focus on the first visible focusable under host
bool focusFirst(ui::Ui& ui, Rml::Element* host);

// the sheet's layer comes in (open, its first frame without the transition)
void enter(ui::Ui& ui, Rml::Element* layer);
// the sheet's layer goes: its exit animation, then hidden unless closed() says it opened again meanwhile
void leave(ui::Ui& ui, const Alive& alive, Rml::Element* layer, std::function<bool()> closed);

}  // namespace cadence::screen::routes::sheet
