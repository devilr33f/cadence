#include "app/routes/system/card-sheet.h"

#include "ui/tokens.h"

#include <RmlUi/Core/Element.h>

#include <pb_encode.h>

#include <algorithm>
#include <cstring>
#include <vector>

namespace cadence::screen::routes::sheet {

cards::PagePtr copyPage(const cadence_cards_v1_Page& page) {
  std::size_t size = 0;
  if (!pb_get_encoded_size(&size, cadence_cards_v1_Page_fields, &page)) return nullptr;
  std::vector<std::uint8_t> buf(size);
  auto out = pb_ostream_from_buffer(buf.data(), buf.size());
  if (!pb_encode(&out, cadence_cards_v1_Page_fields, &page)) return nullptr;
  return cards::decodePage(buf);
}

std::string cardIdOf(Rml::Element* e) {
  for (auto* p = e; p != nullptr; p = p->GetParentNode()) {
    if (p->IsClassSet("ccard")) return p->GetAttribute<Rml::String>("data-card", "");
  }
  return {};
}

Rml::Element* findCard(Rml::Element* host, std::string_view cardId) {
  if (host == nullptr || cardId.empty()) return nullptr;
  Rml::ElementList cs;
  host->QuerySelectorAll(cs, ".ccard");
  for (auto* c : cs) {
    if (c->GetAttribute<Rml::String>("data-card", "") == cardId) return c;
  }
  return nullptr;
}

cards::CardHost::Sender actionSender(app::Outbox& out, std::string prefix) {
  return [&out, prefix = std::move(prefix)](const cards::ActionCall& call) {
    cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
    e.which_body = cadence_screen_v1_Envelope_rpc_request_tag;
    copyTo(e.body.rpc_request.id, prefix + call.rpcId);
    copyTo(e.body.rpc_request.method, "card.action");
    std::string json = call.json;
    e.body.rpc_request.json = json.data();
    return out.send(e);
  };
}

bool actionResponse(const cadence_screen_v1_Envelope& m, std::string_view prefix, cards::CardHost& host) {
  if (m.which_body != cadence_screen_v1_Envelope_rpc_response_tag) return false;
  const auto& r = m.body.rpc_response;
  if (std::strncmp(r.id, prefix.data(), prefix.size()) != 0) return false;
  cadence_v2_RpcResponse copy = r;
  copyTo(copy.id, std::string_view(r.id + prefix.size()));
  host.response(copy);
  return true;
}

FocusMark markFocus(Rml::Element* host, Rml::Element* focused) {
  FocusMark m;
  m.card = cardIdOf(focused);
  if (m.card.empty()) return m;
  if (auto* c = findCard(host, m.card)) {
    Rml::ElementList fs;
    c->QuerySelectorAll(fs, ".f");
    for (std::size_t i = 0; i < fs.size(); ++i) {
      if (fs[i] == focused) m.index = static_cast<int>(i);
    }
  }
  return m;
}

bool restoreFocus(ui::Ui& ui, Rml::Element* host, const FocusMark& mark) {
  if (mark.card.empty() || mark.index < 0) return false;
  auto* c = findCard(host, mark.card);
  if (c == nullptr) return false;
  Rml::ElementList fs;
  c->QuerySelectorAll(fs, ".f");
  if (fs.empty()) return false;
  const auto i = std::min(static_cast<std::size_t>(mark.index), fs.size() - 1);
  ui.focus().focus(fs[i]);
  return true;
}

bool focusCard(ui::Ui& ui, Rml::Element* card) {
  if (card == nullptr) return false;
  if (auto* f = card->QuerySelector(".f")) {
    ui.focus().focus(f);
    return true;
  }
  return false;
}

bool focusFirst(ui::Ui& ui, Rml::Element* host) {
  if (host == nullptr) return false;
  Rml::ElementList fs;
  host->QuerySelectorAll(fs, ".f");
  for (auto* f : fs) {
    if (f->IsVisible(true)) {
      ui.focus().focus(f);
      return true;
    }
  }
  return false;
}

void enter(ui::Ui& ui, Rml::Element* layer) {
  if (layer == nullptr) return;
  layer->SetClass("leaving", false);
  layer->SetClass("open", true);
  layer->SetClass("entering", true);
  auto ptr = layer->GetObserverPtr();
  ui.after(0, [ptr] {
    if (auto* x = ptr.get()) x->SetClass("entering", false);
  });
}

void leave(ui::Ui& ui, const Alive& alive, Rml::Element* layer, std::function<bool()> closed) {
  if (layer == nullptr) return;
  layer->SetClass("leaving", true);
  auto ptr = layer->GetObserverPtr();
  ui.after(ui::tokens::kMotionSheetExitMs, alive.guard([ptr, closed = std::move(closed)] {
    auto* x = ptr.get();
    if (x != nullptr && closed()) {
      x->SetClass("open", false);
      x->SetClass("leaving", false);
    }
  }));
}

}  // namespace cadence::screen::routes::sheet
