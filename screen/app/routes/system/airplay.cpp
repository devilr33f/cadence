#include "app/routes/system/airplay.h"

#include "i18n/gen/keys.h"
#include "ui/core/rml.h"

#include <RmlUi/Core/ElementDocument.h>

#include <algorithm>
#include <cstdio>

namespace cadence::screen::routes {

namespace {

using ui::rml::esc;
using ui::rml::icon;

constexpr int kZ = 50;
constexpr int kConnectToastMs = 4000;

}  // namespace

Airplay::Airplay(Env env, const DeviceState& device) : env_(std::move(env)), device_(device) {
  hooks_.push_back(env_.ui.addKeyFilter([this](const platform::KeyEvent& e) { return key(e); }));
  hooks_.push_back(env_.ui.addTicker([this](std::int64_t now) { tick(now); }));
  env_.ui.addDocument("airplay", kZ, [this] { return build(); }, [this](Rml::ElementDocument& d) { loaded(d); });
}

Airplay::~Airplay() {
  if (uiAbandoned()) return;
  if (mirroring_) env_.ui.setSeeThrough(false);
  for (const int h : hooks_) env_.ui.remove(h);
  env_.ui.removeDocument("airplay");
}

Rml::Element* Airplay::el(const char* id) const { return doc_ != nullptr ? doc_->GetElementById(id) : nullptr; }

std::string Airplay::build() {
  return "<div id=\"apbadge\" class=\"apbadge hidden\">" + icon("airplay") +
         "<span>[[airplay.badge]]</span></div>"
         "<div id=\"appin\" class=\"appin hidden\"><div class=\"apbox\"><div class=\"aptitle\">[[airplay.pin_title]]</div>"
         "<div id=\"apdigits\" class=\"apdigits\"/><div id=\"apname\" class=\"apname\"/>"
         "<div class=\"eline apline\"><div id=\"apline\" class=\"elinefill\"/></div></div></div>";
}

void Airplay::loaded(Rml::ElementDocument& doc) {
  doc_ = &doc;
  render();
}

void Airplay::render() {
  if (auto* b = el("apbadge")) b->SetClass("hidden", !mirroring_ || pin_);
  if (auto* p = el("appin")) p->SetClass("hidden", !pin_);
  if (auto* d = el("apdigits")) d->SetInnerRML(esc(digits_));
  if (auto* n = el("apname")) n->SetInnerRML(esc(device_.deviceName));
  lastSecond_ = -1;
  tick(0);
  env_.ui.markDirty();
}

bool Airplay::message(const cadence_screen_v1_Envelope& m) {
  switch (m.which_body) {
    case cadence_screen_v1_Envelope_airplay_pin_tag: {
      const auto& p = m.body.airplay_pin;
      pin_ = p.active;
      digits_ = p.active ? std::string(p.pin) : std::string();
      expiresMs_ = p.expires_at_ms;
      shownMs_ = env_.epochMs();
      render();
      if (onChanged) onChanged();
      return true;
    }
    case cadence_screen_v1_Envelope_mirror_start_tag: {
      const auto& s = m.body.mirror_start;
      mirroring_ = true;
      session_ = s.session_id;
      pin_ = false;
      // the phone's frames are on the video plane under the gl surface: the page steps aside
      env_.ui.setSeeThrough(true);
      // the connect toast, then the corner badge
      env_.ui.toast(ui::ToastSpec{"mirror", ui::ToastTone::Info, esc(env_.tr.tr(i18n::keys::AirplayMirroring{s.client_name})),
                                  "airplay", {}, {}, kConnectToastMs});
      render();
      if (onChanged) onChanged();
      // the app's player hears of it too (it is displaced)
      return false;
    }
    case cadence_screen_v1_Envelope_mirror_stop_tag:
      if (session_.empty() || session_ == m.body.mirror_stop.session_id) {
        mirroring_ = false;
        session_.clear();
        env_.ui.setSeeThrough(false);
        render();
        if (onChanged) onChanged();
      }
      return false;
    case cadence_screen_v1_Envelope_command_result_tag: {
      const auto& r = m.body.command_result;
      if (stopRequest_.empty() || stopRequest_ != r.request_id) return false;
      if (r.code[0] != '\0')
        stopFailed();
      else
        stopRequest_.clear();
      return true;
    }
    default:
      return false;
  }
}

bool Airplay::key(const platform::KeyEvent& e) {
  if (e.key != cadence_screen_v1_Key_KEY_BACK || env_.ui.levelOpen()) return false;
  const bool press = e.action == cadence_screen_v1_KeyAction_KEY_ACTION_PRESS || e.action == cadence_screen_v1_KeyAction_KEY_ACTION_UP;
  if (pin_) return true;
  if (!mirroring_ || !press) return false;
  ui::DialogSpec d;
  d.title = "[[airplay.stop_question]]";
  d.confirmLabel = "[[airplay.stop]]";
  d.onConfirm = [this] { stop(); };
  env_.ui.dialog(std::move(d));
  return true;
}

// cadenced stops the session and lets the sender go; the MirrorStop (reason stopped) that follows clears the badge. a
// toast only when it could not: no link, or a code in its answer
void Airplay::stop() {
  cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
  e.which_body = cadence_screen_v1_Envelope_mirror_stop_tag;
  stopRequest_ = "mirror-stop-" + std::to_string(++stops_);
  copyTo(e.body.mirror_stop.session_id, session_);
  copyTo(e.body.mirror_stop.request_id, stopRequest_);
  if (!env_.out.send(e)) stopFailed();
}

void Airplay::stopFailed() {
  stopRequest_.clear();
  env_.ui.toast(ui::ToastSpec{{}, ui::ToastTone::Info, "[[common.unavailable_now]]", "info", {}, {}, 0});
}

void Airplay::tick(std::int64_t) {
  if (!pin_) return;
  const auto now = env_.epochMs();
  if (now / 1000 == lastSecond_) return;
  lastSecond_ = now / 1000;
  auto* line = el("apline");
  if (line == nullptr) return;
  const double total = static_cast<double>(std::max<std::int64_t>(1, expiresMs_ - shownMs_));
  const double left = std::clamp(static_cast<double>(expiresMs_ - now) / total, 0.0, 1.0);
  char buf[48];
  std::snprintf(buf, sizeof buf, "scaleX(%.4f)", left);
  line->SetProperty("transform", buf);
  env_.ui.markDirty();
}

}  // namespace cadence::screen::routes
