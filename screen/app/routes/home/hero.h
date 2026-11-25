#pragma once

#include <string>
#include <string_view>

// what home's hero shows, by one rule everywhere (pure: the unit tests pin it):
//   - focus on a card, in any row: that card's item (its name at once, its details as they arrive);
//   - otherwise (start, the hero's own buttons, the top bar): the film playing on behind the page while there is one,
//     else the item the hero's buttons act on (the one to continue, else the newest), else the placeholder
namespace cadence::screen::routes::hero {

enum class Source { Placeholder, Default, Play, Card };

struct Show {
  Source source = Source::Placeholder;
  std::string ref;
  bool operator==(const Show&) const = default;
};

// cardRef: the focused card's item ("" when focus is not on a card); playRef: the film behind the page ("" when none);
// defaultRef: the item the hero's buttons act on ("" when nothing has loaded)
inline Show showFor(std::string_view cardRef, std::string_view playRef, std::string_view defaultRef) {
  if (!cardRef.empty()) return {Source::Card, std::string(cardRef)};
  if (!playRef.empty()) return {Source::Play, std::string(playRef)};
  if (!defaultRef.empty()) return {Source::Default, std::string(defaultRef)};
  return {};
}

}  // namespace cadence::screen::routes::hero
