#include "app/routes/system/tasks.h"

#include "app/routes/system/card-sheet.h"

#include "i18n/gen/keys.h"
#include "ui/core/rml.h"
#include "ui/tokens.h"

#include <RmlUi/Core/ElementDocument.h>

#include <algorithm>
#include <cstring>
#include <map>

namespace cadence::screen::routes {

namespace {

using ui::rml::esc;
using ui::rml::icon;

constexpr int kZ = 20;

cadence_cards_v1_Tone toneOf(const cadence_cards_v1_Card& c) {
  if (c.root == nullptr || c.root->which_kind != cadence_cards_v1_Node_card_tag || c.root->kind.card == nullptr) {
    return cadence_cards_v1_Tone_TONE_NEUTRAL;
  }
  return cards::val(c.root->kind.card->tone, cadence_cards_v1_Tone_TONE_NEUTRAL);
}

float maxScroll(Rml::Element* e) { return std::max(0.0f, e->GetScrollHeight() - e->GetClientHeight()); }

}  // namespace

Tasks::Tasks(Env env, PageRpc& rpc, const DeviceState& device) : env_(std::move(env)), rpc_(rpc), device_(device) {
  cards_ = std::make_unique<cards::CardHost>(env_.ui, sheet::actionSender(env_.out, "tsk:"));
  // a list card's «Открыть» answers its task's panel
  cards_->onPage([this](const cadence_cards_v1_Page& page) { showTask(page, true); });
  hooks_.push_back(env_.ui.addKeyFilter([this](const platform::KeyEvent& e) { return key(e); }));
  hooks_.push_back(env_.ui.addActivateFilter([this](const ui::Activation& a) { return activate(a); }));
  hooks_.push_back(env_.ui.addTicker([this](std::int64_t now) { tick(now); }));
  env_.ui.addDocument("tasks", kZ, [this] { return build(); }, [this](Rml::ElementDocument& d) { loaded(d); });
}

Tasks::~Tasks() {
  if (uiAbandoned()) return;
  for (const int h : hooks_) env_.ui.remove(h);
  if (level_ != Level::Closed) env_.ui.focus().popScope();
  env_.ui.removeDocument("tasks");
}

Rml::Element* Tasks::el(const char* id) const { return doc_ != nullptr ? doc_->GetElementById(id) : nullptr; }

std::string Tasks::build() {
  return "<div id=\"tl\" class=\"tlayer\"><div class=\"scrim-sheet tscrim\"/><div id=\"tpanel\" class=\"tpanel\">"
         "<div class=\"tk-head\">" +
         icon("sparkle", "ticon") +
         "<span id=\"tk-title\" class=\"tk-title\">[[tasks.title]]</span></div>"
         "<div id=\"tbody\" class=\"tbody\"><div id=\"tcards\" class=\"cstack tcards vscroll\"/></div>"
         "<div id=\"tnew\" class=\"tnew hidden\">[[tasks.new_below]]</div></div></div>";
}

void Tasks::loaded(Rml::ElementDocument& doc) {
  doc_ = &doc;
  cards_->clear();
  cards_->attach(el("tcards"), cards::kMaxPageCards, true);
  if (auto* l = el("tl")) {
    l->SetClass("open", level_ != Level::Closed);
    l->SetClass("under", under_);
  }
  if (level_ == Level::List && list_) draw(*list_);
  if (level_ == Level::Task && task_) draw(*task_);
  if (level_ != Level::Closed) {
    if (auto* p = el("tpanel")) {
      env_.ui.focus().pushScope(p);
      focusInitial();
    }
  }
}

bool Tasks::message(const cadence_screen_v1_Envelope& m) {
  switch (m.which_body) {
    case cadence_screen_v1_Envelope_rpc_response_tag:
      // the answers to this host's card actions carry its prefix
      return sheet::actionResponse(m, "tsk:", *cards_);
    case cadence_screen_v1_Envelope_tasks_summary_tag:
      summary(m.body.tasks_summary);
      return true;
    case cadence_screen_v1_Envelope_link_status_tag:
      // the feed runs while the link is up; a new link subscribes again
      if (device_.online()) {
        if (feedId_.empty() || !rpc_.open(feedId_)) subscribe();
      } else {
        feedId_.clear();
      }
      return false;
    case cadence_screen_v1_Envelope_route_tag: {
      const auto& r = m.body.route;
      if (r.back) {
        if (level_ == Level::Task) {
          openList();
          return true;
        }
        if (level_ == Level::List) {
          close();
          return true;
        }
        return false;
      }
      if (r.route == cadence_v2_ScreenRoute_SCREEN_ROUTE_TASKS) {
        openList();
        return true;
      }
      if (r.route == cadence_v2_ScreenRoute_SCREEN_ROUTE_TASK && r.ref[0] != '\0') {
        openTask(r.ref);
        return true;
      }
      // another route: the panel closes over it
      if (level_ != Level::Closed && r.route != cadence_v2_ScreenRoute_SCREEN_ROUTE_UNSPECIFIED) close();
      return false;
    }
    default:
      return false;
  }
}

void Tasks::subscribe() {
  feedId_ = rpc_.call(
      "tasks.list", "{}",
      [this](const cadence_v2_RpcResponse& r) {
        if (std::strcmp(r.status, "ok") != 0) {
          feedId_.clear();
          return;
        }
        if (r.page != nullptr) showList(*r.page);
      },
      [this](const cadence_v2_RpcEvent& ev) {
        if (ev.page != nullptr) showList(*ev.page);
        if (ev.last) feedId_.clear();
      },
      true);
}

void Tasks::summary(const cadence_v2_TasksSummary& t) {
  // the server's typed counts; a late one (a lower revision) is dropped
  if (summarized_ && t.revision != 0 && t.revision <= summaryRevision_) return;
  summarized_ = true;
  summaryRevision_ = t.revision;
  setCounts(static_cast<int>(t.running + t.waiting), static_cast<int>(t.waiting), static_cast<int>(t.finished));
}

void Tasks::setCounts(int active, int waiting, int finished) {
  if (active == active_ && waiting == waiting_ && finished == finished_) return;
  active_ = active;
  waiting_ = waiting;
  finished_ = finished;
  if (onIndicatorChanged) onIndicatorChanged();
}

void Tasks::count(const cadence_cards_v1_Page& page) {
  // a cadenced before tasks_summary: the list's tones stand in for the counts
  if (summarized_) return;
  int a = 0, w = 0;
  for (pb_size_t i = 0; i < page.cards_count; ++i) {
    // the server's tones say it (task cards: warning waits for you, info runs); the device reads nothing else
    const auto t = toneOf(page.cards[i]);
    if (t == cadence_cards_v1_Tone_TONE_WARNING) ++w;
    if (t == cadence_cards_v1_Tone_TONE_WARNING || t == cadence_cards_v1_Tone_TONE_INFO) ++a;
  }
  setCounts(a, w, finished_);
}

std::string Tasks::indicator() const {
  if (active_ == 0) return {};
  std::string s = "<div id=\"tasks-ind\" class=\"tind f\" data-action=\"tasks:open\"><div class=\"glow\"/><div class=\"ring\"/>" +
                  icon("sparkle");
  if (waiting_ == 0) s += "<span class=\"tcount\">" + std::to_string(active_) + "</span>";
  if (waiting_ > 0) {
    // something waits: a small dot plus the words, never the colour alone
    s += "<div class=\"tdot\"/><span class=\"twords\">" + esc(env_.tr.tr(i18n::keys::TasksCount{active_})) + " · " +
         esc(env_.tr.tr(i18n::keys::TasksWaiting{waiting_})) + "</span>";
  }
  return s + "</div>";
}

void Tasks::showList(const cadence_cards_v1_Page& page) {
  count(page);
  list_ = sheet::copyPage(page);
  if (level_ == Level::List) update(page);
}

void Tasks::showTask(const cadence_cards_v1_Page& page, bool live) {
  task_ = sheet::copyPage(page);
  if (level_ != Level::Task) {
    // remember the list card it came from, for BACK
    if (level_ == Level::List) openerCard_ = sheet::cardIdOf(env_.ui.focus().current());
    openLevel(Level::Task);
    draw(page);
    focusInitial();
    if (live && itemId_.empty()) watch();
    return;
  }
  update(page);
}

void Tasks::openList() {
  const bool fromTask = level_ == Level::Task;
  if (!itemId_.empty()) rpc_.cancel(itemId_);
  if (!watchId_.empty()) rpc_.cancel(watchId_);
  itemId_.clear();
  watchId_.clear();
  taskRef_.clear();
  task_.reset();
  openLevel(Level::List);
  cards_->clear();
  if (list_) draw(*list_);
  if (list_ == nullptr || list_->cards_count == 0) {
    if (auto* c = el("tcards")) c->SetInnerRML("<div class=\"tempty\">[[tasks.empty]]</div>");
  }
  if (feedId_.empty() && device_.online()) subscribe();
  // BACK from a task comes back to its card (once laid out)
  afterLayout(env_.ui, alive_.guard([this, fromTask] {
    if (fromTask && sheet::focusCard(env_.ui, sheet::findCard(el("tcards"), openerCard_))) return;
    focusNow();
  }));
}

void Tasks::openTask(const std::string& ref) {
  if (!itemId_.empty()) rpc_.cancel(itemId_);
  if (!watchId_.empty()) rpc_.cancel(watchId_);
  watchId_.clear();
  taskRef_ = ref;
  if (level_ == Level::List) openerCard_ = sheet::cardIdOf(env_.ui.focus().current());
  openLevel(Level::Task);
  cards_->clear();
  itemId_ = rpc_.call(
      "tasks.item", "{\"taskId\":" + jsonString(ref) + "}",
      [this](const cadence_v2_RpcResponse& r) {
        if (std::strcmp(r.status, "ok") != 0) {
          itemId_.clear();
          env_.ui.toast(ui::ToastSpec{{}, ui::ToastTone::Warning, "[[common.load_failed]]", "triangle", {}, {}, 0});
          return;
        }
        if (r.page != nullptr) {
          task_ = sheet::copyPage(*r.page);
          draw(*r.page);
          focusInitial();
        }
      },
      [this](const cadence_v2_RpcEvent& ev) {
        if (ev.page != nullptr) {
          task_ = sheet::copyPage(*ev.page);
          update(*ev.page);
        }
        if (ev.last) itemId_.clear();
      },
      true);
}

void Tasks::watch() {
  // a panel a card opened has no subscription of its own: its cards' revisions come through cards.watch
  if (!task_) return;
  std::string ids;
  for (pb_size_t i = 0; i < task_->cards_count; ++i) {
    if (!ids.empty()) ids += ",";
    ids += jsonString(cards::str(task_->cards[i].id));
  }
  if (ids.empty()) return;
  watchId_ = rpc_.call("cards.watch", "{\"cardIds\":[" + ids + "]}", {}, [this](const cadence_v2_RpcEvent& ev) {
    if (ev.page != nullptr) update(*ev.page);
    if (ev.last) watchId_.clear();
  }, true);
}

void Tasks::close() {
  if (level_ == Level::Closed) return;
  while (env_.ui.levelOpen()) env_.ui.closeLevel();
  level_ = Level::Closed;
  if (!itemId_.empty()) rpc_.cancel(itemId_);
  if (!watchId_.empty()) rpc_.cancel(watchId_);
  itemId_.clear();
  watchId_.clear();
  taskRef_.clear();
  task_.reset();
  newBelow_ = false;
  env_.ui.focus().popScope();
  sheet::leave(env_.ui, alive_, el("tl"), [this] { return level_ == Level::Closed; });
  cards_->clear();
  if (onOpenChanged) onOpenChanged(false);
  env_.ui.markDirty();
}

void Tasks::openLevel(Level l) {
  const bool was = level_ != Level::Closed;
  level_ = l;
  newBelow_ = false;
  following_ = true;
  if (auto* t = el("tnew")) t->SetClass("hidden", true);
  if (auto* t = el("tk-title")) t->SetInnerRML(l == Level::Task ? "[[tasks.task]]" : "[[tasks.title]]");
  if (!was) {
    if (onOpening) onOpening();
    sheet::enter(env_.ui, el("tl"));
    if (auto* p = el("tpanel")) env_.ui.focus().pushScope(p);
    if (onOpenChanged) onOpenChanged(true);
  }
  env_.ui.markDirty();
}

void Tasks::draw(const cadence_cards_v1_Page& page) {
  cards_->clear();
  if (auto* c = el("tcards")) c->SetInnerRML("");
  cards_->showPage(page);
  if (level_ == Level::List) divider();
  // a tail starts at its end
  if (auto* host = el("tcards")) {
    afterLayout(env_.ui, alive_.guard([this] {
      auto* h = el("tcards");
      if (h == nullptr) return;
      Rml::ElementList tails;
      h->QuerySelectorAll(tails, ".clong.tail");
      for (auto* t : tails) t->SetScrollTop(maxScroll(t));
    }));
    (void)host;
  }
  env_.ui.markDirty();
}

void Tasks::divider() {
  auto* host = el("tcards");
  if (host == nullptr) return;
  // the finished ones sit under a divider: the first card that neither waits nor runs
  for (int i = 0; i < host->GetNumChildren(); ++i) {
    auto* slot = host->GetChild(i);
    if (slot->QuerySelector(".stripe-warning") != nullptr || slot->QuerySelector(".stripe-info") != nullptr) continue;
    if (i == 0) return;
    auto d = doc_->CreateElement("div");
    d->SetClassNames("tdivider");
    d->SetInnerRML("[[tasks.finished]]");
    host->InsertBefore(std::move(d), slot);
    return;
  }
}

void Tasks::update(const cadence_cards_v1_Page& page) {
  auto* host = el("tcards");
  if (host == nullptr || level_ == Level::Closed) return;
  // what the reader holds: the focused element by its card and index, each tail's scroll and whether it follows
  const auto mark = sheet::markFocus(host, env_.ui.focus().current());
  std::map<std::string, std::pair<float, bool>> tails;
  {
    Rml::ElementList ts;
    host->QuerySelectorAll(ts, ".clong.tail");
    for (auto* t : ts) {
      const auto id = sheet::cardIdOf(t);
      tails[id] = {t->GetScrollTop(), t->GetScrollTop() >= maxScroll(t) - 1.0f};
    }
  }
  auto* body = el("tbody");
  const float bodyScroll = body != nullptr ? body->GetScrollTop() : 0.0f;
  bool grew = false;
  for (pb_size_t i = 0; i < page.cards_count; ++i) {
    if (cards_->show(page.cards[i])) grew = grew || tails.count(std::string(cards::str(page.cards[i].id))) != 0;
  }
  if (level_ == Level::List) {
    Rml::ElementList ds;
    host->QuerySelectorAll(ds, ".tdivider");
    for (auto* d : ds) d->GetParentNode()->RemoveChild(d);
    divider();
  }
  if (body != nullptr) body->SetScrollTop(bodyScroll);
  // after layout: the tails where they were (or at their end while following), focus back on its element
  afterLayout(env_.ui, alive_.guard([this, tails, mark, grew] {
    auto* h = el("tcards");
    if (h == nullptr) return;
    Rml::ElementList ts;
    h->QuerySelectorAll(ts, ".clong.tail");
    bool hidden = false;
    for (auto* t : ts) {
      const auto it = tails.find(sheet::cardIdOf(t));
      if (it == tails.end() || it->second.second) {
        t->SetScrollTop(maxScroll(t));
      } else {
        t->SetScrollTop(it->second.first);
        hidden = hidden || it->second.first < maxScroll(t) - 1.0f;
      }
    }
    if (grew && hidden) {
      newBelow_ = true;
      if (auto* n = el("tnew")) n->SetClass("hidden", false);
    }
    sheet::restoreFocus(env_.ui, h, mark);
    env_.ui.markDirty();
  }));
  env_.ui.markDirty();
}

void Tasks::focusInitial() {
  // once laid out: the page scrolls to keep focus low
  afterLayout(env_.ui, alive_.guard([this] { focusNow(); }));
}

void Tasks::focusNow() {
  auto* host = el("tcards");
  if (host == nullptr) return;
  // an approval opens on «Запретить»: the card with the command, its button that is not the primary
  Rml::ElementList codes;
  host->QuerySelectorAll(codes, ".ccode");
  for (auto* code : codes) {
    for (auto* p = code; p != nullptr; p = p->GetParentNode()) {
      if (!p->IsClassSet("ccard")) continue;
      Rml::ElementList buttons;
      p->QuerySelectorAll(buttons, ".cactions .btn.f");
      for (auto* b : buttons) {
        if (!b->IsClassSet("primary")) {
          env_.ui.focus().focus(b);
          return;
        }
      }
      break;
    }
  }
  sheet::focusFirst(env_.ui, host);
}

void Tasks::setUnderDrawer(bool under) {
  under_ = under;
  if (auto* l = el("tl")) l->SetClass("under", under);
  env_.ui.markDirty();
}

bool Tasks::key(const platform::KeyEvent& e) {
  if (level_ == Level::Closed || under_ || env_.ui.levelOpen() || env_.ui.toastFocused()) return false;
  if (e.key == cadence_screen_v1_Key_KEY_BACK) {
    if (e.action != cadence_screen_v1_KeyAction_KEY_ACTION_PRESS && e.action != cadence_screen_v1_KeyAction_KEY_ACTION_UP) return true;
    // the task goes back to the list with focus on its card; the list closes
    if (level_ == Level::Task) {
      openList();
    } else {
      close();
    }
    return true;
  }
  return false;
}

bool Tasks::activate(const ui::Activation& a) {
  if (a.action == "tasks:open") {
    openList();
    return true;
  }
  // a task toast's «Открыть» (a typed open action on the task route, which the shell names task:<ref>) opens its
  // panel here, over whatever plays
  if (a.action.starts_with("toast:task:")) {
    openTask(a.action.substr(11));
    return true;
  }
  auto* p = el("tpanel");
  if (p == nullptr || a.element == nullptr || !p->Contains(a.element)) return false;
  return cards_->activate(a);
}

void Tasks::tick(std::int64_t) {
  if (level_ != Level::Task || !newBelow_) return;
  // ↓ to the end resumes following: the pill goes
  auto* h = el("tcards");
  if (h == nullptr) return;
  Rml::ElementList ts;
  h->QuerySelectorAll(ts, ".clong.tail");
  bool atEnd = true;
  for (auto* t : ts) atEnd = atEnd && t->GetScrollTop() >= maxScroll(t) - 1.0f;
  if (atEnd) {
    newBelow_ = false;
    if (auto* n = el("tnew")) n->SetClass("hidden", true);
    env_.ui.markDirty();
  }
}

}  // namespace cadence::screen::routes
