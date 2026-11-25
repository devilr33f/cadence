#pragma once

#include "app/routes/system/alarm-ring.h"
#include "app/routes/system/alarms-model.h"
#include "app/routes/system/env.h"
#include "app/routes/system/page-rpc.h"

#include <cstdint>
#include <functional>
#include <memory>
#include <string>
#include <vector>

namespace cadence::screen::routes {

// the household's alarms on this screen: the server's AlarmsSummary as cadenced relays it (at the link's start and
// on each change), home's strip and the alarms page read it, and the ring overlay shows the first ring this screen
// may: its own always (cadenced's display is woken for it, app.cpp), another device's only while the display is
// awake, so a ring elsewhere never lights a sleeping tv. stop, snooze and cancel go as rpc alarms.act; a refusal is a
// short toast. nothing here sets an alarm (rule 7). logs carry ids, kinds and codes alone (rule 8)
class Alarms {
 public:
  Alarms(Env env, PageRpc& rpc);
  ~Alarms();
  Alarms(const Alarms&) = delete;
  Alarms& operator=(const Alarms&) = delete;

  // alarms_summary (true: it was this part's), display_power, ready and link_status
  bool message(const cadence_screen_v1_Envelope& m);
  // a summary as the link would hand it, already copied (tests and the sim)
  void apply(alarms::Summary summary);

  [[nodiscard]] const alarms::Summary& summary() const { return summary_; }
  // one higher whenever what the strip and the page draw changed
  [[nodiscard]] std::uint64_t revision() const { return revision_; }
  [[nodiscard]] bool awake() const { return awake_; }
  [[nodiscard]] std::int64_t epochMs() const { return env_.epochMs ? env_.epochMs() : 0; }
  [[nodiscard]] AlarmRing& ring() { return *ring_; }

  // alarms.act on a device's item: action stop, snooze or cancel; done(true) once the server took it
  void act(const std::string& deviceId, const std::string& kind, const std::string& id, const std::string& action,
           std::function<void(bool ok)> done = {});
  // the alarms page, on a device ("" for none): the layer opens it on the router
  std::function<void(const std::string& deviceId)> onOpenPage;

 private:
  bool activate(const ui::Activation& a);
  void summary(const cadence_v2_AlarmsSummary& s);
  // the overlay follows the summary, the display and what was stopped
  void follow();

  Env env_;
  PageRpc& rpc_;
  std::unique_ptr<AlarmRing> ring_;
  std::vector<int> hooks_;
  alarms::Summary summary_;
  std::string sig_;
  std::uint64_t revision_ = 0;
  // the last summary's revision on this link; a lower one is late and dropped
  bool summarized_ = false;
  std::uint64_t summaryRevision_ = 0;
  bool awake_ = true;
  // rings a stop or snooze ended here, hidden until a summary stops listing them
  std::vector<std::string> dismissed_;
};

// the alarms part of the router's last attach (home's strip and the alarms page read it; null without the layer)
Alarms* alarmsPart();

}  // namespace cadence::screen::routes
