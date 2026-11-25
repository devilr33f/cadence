#pragma once

#include "app/routes/panels/side-sheet.h"

namespace cadence::screen::routes {

// «Главы» (design §14): the item's chapters as list items, title and start time; the current one shows the cadence
// line; OK seeks there and closes the sheet. built from the metadata's chapters, no call
class ChaptersContent final : public SheetContent {
 public:
  [[nodiscard]] std::string title() const override { return "[[player.osd.chapters]]"; }
  [[nodiscard]] cadence_v2_ScreenRoute route() const override { return cadence_v2_ScreenRoute_SCREEN_ROUTE_PANEL; }
  void open(SideSheet& sheet, Rml::Element* body) override;
  bool activate(const ui::Activation& a) override;

 private:
  SideSheet* sheet_ = nullptr;
};

}  // namespace cadence::screen::routes
