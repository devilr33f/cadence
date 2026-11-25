#pragma once

#include "app/routes/core/failure.h"
#include "app/routes/core/catalog.h"
#include "app/routes/core/play.h"
#include "app/routes/core/router.h"

#include <string>

namespace cadence::screen::routes {

// the voiceover and quality picker (design r2 section 18.4): one row per translator (studio, a type tag with its
// icon: ОЗВУЧКА with a mic, СУБТИТРЫ with cc, ОРИГИНАЛ with a globe, the language) then its quality chips; ←/→ move
// across the chips and OK on one plays. the remembered or default choice is marked «как в прошлый раз» and focus
// starts on its best quality. torrents form their own group under a divider (episodes, quality, codec, size,
// seeders; «нет сидов» at 50 %, still focusable), and a last row says how many the owner's filters hid. a
// voiceover without this episode reads «серии 13 нет» and does not play
class PickerRoute final : public Route {
 public:
  PickerRoute(Services& s, const RouteArgs& args);

  [[nodiscard]] cadence_v2_ScreenRoute id() const override { return cadence_v2_ScreenRoute_SCREEN_ROUTE_PICKER; }
  std::string body() override;
  void loaded(Rml::ElementDocument& doc, bool restoring) override;
  void start() override;
  bool activate(const ui::Activation& a) override;
  void context(ScreenContextData& c) const override;
  void online() override;

 private:
  enum class State { Loading, Ready, Failed };

  void load();
  std::string listRml() const;
  std::string voiceoverRow(const catalog::Choice& vo, const catalog::ChoiceGroup* quality) const;
  std::string torrentRow(const catalog::Choice& t) const;
  [[nodiscard]] std::string defaultFocusId() const;
  void focusDefault();
  const catalog::Choice* choice(const std::string& group, const std::string& id) const;

  play::Title title_;
  // the last failure as it came, a retry under way (the failure stays, «Повторить» spinning)
  failure::Call failure_;
  bool retrying_ = false;
  std::int64_t askedMs_ = 0;
  State state_ = State::Loading;
  Json raw_;
  catalog::Options options_;
};

}  // namespace cadence::screen::routes
