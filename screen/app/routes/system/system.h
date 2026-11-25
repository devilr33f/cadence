#pragma once

#include "app/routes/core/router.h"
#include "app/routes/system/airplay.h"
#include "app/routes/system/alarms.h"
#include "app/routes/system/devices.h"
#include "app/routes/system/drawer.h"
#include "app/routes/system/enroll.h"
#include "app/routes/system/page-rpc.h"
#include "app/routes/system/settings.h"
#include "app/routes/system/state.h"
#include "app/routes/system/tasks.h"

#include <memory>
#include <string>
#include <vector>

namespace cadence::screen::routes {

// i3's layer over i1's router: the assistant drawer, the tasks panels, the devices panel and airplay, with the device's state they
// share with i3's routes (enrollment, settings). it takes the link's messages first: the turn and its levels and
// cards, its own calls' answers, the claim (opening enrollment over everything while there is no token), airplay;
// the press paths (G, long BACK, home's «Ассистент», the mic button, «Ассистент» in an actions sheet); and it adds
// the drawer's flag and an open panel's route and task to the status and the screen context
class SystemLayer final : public Layer {
 public:
  SystemLayer(Services& s, app::Outbox& out, std::shared_ptr<SystemState> state);
  ~SystemLayer() override;

  bool message(const cadence_screen_v1_Envelope& m) override;
  void status(app::UiStatus& s) const override;
  void context(ScreenContextData& c) const override;

  // the top bar's part (home's): the tasks and devices indicators, the mic button, settings
  [[nodiscard]] std::string topBar() const;

  [[nodiscard]] DeviceState& device() { return state_->device; }
  [[nodiscard]] SystemState& state() { return *state_; }
  [[nodiscard]] Drawer& drawer() { return *drawer_; }
  [[nodiscard]] Tasks& tasks() { return *tasks_; }
  [[nodiscard]] Devices& devices() { return *devices_; }
  [[nodiscard]] Airplay& airplay() { return *airplay_; }
  [[nodiscard]] Alarms& alarms() { return *alarms_; }
  [[nodiscard]] Enroll* enroll() const;

 private:
  bool activate(const ui::Activation& a);
  void micFromTurn(const cadence_v2_AssistantTurn& t);
  // enrollment over everything, and the message handed to it
  void claim(const cadence_screen_v1_Envelope& m);

  Services& s_;
  std::shared_ptr<SystemState> state_;
  PageRpc rpc_;
  std::unique_ptr<Tasks> tasks_;
  std::unique_ptr<Devices> devices_;
  std::unique_ptr<Drawer> drawer_;
  std::unique_ptr<Airplay> airplay_;
  // last: its ring overlay's keys come before the panels' (newest first)
  std::unique_ptr<Alarms> alarms_;
  std::vector<int> hooks_;
};

// the layer of the router's last attach (tests and the gallery reach the drawer through it)
SystemLayer* systemLayer();

}  // namespace cadence::screen::routes
