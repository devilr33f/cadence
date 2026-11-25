#include "ui/core/focus.h"

#include <RmlUi/Core/ElementDocument.h>
#include <RmlUi/Core/ElementText.h>
#include <RmlUi/Core/ElementUtilities.h>
#include <RmlUi/Core/PropertyDictionary.h>
#include <RmlUi/Core/StyleSheetSpecification.h>
#include <RmlUi/Core/Tween.h>

#include <algorithm>
#include <cmath>
#include <iterator>
#include <limits>
#include <string>

namespace cadence::screen::ui {

namespace {

// the title-safe bottom (y = 1026 dp): a focused row's last line ends there (design section 5)
constexpr float kSafeBottomDp = 1026;
constexpr float kNudgeDp = 12;
constexpr std::int64_t kNudgeOutMs = 80;
constexpr std::int64_t kNudgeBackMs = 160;
// the scrolls: a row 300 ms, the column 360 ms, cubic out; while a key repeats, 140 ms so held keys stay fluid
constexpr int kRowGlideMs = 300;
constexpr int kColumnGlideMs = 360;
constexpr int kHeldGlideMs = 140;
constexpr int kReducedGlideMs = 120;

float attrFloat(Rml::Element* e, const char* name) { return e->GetAttribute<float>(name, 0.0f); }

std::string px(float v) {
  char buf[48];
  std::snprintf(buf, sizeof buf, "%.2fpx", static_cast<double>(v));
  return buf;
}

// inert: a container shown but not reachable by the remote yet (the audio page's queue while its controls sleep)
bool inert(Rml::Element* e, Rml::Element* scope) {
  for (auto* p = e->GetParentNode(); p != nullptr && p != scope; p = p->GetParentNode()) {
    if (p->IsClassSet("inert")) return true;
  }
  return false;
}

bool inside(Rml::Element* e, Rml::Element* root) {
  for (auto* p = e; p != nullptr; p = p->GetParentNode()) {
    if (p == root) return true;
  }
  return false;
}

std::string textOf(Rml::Element* e) {
  std::string out;
  for (int i = 0; i < e->GetNumChildren(); ++i) {
    auto* c = e->GetChild(i);
    if (auto* t = dynamic_cast<Rml::ElementText*>(c)) {
      out += t->GetText();
    } else {
      out += textOf(c);
    }
  }
  return out;
}

}  // namespace

Rml::Element* Focus::ancestor(Rml::Element* e, const char* cls) {
  for (auto* p = e; p != nullptr; p = p->GetParentNode()) {
    if (p->IsClassSet(cls)) return p;
  }
  return nullptr;
}

void Focus::setRoot(Rml::Element* root, const std::function<bool(Rml::Element*)>& keep) {
  root_ = root ? root->GetObserverPtr() : Rml::ObserverPtr<Rml::Element>{};
  std::vector<Scope> kept;
  for (auto& sc : scopes_) {
    if (auto* r = sc.root.get(); r != nullptr && keep && keep(r)) kept.push_back(sc);
  }
  scopes_ = std::move(kept);
  if (!scopes_.empty()) {
    // the kept scope's focus stays where it was
    auto* cur = current_.get();
    if (cur != nullptr && inside(cur, scope())) {
      nudges_.clear();
      entries_.clear();
      return;
    }
  }
  current_.reset();
  parent_.reset();
  row_.reset();
  index_ = -1;
  nudges_.clear();
  entries_.clear();
}

void Focus::prune() {
  // a scope whose root went (its document was built again) is gone too
  while (!scopes_.empty() && scopes_.back().root.get() == nullptr) scopes_.pop_back();
}

void Focus::pushScope(Rml::Element* scope) {
  prune();
  scopes_.push_back(Scope{scope->GetObserverPtr(), current_});
  current_.reset();
  repair();
}

bool Focus::popScope() {
  prune();
  if (scopes_.empty()) return false;
  auto opener = scopes_.back().opener;
  scopes_.pop_back();
  current_.reset();
  if (auto* o = opener.get(); o != nullptr && inside(o, scope()) && o->IsVisible(true)) {
    focus(o);
  } else {
    repair();
  }
  return true;
}

bool Focus::dropScope(Rml::Element* scope) {
  prune();
  for (auto it = scopes_.begin(); it != scopes_.end(); ++it) {
    if (it->root.get() != scope) continue;
    if (std::next(it) == scopes_.end()) return popScope();
    // the scope above it gives focus back where this one would have
    std::next(it)->opener = it->opener;
    scopes_.erase(it);
    return true;
  }
  return false;
}

Rml::Element* Focus::scope() const {
  for (auto it = scopes_.rbegin(); it != scopes_.rend(); ++it) {
    if (auto* s = it->root.get()) return s;
  }
  return root_.get();
}

std::vector<Rml::Element*> Focus::candidates() const {
  std::vector<Rml::Element*> out;
  auto* s = scope();
  if (s == nullptr) return out;
  Rml::ElementList list;
  s->QuerySelectorAll(list, ".f");
  for (auto* e : list) {
    if (e->IsVisible(true) && !inert(e, s)) out.push_back(e);
  }
  return out;
}

VisualRect Focus::rect(Rml::Element* e) const {
  const auto pos = e->GetAbsoluteOffset(Rml::BoxArea::Border);
  const auto size = e->GetBox().GetSize(Rml::BoxArea::Border);
  VisualRect r{pos.x, pos.y, size.x, size.y};
  for (auto* p = e->GetParentNode(); p != nullptr; p = p->GetParentNode()) {
    if (p->IsClassSet("track")) r.x -= attrFloat(p, "data-scroll");
    if (p->IsClassSet("vscroll")) r.y -= attrFloat(p, "data-scroll");
  }
  return r;
}

std::vector<Rml::Element*> Focus::rowItems(Rml::Element* row) const {
  std::vector<Rml::Element*> out;
  Rml::ElementList list;
  row->QuerySelectorAll(list, ".f");
  for (auto* e : list) {
    if (e->IsVisible(true)) out.push_back(e);
  }
  return out;
}

Rml::Element* Focus::entryOf(Rml::Element* row, const VisualRect& from) const {
  const auto items = rowItems(row);
  if (items.empty()) return nullptr;
  const int last = row->GetAttribute<int>("data-last", -1);
  if (last >= 0 && last < static_cast<int>(items.size())) return items[static_cast<std::size_t>(last)];
  Rml::Element* best = items.front();
  float bestD = std::numeric_limits<float>::max();
  for (auto* e : items) {
    const float d = std::fabs(rect(e).cx() - from.cx());
    if (d < bestD) {
      bestD = d;
      best = e;
    }
  }
  return best;
}

bool Focus::move(Dir dir) {
  auto* cur = current();
  if (cur == nullptr || !inside(cur, scope())) {
    repair();
    return current() != nullptr;
  }
  auto* row = ancestor(cur, "row");
  if (row != nullptr && !inside(row, scope())) row = nullptr;
  if (row != nullptr && (dir == Dir::Left || dir == Dir::Right)) {
    const auto items = rowItems(row);
    const auto it = std::find(items.begin(), items.end(), cur);
    const auto i = static_cast<int>(it - items.begin());
    const int j = dir == Dir::Right ? i + 1 : i - 1;
    if (it != items.end() && j >= 0 && j < static_cast<int>(items.size())) {
      focus(items[static_cast<std::size_t>(j)]);
    } else {
      nudge(cur, dir);
    }
    return true;
  }

  const auto from = rect(cur);
  Rml::Element* best = nullptr;
  float bestScore = std::numeric_limits<float>::max();
  for (auto* c : candidates()) {
    if (c == cur) continue;
    const auto r = rect(c);
    float primary = 0;
    float secondary = 0;
    switch (dir) {
      case Dir::Down:
        if (r.cy() <= from.cy() + 1 || r.y < from.y + 1) continue;
        primary = std::max(0.0f, r.y - from.bottom());
        secondary = std::max({0.0f, r.x - from.right(), from.x - r.right()});
        break;
      case Dir::Up:
        if (r.cy() >= from.cy() - 1 || r.bottom() > from.bottom() - 1) continue;
        primary = std::max(0.0f, from.y - r.bottom());
        secondary = std::max({0.0f, r.x - from.right(), from.x - r.right()});
        break;
      case Dir::Right:
        if (r.cx() <= from.cx() + 1 || r.x < from.x + 1) continue;
        primary = std::max(0.0f, r.x - from.right());
        secondary = std::max({0.0f, r.y - from.bottom(), from.y - r.bottom()});
        break;
      case Dir::Left:
        if (r.cx() >= from.cx() - 1 || r.right() > from.right() - 1) continue;
        primary = std::max(0.0f, from.x - r.right());
        secondary = std::max({0.0f, r.y - from.bottom(), from.y - r.bottom()});
        break;
    }
    // overlapping on the other axis wins over anything that does not; then the nearest, then the most aligned
    const bool vertical = dir == Dir::Up || dir == Dir::Down;
    const float align = vertical ? std::fabs(r.cx() - from.cx()) : std::fabs(r.cy() - from.cy());
    const float score = (secondary > 0 ? 100000.0f : 0.0f) + primary * 4 + secondary * 8 + align * 0.5f;
    if (score < bestScore) {
      bestScore = score;
      best = c;
    }
  }
  if (best == nullptr) {
    nudge(cur, dir);
    return dir == Dir::Left || dir == Dir::Right;
  }
  if (auto* target = ancestor(best, "row"); target != nullptr && target != row) {
    if (auto* e = entryOf(target, from)) best = e;
  }
  focus(best);
  return true;
}

void Focus::remember(Rml::Element* e) {
  current_ = e->GetObserverPtr();
  auto* parent = e->GetParentNode();
  parent_ = parent ? parent->GetObserverPtr() : Rml::ObserverPtr<Rml::Element>{};
  index_ = -1;
  if (parent != nullptr) {
    for (int i = 0; i < parent->GetNumChildren(); ++i) {
      if (parent->GetChild(i) == e) index_ = i;
    }
  }
}

void Focus::focus(Rml::Element* e) {
  if (e == nullptr) return;
  auto* oldRow = row_.get();
  e->Focus(true);
  remember(e);
  auto* row = ancestor(e, "row");
  if (oldRow != nullptr && oldRow != row) oldRow->SetClass("focus-within", false);
  row_ = row ? row->GetObserverPtr() : Rml::ObserverPtr<Rml::Element>{};
  if (row != nullptr) {
    const auto items = rowItems(row);
    const auto it = std::find(items.begin(), items.end(), e);
    if (it != items.end()) row->SetAttribute("data-last", static_cast<int>(it - items.begin()));
    scrollRow(row, e);
    const bool entering = oldRow != row;
    row->SetClass("focus-within", true);
    placeLine(row, e, entering);
  }
  scrollPage(row != nullptr ? row : e);
  if (listener_) listener_(e);
}

void Focus::scrollRow(Rml::Element* row, Rml::Element* item) {
  auto* track = row->QuerySelector(".track");
  if (track == nullptr) return;
  const auto items = rowItems(row);
  const auto it = std::find(items.begin(), items.end(), item);
  if (it == items.end()) return;
  const auto i = static_cast<std::size_t>(it - items.begin());
  // anchored: focus walks freely to slot 2, then the row scrolls and focus stays in slot 2
  float scroll = 0;
  if (i >= 1 && items.size() > 1) {
    scroll = item->GetAbsoluteOffset(Rml::BoxArea::Border).x - items[1]->GetAbsoluteOffset(Rml::BoxArea::Border).x;
  }
  scroll = std::max(0.0f, scroll);
  track->SetAttribute("data-scroll", scroll);
  glide(track, "translateX(" + px(-scroll) + ")", rowGlideMs());
}

int Focus::rowGlideMs() const { return held_ ? kHeldGlideMs : reduced_ ? kReducedGlideMs : kRowGlideMs; }

int Focus::columnGlideMs() const { return held_ ? kHeldGlideMs : reduced_ ? kReducedGlideMs : kColumnGlideMs; }

void Focus::glide(Rml::Element* e, const std::string& transform, int ms) const {
  if (e == nullptr) return;
  Rml::PropertyDictionary d;
  const Rml::Property* target = nullptr;
  if (Rml::StyleSheetSpecification::ParsePropertyDeclaration(d, "transform", transform)) target = d.GetProperty(Rml::PropertyId::Transform);
  if (target == nullptr) {
    e->SetProperty("transform", transform);
    return;
  }
  // from the value it has now (an animation running included): a second press retargets, never jumps
  if (ms <= 0 || !e->Animate(Rml::PropertyId::Transform, *target, static_cast<float>(ms) / 1000.0f, Rml::Tween{Rml::Tween::Cubic, Rml::Tween::Out})) {
    e->SetProperty(Rml::PropertyId::Transform, *target);
  }
}

void Focus::placeLine(Rml::Element* row, Rml::Element* item, bool entering) {
  auto* line = row->QuerySelector(".cline");
  if (line == nullptr) return;
  auto* title = item->QuerySelector(".title");
  if (title == nullptr) title = item;
  const auto rowPos = row->GetAbsoluteOffset(Rml::BoxArea::Border);
  const auto r = rect(title);
  const float textW = static_cast<float>(Rml::ElementUtilities::GetStringWidth(title, textOf(title)));
  const float w = std::max(8.0f * dp_, std::min(textW, r.w));
  const float x = r.x - rowPos.x;
  // y in layout terms: the line moves with its row when the page scrolls
  const float y = title->GetAbsoluteOffset(Rml::BoxArea::Border).y - rowPos.y +
                  title->GetBox().GetSize(Rml::BoxArea::Border).y + 2 * dp_;
  const float base = line->GetBox().GetSize(Rml::BoxArea::Border).x;
  const float scale = base > 0 ? w / base : 1;
  if (entering) {
    // grows from the item's centre (180 ms): placed there at scale 0 now, at the title on the next tick
    line->SetClass("entering", true);
    line->SetProperty("transform", "translate(" + px(x + w / 2) + ", " + px(y) + ") scaleX(0)");
    entries_.push_back(Entry{row->GetObserverPtr(), item->GetObserverPtr()});
    return;
  }
  char buf[160];
  std::snprintf(buf, sizeof buf, "translate(%s, %s) scaleX(%.4f)", px(x).c_str(), px(y).c_str(),
                static_cast<double>(scale));
  line->SetProperty("transform", buf);
}

void Focus::scrollPage(Rml::Element* e) {
  auto* col = ancestor(e, "vscroll");
  // a column the page scrolls by its own rule (home's feed: the focused row held in one place)
  if (col == nullptr || col->IsClassSet("manual")) return;
  const float colTop = col->GetAbsoluteOffset(Rml::BoxArea::Border).y;
  const float bottom = e->GetAbsoluteOffset(Rml::BoxArea::Border).y + e->GetBox().GetSize(Rml::BoxArea::Border).y;
  // rows carry their gap below as a margin, which the bottom line leaves out
  float scroll = bottom - kSafeBottomDp * dp_;
  // the column never moves down past its own top
  scroll = std::max(0.0f, scroll);
  (void)colTop;
  col->SetAttribute("data-scroll", scroll);
  glide(col, "translateY(" + px(-scroll) + ")", columnGlideMs());
}

void Focus::nudge(Rml::Element* e, Dir dir) {
  // a row's end: the whole row moves 12 dp and back; elsewhere the element itself
  auto* row = ancestor(e, "row");
  auto* track = row != nullptr ? row->QuerySelector(".track") : nullptr;
  if (track == nullptr) return;
  const float sign = dir == Dir::Right || dir == Dir::Down ? -1.0f : 1.0f;
  if (dir == Dir::Up || dir == Dir::Down) return;
  for (auto& n : nudges_) {
    if (n.track.get() == track) return;
  }
  const float scroll = attrFloat(track, "data-scroll");
  track->SetClass("nudging", true);
  glide(track, "translateX(" + px(-scroll + sign * kNudgeDp * dp_) + ")", static_cast<int>(kNudgeOutMs));
  nudges_.push_back(Nudge{track->GetObserverPtr(), nowMs_ + kNudgeOutMs, false, sign});
}

bool Focus::tick(std::int64_t nowMs) {
  nowMs_ = nowMs;
  for (auto& entry : entries_) {
    auto* row = entry.row.get();
    auto* item = entry.item.get();
    if (row == nullptr || item == nullptr) continue;
    if (auto* line = row->QuerySelector(".cline")) line->SetClass("entering", false);
    if (current() == item) placeLine(row, item, false);
  }
  const bool entered = !entries_.empty();
  entries_.clear();
  for (auto it = nudges_.begin(); it != nudges_.end();) {
    auto* track = it->track.get();
    if (track == nullptr) {
      it = nudges_.erase(it);
      continue;
    }
    if (nowMs < it->untilMs) {
      ++it;
      continue;
    }
    if (!it->started) {
      // back over 160 ms
      track->SetClass("nudging", false);
      glide(track, "translateX(" + px(-attrFloat(track, "data-scroll")) + ")", static_cast<int>(kNudgeBackMs));
      it->started = true;
      it->untilMs = nowMs + kNudgeBackMs;
      ++it;
      continue;
    }
    it = nudges_.erase(it);
  }
  return entered || !nudges_.empty();
}

std::int64_t Focus::nextDueMs(std::int64_t nowMs) const {
  if (!entries_.empty()) return 0;
  std::int64_t best = -1;
  for (const auto& n : nudges_) {
    const std::int64_t due = n.untilMs > nowMs ? n.untilMs - nowMs : 0;
    if (best < 0 || due < best) best = due;
  }
  return best;
}

void Focus::setHeld(bool held) {
  held_ = held;
  if (auto* r = root_.get()) {
    if (auto* doc = r->GetOwnerDocument()) doc->SetClass("held", held);
  }
}

void Focus::repair() {
  auto* s = scope();
  if (s == nullptr) return;
  auto* cur = current();
  if (cur != nullptr && inside(cur, s) && cur->IsVisible(true)) return;
  const auto cands = candidates();
  if (cands.empty()) {
    current_.reset();
    return;
  }
  Rml::Element* pick = nullptr;
  // the next sibling (which now sits at the removed one's index), else the previous one
  if (auto* parent = parent_.get(); parent != nullptr && inside(parent, s) && index_ >= 0) {
    const auto focusableAt = [&](int i) -> Rml::Element* {
      if (i < 0 || i >= parent->GetNumChildren()) return nullptr;
      auto* c = parent->GetChild(i);
      return std::find(cands.begin(), cands.end(), c) != cands.end() ? c : nullptr;
    };
    pick = focusableAt(index_);
    if (pick == nullptr) pick = focusableAt(index_ - 1);
    for (int i = index_ - 2; pick == nullptr && i >= 0; --i) pick = focusableAt(i);
    // else the nearest focusable in the same row
    if (pick == nullptr) {
      if (auto* row = ancestor(parent, "row")) {
        const auto items = rowItems(row);
        if (!items.empty()) pick = items[std::min(items.size() - 1, static_cast<std::size_t>(std::max(0, index_)))];
      }
    }
  }
  if (pick == nullptr) {
    Rml::ElementList defaults;
    s->QuerySelectorAll(defaults, ".default-focus");
    for (auto* d : defaults) {
      if (std::find(cands.begin(), cands.end(), d) != cands.end()) {
        pick = d;
        break;
      }
    }
  }
  if (pick == nullptr) pick = cands.front();
  focus(pick);
}

}  // namespace cadence::screen::ui
