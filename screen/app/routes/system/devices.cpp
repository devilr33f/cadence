#include "app/routes/system/devices.h"

#include "app/routes/system/card-sheet.h"
#include "ui/core/rml.h"

#include <RmlUi/Core/ElementDocument.h>

#include <cstring>

namespace cadence::screen::routes {

namespace {

using ui::rml::icon;

// with the tasks panel's sheet (they never show together), under the drawer
constexpr int kZ = 21;
constexpr const char* kPrefix = "dev:";

Devices*& last() {
  static Devices* d = nullptr;
  return d;
}

bool sameOrder(const std::vector<std::string>& shown, const cadence_cards_v1_Page& page) {
  if (shown.size() != page.cards_count) return false;
  for (pb_size_t i = 0; i < page.cards_count; ++i) {
    if (shown[i] != cards::str(page.cards[i].id)) return false;
  }
  return true;
}

}  // namespace

Devices* devicesPart() { return last(); }

Devices::Devices(Env env, PageRpc& rpc, const DeviceState& device) : env_(std::move(env)), rpc_(rpc), device_(device) {
  cards_ = std::make_unique<cards::CardHost>(env_.ui, sheet::actionSender(env_.out, kPrefix));
  hooks_.push_back(env_.ui.addKeyFilter([this](const platform::KeyEvent& e) { return key(e); }));
  hooks_.push_back(env_.ui.addActivateFilter([this](const ui::Activation& a) { return activate(a); }));
  hooks_.push_back(env_.ui.addTicker([this](std::int64_t now) { tick(now); }));
  env_.ui.addDocument("devices", kZ, [this] { return build(); }, [this](Rml::ElementDocument& d) { loaded(d); });
  last() = this;
}

Devices::~Devices() {
  if (last() == this) last() = nullptr;
  if (uiAbandoned()) return;
  for (const int h : hooks_) env_.ui.remove(h);
  if (open_) env_.ui.focus().popScope();
  env_.ui.removeDocument("devices");
}

Rml::Element* Devices::el(const char* id) const { return doc_ != nullptr ? doc_->GetElementById(id) : nullptr; }

std::string Devices::build() {
  // the tasks panel's sheet: the same layer, scrim, panel and head
  return "<div id=\"dl\" class=\"tlayer\"><div class=\"scrim-sheet tscrim\"/><div id=\"dpanel\" class=\"tpanel\">"
         "<div class=\"tk-head\">" +
         icon("share_to_speaker", "ticon") +
         "<span class=\"tk-title\">[[devices.title]]</span></div>"
         "<div id=\"dbody\" class=\"tbody\"><div id=\"dcards\" class=\"cstack tcards vscroll\"/></div></div></div>";
}

void Devices::loaded(Rml::ElementDocument& doc) {
  doc_ = &doc;
  cards_->clear();
  cards_->attach(el("dcards"), cards::kMaxPageCards, true);
  if (auto* l = el("dl")) {
    l->SetClass("open", open_);
    l->SetClass("under", under_);
  }
  if (!open_) return;
  if (page_) {
    draw(*page_);
  } else {
    empty();
  }
  if (auto* p = el("dpanel")) env_.ui.focus().pushScope(p);
  placed_ = false;
  focusInitial();
}

bool Devices::message(const cadence_screen_v1_Envelope& m) {
  switch (m.which_body) {
    case cadence_screen_v1_Envelope_rpc_response_tag:
      return sheet::actionResponse(m, kPrefix, *cards_);
    case cadence_screen_v1_Envelope_link_status_tag:
      // the feed runs while the link is up; a new link subscribes again (and the open panel with it)
      if (device_.online()) {
        if (feedId_.empty() || !rpc_.open(feedId_)) subscribeFeed();
        if (open_ && (panelId_.empty() || !rpc_.open(panelId_))) subscribePanel();
      } else {
        feedId_.clear();
        panelId_.clear();
        // what played may have changed by the time the link is back
        showItems("{\"items\":[]}");
      }
      return false;
    case cadence_screen_v1_Envelope_route_tag: {
      const auto& r = m.body.route;
      if (r.back) {
        if (!open_) return false;
        close();
        return true;
      }
      if (r.route == cadence_v2_ScreenRoute_SCREEN_ROUTE_DEVICES) {
        open(r.ref);
        return true;
      }
      // another route: the panel closes over it
      if (open_ && r.route != cadence_v2_ScreenRoute_SCREEN_ROUTE_UNSPECIFIED) close();
      return false;
    }
    default:
      return false;
  }
}

void Devices::subscribeFeed() {
  feedId_ = rpc_.call(
      "devices.media", "{}",
      [this](const cadence_v2_RpcResponse& r) {
        if (std::strcmp(r.status, "ok") != 0) {
          feedId_.clear();
          return;
        }
        showItems(sv(r.json));
      },
      [this](const cadence_v2_RpcEvent& ev) {
        showItems(sv(ev.json));
        if (ev.last) feedId_.clear();
      },
      true);
}

void Devices::showItems(std::string_view json) {
  // a broken answer keeps the list there is
  auto items = nearby::parse(json);
  if (!items) return;
  auto sig = nearby::signature(*items);
  items_ = std::move(*items);
  if (sig == itemsSig_) return;
  itemsSig_ = std::move(sig);
  ++revision_;
  if (onItemsChanged) onItemsChanged();
  env_.ui.markDirty();
}

std::string Devices::indicator() const {
  if (items_.empty()) return {};
  return "<div id=\"devices-ind\" class=\"tind dind f\" data-action=\"devices:open\"><div class=\"glow\"/><div class=\"ring\"/>" +
         icon("share_to_speaker") + "<span class=\"tcount\">" + std::to_string(items_.size()) + "</span></div>";
}

void Devices::open(const std::string& focusDeviceId) {
  const bool was = open_;
  focus_ = focusDeviceId;
  placed_ = false;
  if (!was) {
    if (onOpening) onOpening();
    open_ = true;
    sheet::enter(env_.ui, el("dl"));
    if (auto* p = el("dpanel")) env_.ui.focus().pushScope(p);
    if (onOpenChanged) onOpenChanged(true);
  }
  // a new focus asks the server again: it may order the page around it
  if (!panelId_.empty()) rpc_.cancel(panelId_);
  panelId_.clear();
  if (page_) {
    draw(*page_);
  } else {
    empty();
  }
  focusInitial();
  subscribePanel();
  env_.ui.markDirty();
}

void Devices::subscribePanel() {
  const std::string args = focus_.empty() ? "{}" : "{\"focus\":" + jsonString(focus_) + "}";
  panelId_ = rpc_.call(
      "devices.panel", args,
      [this](const cadence_v2_RpcResponse& r) {
        if (std::strcmp(r.status, "ok") != 0) {
          panelId_.clear();
          if (!open_) return;
          env_.ui.toast(ui::ToastSpec{{}, ui::ToastTone::Warning, "[[common.load_failed]]", "triangle", {}, {}, 0});
          if (!page_) empty();
          return;
        }
        if (r.page != nullptr) showPage(*r.page);
      },
      [this](const cadence_v2_RpcEvent& ev) {
        if (ev.page != nullptr) showPage(*ev.page);
        if (ev.last) panelId_.clear();
      },
      true);
}

void Devices::showPage(const cadence_cards_v1_Page& page) {
  page_ = sheet::copyPage(page);
  if (!open_) return;
  if (placed_ && sameOrder(cards_->ids(), page)) {
    update(page);
    return;
  }
  // cards came, went or moved: drawn again, focus kept on its card while that card stays
  const auto mark = sheet::markFocus(el("dcards"), env_.ui.focus().current());
  draw(page);
  if (!placed_) {
    focusInitial();
    return;
  }
  afterLayout(env_.ui, alive_.guard([this, mark] {
    auto* host = el("dcards");
    if (!sheet::restoreFocus(env_.ui, host, mark)) sheet::focusFirst(env_.ui, host);
    env_.ui.markDirty();
  }));
}

void Devices::draw(const cadence_cards_v1_Page& page) {
  cards_->clear();
  if (auto* c = el("dcards")) c->SetInnerRML("");
  if (page.cards_count == 0) {
    empty();
    return;
  }
  cards_->showPage(page);
  env_.ui.markDirty();
}

void Devices::empty() {
  cards_->clear();
  if (auto* c = el("dcards")) c->SetInnerRML("<div class=\"tempty\">[[devices.empty]]</div>");
  env_.ui.markDirty();
}

void Devices::update(const cadence_cards_v1_Page& page) {
  auto* host = el("dcards");
  if (host == nullptr) return;
  const auto mark = sheet::markFocus(host, env_.ui.focus().current());
  auto* body = el("dbody");
  const float scroll = body != nullptr ? body->GetScrollTop() : 0.0f;
  for (pb_size_t i = 0; i < page.cards_count; ++i) cards_->show(page.cards[i]);
  if (body != nullptr) body->SetScrollTop(scroll);
  afterLayout(env_.ui, alive_.guard([this, mark] {
    sheet::restoreFocus(env_.ui, el("dcards"), mark);
    env_.ui.markDirty();
  }));
  env_.ui.markDirty();
}

void Devices::focusInitial() {
  afterLayout(env_.ui, alive_.guard([this] {
    if (!open_ || placed_) return;
    auto* host = el("dcards");
    if (host == nullptr) return;
    // the focus device's card, else the first; nothing yet keeps the next page placing it
    if (!focus_.empty()) {
      Rml::ElementList cs;
      host->QuerySelectorAll(cs, ".ccard");
      for (auto* c : cs) {
        if (nearby::cardOfDevice(c->GetAttribute<Rml::String>("data-card", ""), focus_) && sheet::focusCard(env_.ui, c)) {
          placed_ = true;
          return;
        }
      }
    }
    placed_ = sheet::focusFirst(env_.ui, host);
  }));
}

void Devices::close() {
  if (!open_) return;
  while (env_.ui.levelOpen()) env_.ui.closeLevel();
  open_ = false;
  placed_ = false;
  if (!panelId_.empty()) rpc_.cancel(panelId_);
  panelId_.clear();
  focus_.clear();
  page_.reset();
  env_.ui.focus().popScope();
  sheet::leave(env_.ui, alive_, el("dl"), [this] { return !open_; });
  cards_->clear();
  if (onOpenChanged) onOpenChanged(false);
  env_.ui.markDirty();
}

void Devices::setUnderDrawer(bool under) {
  under_ = under;
  if (auto* l = el("dl")) l->SetClass("under", under);
  env_.ui.markDirty();
}

bool Devices::key(const platform::KeyEvent& e) {
  if (!open_ || under_ || env_.ui.levelOpen() || env_.ui.toastFocused()) return false;
  if (e.key == cadence_screen_v1_Key_KEY_BACK) {
    if (e.action == cadence_screen_v1_KeyAction_KEY_ACTION_PRESS || e.action == cadence_screen_v1_KeyAction_KEY_ACTION_UP) close();
    return true;
  }
  return false;
}

bool Devices::activate(const ui::Activation& a) {
  // the top bar's indicator, and a strip item on home (devices:open:<deviceId>)
  if (a.action == "devices:open") {
    open();
    return true;
  }
  if (a.action.starts_with("devices:open:")) {
    open(a.action.substr(13));
    return true;
  }
  auto* p = el("dpanel");
  if (!open_ || p == nullptr || a.element == nullptr || !p->Contains(a.element)) return false;
  return cards_->activate(a);
}

void Devices::tick(std::int64_t) {
  if (open_ && env_.epochMs) cards_->tick(env_.epochMs());
}

}  // namespace cadence::screen::routes
