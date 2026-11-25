#include "app/routes/panels/panel.h"

#include "ui/core/rml.h"

#include <RmlUi/Core/ElementDocument.h>

#include <cstring>
#include <nlohmann/json.hpp>

namespace cadence::screen::routes {

void PanelContent::open(SideSheet& sheet, Rml::Element* body) {
  sheet_ = &sheet;
  auto& c = sheet.context();
  body->SetInnerRML("<div id=\"panel-state\">" + ui::rml::spinner("[[common.loading]]") +
                    "</div><div id=\"panel-cards\" class=\"cstack\"/>");
  cards_ = std::make_unique<ui::cards::CardHost>(c.ui(), [&c](const ui::cards::ActionCall& call) {
    cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
    e.which_body = cadence_screen_v1_Envelope_rpc_request_tag;
    auto& r = e.body.rpc_request;
    std::strncpy(r.id, call.rpcId.c_str(), sizeof r.id - 1);
    std::strncpy(r.method, "card.action", sizeof r.method - 1);
    std::string json = call.json;
    r.json = json.data();
    return c.host->send(e);
  });
  cards_->attach(body->GetElementById("panel-cards"), ui::cards::kMaxPageCards, true);
  cards_->onPage([this](const cadence_cards_v1_Page& page) { cards_->showPage(page); });
  if (!c.session) return;
  // a panel the server's session state listed is the session's (media.panel); the catalog's is the item's
  const bool server = c.session->serverFields;
  if (!server && c.session->itemRef.empty()) return;
  const nlohmann::json args = server ? nlohmann::json{{"sessionId", c.session->sessionId}, {"panelId", panel_.id}}
                                     : nlohmann::json{{"ref", c.session->itemRef}, {"panel", panel_.id}};
  const char* method = server ? "media.panel" : "catalog.panel";
  const auto asked = c.nowMs;
  c.rpc.call(method, args.dump(), [this, &c, method, asked](const RpcAnswer& a) {
    if (sheet_ == nullptr || !sheet_->isOpen() || sheet_->content() != this) return;
    loading_ = false;
    auto* state = c.el("panel-state");
    if (a.ok() && a.response != nullptr && a.response->page != nullptr) {
      setText(state, "");
      show(*a.response->page);
      return;
    }
    setText(state, failureCard(method, a, asked, c.nowMs));
  }, c.nowMs);
}

void PanelContent::show(const cadence_cards_v1_Page& page) {
  loading_ = false;
  if (auto* state = sheet_ != nullptr ? sheet_->context().el("panel-state") : nullptr) setText(state, "");
  cards_->showPage(page);
  loaded();
}

void PanelContent::loaded() {
  // focus starts on the first focusable of the page
  auto& c = sheet_->context();
  const auto all = c.ui().focus().candidates();
  if (!all.empty()) {
    c.ui().focus().focus(all.front());
    sheet_->scrollTo(all.front());
  }
}

bool PanelContent::activate(const ui::Activation& a) { return cards_ && cards_->activate(a); }

bool PanelContent::message(const cadence_screen_v1_Envelope& m) {
  return cards_ && m.which_body == cadence_screen_v1_Envelope_rpc_response_tag && cards_->response(m.body.rpc_response);
}

void PanelContent::tick(std::int64_t nowMs) {
  (void)nowMs;
  if (cards_) cards_->tick(sheet_->context().host->epochMs());
}

void PanelContent::focused(Rml::Element* e) {
  // comments load more on their own when focus nears the end (design §14)
  if (panel_.kind != "comments" || sheet_ == nullptr) return;
  auto& c = sheet_->context();
  const auto all = c.ui().focus().candidates();
  const auto it = std::find(all.begin(), all.end(), e);
  if (it == all.end() || all.end() - it > 3 || c.nowMs - autoMoreMs_ < 2000) return;
  for (auto* n : all) {
    if (n->HasAttribute("data-page-next")) {
      autoMoreMs_ = c.nowMs;
      cards_->activate(ui::Activation{n, n->GetAttribute<Rml::String>("data-action", ""), false, std::nullopt, std::nullopt});
      break;
    }
  }
}

}  // namespace cadence::screen::routes
