#include "app/routes/system/enroll.h"

#include "i18n/gen/keys.h"
#include "i18n/locale.h"
#include "ui/core/rml.h"
#include "ui/rml/sprites.h"

#include <RmlUi/Core/ElementDocument.h>

#include <algorithm>
#include <cctype>
#include <cstdio>
#include <cstring>

namespace cadence::screen::routes {

namespace {

using ui::rml::Button;
using ui::rml::esc;
using ui::rml::icon;

// «Подключено» stays this long before home
constexpr std::int64_t kConnectedMs = 2000;

std::string button(const char* label, const char* action, const char* id, bool primary = false, bool first = false) {
  return ui::rml::button(Button{label, {}, action, primary, false, false, id, first ? "default-focus" : ""});
}

}  // namespace

std::string Enroll::grouped(std::string_view code) {
  if (code.size() != 6) return std::string(code);
  return std::string(code.substr(0, 3)) + " · " + std::string(code.substr(3));
}

Enroll::Enroll(Services& s, std::shared_ptr<SystemState> state)
    : Route(s), state_(std::move(state)), env_{s.ui, *state_->out, s.ui.translator(), [st = state_.get()] { return st->epochMs(); }}, device_(state_->device) {
  hooks_.push_back(env_.ui.addTicker([this](std::int64_t now) { tick(now); }));
}

Enroll::~Enroll() {
  if (uiAbandoned()) return;
  for (const int h : hooks_) env_.ui.remove(h);
}

Rml::Element* Enroll::el(const char* id) const { return doc_ != nullptr ? doc_->GetElementById(id) : nullptr; }

void Enroll::message(const cadence_screen_v1_Envelope& m) {
  if (m.which_body == cadence_screen_v1_Envelope_link_status_tag) {
    const auto& l = m.body.link_status;
    switch (l.state) {
      case cadence_screen_v1_LinkState_LINK_STATE_UNENROLLED:
        wanted_ = true;
        if (phase_ == Phase::Connected) set(Phase::Connecting);
        return;
      case cadence_screen_v1_LinkState_LINK_STATE_REMOVED:
        // removed from cadence: said so, then back to the claim when cadenced opens one
        wanted_ = true;
        why_.clear();
        code_.clear();
        set(Phase::Removed);
        return;
      case cadence_screen_v1_LinkState_LINK_STATE_OFFLINE:
        if (!wanted_) return;
        error_ = l.code;
        if (phase_ != Phase::Code && phase_ != Phase::Address) set(error_ == "no_network" ? Phase::NoNetwork : Phase::Unreachable);
        return;
      case cadence_screen_v1_LinkState_LINK_STATE_ONLINE:
        // enrolled (by the claim or a typed code): connected, then home
        if (wanted_ && phase_ != Phase::Connected) {
          connectedMs_ = env_.ui.nowMs();
          set(Phase::Connected);
        }
        return;
      default:
        return;
    }
  }
  if (m.which_body != cadence_screen_v1_Envelope_claim_state_tag) return;
  const auto& c = m.body.claim_state;
  wanted_ = true;
  switch (c.phase) {
    case cadence_screen_v1_ClaimPhase_CLAIM_PHASE_PENDING: {
      const std::string code = c.code;
      // a fresh code cross-fades in by itself
      freshCode_ = !code_.empty() && code != code_;
      if (code != code_) issuedMs_ = env_.epochMs();
      code_ = code;
      url_ = sv(c.url);
      expiresMs_ = c.expires_at_ms;
      if (phase_ == Phase::Removed && !why_.empty()) why_.clear();
      if (phase_ == Phase::Code || phase_ == Phase::Address) return;
      set(Phase::Claim);
      return;
    }
    case cadence_screen_v1_ClaimPhase_CLAIM_PHASE_APPROVED:
      connectedMs_ = env_.ui.nowMs();
      set(Phase::Connected);
      return;
    case cadence_screen_v1_ClaimPhase_CLAIM_PHASE_DECLINED:
      why_ = "[[enroll.declined]]";
      set(Phase::Claim);
      return;
    case cadence_screen_v1_ClaimPhase_CLAIM_PHASE_EXPIRED:
      why_ = "[[enroll.new_code]]";
      set(Phase::Claim);
      return;
    case cadence_screen_v1_ClaimPhase_CLAIM_PHASE_FAILED:
      error_ = c.error_code;
      set(error_ == "no_network" ? Phase::NoNetwork : Phase::Unreachable);
      return;
    default:
      return;
  }
}

void Enroll::set(Phase p) {
  phase_ = p;
  changed();
  // the page again from the model, focus kept by id
  if (current()) s_.router.refresh();
}

void Enroll::done() {
  // home under it, else whatever was there
  if (s_.router.find(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME) != nullptr) {
    s_.router.popTo(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  } else {
    s_.router.pop();
  }
}

std::string Enroll::notice() const {
  if (why_.empty()) return {};
  return "<div class=\"enotice\">" + icon("info", "tone-info") + "<span>" + why_ + "</span></div>";
}

std::string Enroll::claimBody() const {
  std::string s = "<div class=\"eclaim\"><div class=\"eqr\" style=\"decorator: image(&quot;gen:qr:" + ui::hexOf(url_) +
                  "&quot;);\"/><div class=\"eright\">";
  s += "<div id=\"ecode\" class=\"ecode" + std::string(freshCode_ ? " fresh" : "") + "\">" + esc(grouped(code_)) + "</div>";
  s += "<div class=\"emeta\">[[enroll.open_phone]]</div>";
  s += "<div class=\"eline\"><div id=\"eline\" class=\"elinefill\"/></div><div id=\"eexpiry\" class=\"eexpiry\"/>";
  s += notice();
  s += "<div class=\"ebuttons\">" + button("[[enroll.have_code]]", "enroll:code", "e-have", false, true) + "</div>";
  return s + "</div></div>";
}

std::string Enroll::codeBody() const {
  std::string s = "<div class=\"en-head\">[[enroll.code_title]]</div><div class=\"emeta\">[[enroll.code_hint]]</div>"
                  "<div id=\"eboxes\" class=\"eboxes\">" + boxes() + "</div><div class=\"ekeys\">";
  for (const char c : kCodeAlphabet) {
    const std::string k(1, c);
    s += "<div class=\"key f\" id=\"ek-" + k + "\" data-action=\"code:" + k + "\">" + k + "</div>";
  }
  s += "<div class=\"key wide2 f\" id=\"ek-del\" data-action=\"code:del\">" + icon("backspace") + "</div>";
  s += "</div><div class=\"ebuttons\">" +
       ui::rml::button(Button{"[[enroll.connect]]", {}, "code:send", entry_.size() == kCodeLength, false, false, "e-send",
                              entry_.size() == kCodeLength ? "" : "unavailable"}) +
       "</div>";
  return s;
}

std::string Enroll::boxes() const {
  std::string s;
  for (std::size_t i = 0; i < kCodeLength; ++i) {
    const bool filled = i < entry_.size();
    s += "<div class=\"ebox" + std::string(filled ? " filled" : "") + (i == entry_.size() ? " cur" : "") + "\">" +
         (filled ? esc(entry_.substr(i, 1)) : std::string()) + "</div>";
  }
  return s;
}

void Enroll::refreshEntry() {
  // typing changes the boxes or the entry line in place: focus stays on its key
  if (phase_ == Phase::Code) {
    if (auto* b = el("eboxes")) b->SetInnerRML(boxes());
    if (auto* send = el("e-send")) {
      const bool full = entry_.size() == kCodeLength;
      send->SetClass("primary", full);
      send->SetClass("unavailable", !full);
    }
  }
  if (phase_ == Phase::Address && doc_ != nullptr) {
    if (auto* e = doc_->QuerySelector(".osk .entry")) e->SetInnerRML(esc(address_) + "<span class=\"caret\">|</span>");
  }
  env_.ui.markDirty();
}

std::string Enroll::addressBody() const {
  return "<div class=\"en-head\">[[enroll.address_title]]</div><div class=\"eaddr\">" +
         ui::rml::keyboard(latin_ ? ui::rml::KeyLayout::Latin : ui::rml::KeyLayout::Digits, address_) +
         "<div class=\"ebuttons\">" + button("[[enroll.save]]", "address:save", "e-save", true) +
         button("[[common.cancel]]", "address:cancel", "e-cancel") + "</div></div>";
}

std::string Enroll::build() {
  const std::string address = esc(device_.serverUrl.empty() ? std::string("—") : device_.serverUrl);
  std::string s = "<div class=\"safe ewrap\">";
  switch (phase_) {
    case Phase::Connecting:
      s += "<div class=\"etitle\">[[enroll.title]]</div><div class=\"ewhat\">[[enroll.what]]</div>"
           "<div class=\"hstack econnect\"><div class=\"spinner\"/><span>[[enroll.connecting]]</span></div>"
           "<div class=\"eaddress\">" +
           address + "</div>";
      break;
    case Phase::Unreachable:
      s += "<div class=\"etitle\">[[enroll.title]]</div><div class=\"estate\">" + icon("offline") +
           "<div class=\"stitle\">[[enroll.unreachable]]</div><div class=\"eaddress\">" + address + " · " +
           esc(env_.tr.tr(i18n::keys::EnrollReason{error_.empty() ? "unreachable" : error_})) +
           "</div><div class=\"ebuttons\">" + button("[[common.retry]]", "enroll:retry", "e-retry", false, true) +
           button("[[enroll.change_address]]", "enroll:address", "e-address") + "</div></div>";
      break;
    case Phase::NoNetwork:
      s += "<div class=\"etitle\">[[enroll.title]]</div><div class=\"estate\">" + icon("wifi_off") +
           "<div class=\"stitle\">[[offline.no_network]]</div><div class=\"ebuttons\">" +
           button("[[enroll.network_settings]]", "enroll:network", "e-network", false, true) + "</div></div>";
      break;
    case Phase::Claim:
      s += "<div class=\"etitle\">[[enroll.title]]</div>" + claimBody();
      break;
    case Phase::Connected: {
      const std::string name = device_.deviceName.empty() ? std::string("Cadence") : device_.deviceName;
      const std::string room = device_.roomName.empty() ? env_.tr.tr("room.no_room") : device_.roomName;
      s += "<div class=\"estate econnected\">" + icon("check", "tone-success") + "<div class=\"etitle\">[[enroll.connected]]</div>" +
           "<div class=\"ewhat\">" + esc(env_.tr.tr(i18n::keys::EnrollWhere{name, room})) + "</div>";
      if (device_.mic == Mic::Speaker && !device_.roomName.empty()) {
        s += "<div class=\"emeta\">" + esc(env_.tr.tr(i18n::keys::EnrollVoice{device_.roomName})) + "</div>";
      }
      s += "</div>";
      break;
    }
    case Phase::Removed:
      s += "<div class=\"estate\">" + icon("octagon", "tone-danger") +
           "<div class=\"stitle\">[[enroll.removed]]</div><div class=\"hstack econnect\"><div class=\"spinner\"/><span>"
           "[[enroll.new_code]]</span></div></div>";
      break;
    case Phase::Code:
      s += codeBody();
      break;
    case Phase::Address:
      s += addressBody();
      break;
  }
  return s + "</div>";
}

void Enroll::loaded(Rml::ElementDocument& doc, bool) {
  doc_ = &doc;
  lastSecond_ = -1;
  refreshExpiry();
  if (freshCode_) {
    // the new code fades in (the old one is gone with the page it was on)
    env_.ui.after(0, alive_.guard([this] {
      if (auto* c = el("ecode")) c->SetClass("fresh", false);
      freshCode_ = false;
    }));
  }
}

void Enroll::refreshExpiry() {
  auto* text = el("eexpiry");
  auto* line = el("eline");
  if (text == nullptr || line == nullptr) return;
  const auto now = env_.epochMs();
  const auto left = std::max<std::int64_t>(0, (expiresMs_ - now + 999) / 1000);
  const auto total = std::max<std::int64_t>(1, (expiresMs_ - issuedMs_) / 1000);
  // the expiry as a shrinking cadence line, and its words
  const std::string t = env_.tr.tr(i18n::keys::EnrollCodeValid{i18n::countdown(left)});
  if (text->GetInnerRML() != esc(t)) text->SetInnerRML(esc(t));
  char buf[48];
  std::snprintf(buf, sizeof buf, "scaleX(%.4f)", std::clamp(static_cast<double>(left) / static_cast<double>(total), 0.0, 1.0));
  line->SetProperty("transform", buf);
  env_.ui.markDirty();
}

void Enroll::tick(std::int64_t nowMs) {
  now_ = nowMs;
  if (phase_ == Phase::Claim) {
    // when the seconds shown change: the clock ticks without a transition
    const auto left = std::max<std::int64_t>(0, (expiresMs_ - env_.epochMs() + 999) / 1000);
    if (left != lastSecond_) {
      lastSecond_ = left;
      refreshExpiry();
    }
  }
  if (phase_ == Phase::Connected && connectedMs_ >= 0 && nowMs - connectedMs_ >= kConnectedMs) {
    connectedMs_ = -1;
    wanted_ = false;
    done();
  }
}

bool Enroll::back() {
  // the fallbacks go back to the claim; the claim itself has nowhere to go back to
  if (phase_ == Phase::Code || phase_ == Phase::Address) {
    set(code_.empty() ? Phase::Connecting : Phase::Claim);
    if (phase_ == Phase::Connecting && !error_.empty()) set(Phase::Unreachable);
  }
  return true;
}

bool Enroll::activate(const ui::Activation& a) {
  const auto& act = a.action;
  if (act == "enroll:code") {
    entry_.clear();
    set(Phase::Code);
    return true;
  }
  if (act == "enroll:address") {
    address_ = device_.serverUrl;
    latin_ = true;
    set(Phase::Address);
    return true;
  }
  if (act == "enroll:retry") {
    // cadenced retries by itself; the screen says it is connecting again
    error_.clear();
    set(Phase::Connecting);
    return true;
  }
  if (act == "enroll:network") {
    // the os's network settings: not reachable from the screen yet (a stub)
    env_.ui.toast(ui::ToastSpec{{}, ui::ToastTone::Info, "[[common.unavailable_now]]", "info", {}, {}, 0});
    return true;
  }
  if (act.starts_with("code:")) {
    const auto k = act.substr(5);
    if (k == "del") {
      if (!entry_.empty()) entry_.pop_back();
    } else if (k == "send") {
      if (entry_.size() != kCodeLength) return true;
      cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
      e.which_body = cadence_screen_v1_Envelope_enroll_code_tag;
      copyTo(e.body.enroll_code.code, entry_);
      const bool sent = env_.out.send(e);
      env_.ui.toast(ui::ToastSpec{{}, sent ? ui::ToastTone::Info : ui::ToastTone::Warning,
                                  sent ? "[[enroll.code_sent]]" : "[[enroll.failed]]", sent ? "info" : "triangle", {}, {}, 0});
      return true;
    } else if (k.size() == 1 && kCodeAlphabet.find(k[0]) != std::string_view::npos && entry_.size() < kCodeLength) {
      entry_ += k;
    }
    refreshEntry();
    return true;
  }
  if (act.starts_with("key:")) {
    const auto k = act.substr(4);
    if (k == "backspace") {
      // one code point back
      while (!address_.empty() && (static_cast<unsigned char>(address_.back()) & 0xC0) == 0x80) address_.pop_back();
      if (!address_.empty()) address_.pop_back();
    } else if (k == "space" || k == "voice") {
      return true;
    } else if (address_.size() < 200) {
      address_ += k;
    }
    refreshEntry();
    return true;
  }
  if (act.starts_with("layout:")) {
    latin_ = act != "layout:digits";
    set(Phase::Address);
    return true;
  }
  if (act == "address:save") {
    cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
    e.which_body = cadence_screen_v1_Envelope_server_address_tag;
    std::string url = address_;
    e.body.server_address.url = url.data();
    env_.out.send(e);
    error_.clear();
    set(Phase::Connecting);
    return true;
  }
  if (act == "address:cancel") {
    back();
    return true;
  }
  return false;
}

}  // namespace cadence::screen::routes
