#include "ui/core/shell.h"

#include "i18n/locale.h"
#include "ipc/log.h"
#include "ui/core/rml.h"

#include <chrono>
#include <cstring>
#include <ctime>

namespace cadence::screen::ui {

namespace {

std::int64_t epochMs() {
  return std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::system_clock::now().time_since_epoch()).count();
}

ToastTone toneOf(cadence_screen_v1_ToastKind k) {
  switch (k) {
    case cadence_screen_v1_ToastKind_TOAST_KIND_SUCCESS:
      return ToastTone::Success;
    case cadence_screen_v1_ToastKind_TOAST_KIND_WARNING:
      return ToastTone::Warning;
    case cadence_screen_v1_ToastKind_TOAST_KIND_DANGER:
      return ToastTone::Danger;
    default:
      return ToastTone::Info;
  }
}

}  // namespace

ToastSpec toastSpecOf(const cadence_screen_v1_Toast& t) {
  ToastSpec spec;
  spec.id = t.id;
  spec.tone = toneOf(t.kind);
  spec.text = rml::esc(t.text != nullptr ? t.text : "");
  spec.icon = rml::hasIcon(t.icon) ? t.icon : "";
  spec.durationMs = static_cast<int>(t.duration_ms);
  if (t.has_action) {
    // typed: the button's activation names what it does, so nothing parses a server id for it
    const auto& a = t.action;
    const std::string label = a.label != nullptr ? rml::esc(a.label) : "";
    if (a.kind == cadence_screen_v1_ToastActionKind_TOAST_ACTION_KIND_OPEN &&
        a.route == cadence_v2_ScreenRoute_SCREEN_ROUTE_TASK && a.ref[0] != '\0') {
      spec.actionId = std::string("task:") + a.ref;
      spec.actionLabel = label;
    } else if (a.kind == cadence_screen_v1_ToastActionKind_TOAST_ACTION_KIND_RPC && a.id[0] != '\0') {
      spec.actionId = a.id;
      spec.actionLabel = label;
    }
    // any other open (a route this screen does not open from a toast) shows no button
    return spec;
  }
  spec.actionId = t.action_id;
  spec.actionLabel = t.action_label != nullptr ? rml::esc(t.action_label) : "";
  return spec;
}

ShellLayer::ShellLayer() : ui_(std::make_unique<Ui>(translator_)) {
  cards_ = std::make_unique<cards::CardHost>(*ui_, [this](const cards::ActionCall& call) {
    if (outbox_ == nullptr) return false;
    cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
    e.which_body = cadence_screen_v1_Envelope_rpc_request_tag;
    auto& r = e.body.rpc_request;
    std::strncpy(r.id, call.rpcId.c_str(), sizeof r.id - 1);
    std::strncpy(r.method, "card.action", sizeof r.method - 1);
    std::string json = call.json;
    r.json = json.data();
    return outbox_->send(e);
  });
  ui_->onActivate([this](const Activation& a) {
    if (cards_->activate(a)) return;
    if (pages_ && pages_->activate(a)) return;
    if (a.action.starts_with("toast:") && outbox_ != nullptr) {
      // toast.action {toastId, actionId}: the toast's own button
      cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
      e.which_body = cadence_screen_v1_Envelope_rpc_request_tag;
      std::strncpy(e.body.rpc_request.method, "toast.action", sizeof e.body.rpc_request.method - 1);
      std::string json = "{\"actionId\":\"" + rml::esc(a.action.substr(6)) + "\"}";
      e.body.rpc_request.json = json.data();
      outbox_->send(e);
    }
  });
  ui_->setPage([this] { return page(); });
  ui_->onPageLoaded([this](Rml::ElementDocument& doc) {
    if (auto* host = doc.GetElementById("cards")) cards_->attach(host, cards::kMaxDrawerCards, false);
    if (pages_) pages_->loaded(doc);
  });
  ui_->onBack([this] { return pages_ && pages_->back(); });
}

ShellLayer::~ShellLayer() = default;

std::string ShellLayer::page() const {
  std::string s = "<div class=\"safe\"><div style=\"position: absolute; right: 0dp; top: 0dp;\">" + rml::clockTile() + "</div>";
  if (offline_) {
    s += "<div class=\"badge\" style=\"position: absolute; left: 0dp; top: 0dp;\">" + rml::icon("offline") +
         "<span>[[offline.no_server]]</span></div>";
  }
  s += "<div id=\"cards\" class=\"cstack\" style=\"position: absolute; left: 0dp; bottom: 0dp;\"/></div>";
  return s;
}

void ShellLayer::attach(render::RenderBackend& backend, const app::UiGeometry& geometry, app::Outbox& outbox) {
  outbox_ = &outbox;
  geometry_ = geometry;
  std::string error;
  failed_ = !ui_->attach(backend, geometry.screen, geometry.dpRatio, &error);
  if (failed_) CADENCE_LOG(Error, "ui_attach_failed", {{"code", ipc::Token(error)}});
  if (!failed_ && pages_) pages_->attach(*ui_, outbox);
  if (!failed_) volume_ = std::make_unique<VolumeOverlay>(*ui_);
}

void ShellLayer::detach() {
  volume_.reset();
  if (pages_) pages_->detach();
  ui_->detach();
  outbox_ = nullptr;
}

void ShellLayer::resize(const app::UiGeometry& geometry) {
  geometry_ = geometry;
  ui_->resize(geometry.screen, geometry.dpRatio);
}

bool ShellLayer::key(const platform::KeyEvent& event) { return !failed_ && ui_->key(event); }

void ShellLayer::message(const cadence_screen_v1_Envelope& m) {
  if (failed_) return;
  if (pages_) pages_->message(m);
  if (volume_) volume_->message(m);
  switch (m.which_body) {
    case cadence_screen_v1_Envelope_ready_tag:
      translator_.set_language(i18n::parse_lang(m.body.ready.language));
      break;
    case cadence_screen_v1_Envelope_toast_tag:
      ui_->toast(toastSpecOf(m.body.toast));
      break;
    case cadence_screen_v1_Envelope_cards_tag: {
      const auto& c = m.body.cards;
      for (pb_size_t i = 0; i < c.cards_count; ++i) cards_->show(c.cards[i]);
      break;
    }
    case cadence_screen_v1_Envelope_rpc_response_tag:
      cards_->response(m.body.rpc_response);
      break;
    case cadence_screen_v1_Envelope_link_status_tag: {
      const bool offline = m.body.link_status.state == cadence_screen_v1_LinkState_LINK_STATE_OFFLINE;
      if (offline != offline_) {
        offline_ = offline;
        // the screens draw their own offline state
        if (!pages_) ui_->reload();
      }
      break;
    }
    default:
      break;
  }
}

void ShellLayer::playerState(const player::PlayerState& state) {
  if (pages_) pages_->playerState(state);
}

void ShellLayer::tickClock() {
  const auto now = std::time(nullptr);
  const auto minute = static_cast<std::int64_t>(now / 60);
  if (minute == clockMinute_) return;
  clockMinute_ = minute;
  std::tm local{};
  localtime_r(&now, &local);
  Ui::ClockModel c;
  // the clock updates once a minute, no animation
  c.time = i18n::clock(translator_.language(), local.tm_hour, local.tm_min);
  ui_->setClock(c);
}

void ShellLayer::update(std::int64_t nowMs) {
  if (failed_) return;
  tickClock();
  if (pages_) pages_->update(nowMs);
  ui_->update(nowMs);
  cards_->tick(epochMs());
}

bool ShellLayer::dirty() const { return !failed_ && ui_->dirty(); }

std::int64_t ShellLayer::nextUpdateMs(std::int64_t nowMs) const { return failed_ ? -1 : ui_->nextUpdateMs(nowMs); }

// a ui that failed to start shows nothing rather than hold the screen dark
bool ShellLayer::ready() const { return failed_ || ui_->ready(); }

void ShellLayer::render(render::RenderBackend& backend) {
  if (failed_) return;
  // the overscan setting shrinks the whole ui into the safe area: one transform at composite time, no relayout
  ui_->render(backend, geometry_.safeArea.width > 0 ? geometry_.safeArea : render::Rect{0, 0, geometry_.screen.width,
                                                                                          geometry_.screen.height});
}

std::unique_ptr<app::UiLayer> makeUiLayer() { return std::make_unique<ShellLayer>(); }

}  // namespace cadence::screen::ui
