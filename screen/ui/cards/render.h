#pragma once

#include "ui/cards/model.h"

#include <cstdint>
#include <string>

namespace cadence::screen::ui::cards {

struct RenderOptions {
  // the device clock (epoch ms) a countdown starts from; the host ticks it on
  std::int64_t nowEpochMs = 0;
  // where the card sits: the drawer or a panel
  bool panel = false;
};

// the card as afterglow rml: every primitive from the device's own tokens, never a colour, size or font from the
// server. text is escaped; focusables carry .f and their action (data-action, data-confirm, data-value), and the
// card's root carries data-card and data-revision for the host. a card check() refuses is drawn as its fallback
std::string render(const cadence_cards_v1_Card& card, const RenderOptions& options = {});
// the plain card of a fallback text with the «недоступно на этом экране» line
std::string renderFallback(std::string_view fallbackText, std::string_view id);
// a page: its cards in a column, and its next action as a «Показать ещё» row
std::string renderPage(const cadence_cards_v1_Page& page, const RenderOptions& options = {});

// a countdown's text at nowEpochMs
std::string countdownText(std::int64_t endsAtMs, std::int64_t nowEpochMs, cadence_cards_v1_CountdownFormat format);

}  // namespace cadence::screen::ui::cards
