#include "app/routes/system/chat-view.h"

#include "ui/tokens.h"

#include <RmlUi/Core/ElementDocument.h>

#include <algorithm>
#include <cmath>
#include <cstdio>
#include <cstdlib>

namespace cadence::screen::routes {

namespace {

// a new item rises this far as it fades in; a fresh step a little
constexpr double kRiseDp = 32;
constexpr double kStepRiseDp = 12;
// history revealed for a focused element keeps it clear of the top fade (160 dp) for the most part
constexpr double kRevealMarginDp = 140;

std::string fmtTransform(double ty, double s) {
  if (std::abs(ty) < 0.01 && std::abs(s - 1) < 0.0001) return "none";
  char buf[96];
  std::snprintf(buf, sizeof buf, "translateY(%.2fpx) scale(%.4f)", ty, s);
  return buf;
}

std::string fmtNumber(const char* f, double v) {
  char buf[48];
  std::snprintf(buf, sizeof buf, f, v);
  return buf;
}

}  // namespace

void ChatView::attach(Rml::Element* viewport, Rml::Element* list) {
  // a reload built the list again: the first sync puts everything where it was, at rest
  rest_ = !nodes_.empty();
  nodes_.clear();
  viewport_ = viewport != nullptr ? viewport->GetObserverPtr() : Rml::ObserverPtr<Rml::Element>{};
  list_ = list != nullptr ? list->GetObserverPtr() : Rml::ObserverPtr<Rml::Element>{};
  revealing_.reset();
  offset_ = 0;
  dirty_ = true;
}

ChatView::Node* ChatView::find(std::string_view key) {
  for (auto& n : nodes_) {
    if (n.key == key) return &n;
  }
  return nullptr;
}

const ChatView::Node* ChatView::find(std::string_view key) const {
  for (const auto& n : nodes_) {
    if (n.key == key) return &n;
  }
  return nullptr;
}

Rml::Element* ChatView::element(std::string_view key) const {
  const auto* n = find(key);
  return n != nullptr ? n->el.get() : nullptr;
}

double ChatView::opacity(std::string_view key) const {
  const auto* n = find(key);
  return n != nullptr ? n->opacity.at(now_) : 1.0;
}

Rml::Element* ChatView::itemOf(Rml::Element* e) const {
  auto* list = list_.get();
  if (list == nullptr) return nullptr;
  for (; e != nullptr; e = e->GetParentNode()) {
    if (e->GetParentNode() == list) return e;
  }
  return nullptr;
}

void ChatView::sync(const std::vector<Spec>& specs) {
  auto* list = list_.get();
  if (list == nullptr) return;
  auto* doc = list->GetOwnerDocument();
  std::vector<Node> next;
  next.reserve(specs.size());
  for (const auto& s : specs) {
    Node n;
    if (auto* old = find(s.key); old != nullptr && old->el.get() != nullptr) {
      n = std::move(*old);
      old->key.clear();
    } else {
      auto fresh = doc->CreateElement("div");
      fresh->SetAttribute("data-key", s.key);
      n.key = s.key;
      n.host = s.host;
      n.el = list->AppendChild(std::move(fresh))->GetObserverPtr();
    }
    auto* e = n.el.get();
    if (n.cls != s.cls) {
      e->SetClassNames(s.cls);
      n.cls = s.cls;
    }
    if (!s.host && n.rml != s.rml) {
      e->SetInnerRML(s.rml);
      n.rml = s.rml;
    }
    n.look = s.look;
    fresh_ = fresh_ || s.rml.find("fresh") != std::string::npos;
    next.push_back(std::move(n));
  }
  // what no spec names leaves
  for (auto& o : nodes_) {
    if (o.key.empty()) continue;
    if (auto* e = o.el.get()) e->GetParentNode()->RemoveChild(e);
  }
  // and the rest stands in the specs' order
  for (std::size_t i = 0; i < next.size(); ++i) {
    auto* e = next[i].el.get();
    if (e == nullptr || list->GetChild(static_cast<int>(i)) == e) continue;
    auto held = e->GetParentNode()->RemoveChild(e);
    list->InsertBefore(std::move(held), list->GetChild(static_cast<int>(i)));
  }
  nodes_ = std::move(next);
  dirty_ = true;
  ui_.markDirty();
}

void ChatView::reveal(Rml::Element* e) {
  revealing_ = e != nullptr ? e->GetObserverPtr() : Rml::ObserverPtr<Rml::Element>{};
  ui_.markDirty();
}

bool ChatView::tick(std::int64_t nowMs) {
  now_ = nowMs;
  auto* vp = viewport_.get();
  auto* list = list_.get();
  if (vp == nullptr || list == nullptr || nodes_.empty()) return false;
  // a card's wrapper the card host emptied (its ttl, the drawer's limit) takes no room
  for (auto& n : nodes_) {
    auto* e = n.el.get();
    if (e == nullptr || !n.host) continue;
    const bool empty = e->GetNumChildren() == 0;
    if (e->IsClassSet("empty") != empty) {
      e->SetClass("empty", empty);
      dirty_ = true;
    }
  }
  // what changed is laid out now, so this frame draws the new layout with its transforms
  if (dirty_) {
    list->GetOwnerDocument()->UpdateDocument();
    dirty_ = false;
  }
  const bool reduced = ui_.reducedMotion();
  const double dp = ui_.dp();
  const double top = vp->GetAbsoluteOffset(Rml::BoxArea::Border).y;
  std::vector<std::size_t> shown;
  std::vector<chat::Box> boxes;
  std::vector<chat::Look> looks;
  for (std::size_t i = 0; i < nodes_.size(); ++i) {
    auto* e = nodes_[i].el.get();
    if (e == nullptr || !e->IsVisible()) continue;
    shown.push_back(i);
    boxes.push_back({e->GetAbsoluteOffset(Rml::BoxArea::Border).y - top, e->GetBox().GetSize(Rml::BoxArea::Border).y});
    looks.push_back(nodes_[i].look);
  }
  auto targets = chat::bottoms(boxes, looks, 0);
  // the history moves down until a focused element's top shows (it only ever moves down: the newest is at the foot)
  offset_ = 0;
  if (auto* r = revealing_.get()) {
    auto* item = itemOf(r);
    for (std::size_t k = 0; item != nullptr && k < shown.size(); ++k) {
      if (nodes_[shown[k]].el.get() != item) continue;
      const double itemBottom = boxes[k].y + boxes[k].h;
      const double y = r->GetAbsoluteOffset(Rml::BoxArea::Border).y - top;
      const double visualTop = targets[k] + looks[k].scale * (y - itemBottom);
      offset_ = std::max(0.0, kRevealMarginDp * dp - visualTop);
    }
  }
  const std::int64_t moveMs = reduced ? 0 : ui::tokens::kMotionEnterMs;
  const std::int64_t fadeMs = reduced ? ui::tokens::kMotionReducedMs : ui::tokens::kMotionEnterMs;
  bool moving = false;
  for (std::size_t k = 0; k < shown.size(); ++k) {
    auto& n = nodes_[shown[k]];
    const double target = targets[k] + offset_;
    if (!n.placed) {
      n.placed = true;
      n.scale.snap(n.look.scale);
      if (rest_) {
        n.bottom.snap(target);
        n.opacity.snap(n.look.opacity);
      } else {
        // a new item rises from below its place and fades in
        n.bottom.snap(target + (reduced ? 0 : kRiseDp * dp));
        n.opacity.snap(0);
      }
    }
    n.bottom.retarget(target, nowMs, moveMs);
    n.scale.retarget(n.look.scale, nowMs, moveMs);
    n.opacity.retarget(n.leaving ? 0 : n.look.opacity, nowMs, fadeMs);
    const double ty = std::round((n.bottom.at(nowMs) - (boxes[k].y + boxes[k].h)) * 100) / 100;
    const auto transform = fmtTransform(ty, n.scale.at(nowMs));
    const auto alpha = fmtNumber("%.3f", n.opacity.at(nowMs));
    auto* e = n.el.get();
    if (transform != n.transform) {
      e->SetProperty("transform", transform);
      n.transform = transform;
    }
    if (alpha != n.alpha) {
      e->SetProperty("opacity", alpha);
      n.alpha = alpha;
    }
    moving = moving || !n.bottom.done(nowMs) || !n.scale.done(nowMs) || !n.opacity.done(nowMs);
  }
  rest_ = false;
  // fresh words and steps fade in to their item's opacity (rmlui's opacity is inherited, not multiplied: an element
  // that sets its own must carry its item's); a step also rises a little
  bool anyFresh = false;
  for (std::size_t k = 0; fresh_ && k < shown.size(); ++k) {
    auto& n = nodes_[shown[k]];
    Rml::ElementList fresh;
    n.el.get()->QuerySelectorAll(fresh, ".fresh");
    for (auto* f : fresh) {
      const auto born = std::strtoll(f->GetAttribute<Rml::String>("data-born", "0").c_str(), nullptr, 10);
      const double p = static_cast<double>(nowMs - born) / static_cast<double>(ui::tokens::kMotionEnterMs);
      if (p >= 1 || reduced) {
        f->SetClass("fresh", false);
        f->RemoveAttribute("data-born");
        f->RemoveProperty("opacity");
        f->RemoveProperty("transform");
        continue;
      }
      const double c = chat::cubicOut(p);
      f->SetProperty("opacity", fmtNumber("%.3f", n.opacity.at(nowMs) * c));
      if (f->IsClassSet("rise")) f->SetProperty("transform", "translateY(" + fmtNumber("%.2f", (1 - c) * kStepRiseDp * dp) + "px)");
      moving = true;
      anyFresh = true;
    }
  }
  fresh_ = anyFresh;
  if (moving) ui_.markDirty();
  return moving;
}

void ChatView::fadeOut() {
  for (auto& n : nodes_) n.leaving = true;
  ui_.markDirty();
}

void ChatView::clear() {
  for (auto& n : nodes_) {
    if (auto* e = n.el.get()) e->GetParentNode()->RemoveChild(e);
  }
  nodes_.clear();
  revealing_.reset();
  offset_ = 0;
  rest_ = false;
}

}  // namespace cadence::screen::routes
