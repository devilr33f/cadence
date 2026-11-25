#pragma once

#include "ui/cards/host.h"
#include "ui/core/ui.h"

#include <functional>
#include <memory>
#include <string>
#include <vector>

// the preview gallery (stream b, like dashboard/design-preview for the tv): every component and every card example
// of pkg/cards' testdata (their binary fixtures in proto/fixtures/cards/v1) with fake data, one page each, for the
// golden images, the budget report, the pseudo-locale's overflow check and the d-pad walk. no network, no device
namespace cadence::screen::ui::preview {

// the gallery's clocks: the ui's starts at an arbitrary monotonic value, the card clock 8 minutes before the timer
// fixture ends, so a countdown reads 08:00 in every run
inline constexpr std::int64_t kStartMs = 1'000'000;
inline constexpr std::int64_t kCardEpochMs = 1794652800000 - 8 * 60 * 1000;

struct Context {
  Ui& ui;
  cards::CardHost& cards;
  // what the card host sent (the fake link)
  std::vector<cards::ActionCall>& sent;
};

struct Page {
  std::string name;
  // the page's body
  std::function<std::string()> body;
  // after the page loaded (and was laid out once): open a dialog, show cards, press keys
  std::function<void(Context&)> setup;
  // a page whose budget may exceed the design's (a sheet over a scrim is 1 layer, a dialog 1; the hand-off moves
  // more than 30 % on purpose): what the report expects
  int maxLayers = 2;
  double maxAnimatedPct = 30;
  // walked by the d-pad check
  bool walk = true;
};

std::vector<Page> pages();
// the card fixtures by file name (card_*.binpb, page_*.binpb), sorted
std::vector<std::string> cardFixtures();

}  // namespace cadence::screen::ui::preview
