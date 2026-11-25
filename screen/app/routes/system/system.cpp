#include "app/routes/system/system.h"

#include "ui/core/rml.h"

#include <RmlUi/Core/ElementDocument.h>

#include <chrono>
#include <cstring>

namespace cadence::screen::routes {

namespace {

SystemLayer*& last() {
  static SystemLayer* l = nullptr;
  return l;
}

}  // namespace

SystemLayer* systemLayer() { return last(); }

SystemLayer::SystemLayer(Services& s, app::Outbox& out, std::shared_ptr<SystemState> state)
    : s_(s), state_(std::move(state)), rpc_(out, "i3-") {
  state_->out = &out;
  if (!state_->epochMs) {
    state_->epochMs = [] {
      return std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::system_clock::now().time_since_epoch()).count();
    };
  }
  Env env{s.ui, out, s.ui.translator(), [st = state_.get()] { return st->epochMs(); }};
  tasks_ = std::make_unique<Tasks>(env, rpc_, state_->device);
  devices_ = std::make_unique<Devices>(env, rpc_, state_->device);
  drawer_ = std::make_unique<Drawer>(env, rpc_, state_->device);
  airplay_ = std::make_unique<Airplay>(env, state_->device);
  alarms_ = std::make_unique<Alarms>(env, rpc_);
  tasks_->onOpenChanged = [this](bool) { s_.router.contextChanged(); };
  devices_->onOpenChanged = [this](bool) { s_.router.contextChanged(); };
  // one right sheet at a time: the other closes before the new one takes focus
  tasks_->onOpening = [this] { devices_->close(); };
  devices_->onOpening = [this] { tasks_->close(); };
  drawer_->onOpenChanged = [this](bool open) {
    // the duck stands in for a panel's scrim under it
    tasks_->setUnderDrawer(open);
    devices_->setUnderDrawer(open);
    s_.router.contextChanged();
    // a press opened it: the agent reads the screen as it is now (a turn's start)
    if (open) {
      if (auto* p = s_.router.publisher()) {
        p->set(s_.router.computeContext(), s_.ui.nowMs());
        p->flush();
      }
    }
  };
  drawer_->onRoomSettings = [this] { Settings::openRoom(s_.router); };
  airplay_->onChanged = [this] { s_.router.contextChanged(); };
  alarms_->onOpenPage = [this](const std::string& deviceId) {
    RouteArgs a;
    a.ref = deviceId;
    s_.router.open(cadence_v2_ScreenRoute_SCREEN_ROUTE_ALARMS, a);
  };
  hooks_.push_back(s.ui.addActivateFilter([this](const ui::Activation& a) { return activate(a); }));
  last() = this;
}

SystemLayer::~SystemLayer() {
  if (uiAbandoned()) return;
  if (last() == this) last() = nullptr;
  for (const int h : hooks_) s_.ui.remove(h);
  alarms_.reset();
  airplay_.reset();
  drawer_.reset();
  devices_.reset();
  tasks_.reset();
}

Enroll* SystemLayer::enroll() const {
  return dynamic_cast<Enroll*>(s_.router.find(cadence_v2_ScreenRoute_SCREEN_ROUTE_ENROLL));
}

void SystemLayer::status(app::UiStatus& s) const {
  if (airplay_->pinShown()) s.route = cadence_v2_ScreenRoute_SCREEN_ROUTE_AIRPLAY;
  else if (tasks_->isOpen()) s.route = tasks_->inTask() ? cadence_v2_ScreenRoute_SCREEN_ROUTE_TASK : cadence_v2_ScreenRoute_SCREEN_ROUTE_TASKS;
  else if (devices_->isOpen()) s.route = cadence_v2_ScreenRoute_SCREEN_ROUTE_DEVICES;
  s.drawerOpen = drawer_->isOpen();
}

void SystemLayer::context(ScreenContextData& c) const {
  // a panel over the page is where the viewer is; the drawer says it is open
  if (airplay_->pinShown()) {
    c.route = cadence_v2_ScreenRoute_SCREEN_ROUTE_AIRPLAY;
  } else if (tasks_->isOpen()) {
    c.route = tasks_->inTask() ? cadence_v2_ScreenRoute_SCREEN_ROUTE_TASK : cadence_v2_ScreenRoute_SCREEN_ROUTE_TASKS;
    c.pageRef = tasks_->inTask() ? tasks_->taskRef() : std::string();
    c.focusedRef.clear();
    c.visibleRefs.clear();
  } else if (devices_->isOpen()) {
    // the panel's page ref is the device it was opened on (ids, never names)
    c.route = cadence_v2_ScreenRoute_SCREEN_ROUTE_DEVICES;
    c.pageRef = devices_->focusDevice();
    c.focusedRef.clear();
    c.visibleRefs.clear();
  }
  c.drawerOpen = drawer_->isOpen();
}

std::string SystemLayer::topBar() const {
  return "<div class=\"topbar-i3\">" + tasks_->indicator() + devices_->indicator() + micButton(s_.ui.translator(), state_->device) +
         "<div id=\"settings-btn\" class=\"micbtn f\" data-action=\"settings:open\"><div class=\"glow\"/><div class=\"ring\"/>" +
         ui::rml::icon("settings") + "<span class=\"micword\">[[settings.title]]</span></div></div>";
}

void SystemLayer::micFromTurn(const cadence_v2_AssistantTurn& t) {
  if (!t.has_mic_source) return;
  auto& d = state_->device;
  switch (t.mic_source.kind) {
    case cadence_v2_MicSourceKind_MIC_SOURCE_KIND_NONE:
      d.mic = Mic::None;
      break;
    case cadence_v2_MicSourceKind_MIC_SOURCE_KIND_SPEAKER:
      d.mic = Mic::Speaker;
      d.micDeviceId = t.mic_source.device_id;
      break;
    case cadence_v2_MicSourceKind_MIC_SOURCE_KIND_LOCAL:
      d.mic = Mic::Local;
      break;
    default:
      return;
  }
  d.micOffline = t.mic_source.offline;
}

void SystemLayer::claim(const cadence_screen_v1_Envelope& m) {
  if (enroll() == nullptr) s_.router.open(cadence_v2_ScreenRoute_SCREEN_ROUTE_ENROLL);
  if (auto* e = enroll()) e->message(m);
}

bool SystemLayer::message(const cadence_screen_v1_Envelope& m) {
  bool deviceChanged = state_->device.apply(m);
  // Ready answers the Hello: the microphone the owner picked goes after it, each link
  if (m.which_body == cadence_screen_v1_Envelope_ready_tag && state_->out != nullptr) sendMicChoice(*state_->out, state_->mic);
  if (m.which_body == cadence_screen_v1_Envelope_capability_config_tag &&
      std::strcmp(m.body.capability_config.capability, "display.screen") == 0 && m.body.capability_config.json != nullptr &&
      applyScreenConfig(state_->local, m.body.capability_config.json)) {
    // the server's values are the page's (a write of this tv's, or the dashboard's)
    s_.ui.setReducedMotion(state_->local.reducedMotion);
    deviceChanged = true;
  }
  if (rpc_.message(m)) return true;
  // before the drawer and the panels: display_power and ready reach it whatever they do with them
  if (alarms_->message(m)) return true;
  if (m.which_body == cadence_screen_v1_Envelope_assistant_turn_tag) {
    micFromTurn(m.body.assistant_turn);
    drawer_->message(m);
    // the router still sends the context at the turn's start
    return false;
  }
  if (drawer_->message(m)) return true;
  if (tasks_->message(m)) return true;
  if (devices_->message(m)) return true;
  if (airplay_->message(m)) return true;
  switch (m.which_body) {
    case cadence_screen_v1_Envelope_claim_state_tag:
      claim(m);
      return true;
    case cadence_screen_v1_Envelope_link_status_tag: {
      const auto st = m.body.link_status.state;
      // no token, or the server refused it: enrollment covers everything
      if (st == cadence_screen_v1_LinkState_LINK_STATE_UNENROLLED || st == cadence_screen_v1_LinkState_LINK_STATE_REMOVED) {
        claim(m);
      } else if (auto* e = enroll()) {
        e->message(m);
      }
      return false;
    }
    default:
      break;
  }
  if (deviceChanged) {
    // what settings show changed under them
    if (auto* top = dynamic_cast<Settings*>(s_.router.top())) {
      (void)top;
      s_.router.refresh();
    }
  }
  return false;
}

bool SystemLayer::activate(const ui::Activation& a) {
  if (a.action == "mic:open") {
    // ready opens the assistant, otherwise the room and microphone settings
    const std::string state = state_->device.micState();
    if (state == "ready" || state == "unknown") {
      drawer_->invoke();
    } else {
      Settings::openRoom(s_.router);
    }
    return true;
  }
  if (a.action == "settings:open") {
    s_.router.open(cadence_v2_ScreenRoute_SCREEN_ROUTE_SETTINGS);
    return true;
  }
  // home's «Ассистент» and the last entry of every actions sheet
  if (a.action == "assistant") {
    drawer_->invoke();
    return true;
  }
  return false;
}

}  // namespace cadence::screen::routes
