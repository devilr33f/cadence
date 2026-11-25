#pragma once

#include "app/routes/panels/side-sheet.h"

#include <functional>
#include <vector>

namespace cadence::screen::routes {

// switching the voiceover (or the source) while watching (design §18.6), from the «Озвучка · AniLibria» chip: this
// source's other voiceovers first, then other sources, each saying whether this episode exists there («серия 13
// есть»). the focused row says what will happen («Продолжу с 14:52», or «Серия начнётся сначала»); OK hands the
// choice to the routes, which ask the server to play it at the position kept (media.play, reason switch), with a
// spinner in the sheet until the new session starts or the server refuses (failed())
class SwitcherContent final : public SheetContent {
 public:
  // the choice and the selection to play it with: the routes play it (keeping the position when hasEpisode)
  using Chosen = std::function<void(const SwitchChoice& choice, const std::string& choicesJson)>;
  explicit SwitcherContent(Chosen chosen) : chosen_(std::move(chosen)) {}

  [[nodiscard]] std::string title() const override { return "[[player.switcher.title]]"; }
  [[nodiscard]] cadence_v2_ScreenRoute route() const override { return cadence_v2_ScreenRoute_SCREEN_ROUTE_PICKER; }
  void open(SideSheet& sheet, Rml::Element* body) override;
  bool activate(const ui::Activation& a) override;
  void focused(Rml::Element* e) override;
  // the server refused the play: the rows again
  void failed();

  // the gallery and tests: catalog.options' answer
  void show(const std::string& optionsJson);
  [[nodiscard]] const std::vector<SwitchChoice>& choices() const { return choices_; }

 private:
  void render();
  void note(Rml::Element* focused);

  Chosen chosen_;
  SideSheet* sheet_ = nullptr;
  Rml::ObserverPtr<Rml::Element> body_;
  std::string options_;
  std::vector<SwitchChoice> choices_;
  bool resolving_ = false;
};

}  // namespace cadence::screen::routes
