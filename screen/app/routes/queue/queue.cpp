#include "app/routes/queue/queue.h"

#include "app/routes/core/catalog.h"
#include "ui/core/rml.h"

#include <nlohmann/json.hpp>

#include <cstdlib>

namespace cadence::screen::routes {

namespace {

using ui::rml::esc;

// an item's art, else the show's (the item playing now), else a generated one
std::string art(const QueueItem& q, const std::string& show, const std::string& seed) {
  if (!q.art.empty()) return q.art;
  return show.empty() ? "gen:art:" + seed : show;
}

// a title that only repeats the number: "7", "7.5", "Серия 7", "Episode 7"
bool bareNumber(const std::string& title, const std::string& episode) {
  if (title.empty() || title == episode) return true;
  if (title.find_first_not_of("0123456789. ") == std::string::npos) return true;
  for (const std::string prefix : {"Серия ", "серия ", "Episode ", "episode ", "Ep. ", "EP"}) {
    if (title.starts_with(prefix) && title.substr(prefix.size()) == episode) return true;
  }
  return false;
}

}  // namespace

QueueContent::Line QueueContent::line(const QueueItem& q) {
  Line l;
  if (q.episode.empty()) {
    l.title = esc(q.title);
    l.meta = esc(q.subtitle);
    return l;
  }
  l.title = "[[episodes.episode|n=" + catalog::argOf(q.episode) + "]]";
  std::string name = q.episodeName;
  if (name.empty() && !bareNumber(q.title, q.episode)) name = q.title;
  if (bareNumber(name, q.episode)) name.clear();
  l.meta = esc(!name.empty() ? name : q.subtitle);
  return l;
}

QueuePage QueueContent::fromSession(const Session& s) {
  QueuePage p;
  QueueItem now;
  now.ref = s.itemRef;
  now.title = s.title;
  now.subtitle = !s.artist.empty() ? s.artist : s.series;
  now.episode = s.episode;
  now.art = s.art;
  p.current = now;
  // the server's up next (its first 20) for a session it sends the fields of, else the episodes after this one
  if (s.serverFields) {
    p.next = s.serverUpNext;
    return p;
  }
  bool after = false;
  for (const auto& e : s.episodes) {
    if (after) p.next.push_back(e);
    if (!s.episode.empty() && e.episode == s.episode) after = true;
  }
  if (p.next.empty() && s.upNext) p.next.push_back(*s.upNext);
  return p;
}

void QueueContent::open(SideSheet& sheet, Rml::Element* body) {
  sheet_ = &sheet;
  body_ = body->GetObserverPtr();
  auto& c = sheet.context();
  if (c.session) page_ = fromSession(*c.session);
  render();
  load();
}

void QueueContent::load() {
  auto& c = sheet_->context();
  if (!c.session) return;
  nlohmann::json args = {{"sessionId", c.session->sessionId}};
  c.rpc.call("media.queue", args.dump(), [this](const RpcAnswer& a) {
    if (sheet_ == nullptr || !sheet_->isOpen() || sheet_->content() != this || !a.ok()) return;
    auto q = queueFrom(a.json);
    if (!q) return;
    // the server's page lists what comes after the playing item: the item itself is the session's
    if (!q->current && sheet_->context().session) q->current = fromSession(*sheet_->context().session).current;
    show(*q);
  }, c.nowMs);
}

void QueueContent::show(const QueuePage& page) {
  page_ = page;
  render();
}

void QueueContent::render() {
  auto* body = body_.get();
  if (body == nullptr || sheet_ == nullptr) return;
  auto& c = sheet_->context();
  const auto progress = [&]() -> double {
    return c.state.durationMs > 0 ? static_cast<double>(c.position()) / static_cast<double>(c.state.durationMs) : 0.0;
  };
  std::string s;
  const std::string showArt = page_.current ? page_.current->art : std::string();
  constexpr const char* kActions = "now|[[queue.play_now]]|play;next|[[queue.play_next]]|playlist;remove|[[queue.remove]]|close";
  if (!page_.history.empty()) {
    s += "<div class=\"sgroup\">[[queue.history]]</div>";
    const std::size_t from = allHistory_ || page_.history.size() <= 3 ? 0 : page_.history.size() - 3;
    if (from > 0) {
      ui::rml::ListItem all;
      all.title = "[[queue.all_history]]";
      all.trailIcon = "chevron_down";
      all.action = "queue:history";
      all.id = "q-history-all";
      s += ui::rml::listItem(all);
    }
    for (std::size_t i = from; i < page_.history.size(); ++i) {
      const auto& h = page_.history[i];
      ui::rml::ListItem it;
      it.leadArt = art(h, showArt, "h" + std::to_string(i));
      const auto l = line(h);
      it.title = l.title;
      it.meta = l.meta;
      it.action = "queue:h" + std::to_string(i);
      it.actions = kActions;
      it.id = "q-h" + std::to_string(i);
      std::string item = ui::rml::listItem(it);
      item.replace(item.find("class=\"item"), 11, "class=\"item history");
      s += item;
    }
  }
  if (page_.current) {
    s += "<div class=\"sgroup\">[[common.now]]</div>";
    ui::rml::ListItem it;
    it.leadArt = art(*page_.current, showArt, "now");
    it.title = esc(page_.current->title);
    std::string meta = page_.current->episode.empty() ? esc(page_.current->subtitle)
                                                      : "[[episodes.episode|n=" + catalog::argOf(page_.current->episode) + "]]";
    it.meta = "<div class=\"line1\">" + meta + "</div><div style=\"width: 360dp; margin-top: 8dp;\">" + ui::rml::progress(progress()) + "</div>";
    // the equaliser glyph: three static bars, never animated
    it.trail = "<div class=\"eqbars\"><div style=\"height: 18dp;\"/><div style=\"height: 30dp;\"/><div style=\"height: 22dp;\"/></div>";
    it.action = "queue:now";
    it.id = "q-now";
    std::string item = ui::rml::listItem(it);
    item.replace(item.find("class=\"item"), 11, "class=\"item now");
    s += item;
  }
  s += "<div class=\"sgroup\">[[queue.next]]</div>";
  if (page_.next.empty()) s += "<div class=\"muted\">[[ui.empty]]</div>";
  for (std::size_t i = 0; i < page_.next.size(); ++i) {
    const auto& n = page_.next[i];
    ui::rml::ListItem it;
    it.leadArt = art(n, showArt, "n" + std::to_string(i));
    const auto l = line(n);
    it.title = l.title;
    it.meta = l.meta;
    if (n.durationMs > 0) it.trail = "<span class=\"figures\">" + clockTime(n.durationMs) + "</span>";
    it.action = "queue:n" + std::to_string(i);
    it.actions = kActions;
    it.id = "q-n" + std::to_string(i);
    s += ui::rml::listItem(it);
  }
  body->SetInnerRML(s);
  // focus starts on the current item, which sits in the upper third
  if (auto* now = body->GetElementById("q-now")) {
    c.ui().focus().focus(now);
    sheet_->scrollTo(now);
  } else if (auto* first = body->QuerySelector(".f")) {
    c.ui().focus().focus(first);
  }
  c.ui().markDirty();
}

const QueueItem* QueueContent::item(const std::string& key) const {
  if (key == "now") return page_.current ? &*page_.current : nullptr;
  const auto index = static_cast<std::size_t>(std::atoi(key.c_str() + 1));
  if (key[0] == 'h' && index < page_.history.size()) return &page_.history[index];
  if (key[0] == 'n' && index < page_.next.size()) return &page_.next[index];
  return nullptr;
}

bool QueueContent::activate(const ui::Activation& a) {
  // a long OK's entry names itself; its element is the item it was opened on
  const std::string own = a.secondary && a.element != nullptr ? a.element->GetAttribute<Rml::String>("data-action", "")
                                                               : a.action;
  if (!own.starts_with("queue:")) return false;
  const auto key = own.substr(6);
  if (key == "history") {
    allHistory_ = true;
    render();
    return true;
  }
  const auto* q = item(key);
  if (q == nullptr) return true;
  auto& c = sheet_->context();
  if (!a.secondary || a.action == "now") {
    if (key != "now" && jump_) jump_(*q);
    return true;
  }
  // play next and remove change the server's queue (media.control {sessionId, action, ref}); the list is asked
  // again after its answer, so a refusal leaves it as it was
  if (!c.transport) return true;
  const std::string action = a.action == "next" ? "play_next" : a.action;
  c.transport(action, q->ref, [this](bool ok) {
    if (ok && sheet_ != nullptr && sheet_->isOpen() && sheet_->content() == this) load();
  });
  return true;
}

}  // namespace cadence::screen::routes
