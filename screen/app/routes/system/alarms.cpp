#include "app/routes/system/alarms.h"

#include "i18n/gen/keys.h"
#include "ipc/log.h"
#include "ui/core/rml.h"

#include <algorithm>
#include <cstring>

namespace cadence::screen::routes {

namespace {

Alarms*& last() {
  static Alarms* a = nullptr;
  return a;
}

// the frame's pointers are gone after the message: everything is copied out
alarms::Summary copySummary(const cadence_v2_AlarmsSummary& s) {
  alarms::Summary out;
  out.revision = s.revision;
  for (pb_size_t i = 0; s.devices != nullptr && i < s.devices_count; ++i) {
    const auto& d = s.devices[i];
    alarms::Device x;
    x.id = d.device_id;
    x.name = sv(d.name);
    x.room = sv(d.room_name);
    x.here = d.here;
    x.sameRoom = d.same_room;
    x.snoozeMinutes = d.snooze_minutes;
    for (pb_size_t j = 0; d.items != nullptr && j < d.items_count; ++j) {
      const auto& it = d.items[j];
      alarms::Item y;
      y.kind = it.kind;
      y.id = it.id;
      y.label = sv(it.label);
      y.time = it.time;
      for (pb_size_t k = 0; k < it.days_count; ++k) y.days.emplace_back(it.days[k]);
      y.nextMs = it.next_unix_ms;
      y.seconds = it.seconds;
      y.snoozed = it.snoozed;
      x.items.push_back(std::move(y));
    }
    out.devices.push_back(std::move(x));
  }
  for (pb_size_t i = 0; i < s.ringing_count; ++i) {
    const auto& r = s.ringing[i];
    alarms::Ring x;
    x.deviceId = r.device_id;
    x.deviceName = sv(r.device_name);
    x.room = sv(r.room_name);
    x.kind = r.kind;
    x.id = r.id;
    x.label = sv(r.label);
    x.time = r.time;
    x.seconds = r.seconds;
    x.startedMs = r.started_unix_ms;
    x.own = r.own;
    x.snoozeMinutes = r.snooze_minutes;
    out.ringing.push_back(std::move(x));
  }
  return out;
}

}  // namespace

Alarms* alarmsPart() { return last(); }

Alarms::Alarms(Env env, PageRpc& rpc) : env_(std::move(env)), rpc_(rpc) {
  ring_ = std::make_unique<AlarmRing>(env_);
  ring_->onAct = [this](const alarms::Ring& r, const std::string& action) {
    ring_->setPending(true);
    act(r.deviceId, r.kind, r.id, action, [this](bool) { ring_->setPending(false); });
  };
  hooks_.push_back(env_.ui.addActivateFilter([this](const ui::Activation& a) { return activate(a); }));
  last() = this;
}

Alarms::~Alarms() {
  if (last() == this) last() = nullptr;
  if (uiAbandoned()) return;
  for (const int h : hooks_) env_.ui.remove(h);
  ring_.reset();
}

bool Alarms::message(const cadence_screen_v1_Envelope& m) {
  switch (m.which_body) {
    case cadence_screen_v1_Envelope_alarms_summary_tag:
      summary(m.body.alarms_summary);
      return true;
    case cadence_screen_v1_Envelope_display_power_tag:
      awake_ = m.body.display_power.on;
      follow();
      return false;
    case cadence_screen_v1_Envelope_ready_tag:
      // a new link: cadenced sends the summary again, its revision may start over
      summarized_ = false;
      return false;
    default:
      return false;
  }
}

void Alarms::summary(const cadence_v2_AlarmsSummary& s) {
  if (summarized_ && s.revision != 0 && s.revision <= summaryRevision_) return;
  summarized_ = true;
  summaryRevision_ = s.revision;
  apply(copySummary(s));
}

void Alarms::apply(alarms::Summary summary) {
  summary_ = std::move(summary);
  // the app lit the display for this screen's own ring (app.cpp): it is awake from here
  if (alarms::wakesDisplay(summary_)) awake_ = true;
  auto sig = alarms::signature(summary_);
  if (sig != sig_) {
    sig_ = std::move(sig);
    ++revision_;
    env_.ui.markDirty();
  }
  follow();
}

void Alarms::follow() {
  std::erase_if(dismissed_, [this](const std::string& key) { return !alarms::listed(summary_, key); });
  const auto shown = alarms::shownRing(summary_, awake_, dismissed_);
  const bool was = ring_->shown();
  ring_->show(shown);
  if (shown && !was) {
    CADENCE_LOG(Info, "alarm_ring_shown", {{"kind", ipc::Token(shown->kind)}, {"own", shown->own}});
  } else if (!shown && was) {
    CADENCE_LOG(Info, "alarm_ring_hidden");
  }
}

void Alarms::act(const std::string& deviceId, const std::string& kind, const std::string& id, const std::string& action,
                 std::function<void(bool ok)> done) {
  const std::string args = "{\"deviceId\":" + jsonString(deviceId) + ",\"kind\":" + jsonString(kind) + ",\"id\":" + jsonString(id) +
                           ",\"action\":" + jsonString(action) + "}";
  auto failed = [this, kind, action, done](const std::string& code) {
    CADENCE_LOG(Info, "alarms_act_failed", {{"kind", ipc::Token(kind)}, {"action", ipc::Token(action)}, {"code", ipc::Token(code)}});
    env_.ui.toast(ui::ToastSpec{{}, ui::ToastTone::Warning, ui::rml::esc(env_.tr.tr(i18n::keys::AlarmsFailed{code})), "triangle",
                                {}, {}, 0});
    if (done) done(false);
  };
  const std::string key = deviceId + "/" + kind + "/" + id;
  const auto id2 = rpc_.call("alarms.act", args, [this, failed, done, kind, action, key](const cadence_v2_RpcResponse& r) {
    if (std::strcmp(r.status, "ok") == 0) {
      CADENCE_LOG(Info, "alarms_act", {{"kind", ipc::Token(kind)}, {"action", ipc::Token(action)}});
      // the summary that drops a stopped or snoozed ring may come after the answer: it goes from here at once
      if (action == "stop" || action == "snooze") dismissed_.push_back(key);
      if (done) done(true);
      follow();
      return;
    }
    // the refusal's code (offline, not_found, unsupported, ...), else the call's status
    auto code = jsonField(sv(r.json), "code");
    failed(code.empty() ? std::string(r.status) : code);
  });
  if (id2.empty()) failed("unavailable");
}

bool Alarms::activate(const ui::Activation& a) {
  // home's strip (alarms:open:<deviceId>)
  if (a.action == "alarms:open") {
    if (onOpenPage) onOpenPage({});
    return true;
  }
  if (a.action.starts_with("alarms:open:")) {
    if (onOpenPage) onOpenPage(a.action.substr(12));
    return true;
  }
  return false;
}

}  // namespace cadence::screen::routes
