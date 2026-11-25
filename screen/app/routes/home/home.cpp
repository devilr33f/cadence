#include "app/routes/home/home.h"

#include "app/routes/core/failure-card.h"
#include "app/routes/core/backdrop.h"
#include "app/routes/home/alarms-strip.h"
#include "app/routes/home/answer.h"
#include "app/routes/home/feed.h"
#include "app/routes/home/hero.h"
#include "app/routes/home/nearby-strip.h"
#include "app/routes/home/now-playing.h"
#include "app/routes/core/view.h"
#include "app/routes/core/methods.h"
#include "app/routes/system/alarms-view.h"
#include "app/routes/system/alarms.h"
#include "app/routes/system/devices.h"
#include "i18n/locale.h"
#include "ui/core/focus.h"
#include "ui/tokens.h"

#include <RmlUi/Core/ElementDocument.h>

#include <algorithm>
#include <map>

namespace cadence::screen::routes {

using namespace ui::rml;

namespace {

constexpr std::int64_t kSlowMs = 8000;
// focus that rested this long loads the card's backdrop and details (a fast walk along a row loads nothing)
constexpr std::int64_t kPreviewRestMs = 150;
// the visible 7 plus 3 to the right; more join as focus nears the end
constexpr std::size_t kFirstCards = 10;
constexpr std::size_t kGrowBy = 5;
constexpr int kStripAfterFailures = 3;
// where the focused row's top sits: under the hero's name and details
constexpr float kFeedAnchorDp = 500;

std::string defaultTitle(const std::string& provider) {
  if (provider == "torrserver") return "[[home.films_series]]";
  if (provider == "torrplayer") return "[[home.anime]]";
  if (provider == "ytmusic") return "[[home.youtube]]";
  return "[[home.music]]";
}

bool isContinue(const std::string& sectionId) { return sectionId.find("continue") != std::string::npos; }

std::string playSignature(const std::optional<BackgroundPlay>& play) {
  if (!play) return {};
  return play->sessionId + "|" + play->itemRef + "|" + play->art + "|" + play->title + "|" + play->artist +
         (play->audio ? "|audio" : "|video") + (play->paused ? "|paused" : "|playing");
}

// what OK on an item opens: a title page for anything with children, the player for a leaf that plays
bool opensTitle(const catalog::Item& it) {
  static const char* kinds[] = {"title_group", "series", "season", "album", "playlist", "artist", "channel", "folder", "torrent"};
  for (const auto* k : kinds) {
    if (it.kind == k) return true;
  }
  return it.hasChildren || !it.playable;
}

}  // namespace

const std::vector<std::string>& HomeRoute::providers() {
  static const std::vector<std::string> p = {"torrserver", "torrplayer", "ytmusic", "spotify"};
  return p;
}

HomeRoute::HomeRoute(Services& s, const RouteArgs& args) : Route(s) {
  for (const auto& id : providers()) {
    Provider p;
    p.id = id;
    providers_.push_back(std::move(p));
  }
  if (args.data.is_object()) refreshMs_ = std::max<std::int64_t>(1000, args.data.value("refreshMs", kRefreshMs));
}

HomeRoute::Provider* HomeRoute::provider(const std::string& id) {
  for (auto& p : providers_) {
    if (p.id == id) return &p;
  }
  return nullptr;
}

void HomeRoute::start() {
  for (auto& p : providers_) load(p);
  nextRefreshMs_ = s_.ui.nowMs() + refreshMs_;
}

void HomeRoute::online() {
  for (auto& p : providers_) {
    if (p.state == State::Down) retry(p);
  }
}

void HomeRoute::retry(Provider& p) {
  if (p.state != State::Down) {
    load(p);
    return;
  }
  if (p.retrying) return;
  p.retrying = true;
  if (auto* b = view::byId(page(), "retry-" + p.id)) b->SetClass("loading", true);
  p.askedMs = s_.ui.nowMs();
  const auto id = p.id;
  cancel(p.callId);
  p.callId = call(rpc::kCatalogBrowse, Json{{"path", id}, {"limit", 30}},
                  {[this, id](const std::string& status, const Json& json) { answered(id, status, json); }, nullptr});
}

void HomeRoute::load(Provider& p) {
  p.state = State::Loading;
  p.askedMs = s_.ui.nowMs();
  const auto id = p.id;
  cancel(p.callId);
  p.callId = call(rpc::kCatalogBrowse, Json{{"path", id}, {"limit", 30}},
                  {[this, id](const std::string& status, const Json& json) { answered(id, status, json); }, nullptr});
  redrawProvider(p);
}

namespace {

home_answer::Outcome outcomeOf(const std::string& status, const Json& json) {
  const std::string code = json.is_object() ? json.value("code", std::string()) : std::string();
  return home_answer::outcomeOf(status, code);
}

}  // namespace

void HomeRoute::hide(Provider& p, home_answer::Outcome how) {
  // no row, silently; asked again only on the slow re-check, or never
  p.state = State::Absent;
  p.sig.clear();
  p.sections.clear();
  p.failures = 0;
  p.never = how == home_answer::Outcome::HideForGood;
  p.recheckAtMs = s_.ui.nowMs() + kRecheckMs;
}

std::vector<HomeRoute::Section> HomeRoute::parseSections(const Json& json) {
  std::vector<Section> out;
  for (const auto& s : json.value("sections", Json::array())) {
    Section sec;
    sec.id = s.value("id", "");
    sec.title = s.value("title", "");
    sec.path = s.value("path", "");
    sec.next = s.value("next", "");
    sec.items = catalog::parseItems(s.value("items", Json::array()));
    sec.cont = isContinue(sec.id);
    if (!sec.items.empty()) out.push_back(std::move(sec));
  }
  // continue watching leads its provider's rows
  std::stable_sort(out.begin(), out.end(), [](const Section& a, const Section& b) { return a.cont && !b.cont; });
  return out;
}

void HomeRoute::answered(const std::string& id, const std::string& status, const Json& json) {
  auto* p = provider(id);
  if (p == nullptr) return;
  p->callId.clear();
  p->held = false;
  p->retrying = false;
  if (status != rpc::kStatusOK) p->failure = failure::of(rpc::kCatalogBrowse, status, json, p->askedMs > 0 ? s_.ui.nowMs() - p->askedMs : -1);
  // a skeleton giving way to rows cross-fades into them
  if ((p->state == State::Loading || p->state == State::Slow) && status == rpc::kStatusOK && page() != nullptr) {
    s_.ui.crossfade();
  }
  const auto outcome = outcomeOf(status, json);
  if (outcome == home_answer::Outcome::Rows) {
    p->state = State::Ready;
    p->failures = 0;
    p->sections = parseSections(json);
    p->sig = json.value("sections", Json::array()).dump();
    if (p->sections.empty()) p->state = State::Absent;
  } else if (outcome != home_answer::Outcome::Error) {
    // no feed for this household (home_answer.h): no row, no error
    hide(*p, outcome);
  } else {
    if (p->state != State::Down) p->downSinceMs = s_.ui.nowMs();
    p->state = State::Down;
    p->failures++;
  }
  // focus inside the provider's rows (its retry, its cards) stays there through the redraw
  std::string focused;
  if (auto* doc = page()) {
    auto* f = s_.ui.focus().current();
    auto* host = view::byId(doc, "prov-" + p->id);
    for (auto* e = f; e != nullptr && host != nullptr; e = e->GetParentNode()) {
      if (e == host) {
        focused = f->GetId();
        break;
      }
    }
  }
  redrawProvider(*p);
  if (!focused.empty()) {
    if (auto* doc = page()) {
      doc->UpdateDocument();
      auto* e = view::byId(doc, focused);
      if (e == nullptr) {
        if (auto* host = view::byId(doc, "prov-" + p->id)) e = host->QuerySelector(".f");
      }
      if (e != nullptr) {
        ownFocus_ = true;
        s_.ui.focus().focus(e);
      }
    }
  }
  redrawHero();
  changed();
}

bool HomeRoute::resting() const { return movedMs_ < 0 || s_.ui.nowMs() - movedMs_ >= kRestMs; }

void HomeRoute::refresh() {
  for (auto& p : providers_) {
    // one already on its way (a load, a retry, the last refresh) answers soon enough
    if (!p.callId.empty() || p.held) continue;
    // a provider that cannot browse is never asked again; a hidden feed only on its slow re-check
    if (p.never || (p.state == State::Absent && s_.ui.nowMs() < p.recheckAtMs)) continue;
    const auto id = p.id;
    p.callId = call(rpc::kCatalogBrowse, Json{{"path", id}, {"limit", 30}},
                    {[this, id](const std::string& status, const Json& json) {
                       auto* q = provider(id);
                       if (q == nullptr) return;
                       q->callId.clear();
                       // under another route nothing is on screen: the model takes it, the page is built from it later
                       if (current() && !resting()) {
                         q->held = true;
                         q->heldStatus = status;
                         q->heldJson = json;
                         return;
                       }
                       refreshed(*q, status, json);
                     },
                     nullptr});
  }
}

void HomeRoute::refreshed(Provider& p, const std::string& status, const Json& json) {
  p.held = false;
  State state = p.state;
  std::vector<Section> sections;
  std::string sig;
  const auto outcome = outcomeOf(status, json);
  if (outcome == home_answer::Outcome::Rows) {
    sig = json.value("sections", Json::array()).dump();
    sections = parseSections(json);
    state = sections.empty() ? State::Absent : State::Ready;
  } else if (outcome != home_answer::Outcome::Error) {
    const bool shown = p.state != State::Absent;
    hide(p, outcome);
    // a feed that went: its rows go quietly
    if (shown) {
      redrawInPlace(p);
      if (heroSig() != heroSig_) redrawHero();
      changed();
    }
    return;
  } else {
    // a refresh that failed leaves what is shown (rows, or the strip with its retry) as it is
    return;
  }
  if (state == p.state && sig == p.sig) return;
  // what the viewer had: how far each row was drawn, and pages more: joined
  for (auto& sec : sections) {
    for (const auto& old : p.sections) {
      if (old.id != sec.id) continue;
      // the server's refs are kept in its memory: a first page sharing none with the last means they were issued
      // anew (its restart), so the later pages joined before carry refs it no longer knows and are left out
      const bool sameRefs = std::any_of(sec.items.begin(), sec.items.end(), [&](const catalog::Item& n) {
        return std::any_of(old.items.begin(), old.items.end(), [&](const catalog::Item& o) { return o.ref == n.ref; });
      });
      if (old.extended && sameRefs) {
        for (const auto& it : old.items) {
          const bool known = std::any_of(sec.items.begin(), sec.items.end(), [&](const catalog::Item& n) { return n.ref == it.ref; });
          if (!known) sec.items.push_back(it);
        }
        sec.next = old.next;
        sec.extended = true;
      }
      sec.shown = std::min(old.shown, sec.items.size());
    }
  }
  const bool moves = p.failures >= kStripAfterFailures;
  p.sections = std::move(sections);
  p.sig = std::move(sig);
  p.state = state;
  if (state == State::Ready) p.failures = 0;
  if (moves && p.failures < kStripAfterFailures && page() != nullptr) {
    // from the strips at the bottom back to its rows: the page is built again, focus given back by id
    std::string focused;
    if (auto* f = s_.ui.focus().current(); f != nullptr && page()->Contains(f)) focused = f->GetId();
    s_.ui.setPage([this] { return body(); }, bodyClasses());
    if (!focused.empty()) {
      afterLayout([this, focused] {
        if (auto* e = view::byId(page(), focused)) s_.ui.focus().focus(e);
      });
    }
  } else {
    redrawInPlace(p);
  }
  if (heroSig() != heroSig_) redrawHero();
  changed();
}

void HomeRoute::redrawInPlace(const Provider& p) {
  auto* doc = page();
  if (doc == nullptr) return;
  auto* host = view::byId(doc, "prov-" + p.id);
  if (host == nullptr) {
    redrawProvider(p);
    return;
  }
  auto* f = s_.ui.focus().current();
  const std::string focused = f != nullptr && doc->Contains(f) ? f->GetId() : std::string();
  bool inside = false;
  for (auto* e = f; e != nullptr && !inside; e = e->GetParentNode()) inside = e == host;
  // each row's scroll and remembered card, by its id
  struct Kept {
    std::string scroll;
    int last = -1;
  };
  std::map<std::string, Kept> rows;
  Rml::ElementList els;
  host->QuerySelectorAll(els, ".row");
  for (auto* row : els) {
    Kept k;
    k.last = row->GetAttribute<int>("data-last", -1);
    if (auto* track = row->QuerySelector(".track")) k.scroll = track->GetAttribute<Rml::String>("data-scroll", "");
    rows[row->GetId()] = k;
  }
  std::string focusRow;
  int focusIndex = 0;
  if (inside) {
    if (auto* row = ui::Focus::ancestor(f, "row")) {
      focusRow = row->GetId();
      Rml::ElementList cards;
      row->QuerySelectorAll(cards, ".card.has-ref");
      const auto it = std::find(cards.begin(), cards.end(), f);
      if (it != cards.end()) focusIndex = static_cast<int>(it - cards.begin());
    }
  }
  const bool wasEmpty = doc->QuerySelector(".home-empty") != nullptr;
  redrawProvider(p);
  const bool anyShown = std::any_of(providers_.begin(), providers_.end(), [](const Provider& q) { return q.state != State::Absent; });
  if (!anyShown && !wasEmpty) {
    // the last provider went: the empty state, focus on the hero
    s_.ui.setPage([this] { return body(); }, bodyClasses());
    return;
  }
  els.clear();
  host->QuerySelectorAll(els, ".row");
  for (auto* row : els) {
    const auto k = rows.find(row->GetId());
    if (k == rows.end()) continue;
    if (k->second.last >= 0) row->SetAttribute("data-last", k->second.last);
    auto* track = row->QuerySelector(".track");
    if (track != nullptr && !k->second.scroll.empty()) {
      track->SetAttribute("data-scroll", k->second.scroll);
      // a row drawn again keeps its place at once (no glide from 0)
      s_.ui.focus().glide(track, "translateX(-" + k->second.scroll + "px)", 0);
    }
  }
  // laid out now, so focus is placed again at once (before the focus manager's repair could move it): the same card by
  // id, which scrolls its row and the column for the new layout; one that went: its row's card at that place, else the
  // provider's first, else the hero
  doc->UpdateDocument();
  Rml::Element* e = focused.empty() ? nullptr : view::byId(doc, focused);
  if (e == nullptr && inside) {
    Rml::ElementList cards;
    if (auto* row = focusRow.empty() ? nullptr : view::byId(doc, focusRow)) row->QuerySelectorAll(cards, ".card.has-ref");
    if (!cards.empty()) e = cards[std::min<std::size_t>(cards.size() - 1, static_cast<std::size_t>(focusIndex))];
    if (e == nullptr) e = host->QuerySelector(".card.has-ref");
    if (e == nullptr) e = view::byId(doc, "hero-play");
  }
  // the rows above the focused one are marked again on the next update
  lastFocus_ = nullptr;
  ownFocus_ = true;
  if (e != nullptr) s_.ui.focus().focus(e);
}

std::string HomeRoute::heroSig() const {
  const auto* h = hero();
  if (h == nullptr) return {};
  return h->ref + "|" + h->title + "|" + h->artwork + "|" + std::to_string(h->positionS) + "/" + std::to_string(h->durationS);
}

const catalog::Item* HomeRoute::hero() const {
  // the item to resume, or else the newest library addition (the first row's first)
  for (const auto& p : providers_) {
    for (const auto& s : p.sections) {
      if (s.cont && !s.items.empty()) return &s.items.front();
    }
  }
  for (const auto& p : providers_) {
    for (const auto& s : p.sections) {
      if (!s.items.empty()) return &s.items.front();
    }
  }
  return nullptr;
}

const catalog::Item* HomeRoute::itemOf(const std::string& ref) const {
  for (const auto& p : providers_) {
    for (const auto& s : p.sections) {
      for (const auto& it : s.items) {
        if (it.ref == ref) return &it;
      }
    }
  }
  return nullptr;
}

std::string HomeRoute::sectionRml(const Provider& p, Section& s) {
  Row r;
  r.id = catalog::idOf("hrow", p.id + "/" + s.id);
  r.title = s.title.empty() ? defaultTitle(p.id) : esc(s.title);
  if (s.cont) r.title = "[[home.continue]]";
  r.badge = catalog::providerBadge(p.id);
  // each card's shape is its own item's (the artwork's aspect: an anime's 2:3 cover, a video's 16:9 still), not the
  // row's; the row's kind is its first item's, for its "all" tile
  r.kind = s.items.empty() ? CardKind::Poster : catalog::cardKindOf(s.items.front());
  s.shown = std::max(s.shown, std::min(s.items.size(), kFirstCards));
  for (std::size_t i = 0; i < s.shown; ++i) {
    const auto& it = s.items[i];
    auto c = catalog::cardOf(it, catalog::idOf(r.id, it.ref), "open:" + it.ref);
    if (s.cont && it.hasProgress && it.durationS > 0) {
      c.progress = static_cast<double>(it.positionS) / it.durationS;
    } else if (s.cont && it.watchedTotal > 0 && it.watchedCount > 0) {
      // a series known by episodes watched alone (torrplayer's): how far through it is
      c.progress = static_cast<double>(it.watchedCount) / it.watchedTotal;
    }
    r.cards.push_back(std::move(c));
  }
  if (!s.next.empty() || s.shown < s.items.size()) r.allAction = "more:" + r.id;
  return row(r);
}

std::string HomeRoute::providerRml(const Provider& p) {
  Row r;
  r.id = "prov-" + p.id + "-row";
  r.title = defaultTitle(p.id);
  r.badge = catalog::providerBadge(p.id);
  // the skeleton's shape: the cards most of the provider's rows will have (torrplayer's covers are 2:3, as its own
  // feed draws them)
  r.kind = p.id == "torrplayer" || p.id == "torrserver" ? CardKind::Poster : CardKind::Wide;
  switch (p.state) {
    case State::Loading:
      r.state = RowState::Loading;
      return row(r);
    case State::Slow:
      r.state = RowState::Slow;
      return row(r);
    case State::Down: {
      // one feed of many failed: its row says what failed in one line, the others go on
      ErrorCard e;
      e.compact = true;
      failure::describe(e, p.failure, p.id);
      e.actions = {Button{"[[common.retry]]", "refresh", "retry:" + r.id, false, p.retrying, false, "retry-" + p.id, {}}};
      return "<div class=\"row down\" id=\"" + r.id + "\"><div class=\"row-header\"><div class=\"name\">" + r.title +
             "</div>" + r.badge + "</div><div class=\"track\">" + errorCard(e) + "</div><div class=\"cline\"/></div>";
    }
    case State::Absent:
      return {};
    case State::Ready: {
      std::string out;
      for (auto& s : const_cast<Provider&>(p).sections) out += sectionRml(p, s);
      return out;
    }
  }
  return {};
}

std::string HomeRoute::body() {
  const auto* h = hero();
  heroRef_ = h != nullptr ? h->ref : "";
  // the backdrop under everything: two art layers that cross-fade, the scrims over them
  std::string s = "<div class=\"home-bg\" id=\"home-bg\"><div class=\"bgl\" id=\"bg-0\"/><div class=\"bgl\" id=\"bg-1\"/>"
                  "<div class=\"bgscrim-x\"/><div class=\"bgscrim-y\"/></div>";
  s += "<div class=\"safe home\"><div class=\"topfade\"/><div class=\"bottomfade\"/>";
  s += "<div class=\"topbar\"><div class=\"wordmark\">Cadence</div><div class=\"home-now\" id=\"home-now\">" +
       home_now::chip(s_.router.background()) + "</div><div class=\"grow\"/>";
  // what plays on the household's other devices: the quick panel's way in
  s += "<div id=\"home-devices-ind\" class=\"home-ind\">" + devicesIndicator() + "</div>";
  s += button(Button{"[[search.title]]", "search", "search", false, false, false, "home-search", {}});
  s += button(Button{"[[assistant.title]]", "mic", "assistant", false, false, false, "home-mic", {}});
  if (s_.router.offline()) {
    s += "<div class=\"badge\">" + icon("offline") + "<span>[[offline.no_server]]</span></div>";
  }
  s += clockTile() + "</div>";
  s += "<div id=\"home-preview\" class=\"preview\"/>";
  // the column scrolls by home's own rule (placeRows), not the focus manager's
  s += "<div class=\"vscroll manual\" id=\"home-col\"><div id=\"home-hero\"></div>";
  // what plays in this room on other devices, between the hero and the feed
  s += "<div id=\"home-nearby\">" + home_nearby::strip(nearbyItems()) + "</div>";
  // the next alarm and the running timers, under it
  s += "<div id=\"home-alarms\">" + alarmsStrip() + "</div><div class=\"hero-gap\"/>";
  if (const auto* d = devicesPart()) nearbyRev_ = d->revision();
  if (const auto* a = alarmsPart()) alarmsRev_ = a->revision();
  bool any = false;
  for (const auto& p : providers_) {
    if (p.failures >= kStripAfterFailures) continue;
    s += "<div id=\"prov-" + p.id + "\">" + providerRml(p) + "</div>";
    any = any || p.state != State::Absent;
  }
  s += "<div id=\"home-down\">";
  for (const auto& p : providers_) {
    if (p.failures >= kStripAfterFailures) s += providerRml(p);
  }
  s += "</div>";
  if (!any) {
    s += "<div class=\"home-empty\">" +
         stateBlock("tv", "[[ui.empty]]", "[[home.no_sources]]", std::nullopt) + "</div>";
  }
  return s + "</div></div>";
}

void HomeRoute::loaded(Rml::ElementDocument& doc, bool restoring) {
  lastFocus_ = nullptr;
  inRows_ = false;
  previewRef_.clear();
  bgArt_.clear();
  bgTop_ = 0;
  bgPending_ = -1;
  bgSwapMs_ = -1;
  // back on top: what changed meanwhile (a progress just made, a source added) is asked for at once
  if (restoring) refreshDue_ = true;
  const auto play = s_.router.background();
  playSig_ = playSignature(play);
  heroHasPlay_ = play.has_value() && !play->audio;
  doc.SetClass("over-video", heroHasPlay_);
  redrawHeroNow();
  // background playback owns the way back: audio starts on its chip, video on the hero's «Продолжить»
  if (play && play->audio) {
    if (auto* p = doc.GetElementById("home-now-playing")) s_.ui.focus().focus(p);
  } else if (!restoring || play) {
    if (auto* p = doc.GetElementById("hero-play")) s_.ui.focus().focus(p);
  }
  const auto* h = hero();
  backdrop(play ? (play->audio ? play->art : std::string()) : h == nullptr ? std::string() : catalog::artOf(*h), true);
}

void HomeRoute::redrawHeroNow() {
  // the hero is drawn again even under focus (a play that came or went changes what it offers)
  heroRef_.clear();
  auto* doc = page();
  if (auto* host = view::byId(doc, "home-hero")) {
    auto* f = s_.ui.focus().current();
    const bool focused = f != nullptr && ui::Focus::ancestor(f, "hero") != nullptr;
    host->SetInnerRML("");
    redrawHero();
    if (focused) {
      if (auto* p = view::byId(doc, "hero-play")) s_.ui.focus().focus(p);
    }
  }
}

void HomeRoute::followPlay() {
  const auto play = s_.router.background();
  const std::string sig = playSignature(play);
  if (sig == playSig_) return;
  playSig_ = sig;
  auto* doc = page();
  if (doc == nullptr) return;
  // the page lets the picture through only for a film; audio keeps its cover and a compact top-bar entry
  const bool film = play.has_value() && !play->audio;
  doc->SetClass("over-video", film);
  redrawNowPlaying();
  // a pause or a new song redraws the chip alone: the ordinary hero and its focus stay put
  if (film || heroHasPlay_) redrawHeroNow();
  heroHasPlay_ = film;
  const auto* h = hero();
  backdrop(play ? (play->audio ? play->art : std::string()) : h == nullptr ? std::string() : catalog::artOf(*h));
}

std::vector<nearby::Item> HomeRoute::nearbyItems() const {
  const auto* d = devicesPart();
  return d != nullptr ? nearby::sameRoom(d->items()) : std::vector<nearby::Item>{};
}

std::string HomeRoute::devicesIndicator() const {
  const auto* d = devicesPart();
  return d != nullptr ? d->indicator() : std::string();
}

void HomeRoute::followNearby() {
  const auto* d = devicesPart();
  const std::uint64_t rev = d != nullptr ? d->revision() : 0;
  if (rev == nearbyRev_) return;
  nearbyRev_ = rev;
  auto* doc = page();
  if (doc == nullptr) return;
  // focus on a strip item stays on its device's item, else the strip's first, else the hero's button; on the
  // indicator it stays while there is one
  auto* f = s_.ui.focus().current();
  std::string device;
  bool inStrip = false;
  for (auto* p = f; p != nullptr; p = p->GetParentNode()) {
    if (p->IsClassSet("nbi")) device = p->GetAttribute<Rml::String>("data-device", "");
    if (p->GetId() == "home-nearby") inStrip = true;
  }
  const bool onIndicator = f != nullptr && f->GetId() == "devices-ind";
  view::fill(view::byId(doc, "home-devices-ind"), devicesIndicator());
  view::fill(view::byId(doc, "home-nearby"), home_nearby::strip(nearbyItems()));
  if (onIndicator) {
    if (view::byId(doc, "devices-ind") != nullptr) {
      focusLater("devices-ind");
    } else {
      focusLater("hero-play");
    }
  }
  if (!inStrip) return;
  afterLayout([this, device] {
    auto* host = view::byId(page(), "home-nearby");
    Rml::ElementList items;
    if (host != nullptr) host->QuerySelectorAll(items, ".nbi");
    for (auto* e : items) {
      if (e->GetAttribute<Rml::String>("data-device", "") == device) {
        s_.ui.focus().focus(e);
        return;
      }
    }
    if (!items.empty()) {
      s_.ui.focus().focus(items.front());
    } else if (auto* play = view::byId(page(), "hero-play")) {
      s_.ui.focus().focus(play);
    }
  });
}

std::string HomeRoute::alarmsStrip() const {
  const auto* a = alarmsPart();
  return a != nullptr ? home_alarms::strip(a->summary(), a->epochMs(), s_.ui.translator()) : std::string();
}

void HomeRoute::followAlarms() {
  const auto* a = alarmsPart();
  auto* doc = page();
  if (a == nullptr || doc == nullptr) return;
  auto* host = view::byId(doc, "home-alarms");
  if (a->revision() != alarmsRev_) {
    alarmsRev_ = a->revision();
    // focus on an alarm chip stays on the strip: its first chip, else the hero's button
    auto* f = s_.ui.focus().current();
    bool inStrip = false;
    for (auto* p = f; p != nullptr; p = p->GetParentNode()) inStrip = inStrip || p == host;
    view::fill(host, alarmsStrip());
    alarmsSecond_ = -1;
    if (inStrip) focusLater(view::byId(doc, "home-alarm-0") != nullptr ? "home-alarm-0" : "hero-play");
  }
  const auto now = a->epochMs();
  if (now / 1000 == alarmsSecond_) return;
  alarmsSecond_ = now / 1000;
  alarms_view::tickCountdowns(host, now);
}

void HomeRoute::redrawNowPlaying() {
  auto* host = view::byId(page(), "home-now");
  if (host == nullptr) return;
  auto* focused = s_.ui.focus().current();
  const bool keepFocus = focused != nullptr && focused->GetId() == "home-now-playing";
  view::fill(host, home_now::chip(s_.router.background()));
  if (keepFocus) focusLater("home-now-playing");
}

void HomeRoute::backdrop(const std::string& art, bool instant) {
  auto* doc = page();
  if (doc == nullptr) return;
  if (!instant) {
    // an item without art keeps the picture there is (a cross-fade only from a ready image to a ready one)
    if (art.empty() || art == bgArt_) {
      bgWant_.clear();
      return;
    }
    // a cross-fade running: the layer under it stays whole; this one waits for its end
    if (bgSwapMs_ >= 0) {
      bgWant_ = art;
      return;
    }
  }
  bgWant_.clear();
  bgArt_ = art;
  const int next = instant ? bgTop_ : 1 - bgTop_;
  auto* layer = view::byId(doc, "bg-" + std::to_string(next));
  if (layer == nullptr) return;
  std::string rml;
  if (art.starts_with("img:")) {
    rml = backdrop::rml(art.substr(4), "bgart");
  } else if (!art.empty()) {
    rml = "<div class=\"bgart\" style=\"decorator: image(&quot;" + esc(art) + "&quot; cover);\"/>";
  }
  layer->SetClass("on", false);
  layer->SetClass("top", false);
  layer->SetClass("now", instant);
  // only a layer with no picture fills with ink (a picture covers it whole: no fill drawn under it)
  layer->SetClass("empty", rml.empty());
  view::fill(layer, rml);
  if (instant) {
    if (auto* other = view::byId(doc, "bg-" + std::to_string(1 - next))) {
      other->SetClass("on", false);
      other->SetClass("top", false);
      view::fill(other, "");
    }
    layer->SetClass("on", true);
    layer->SetClass("top", true);
    bgPending_ = -1;
    bgSwapMs_ = -1;
    return;
  }
  bgPending_ = next;
}

void HomeRoute::stepBackdrop(std::int64_t nowMs) {
  auto* doc = page();
  if (doc == nullptr) return;
  if (bgPending_ >= 0) {
    auto* layer = view::byId(doc, "bg-" + std::to_string(bgPending_));
    auto* art = layer != nullptr ? layer->QuerySelector(".bgart") : nullptr;
    // its image in place (or none to wait for); one that failed leaves the picture there is
    const bool failed = art != nullptr && art->HasAttribute("data-img-failed");
    const bool ready = !failed && (art == nullptr || !art->IsClassSet("netimg") || art->IsClassSet("bound"));
    if (failed) {
      bgPending_ = -1;
      bgArt_.clear();
      if (layer != nullptr) view::fill(layer, "");
    } else if (layer != nullptr && ready) {
      // the new layer fades in on top of the old, which stays whole under it until covered (no dip to ink midway)
      if (auto* old = view::byId(doc, "bg-" + std::to_string(bgTop_))) old->SetClass("top", false);
      layer->SetClass("top", true);
      layer->SetClass("on", true);
      bgTop_ = bgPending_;
      bgPending_ = -1;
      bgSwapMs_ = nowMs;
      // a frame when the fade has covered the old layer (nothing else may wake the loop then)
      s_.ui.after(ui::tokens::kMotionBackdropMs + 60, [] {});
    }
  }
  if (bgSwapMs_ >= 0 && nowMs - bgSwapMs_ >= ui::tokens::kMotionBackdropMs + 50) {
    bgSwapMs_ = -1;
    // covered now: the old layer lets its image go
    if (auto* old = view::byId(doc, "bg-" + std::to_string(1 - bgTop_)); old != nullptr && bgPending_ < 0) {
      old->SetClass("on", false);
      old->SetClass("now", false);
      view::fill(old, "");
    }
    // the art focus asked for meanwhile
    // (a copy: backdrop() clears what it was asked for)
    if (const std::string want = bgWant_; !want.empty()) backdrop(want);
  }
}

void HomeRoute::placeRows(Rml::Element* row) {
  auto* col = view::byId(page(), "home-col");
  if (col == nullptr) return;
  Rml::ElementList all;
  col->QuerySelectorAll(all, ".row");
  std::ptrdiff_t focused = -1;
  for (std::size_t i = 0; i < all.size(); ++i) {
    if (all[i] == row) focused = static_cast<std::ptrdiff_t>(i);
  }
  for (std::size_t i = 0; i < all.size(); ++i) all[i]->SetClass("above", feed::hiddenAbove(static_cast<std::ptrdiff_t>(i), focused));
  float scroll = 0;
  if (row != nullptr) scroll = feed::scrollFor(row->GetAbsoluteOffset(Rml::BoxArea::Border).y, kFeedAnchorDp * s_.ui.dp());
  char buf[64];
  std::snprintf(buf, sizeof buf, "translateY(%.1fpx)", static_cast<double>(-scroll));
  // only a change glides (placeRows runs on every focus move; the same target would restart the ease)
  if (col->GetAttribute<Rml::String>("data-target", "") == buf) return;
  col->SetAttribute("data-target", Rml::String(buf));
  col->SetAttribute("data-scroll", scroll);
  s_.ui.focus().glide(col, buf, s_.ui.focus().columnGlideMs());
}

void HomeRoute::redrawHero() {
  auto* doc = page();
  auto* host = view::byId(doc, "home-hero");
  if (host == nullptr) return;
  const auto* h = hero();
  const auto play = s_.router.background();
  const bool film = play.has_value() && !play->audio;
  // a hero holding focus is not swapped under the viewer
  auto* f = s_.ui.focus().current();
  const bool focused = f != nullptr && ui::Focus::ancestor(f, "hero") != nullptr;
  const std::string ref = film ? "play:" + play->itemRef : h != nullptr ? h->ref : "";
  // (the placeholder hero gives way as soon as an item arrives)
  if (focused && host->GetNumChildren() > 0 && !heroRef_.empty()) return;
  heroRef_ = ref;
  heroSig_ = heroSig();
  std::string rml;
  if (film) {
    // the film behind the page: «Продолжить» brings it back to the front; audio uses the top-bar chip instead
    std::vector<Button> buttons{Button{"[[home.continue]]", "play", "hero:resume", true, false, false, "hero-play", "default-focus"}};
    // a video from a plain link has no catalog item to open
    if (!play->itemRef.empty()) buttons.push_back(Button{"[[common.more]]", "info", "hero:more", false, false, false, "hero-more", {}});
    rml = ui::rml::hero(play->title, "", "", buttons);
    rml.insert(rml.find("<div class=\"body\">") + std::string("<div class=\"body\">").size(),
               "<div class=\"hbadge\">" + badge(BadgeKind::Live, "[[home.now_playing]]") + "</div>");
  } else if (h == nullptr) {
    rml = ui::rml::hero("Cadence", "", "", {Button{"[[search.title]]", "search", "search", true, false, false, "hero-play", "default-focus"}});
  } else {
    std::string meta = catalog::metaOf(*h);
    // the facts the server cached for it: its rank and the owner's own list status
    if (const auto facts = catalog::factsOf(*h); !facts.empty()) meta += (meta.empty() ? "" : " · ") + facts;
    if (h->hasProgress && h->durationS > h->positionS && h->positionS > 0) {
      const auto left = catalog::durationText(s_.ui.translator().language(), h->durationS - h->positionS);
      meta += (meta.empty() ? "" : " · ") + std::string("[[episodes.time_left|left=") + catalog::argOf(left) + "]]";
    }
    rml = ui::rml::hero(esc(!h->nameRu.empty() ? h->nameRu : h->title), meta, "",
               {Button{"[[home.continue]]", "play", "hero:play", true, false, false, "hero-play", "default-focus"},
                Button{"[[common.more]]", "info", "hero:more", false, false, false, "hero-more", {}}});
    rml.insert(rml.find("<div class=\"body\">") + std::string("<div class=\"body\">").size(),
               "<div class=\"hbadge\">" + catalog::providerBadge(h->provider) + "</div>");
  }
  if (inRows_) rml.replace(rml.find("class=\"hero\""), 12, "class=\"hero collapsed\"");
  const bool hadFocus = focused;
  view::fill(host, rml);
  if (hadFocus || f == nullptr) {
    if (auto* p = view::byId(doc, "hero-play")) s_.ui.focus().focus(p);
  }
}

void HomeRoute::redrawProvider(const Provider& p) {
  auto* doc = page();
  // under another route: its page is built from the model when it is back on top
  if (doc == nullptr) return;
  auto* host = view::byId(doc, p.failures >= kStripAfterFailures ? "home-down" : "prov-" + p.id);
  if (host == nullptr) {
    // it moved between the rows and the strips at the bottom: the page is built again
    s_.ui.setPage([this] { return body(); }, bodyClasses());
    return;
  }
  if (p.failures >= kStripAfterFailures) {
    std::string strips;
    for (const auto& q : providers_) {
      if (q.failures >= kStripAfterFailures) strips += providerRml(q);
    }
    if (auto* row = view::byId(doc, "prov-" + p.id)) row->GetParentNode()->RemoveChild(row);
    view::fill(host, strips);
    return;
  }
  view::fill(host, providerRml(p));
  if (auto* empty = doc->QuerySelector(".home-empty"); empty != nullptr && p.state != State::Absent) {
    empty->GetParentNode()->RemoveChild(empty);
  }
}

void HomeRoute::more(const std::string& rowId) {
  for (auto& p : providers_) {
    for (auto& s : p.sections) {
      if (catalog::idOf("hrow", p.id + "/" + s.id) != rowId) continue;
      auto* rowEl = view::byId(page(), rowId);
      auto* track = rowEl != nullptr ? rowEl->QuerySelector(".track") : nullptr;
      if (track == nullptr) return;
      if (s.shown < s.items.size()) {
        grow(nullptr);
        auto* all = track->QuerySelector(".card.all");
        const auto from = s.shown;
        s.shown = std::min(s.items.size(), s.shown + kGrowBy * 2);
        std::string rml;
        for (auto i = from; i < s.shown; ++i) {
          auto c = catalog::cardOf(s.items[i], catalog::idOf(rowId, s.items[i].ref), "open:" + s.items[i].ref);
          rml += card(c);
        }
        auto* first = view::append(track, rml, all);
        if (first != nullptr) s_.ui.focus().focus(first);
        return;
      }
      if (s.next.empty()) return;
      // the section's next page joins the row's end
      const auto path = !s.path.empty() ? s.path : p.id;
      const auto pid = p.id;
      const auto sid = s.id;
      call(rpc::kCatalogBrowse, Json{{"path", path}, {"cursor", s.next}, {"limit", 30}},
           {[this, pid, sid, rowId](const std::string& status, const Json& json) {
              if (status != rpc::kStatusOK) {
                s_.ui.toast({"home-more", ui::ToastTone::Warning, "[[common.load_failed]]", {}, {}, {}, 0});
                return;
              }
              auto* pp = provider(pid);
              if (pp == nullptr) return;
              for (auto& sec : pp->sections) {
                if (sec.id != sid) continue;
                const auto& secs = json.value("sections", Json::array());
                sec.next = json.value("next", "");
                sec.extended = true;
                for (const auto& js : secs) {
                  for (auto& it : catalog::parseItems(js.value("items", Json::array()))) sec.items.push_back(std::move(it));
                  if (sec.next.empty()) sec.next = js.value("next", "");
                }
                more(rowId);
              }
            },
            nullptr});
      return;
    }
  }
}

void HomeRoute::grow(Rml::Element* focused) {
  // section 11's virtualisation: a row holds the visible cards and a few to the right; more join as focus nears
  // the last of them (appended at the end, so nothing under focus moves)
  if (focused == nullptr) return;
  auto* rowEl = ui::Focus::ancestor(focused, "row");
  if (rowEl == nullptr) return;
  for (auto& p : providers_) {
    for (auto& s : p.sections) {
      const auto rowId = catalog::idOf("hrow", p.id + "/" + s.id);
      if (rowEl->GetId() != rowId || s.shown >= s.items.size()) continue;
      Rml::ElementList cards;
      rowEl->QuerySelectorAll(cards, ".card.has-ref");
      const auto it = std::find(cards.begin(), cards.end(), focused);
      if (it == cards.end() || static_cast<std::size_t>(it - cards.begin()) + 3 < s.shown) return;
      auto* track = rowEl->QuerySelector(".track");
      const auto from = s.shown;
      s.shown = std::min(s.items.size(), s.shown + kGrowBy);
      std::string rml;
      for (auto i = from; i < s.shown; ++i) {
        auto c = catalog::cardOf(s.items[i], catalog::idOf(rowId, s.items[i].ref), "open:" + s.items[i].ref);
        rml += card(c);
      }
      view::append(track, rml, track->QuerySelector(".card.all"));
      if (s.shown >= s.items.size() && s.next.empty()) {
        if (auto* all = track->QuerySelector(".card.all")) track->RemoveChild(all);
      }
      return;
    }
  }
}

void HomeRoute::preview(const catalog::Item* item) {
  auto* host = view::byId(page(), "home-preview");
  if (host == nullptr) return;
  if (item == nullptr) {
    host->SetClass("shown", false);
    previewRef_.clear();
    return;
  }
  if (item->ref == previewRef_) {
    host->SetClass("shown", true);
    return;
  }
  previewRef_ = item->ref;
  // the name at once; the details (meta, genres, a few lines of description) when catalog.item answered, faded in
  // then (detailRml), at once when they are known already. the page's backdrop shows the item's art (backdrop())
  const auto d = details_.find(item->ref);
  const std::string detail = d != details_.end() ? detailRml(d->second) : std::string();
  std::string rml = "<div class=\"hptext\"><div class=\"hptitle\">" +
                    esc(!item->nameRu.empty() ? item->nameRu : item->title) + "</div>";
  if (detail.empty()) rml += "<div class=\"hpmeta\">" + catalog::metaOf(*item) + "</div>";
  rml += "<div class=\"hpdetail" + std::string(detail.empty() ? "" : " in now") + "\" id=\"hp-detail\">" + detail +
         "</div></div>";
  view::fill(host, rml);
  host->SetClass("shown", true);
}

std::string HomeRoute::detailRml(const catalog::Item& it) const {
  const auto lang = s_.ui.translator().language();
  std::string meta;
  const auto add = [&](const std::string& part) {
    if (part.empty()) return;
    if (!meta.empty()) meta += " · ";
    meta += part;
  };
  if (it.year > 0) add(std::to_string(it.year));
  add(esc(it.format));
  if (it.episodesTotal > 0 || it.episodesAired > 0) {
    const int aired = it.episodesAired > 0 ? it.episodesAired : it.episodesTotal;
    const int total = it.episodesTotal > 0 ? it.episodesTotal : it.episodesAired;
    add("[[title.of_episodes|aired=" + std::to_string(aired) + "|total=" + std::to_string(total) + "]]");
  }
  if (it.score > 0) add("[[title.score|score=" + i18n::decimal(lang, it.score) + "]]");
  add(esc(it.ageRating));
  if (meta.empty()) meta = catalog::metaOf(it);
  std::string s = "<div class=\"hpmeta\">" + meta + "</div>";
  if (!it.genres.empty()) {
    std::string g;
    for (const auto& x : it.genres) g += (g.empty() ? "" : ", ") + esc(x);
    s += "<div class=\"hpgenres\">" + g + "</div>";
  }
  if (!it.description.empty()) s += "<div class=\"hpdesc\">" + esc(it.description) + "</div>";
  return s;
}

void HomeRoute::loadDetail(const std::string& ref) {
  if (ref.empty() || details_.count(ref) != 0 || ref == detailRef_ || noDetail_.count(ref) != 0) return;
  // the one asked for before is stale now: only the rested card's details are wanted
  cancel(detailCall_);
  detailRef_ = ref;
  detailCall_ = call(rpc::kCatalogItem, Json{{"ref", ref}},
                     {[this, ref](const std::string& status, const Json& json) {
                        detailCall_.clear();
                        if (detailRef_ == ref) detailRef_.clear();
                        if (status != rpc::kStatusOK) {
                          // asked once: a ref without details is not asked again each time focus rests on it
                          if (noDetail_.size() >= 256) noDetail_.clear();
                          noDetail_.insert(ref);
                          // a ref the server forgot (its restart): the feeds are asked again now, so every card
                          // takes the ref it issues today
                          if (status == rpc::kStatusExpired) refreshDue_ = true;
                          return;
                        }
                        if (details_.size() >= 64) details_.clear();
                        details_[ref] = catalog::parseDetail(json).item;
                        if (previewRef_ != ref) return;
                        auto* host = view::byId(page(), "hp-detail");
                        if (host == nullptr) return;
                        // the meta line the card had gives way to the detail's, which fades in
                        if (auto* p = view::byId(page(), "home-preview")) {
                          if (auto* meta = p->QuerySelector(".hptext > .hpmeta")) meta->GetParentNode()->RemoveChild(meta);
                        }
                        view::fill(host, detailRml(details_[ref]));
                        afterLayout([this, ref] {
                          if (previewRef_ != ref) return;
                          if (auto* h = view::byId(page(), "hp-detail")) h->SetClass("in", true);
                        });
                      },
                      nullptr});
}

void HomeRoute::settle() {
  const auto* it = itemOf(restRef_);
  if (it == nullptr) return;
  // the backdrop follows the rested card (a film behind the page keeps the picture)
  if (playSig_.empty()) backdrop(catalog::artOf(*it));
  loadDetail(it->ref);
}

void HomeRoute::update(std::int64_t nowMs) {
  nowMs_ = nowMs;
  followPlay();
  followNearby();
  followAlarms();
  stepBackdrop(nowMs);
  if (nextRefreshMs_ >= 0 && (refreshDue_ || nowMs >= nextRefreshMs_) && resting()) {
    refreshDue_ = false;
    nextRefreshMs_ = nowMs + refreshMs_;
    refresh();
  }
  if (resting()) {
    for (auto& p : providers_) {
      if (p.held) refreshed(p, p.heldStatus, p.heldJson);
    }
  }
  for (auto& p : providers_) {
    if (p.state == State::Loading && nowMs - p.askedMs >= kSlowMs) {
      p.state = State::Slow;
      redrawProvider(p);
    }
  }
  auto* f = s_.ui.focus().current();
  if (f == lastFocus_) {
    // the backdrop and the details wait for focus to rest, so a fast walk loads nothing it passes
    if (inRows_ && restSinceMs_ >= 0 && nowMs - restSinceMs_ >= kPreviewRestMs) {
      restSinceMs_ = -1;
      settle();
    }
    return;
  }
  auto* was = lastFocus_;
  lastFocus_ = f;
  if (ownFocus_) {
    ownFocus_ = false;
  } else {
    movedMs_ = s_.ui.nowMs();
  }
  if (f == nullptr) return;
  auto* row = ui::Focus::ancestor(f, "row");
  const bool rows = row != nullptr;
  // ↑ from the first row lands on «Продолжить», whichever hero button sits nearest
  if (!rows && was != nullptr && inRows_ && f->GetId() == "hero-more") {
    if (auto* play = view::byId(page(), "hero-play")) {
      s_.ui.focus().focus(play);
      lastFocus_ = play;
      f = play;
    }
  }
  if (rows != inRows_) {
    inRows_ = rows;
    if (auto* h = page() != nullptr ? page()->QuerySelector(".hero") : nullptr) h->SetClass("collapsed", rows);
    if (auto* home = page() != nullptr ? page()->QuerySelector(".home") : nullptr) home->SetClass("rows", rows);
  }
  placeRows(row);
  // the hero's one rule (hero.h): a card's item while a card holds focus, else the film behind the page, else the item
  // the hero's buttons act on
  const auto play = s_.router.background();
  const auto* def = hero();
  const auto show = hero::showFor(rows ? f->GetAttribute<Rml::String>("data-ref", "") : std::string(),
                                  // a cast has no item: its session stands for it
                                  play ? (play->itemRef.empty() ? "cast:" + play->sessionId : play->itemRef) : std::string(),
                                  def != nullptr ? def->ref : std::string());
  if (show.source == hero::Source::Card) {
    // the name at once, wherever the row; the backdrop and details once focus rests
    preview(itemOf(show.ref));
    restRef_ = show.ref;
    restSinceMs_ = nowMs;
    // a frame when focus has rested (the loop sleeps when nothing animates)
    s_.ui.after(kPreviewRestMs + 10, [] {});
    grow(f);
  } else {
    preview(nullptr);
    restSinceMs_ = -1;
    if (show.source == hero::Source::Default) backdrop(catalog::artOf(*def));
  }
}

void HomeRoute::open(const catalog::Item& item) {
  RouteArgs args;
  args.ref = item.ref;
  if (opensTitle(item)) {
    s_.router.open(cadence_v2_ScreenRoute_SCREEN_ROUTE_TITLE, args);
    return;
  }
  args.data = Json{{"from", "resume"},
                   {"title", !item.nameRu.empty() ? item.nameRu : item.title},
                   {"art", item.artwork},
                   {"kind", item.kind},
                   {"provider", item.provider}};
  s_.router.open(cadence_v2_ScreenRoute_SCREEN_ROUTE_RESOLVING, args);
}

bool HomeRoute::activate(const ui::Activation& a) {
  const auto& act = a.action;
  if (act == "search") return s_.router.open(cadence_v2_ScreenRoute_SCREEN_ROUTE_SEARCH);
  if (act == "assistant") {
    call(rpc::kAssistantInvoke, Json::object(), {});
    return true;
  }
  if (act == "hero:resume") {
    s_.router.foreground();
    return true;
  }
  if (act == "hero:more" && !playSig_.empty()) {
    if (const auto play = s_.router.background()) {
      RouteArgs args;
      args.ref = play->itemRef;
      s_.router.open(cadence_v2_ScreenRoute_SCREEN_ROUTE_TITLE, args);
    }
    return true;
  }
  if (act == "hero:play" || act == "hero:more") {
    if (const auto* h = hero()) {
      if (act == "hero:play" && !opensTitle(*h)) {
        open(*h);
      } else {
        RouteArgs args;
        args.ref = h->ref;
        s_.router.open(cadence_v2_ScreenRoute_SCREEN_ROUTE_TITLE, args);
      }
    }
    return true;
  }
  if (act.starts_with("open:")) {
    if (const auto* it = itemOf(act.substr(5))) open(*it);
    return true;
  }
  if (act.starts_with("more:")) {
    more(act.substr(5));
    return true;
  }
  if (act.starts_with("retry:prov-")) {
    const auto rest = act.substr(11);
    if (auto* p = provider(rest.substr(0, rest.find("-row")))) retry(*p);
    return true;
  }
  return false;
}

bool HomeRoute::back() {
  // a film playing on behind the page: BACK brings it back to the front
  if (!playSig_.empty() && s_.router.foreground()) return true;
  // home is the root: BACK in the rows goes to the hero, on the hero it does nothing
  if (inRows_) {
    if (auto* play = view::byId(page(), "hero-play")) s_.ui.focus().focus(play);
  }
  return true;
}

}  // namespace cadence::screen::routes
