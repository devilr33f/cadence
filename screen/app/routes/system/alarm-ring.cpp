#include "app/routes/system/alarm-ring.h"

#include "app/routes/system/alarms-view.h"
#include "i18n/gen/keys.h"
#include "i18n/locale.h"
#include "ui/core/rml.h"

#include <RmlUi/Core/ElementDocument.h>

#include <ctime>

namespace cadence::screen::routes {

namespace {

using ui::rml::esc;

constexpr const char* kStop = "alarm-ring:stop";
constexpr const char* kSnooze = "alarm-ring:snooze";

}  // namespace

AlarmRing::AlarmRing(Env env) : env_(std::move(env)) {
  hooks_.push_back(env_.ui.addKeyFilter([this](const platform::KeyEvent& e) { return key(e); }));
  hooks_.push_back(env_.ui.addActivateFilter([this](const ui::Activation& a) { return activate(a); }));
  hooks_.push_back(env_.ui.addTicker([this](std::int64_t now) { tick(now); }));
  env_.ui.addDocument("alarm-ring", kZ, [this] { return build(); }, [this](Rml::ElementDocument& d) { loaded(d); });
}

AlarmRing::~AlarmRing() {
  if (uiAbandoned()) return;
  for (const int h : hooks_) env_.ui.remove(h);
  release();
  env_.ui.removeDocument("alarm-ring");
}

Rml::Element* AlarmRing::el(const char* id) const { return doc_ != nullptr ? doc_->GetElementById(id) : nullptr; }

std::string AlarmRing::build() {
  return "<div id=\"arl\" class=\"arl hidden\"><div id=\"arlbox\" class=\"arl-box\"><div id=\"arlicon\" class=\"arl-icon\"/>"
         "<div id=\"arltime\" class=\"arl-time\"/><div id=\"arltitle\" class=\"arl-title\"/>"
         "<div id=\"arlwhere\" class=\"arl-where\"/><div id=\"arlbtns\" class=\"arl-btns\"/></div></div>";
}

void AlarmRing::loaded(Rml::ElementDocument& doc) {
  doc_ = &doc;
  // a language switch builds the layer again: its old scope went with the old document
  scoped_ = false;
  render(true);
}

void AlarmRing::show(const std::optional<alarms::Ring>& ring) {
  const bool same = ring && ring_ && ring->key() == ring_->key();
  if (!ring && !ring_) return;
  ring_ = ring;
  if (!same) pending_ = false;
  render(!same);
}

void AlarmRing::setPending(bool pending) {
  pending_ = pending;
  for (const char* id : {"arl-stop", "arl-snooze"}) {
    if (auto* b = el(id)) b->SetClass("loading", pending);
  }
  env_.ui.markDirty();
}

void AlarmRing::render(bool focus) {
  auto* layer = el("arl");
  if (layer == nullptr) return;
  layer->SetClass("hidden", !ring_);
  minute_ = -1;
  if (!ring_) {
    release();
    env_.ui.markDirty();
    return;
  }
  const auto& r = *ring_;
  if (auto* i = el("arlicon")) i->SetInnerRML(ui::rml::icon(alarms_view::kindIcon(r.kind)));
  if (auto* t = el("arltitle")) t->SetInnerRML(alarms_view::titleOf(env_.tr, r.kind, r.label));
  if (auto* w = el("arlwhere")) w->SetInnerRML(alarms_view::whereText(r.deviceName, r.room));
  const auto minutes = r.snoozeMinutes > 0 ? r.snoozeMinutes : alarms::kDefaultSnoozeMinutes;
  if (auto* b = el("arlbtns")) {
    b->SetInnerRML(ui::rml::button(ui::rml::Button{"[[alarms.stop]]", "close", kStop, true, pending_, false, "arl-stop", {}}) +
                   ui::rml::button(ui::rml::Button{esc(env_.tr.tr(i18n::keys::AlarmsSnooze{minutes})), "timer", kSnooze, false,
                                                   pending_, false, "arl-snooze", {}}));
  }
  tick(0);
  auto* box = el("arlbox");
  if (!scoped_ && box != nullptr) {
    env_.ui.focus().pushScope(box);
    scoped_ = true;
    focus = true;
  }
  if (focus) {
    // «Стоп» first: the safe answer to a ring is to end it
    afterLayout(env_.ui, alive_.guard([this] {
      if (auto* stop = el("arl-stop"); stop != nullptr && ring_) env_.ui.focus().focus(stop);
      env_.ui.markDirty();
    }));
  }
  env_.ui.markDirty();
}

void AlarmRing::release() {
  if (!scoped_) return;
  scoped_ = false;
  if (auto* box = el("arlbox")) env_.ui.focus().dropScope(box);
}

bool AlarmRing::key(const platform::KeyEvent& e) {
  if (!ring_ || env_.ui.levelOpen() || env_.ui.focus().scope() != el("arlbox")) return false;
  // BACK never stops nor hides a ring: only its buttons do
  return e.key == cadence_screen_v1_Key_KEY_BACK;
}

bool AlarmRing::activate(const ui::Activation& a) {
  if (a.action != kStop && a.action != kSnooze) return false;
  if (!ring_ || pending_) return true;
  if (onAct) onAct(*ring_, a.action == kStop ? "stop" : "snooze");
  return true;
}

void AlarmRing::tick(std::int64_t) {
  if (!ring_ || !env_.epochMs) return;
  const auto now = static_cast<std::time_t>(env_.epochMs() / 1000);
  const auto minute = static_cast<std::int64_t>(now / 60);
  if (minute == minute_) return;
  minute_ = minute;
  std::tm local{};
  localtime_r(&now, &local);
  if (auto* t = el("arltime")) t->SetInnerRML(i18n::clock(env_.tr.language(), local.tm_hour, local.tm_min));
  env_.ui.markDirty();
}

}  // namespace cadence::screen::routes
