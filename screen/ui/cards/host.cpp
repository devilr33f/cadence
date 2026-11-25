#include "ui/cards/host.h"

#include "ui/cards/timeline.h"
#include "ui/core/rml.h"
#include "ui/tokens.h"

#include <RmlUi/Core/ElementDocument.h>

#include <algorithm>
#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <cstring>

namespace cadence::screen::ui::cards {

namespace {

std::string jsonString(std::string_view s) {
  std::string out = "\"";
  for (const char sc : s) {
    const auto c = static_cast<unsigned char>(sc);
    switch (c) {
      case '"':
        out += "\\\"";
        break;
      case '\\':
        out += "\\\\";
        break;
      default:
        if (c < 0x20) {
          char buf[8];
          std::snprintf(buf, sizeof buf, "\\u%04x", c);
          out += buf;
        } else {
          out += static_cast<char>(c);
        }
    }
  }
  return out + "\"";
}

Rml::Element* cardOf(Rml::Element* e) {
  for (auto* p = e; p != nullptr; p = p->GetParentNode()) {
    if (p->IsClassSet("ccard")) return p;
  }
  return nullptr;
}

Rml::Element* pageOf(Rml::Element* e) {
  for (auto* p = e; p != nullptr; p = p->GetParentNode()) {
    if (p->IsClassSet("cpage")) return p;
  }
  return nullptr;
}

// the fills and switches of a card in document order, to animate from the old revision's values
struct Animated {
  std::vector<std::string> transforms;
  std::vector<bool> on;
};

Animated snapshot(Rml::Element* card) {
  Animated a;
  Rml::ElementList fills;
  card->QuerySelectorAll(fills, ".mfill, .sfill, .fill");
  for (auto* f : fills) {
    const auto* p = f->GetProperty("transform");
    a.transforms.push_back(p != nullptr ? p->ToString() : std::string());
  }
  Rml::ElementList toggles;
  card->QuerySelectorAll(toggles, ".toggle");
  for (auto* t : toggles) a.on.push_back(t->IsClassSet("on"));
  return a;
}

}  // namespace

std::string actionJson(std::string_view cardId, std::string_view pageId, std::uint32_t revision, std::string_view actionId,
                       std::string_view value) {
  std::string s = "{";
  if (!cardId.empty()) s += "\"cardId\":" + jsonString(cardId) + ",\"revision\":" + std::to_string(revision) + ",";
  if (!pageId.empty()) s += "\"pageId\":" + jsonString(pageId) + ",";
  s += "\"actionId\":" + jsonString(actionId);
  if (!value.empty()) s += ",\"value\":" + std::string(value);
  return s + "}";
}

void CardHost::attach(Rml::Element* container, int limit, bool panel) {
  container_ = container ? container->GetObserverPtr() : Rml::ObserverPtr<Rml::Element>{};
  limit_ = limit;
  panel_ = panel;
  // a reload built the container again: draw what is held
  for (auto& s : cards_) s.element.reset();
  for (auto& s : cards_) s.element = place(*s.card, nullptr)->GetObserverPtr();
}

bool CardHost::has(std::string_view cardId) const {
  return std::any_of(cards_.begin(), cards_.end(), [&](const Shown& s) { return str(s.card->id) == cardId; });
}

std::vector<std::string> CardHost::ids() const {
  std::vector<std::string> out;
  out.reserve(cards_.size());
  for (const auto& s : cards_) out.emplace_back(str(s.card->id));
  return out;
}

Rml::Element* CardHost::place(const cadence_cards_v1_Card& card, Rml::Element* replacing) {
  auto* host = container_.get();
  if (host == nullptr) return nullptr;
  // the placer may rebuild what is around the old revision: hold it by an observer
  auto held = replacing != nullptr ? replacing->GetObserverPtr() : Rml::ObserverPtr<Rml::Element>{};
  if (placer_) {
    if (auto* p = placer_(str(card.id), replacing != nullptr)) host = p;
  }
  replacing = held.get();
  if (replacing != nullptr && replacing->GetParentNode() != host) {
    replacing->GetParentNode()->RemoveChild(replacing);
    replacing = nullptr;
  }
  const auto verdict = check(card);
  if (!verdict.ok()) fallbacks_[verdict.reason]++;
  RenderOptions o;
  o.nowEpochMs = epochMs_;
  o.panel = panel_;
  auto* doc = host->GetOwnerDocument();
  auto fresh = doc->CreateElement("div");
  fresh->SetClass("cslot", true);
  fresh->SetInnerRML(render(card, o));
  Animated before;
  if (replacing != nullptr) {
    before = snapshot(replacing);
    host->ReplaceChild(std::move(fresh), replacing);
  } else {
    // new cards enter at the bottom of the stack
    host->AppendChild(std::move(fresh));
  }
  // the element we just placed is the container's child matching nothing older; find it by its card
  Rml::Element* placed = nullptr;
  for (int i = host->GetNumChildren() - 1; i >= 0; --i) {
    auto* c = host->GetChild(i);
    if (auto* cc = c->QuerySelector(".ccard"); cc != nullptr && cc->GetAttribute<Rml::String>("data-card", "") == str(card.id)) {
      placed = c;
      break;
    }
  }
  if (placed == nullptr) return host->GetChild(host->GetNumChildren() - 1);
  if (replacing != nullptr) {
    // only what changed animates: fills scale from their old value, switches slide from their old state
    const auto after = snapshot(placed);
    Rml::ElementList fills;
    placed->QuerySelectorAll(fills, ".mfill, .sfill, .fill");
    for (std::size_t i = 0; i < fills.size() && i < before.transforms.size(); ++i) {
      if (before.transforms[i].empty() || before.transforms[i] == after.transforms[i]) continue;
      fills[i]->SetProperty("transform", before.transforms[i]);
      auto ptr = fills[i]->GetObserverPtr();
      const auto target = after.transforms[i];
      ui_.after(0, [ptr, target] {
        if (auto* f = ptr.get()) f->SetProperty("transform", target);
      });
    }
    Rml::ElementList toggles;
    placed->QuerySelectorAll(toggles, ".toggle");
    for (std::size_t i = 0; i < toggles.size() && i < before.on.size(); ++i) {
      if (before.on[i] == after.on[i]) continue;
      toggles[i]->SetClass("on", before.on[i]);
      auto ptr = toggles[i]->GetObserverPtr();
      const bool on = after.on[i];
      ui_.after(0, [ptr, on] {
        if (auto* t = ptr.get()) t->SetClass("on", on);
      });
    }
  }
  ui_.markDirty();
  return placed;
}

bool CardHost::show(const cadence_cards_v1_Card& card) {
  const auto id = str(card.id);
  auto copy = copyCard(card);
  if (!copy) {
    fallbacks_["decode"]++;
    return false;
  }
  for (auto& s : cards_) {
    if (str(s.card->id) != id) continue;
    if (val(card.revision) <= val(s.card->revision)) return false;
    s.card = std::move(copy);
    auto* old = s.element.get();
    if (auto* el = place(*s.card, old)) s.element = el->GetObserverPtr();
    s.expiresMs = val(s.card->ttl_s) > 0 ? ui_.nowMs() + static_cast<std::int64_t>(val(s.card->ttl_s)) * 1000 : 0;
    return true;
  }
  Shown s;
  s.card = std::move(copy);
  s.expiresMs = val(s.card->ttl_s) > 0 ? ui_.nowMs() + static_cast<std::int64_t>(val(s.card->ttl_s)) * 1000 : 0;
  auto* el = place(*s.card, nullptr);
  if (el != nullptr) s.element = el->GetObserverPtr();
  cards_.push_back(std::move(s));
  // the drawer keeps the newest
  while (static_cast<int>(cards_.size()) > limit_) {
    if (auto* e = cards_.front().element.get()) e->GetParentNode()->RemoveChild(e);
    cards_.erase(cards_.begin());
  }
  return true;
}

void CardHost::showPage(const cadence_cards_v1_Page& page) {
  // a page's cards follow its predecessor's (a «Показать ещё» appends)
  if (auto* host = container_.get()) {
    Rml::ElementList nexts;
    host->QuerySelectorAll(nexts, "[data-page-next]");
    for (auto* n : nexts) n->GetParentNode()->RemoveChild(n);
  }
  const auto n = std::min<pb_size_t>(page.cards_count, static_cast<pb_size_t>(kMaxPageCards));
  for (pb_size_t i = 0; i < n; ++i) show(page.cards[i]);
  if (page.next != nullptr && container_.get() != nullptr) {
    auto* host = container_.get();
    auto slot = host->GetOwnerDocument()->CreateElement("div");
    rml::ListItem more;
    more.title = "[[common.show_more]]";
    more.trailIcon = "chevron_down";
    more.action = std::string(str(page.next->id));
    std::string item = rml::listItem(more);
    item.insert(item.find('>'), " data-page-next=\"1\"");
    slot->SetInnerRML("<div class=\"cpage\" data-page=\"" + rml::esc(str(page.id)) + "\">" + item + "</div>");
    host->AppendChild(std::move(slot));
  }
}

void CardHost::clear() {
  for (auto& s : cards_) {
    if (auto* e = s.element.get()) e->GetParentNode()->RemoveChild(e);
  }
  cards_.clear();
  pending_.clear();
}

bool CardHost::activate(const Activation& a) {
  auto* e = a.element;
  if (e == nullptr) return false;
  auto* card = cardOf(e);
  auto* page = card == nullptr ? pageOf(e) : nullptr;
  if (card == nullptr && page == nullptr) return false;
  // a card of another host's (the drawer's, a panel's) is that host's
  if (auto* host = container_.get(); host != nullptr) {
    bool mine = false;
    for (auto* p = e; p != nullptr && !mine; p = p->GetParentNode()) mine = p == host;
    if (!mine) return false;
  }
  if (e->IsClassSet("unavailable") || e->IsClassSet("pending")) return true;
  // multiple choices: a chip checks itself; the submit sends the checked ids
  if (e->GetAttribute<Rml::String>("data-kind", "") == "check") {
    auto* group = e->GetParentNode();
    const bool on = !e->IsClassSet("on");
    int checked = 0;
    Rml::ElementList chips;
    group->QuerySelectorAll(chips, "[data-kind=check]");
    for (auto* c : chips) checked += c->IsClassSet("on") ? 1 : 0;
    const int max = group->GetAttribute<int>("data-max", 8);
    if (on && checked >= max) return true;
    e->SetClass("on", on);
    ui_.markDirty();
    return true;
  }
  std::string value;
  if (a.number) {
    char buf[48];
    std::snprintf(buf, sizeof buf, "%.10g", *a.number);
    value = buf;
  } else if (e->HasAttribute("data-submit")) {
    auto* group = e->GetParentNode();
    Rml::ElementList chips;
    group->QuerySelectorAll(chips, "[data-kind=check]");
    std::string ids;
    int n = 0;
    for (auto* c : chips) {
      if (!c->IsClassSet("on")) continue;
      if (!ids.empty()) ids += ",";
      ids += jsonString(c->GetAttribute<Rml::String>("data-value", ""));
      ++n;
    }
    if (n < group->GetAttribute<int>("data-min", 1)) return true;
    value = "[" + ids + "]";
  } else if (e->HasAttribute("data-value")) {
    value = jsonString(e->GetAttribute<Rml::String>("data-value", ""));
  }
  const auto confirm = e->GetAttribute<Rml::String>("data-confirm", "");
  const std::string actionId = a.action;
  if (!confirm.empty()) {
    // sensitive devices and task approvals ask first; default focus on the safe choice
    auto ptr = e->GetObserverPtr();
    DialogSpec d;
    d.title = rml::esc(confirm);
    d.onConfirm = [this, ptr, actionId, value] {
      if (auto* x = ptr.get()) send(x, actionId, value);
    };
    d.onCancel = [this, ptr] {
      // a toggle flipped optimistically goes back
      auto* x = ptr.get();
      if (x != nullptr && x->GetAttribute<Rml::String>("data-kind", "") == "toggle") {
        x->SetClass("on", !x->IsClassSet("on"));
        x->SetClass("pending", false);
        if (auto* w = x->QuerySelector(".tword")) w->SetInnerRML(x->IsClassSet("on") ? "[[ui.on]]" : "[[ui.off]]");
      }
      ui_.markDirty();
    };
    ui_.dialog(std::move(d));
    return true;
  }
  send(e, actionId, value);
  return true;
}

void CardHost::send(Rml::Element* e, const std::string& actionId, const std::string& value) {
  auto* card = cardOf(e);
  auto* page = card == nullptr ? pageOf(e) : nullptr;
  Pending p;
  if (card != nullptr) p.cardId = card->GetAttribute<Rml::String>("data-card", "");
  if (page != nullptr) p.pageId = page->GetAttribute<Rml::String>("data-page", "");
  const auto revision = card != nullptr ? static_cast<std::uint32_t>(card->GetAttribute<int>("data-revision", 0)) : 0u;
  p.element = e->GetObserverPtr();
  p.sentMs = ui_.nowMs();
  ActionCall call;
  call.rpcId = "card-" + std::to_string(++seq_);
  call.json = actionJson(p.cardId, p.pageId, revision, actionId, value);
  e->SetClass("pending", true);
  if (send_ && send_(call)) {
    pending_[call.rpcId] = std::move(p);
  } else {
    // no link: the action is unavailable now
    rollback(p);
    ui_.toast(ToastSpec{{}, ToastTone::Warning, "[[offline.no_server]]", "offline", {}, {}, 0});
  }
  ui_.markDirty();
}

void CardHost::rollback(const Pending& p) {
  auto* e = p.element.get();
  if (e == nullptr) return;
  e->SetClass("pending", false);
  const auto kind = e->GetAttribute<Rml::String>("data-kind", "");
  if (kind == "toggle") {
    // the server's state is the truth: the switch springs back
    e->SetClass("on", !e->IsClassSet("on"));
    if (auto* w = e->QuerySelector(".tword")) w->SetInnerRML(e->IsClassSet("on") ? "[[ui.on]]" : "[[ui.off]]");
  }
  if (kind == "slider" && e->HasAttribute("data-timeline")) {
    // the choice was refused: the position runs on from the server's anchor
    e->RemoveAttribute("data-moved");
    runTimeline(e);
  } else if (kind == "slider") {
    const double server = e->GetAttribute<double>("data-server-value", e->GetAttribute<double>("data-value", 0.0));
    e->SetAttribute("data-value", server);
    const double lo = e->GetAttribute<double>("data-min", 0.0), hi = e->GetAttribute<double>("data-max", 100.0);
    char buf[48];
    std::snprintf(buf, sizeof buf, "scaleX(%.4f)", hi > lo ? (server - lo) / (hi - lo) : 0.0);
    if (auto* f = e->QuerySelector(".sfill")) f->SetProperty("transform", buf);
    if (auto* l = e->QuerySelector(".svalue")) {
      l->SetInnerRML(rml::esc(rml::sliderLabel(server, e->GetAttribute<Rml::String>("data-unit", ""))));
    }
  }
  // a lock replaces its icon for 4 s
  e->SetClass("refused", true);
  auto ptr = e->GetObserverPtr();
  ui_.after(tokens::kRefusedIconMs, [ptr] {
    if (auto* x = ptr.get()) x->SetClass("refused", false);
  });
  ui_.markDirty();
}

void CardHost::refused(const Pending& p, std::string_view toast) {
  rollback(p);
  ui_.toast(ToastSpec{{}, ToastTone::Warning, toast.empty() ? std::string("[[common.error]]") : rml::esc(toast), "locked", {}, {}, 0});
}

bool CardHost::response(const cadence_v2_RpcResponse& r) {
  const auto it = pending_.find(r.id);
  if (it == pending_.end()) return false;
  const Pending p = it->second;
  pending_.erase(it);
  if (auto* e = p.element.get()) e->SetClass("pending", false);
  const auto* result = r.card_action;
  if (std::strcmp(r.status, "ok") != 0 && (result == nullptr || result->which_result != cadence_cards_v1_CardActionResult_refusal_tag)) {
    refused(p, {});
    return true;
  }
  if (result == nullptr) return true;
  switch (result->which_result) {
    case cadence_cards_v1_CardActionResult_card_tag:
      if (result->result.card != nullptr) show(*result->result.card);
      break;
    case cadence_cards_v1_CardActionResult_page_tag:
      if (result->result.page != nullptr) {
        if (page_) {
          page_(*result->result.page);
        } else {
          showPage(*result->result.page);
        }
      }
      break;
    case cadence_cards_v1_CardActionResult_refusal_tag: {
      const auto* ref = result->result.refusal;
      if (ref != nullptr && ref->card != nullptr) {
        // the card as the server holds it, drawn again even at an equal revision
        for (auto& s : cards_) {
          if (str(s.card->id) == str(ref->card->id) && s.card->revision != nullptr) *s.card->revision = 0;
        }
        show(*ref->card);
        ui_.toast(ToastSpec{{}, ToastTone::Warning, ref->toast != nullptr ? rml::esc(str(ref->toast)) : std::string("[[common.error]]"),
                            "locked", {}, {}, 0});
      } else {
        refused(p, ref != nullptr ? str(ref->toast) : std::string_view());
      }
      break;
    }
    default:
      break;
  }
  ui_.markDirty();
  return true;
}

void CardHost::countdowns() {
  auto* host = container_.get();
  if (host == nullptr) return;
  Rml::ElementList list;
  host->QuerySelectorAll(list, ".ccountdown");
  for (auto* c : list) {
    const auto ends = std::stoll(c->GetAttribute<Rml::String>("data-ends-at", "0"));
    const auto format = static_cast<cadence_cards_v1_CountdownFormat>(c->GetAttribute<int>("data-format", 0));
    if (auto* t = c->QuerySelector(".cdtime")) {
      const auto text = countdownText(ends, epochMs_, format);
      if (t->GetInnerRML() != text) {
        // the clock ticks without a transition
        t->SetInnerRML(text);
        ui_.markDirty();
      }
    }
  }
}

void CardHost::timelines() {
  auto* host = container_.get();
  if (host == nullptr) return;
  Rml::ElementList list;
  host->QuerySelectorAll(list, ".slider[data-timeline]");
  for (auto* e : list) {
    if (!e->HasAttribute("data-moved")) runTimeline(e);
  }
}

void CardHost::runTimeline(Rml::Element* e) {
  Timeline t;
  t.value = e->GetAttribute<double>("data-server-value", 0.0);
  t.min = e->GetAttribute<double>("data-min", 0.0);
  t.max = e->GetAttribute<double>("data-max", 0.0);
  t.anchorAtMs = std::strtoll(e->GetAttribute<Rml::String>("data-anchor", "0").c_str(), nullptr, 10);
  t.rate = e->GetAttribute<double>("data-rate", 0.0);
  // whole seconds, as drawn: a seek from here sends the time the user saw
  const double v = std::floor(t.at(epochMs_));
  const auto text = rml::esc(timelineText(v, t.max));
  auto* label = e->QuerySelector(".svalue");
  // a paused one, or one at its end, draws nothing new: no redraw
  if (label == nullptr || label->GetInnerRML() == text) return;
  e->SetAttribute("data-value", v);
  const double f = t.max > t.min ? std::clamp((v - t.min) / (t.max - t.min), 0.0, 1.0) : 0.0;
  char buf[48];
  std::snprintf(buf, sizeof buf, "scaleX(%.4f)", f);
  if (auto* fill = e->QuerySelector(".sfill")) fill->SetProperty("transform", buf);
  std::snprintf(buf, sizeof buf, "%.2f%%", f * 100);
  if (auto* thumb = e->QuerySelector(".thumb")) thumb->SetProperty("left", buf);
  label->SetInnerRML(text);
  ui_.markDirty();
}

void CardHost::clampTitles() {
  auto* host = container_.get();
  if (host == nullptr) return;
  Rml::ElementList list;
  host->QuerySelectorAll(list, ".ctext.clamp:not(.fitted)");
  for (auto* c : list) {
    // not laid out yet (a hidden sheet): looked at again on a later tick
    if (c->GetClientHeight() <= 0) continue;
    c->SetClass("fitted", true);
    const auto fits = [c] { return c->GetScrollHeight() <= c->GetClientHeight() + 1.0f; };
    if (fits()) continue;
    // the most words that fit with «…» after them: a few layouts of this card's document, once per revision
    const auto full = c->GetAttribute<Rml::String>("data-text", "");
    std::vector<std::size_t> ends;
    for (std::size_t i = 1; i < full.size(); ++i) {
      if (full[i] == ' ' && full[i - 1] != ' ') ends.push_back(i);
    }
    auto* doc = c->GetOwnerDocument();
    const auto set = [&](std::size_t words) {
      const auto cut = words < ends.size() ? full.substr(0, ends[words]) : full;
      c->SetInnerRML(rml::esc(cut) + "…");
      if (doc != nullptr) doc->UpdateDocument();
    };
    std::size_t lo = 0, hi = ends.size();
    while (lo < hi) {
      const auto mid = (lo + hi + 1) / 2;
      set(mid);
      if (fits()) {
        lo = mid;
      } else {
        hi = mid - 1;
      }
    }
    set(lo);
    ui_.markDirty();
  }
}

void CardHost::codeBlocks() {
  auto* host = container_.get();
  if (host == nullptr) return;
  clampTitles();
  Rml::ElementList list;
  host->QuerySelectorAll(list, ".ccode");
  for (auto* c : list) {
    auto* more = c->GetNextSibling();
    if (more == nullptr || !more->IsClassSet("codemore")) continue;
    const float hidden = c->GetScrollHeight() - c->GetClientHeight() - c->GetScrollTop();
    const float line = 32.0f * ui_.dp();
    const int lines = hidden > 1 ? static_cast<int>(std::ceil(hidden / line)) : 0;
    const auto want = lines > 0 ? "[[cards.code_more|count=" + std::to_string(lines) + "]]" : std::string();
    if (more->GetAttribute<Rml::String>("data-lines", "") != std::to_string(lines)) {
      more->SetAttribute("data-lines", std::to_string(lines));
      more->SetInnerRML(want);
      more->SetClass("hidden", lines == 0);
      ui_.markDirty();
    }
  }
}

void CardHost::tick(std::int64_t epochMs) {
  epochMs_ = epochMs;
  // a pending action with no answer in 5 s rolls back
  for (auto it = pending_.begin(); it != pending_.end();) {
    if (ui_.nowMs() - it->second.sentMs >= tokens::kPendingTimeoutMs) {
      refused(it->second, {});
      it = pending_.erase(it);
    } else {
      ++it;
    }
  }
  for (auto it = cards_.begin(); it != cards_.end();) {
    if (it->expiresMs > 0 && ui_.nowMs() >= it->expiresMs) {
      if (auto* e = it->element.get()) e->GetParentNode()->RemoveChild(e);
      it = cards_.erase(it);
      ui_.markDirty();
    } else {
      ++it;
    }
  }
  const auto second = epochMs / 1000;
  if (second != lastSecond_) {
    lastSecond_ = second;
    countdowns();
    timelines();
  }
  codeBlocks();
}

}  // namespace cadence::screen::ui::cards
