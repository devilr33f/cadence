#pragma once

#include "app/routes/core/catalog.h"
#include "app/routes/core/failure.h"
#include "app/routes/core/play.h"
#include "app/routes/core/router.h"

#include <cstdint>
#include <string>
#include <vector>

namespace cadence::screen::routes {

// the title page (design r2 section 18.2): the poster on the left; on the right the russian and english names, the
// meta line («2023 · TV · Осень · 24 из 24 эп. · ★ 8,9 · 16+», «ОНГОИНГ» while airing), genres as text, the buttons
// («Продолжить: серия 13» primary, «Озвучка · AniLibria» the source switcher, the my-list status) and the
// description collapsed to 3 lines; below the fold, the episodes. the options of the episode the primary button
// plays are fetched with the page, so with a remembered voiceover OK plays straight away. the source switcher is a
// 720 dp right sheet: one row per source with its episode count, voiceovers and best quality
class TitleRoute final : public Route {
 public:
  TitleRoute(Services& s, const RouteArgs& args);

  [[nodiscard]] cadence_v2_ScreenRoute id() const override { return cadence_v2_ScreenRoute_SCREEN_ROUTE_TITLE; }
  std::string body() override;
  void loaded(Rml::ElementDocument& doc, bool restoring) override;
  void start() override;
  bool activate(const ui::Activation& a) override;
  void context(ScreenContextData& c) const override;
  void online() override;

 private:
  enum class State { Loading, Ready, Failed };

  void load();
  void loadOptions(const std::string& episode);
  [[nodiscard]] std::string variantRef() const;
  [[nodiscard]] std::string primaryEpisode() const;
  [[nodiscard]] std::string sourceName(const catalog::Item& variant) const;
  std::string headRml() const;
  std::string episodesRml() const;
  std::string buttonsRml() const;
  void redrawButtons();
  void openSwitcher();
  void openMyList();
  // the same title on the other sources (catalog.alternatives), in a right sheet
  void findAlternatives();
  void openAlternatives();
  [[nodiscard]] std::string factsRml() const;
  [[nodiscard]] std::string mineRml() const;
  void play(const std::string& episode);
  [[nodiscard]] play::Title titleFor(const std::string& ref, const std::string& episode) const;

  std::string ref_;
  State state_ = State::Loading;
  // the last failure as it came, and a retry under way (the failure stays shown, its button spinning)
  failure::Call failure_;
  bool retrying_ = false;
  std::int64_t askedMs_ = 0;
  catalog::Detail detail_;
  Json options_;
  catalog::Options parsed_;
  std::string optionsEpisode_;
  bool optionsLoading_ = false;
  // the episode a press waits for options of
  std::string waitingEpisode_;
  std::string optionsCall_;
  std::vector<catalog::Alternative> alts_;
  bool altsLoading_ = false;
};

}  // namespace cadence::screen::routes
