#pragma once

#include "app/routes/core/router.h"

#include <cstdint>
#include <string>
#include <vector>

namespace cadence::screen::routes {

// route ALARMS: the household's alarms, timers and reminders by device (this screen's first, then its room's, then
// the rest, as the summary orders them), each with its kind, its time and days or a timer's live countdown, a
// reminder's label and a snoozed mark. a timer or a reminder offers «Отменить» behind a confirm, the ringing item
// «Стоп» and «Ещё N мин»; an alarm offers nothing (rule 7: the screen never sets nor cancels one). Route.ref names a
// device to focus. a new summary draws the page again with focus kept on the same item
class AlarmsPage final : public Route {
 public:
  AlarmsPage(Services& s, const RouteArgs& args);

  [[nodiscard]] cadence_v2_ScreenRoute id() const override { return cadence_v2_ScreenRoute_SCREEN_ROUTE_ALARMS; }
  std::string body() override;
  void loaded(Rml::ElementDocument& doc, bool restoring) override;
  bool activate(const ui::Activation& a) override;
  void update(std::int64_t nowMs) override;
  void context(ScreenContextData& c) const override;

 private:
  // what an action index names
  struct Target {
    std::string deviceId;
    std::string kind;
    std::string id;
    std::uint32_t snoozeMinutes = 0;
  };
  std::string action(const std::string& verb, Target t);
  void cancel(const Target& t);

  std::string focusDevice_;
  std::vector<Target> targets_;
  std::uint64_t revision_ = 0;
  std::int64_t second_ = -1;
  bool placed_ = false;
  std::shared_ptr<bool> alive_ = std::make_shared<bool>(true);
};

}  // namespace cadence::screen::routes
