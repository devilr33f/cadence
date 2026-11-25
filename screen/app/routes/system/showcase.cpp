#include "app/routes/system/showcase.h"

#include "ui/core/rml.h"

#include <RmlUi/Core/ElementDocument.h>

#include <algorithm>
#include <cstdio>
#include <vector>

namespace cadence::screen::routes {

namespace {

std::string transition(std::int64_t ms) {
  char buf[96];
  std::snprintf(buf, sizeof buf, "transform %.3fs cubic-out, opacity %.3fs cubic-out", static_cast<double>(ms) / 1000.0,
                static_cast<double>(ms) / 1000.0);
  return buf;
}

}  // namespace

Showcase::Showcase(ui::Ui& ui, ui::cards::CardHost::Sender send)
    : ui_(ui), host_(std::make_unique<ui::cards::CardHost>(ui, std::move(send))) {
  host_->setPlacer([this](std::string_view id, bool) { return slot(id); });
}

Showcase::~Showcase() = default;

void Showcase::attach(Rml::Element* box, Rml::Element* deck) {
  box_ = box != nullptr ? box->GetObserverPtr() : Rml::ObserverPtr<Rml::Element>{};
  deck_ = deck != nullptr ? deck->GetObserverPtr() : Rml::ObserverPtr<Rml::Element>{};
  // a reload built the deck again: the held cards find their slots again, at rest
  slots_.clear();
  placed_.clear();
  fresh_.clear();
  placedShownMs_ = -1;
  if (deck != nullptr) host_->attach(deck, ui::cards::kMaxDrawerCards, false);
  dirty_ = true;
}

Rml::Element* Showcase::slot(std::string_view cardId) {
  auto* deck = deck_.get();
  if (deck == nullptr) return nullptr;
  if (auto it = slots_.find(cardId); it != slots_.end()) {
    if (auto* e = it->second.get()) return e;
  }
  // a new slot waits below the deck, unseen, until the reel brings it up: its first transition is the rise
  auto fresh = deck->GetOwnerDocument()->CreateElement("div");
  fresh->SetClassNames("sslot wait");
  fresh->SetAttribute("data-card-id", std::string(cardId));
  auto* e = deck->AppendChild(std::move(fresh));
  slots_[std::string(cardId)] = e->GetObserverPtr();
  placed_[std::string(cardId)] = "sslot wait";
  fresh_.insert(std::string(cardId));
  return e;
}

bool Showcase::show(const cadence_cards_v1_Card& card) {
  const std::string id(ui::cards::str(card.id));
  if (!host_->show(card)) return false;
  // a slot whose card the host let go (its limit) leaves with it
  for (auto it = slots_.begin(); it != slots_.end();) {
    if (host_->has(it->first)) {
      ++it;
      continue;
    }
    if (auto* e = it->second.get()) e->GetParentNode()->RemoveChild(e);
    reel_.remove(it->first);
    placed_.erase(it->first);
    it = slots_.erase(it);
  }
  reel_.push(id, ui_.nowMs());
  dirty_ = true;
  ui_.markDirty();
  return true;
}

bool Showcase::has(std::string_view cardId) const { return host_->has(cardId); }

void Showcase::clear() {
  host_->clear();
  for (auto& [id, s] : slots_) {
    if (auto* e = s.get()) e->GetParentNode()->RemoveChild(e);
  }
  slots_.clear();
  placed_.clear();
  fresh_.clear();
  reel_.clear();
  held_ = false;
  placedShownMs_ = -1;
  if (auto* b = box_.get()) b->SetClass("on", false);
  ui_.markDirty();
}

void Showcase::cite(std::string_view cardId, std::uint32_t n) {
  if (!cardId.empty() && reel_.front(cardId, ui_.nowMs())) dirty_ = true;
  // the numbered source lights in its card alone
  for (auto& [id, s] : slots_) {
    auto* e = s.get();
    if (e == nullptr) continue;
    Rml::ElementList cites;
    e->QuerySelectorAll(cites, ".clink .cite");
    for (auto* c : cites) {
      const bool on = n != 0 && id == cardId && c->GetParentNode()->GetAttribute<Rml::String>("data-cite", "") == std::to_string(n);
      c->SetClass("on", on);
    }
  }
  ui_.markDirty();
}

Rml::Element* Showcase::currentCard() const {
  auto it = slots_.find(reel_.current());
  return it != slots_.end() ? it->second.get() : nullptr;
}

void Showcase::place(std::int64_t nowMs) {
  (void)nowMs;
  const auto ms = ui_.reducedMotion() ? std::int64_t{0} : reel_.slideMs();
  const auto& history = reel_.history();
  for (auto& [id, s] : slots_) {
    auto* e = s.get();
    if (e == nullptr) continue;
    std::string cls = "sslot gone";
    if (id == reel_.current()) {
      cls = "sslot p0";
    } else if (!history.empty() && history[0] == id) {
      cls = "sslot p1";
    } else if (history.size() > 1 && history[1] == id) {
      cls = "sslot p2";
    } else if (std::find(reel_.waiting().begin(), reel_.waiting().end(), id) != reel_.waiting().end()) {
      cls = "sslot wait";
    }
    // a slot made since the last frame is laid out below first, so its rise is a transition and not a jump
    if (fresh_.erase(id) > 0) {
      dirty_ = true;
      continue;
    }
    auto& was = placed_[id];
    if (was == cls) continue;
    // the move's length is the reel's pace: set before the class, so the transition takes it
    e->SetProperty("transition", ms > 0 ? transition(ms) : std::string("none"));
    e->SetClassNames(cls);
    was = cls;
  }
  if (auto* b = box_.get()) b->SetClass("on", !reel_.empty());
  ui_.markDirty();
}

void Showcase::tick(std::int64_t nowMs) {
  if (!held_ && reel_.tick(nowMs)) dirty_ = true;
  if (reel_.shownMs() != placedShownMs_) {
    placedShownMs_ = reel_.shownMs();
    dirty_ = true;
  }
  if (!dirty_) return;
  dirty_ = false;
  place(nowMs);
}

}  // namespace cadence::screen::routes
