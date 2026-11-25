#pragma once

#include "app/routes/panels/side-sheet.h"
#include "ui/cards/host.h"
#include "ui/core/rml.h"

#include <memory>

namespace cadence::screen::routes {

// a panel of the playing item (design §14): media.panel {sessionId, panelId} for a panel the server's session state
// listed, else catalog.panel {ref, panel, cursor}, answers a page of cadence cards, which
// a card host of its own draws in the side sheet, so the sheet knows nothing of descriptions or comments. «Показать
// ещё» is the page's next action (card.action through the same host); comments ask for it when focus reaches the
// last 3 items
class PanelContent final : public SheetContent {
 public:
  explicit PanelContent(Panel panel) : panel_(std::move(panel)) {}

  [[nodiscard]] std::string title() const override { return ui::rml::esc(panel_.title); }
  [[nodiscard]] cadence_v2_ScreenRoute route() const override { return cadence_v2_ScreenRoute_SCREEN_ROUTE_PANEL; }
  void open(SideSheet& sheet, Rml::Element* body) override;
  bool activate(const ui::Activation& a) override;
  bool message(const cadence_screen_v1_Envelope& m) override;
  void tick(std::int64_t nowMs) override;
  void focused(Rml::Element* e) override;

  // the gallery: a page as if the server had answered
  void show(const cadence_cards_v1_Page& page);

 private:
  void loaded();

  Panel panel_;
  SideSheet* sheet_ = nullptr;
  std::unique_ptr<ui::cards::CardHost> cards_;
  bool loading_ = true;
  std::int64_t autoMoreMs_ = 0;
};

}  // namespace cadence::screen::routes
