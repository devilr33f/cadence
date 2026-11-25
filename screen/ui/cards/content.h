#pragma once

#include "ui/cards/model.h"

#include <string>

// which cards are content (the showcase's, on the left) and which are controls or state (the chat's, inline): a
// card is content when it shows found things to look at, a list of posts, links or media tiles, or an image or a
// tile of its own. home assistant, timers, volume and the rest stay in the chat
namespace cadence::screen::ui::cards {

[[nodiscard]] bool isContent(const cadence_cards_v1_Card& card);

// what the chat says of a content card in its one line: the card's title, icon and how many things it holds
struct Summary {
  std::string title;
  std::string icon;
  int count = 0;
};
[[nodiscard]] Summary summarize(const cadence_cards_v1_Card& card);

}  // namespace cadence::screen::ui::cards
