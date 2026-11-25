#include "app/routes/search/search.h"

#include "app/routes/core/failure-card.h"
#include "app/routes/core/methods.h"
#include "app/routes/core/view.h"
#include "ui/core/focus.h"

#include <RmlUi/Core/ElementDocument.h>

#include <algorithm>

namespace cadence::screen::routes {

using namespace ui::rml;

namespace {

constexpr std::int64_t kTypeDebounceMs = 350;
// results start at the second letter
constexpr std::size_t kMinQuery = 2;
const char* const kKinds[] = {"anime", "films", "youtube", "music"};

int kindRank(const std::string& k) {
  for (int i = 0; i < 4; ++i) {
    if (k == kKinds[i]) return i;
  }
  return 4;
}

CardKind cardKindOfRow(const std::string& kind) {
  if (kind == "anime") return CardKind::PosterJp;
  if (kind == "films") return CardKind::Poster;
  if (kind == "youtube") return CardKind::Wide;
  return CardKind::Square;
}

bool opensTitle(const catalog::Item& it) {
  return it.kind == "title_group" || it.kind == "series" || it.kind == "season" || it.kind == "album" ||
         it.kind == "playlist" || it.kind == "artist" || it.kind == "channel" || it.kind == "torrent" ||
         it.kind == "folder" || it.hasChildren || !it.playable;
}

}  // namespace

SearchRoute::SearchRoute(Services& s, const RouteArgs& args) : Route(s), query_(args.query) {
  if (!query_.empty() && !view::cyrillic(query_)) layout_ = KeyLayout::Latin;
}

std::string SearchRoute::rowKindOf(const catalog::Item& it) {
  if (it.provider == "torrplayer") return "anime";
  if (it.provider == "torrserver") return "films";
  if (it.kind == "track" || it.kind == "album" || it.kind == "artist" || it.kind == "playlist") return "music";
  if (it.provider == "spotify") return "music";
  if (it.provider == "ytmusic") return "youtube";
  return "films";
}

void SearchRoute::start() {
  if (view::codepoints(query_) >= kMinQuery) search();
}

std::string SearchRoute::sourceName(const catalog::Item& it) const {
  for (const auto& s : sources_) {
    if (s.provider == it.provider && s.source == it.source) return s.name;
  }
  const auto known = s_.sourceNames.find(it.provider + "/" + it.source);
  return known != s_.sourceNames.end() ? known->second : it.source;
}

std::string SearchRoute::cardRml(const catalog::Item& it) const {
  auto c = catalog::cardOf(it, catalog::idOf("s", it.ref), "open:" + it.ref, sourceName(it));
  c.kind = cardKindOfRow(rowKindOf(it));
  c.unavailable = it.unavailable;
  auto out = card(c);
  if (it.unavailable) {
    // «нет на источниках» says why: B's card adds «Сейчас недоступно» under an unavailable card, the meta2 line
    // already explains it, so the generic line goes
    const std::string generic = "<div class=\"meta\">[[common.unavailable_now]]</div>";
    if (const auto at = out.find(generic); at != std::string::npos) out.erase(at, generic.size());
  }
  return out;
}

std::string SearchRoute::rowRml(const Row& r) const {
  std::string s = "<div class=\"row srow\" id=\"srow-" + r.kind + "\"><div class=\"row-header\"><div class=\"name\">[[search.row." +
                  r.kind + "]]</div></div>";
  s += "<div class=\"row-note\" id=\"snote-" + r.kind + "\">";
  if (!r.unenforced.empty()) {
    std::string names;
    for (const auto& n : r.unenforced) names += (names.empty() ? "" : ", ") + esc(n);
    s += icon("info") + "<span>[[search.unenforced]] · " + names + "</span>";
  }
  s += "</div><div class=\"track\">";
  for (const auto& it : r.items) s += cardRml(it);
  return s + "</div><div class=\"cline\"/></div>";
}

std::string SearchRoute::stripRml() const {
  if (sources_.empty()) return {};
  int answered = 0;
  int failed = 0;
  for (const auto& s : sources_) {
    if (s.state != SourceState::Pending) answered++;
    if (s.state == SourceState::Failed) failed++;
  }
  if (done_ || answered == static_cast<int>(sources_.size())) {
    // all answered: one focusable line; OK lists the failures
    std::string line = "[[search.summary|answered=" + std::to_string(answered) + "|total=" + std::to_string(sources_.size()) + "]]";
    // the failure itself, not its count alone: the first failed source and how (OK lists them all)
    for (const auto& src : sources_) {
      if (src.state != SourceState::Failed) continue;
      const auto words = failure::wordsFor(src.failure);
      line += " · <span class=\"sfail\">" + esc(failure::sourceLine(src.failure, src.name)) + ": " +
              (!words.reason.empty() ? words.reason : esc(failure::reasonLine(src.failure))) + "</span>";
      break;
    }
    if (failed > 1) line += " · [[search.summary_errors|count=" + std::to_string(failed) + "]]";
    return "<div class=\"ssum f\" id=\"s-sum\" data-action=\"failures\">" + view::focusDecor() + icon(failed > 0 ? "triangle" : "check") +
           "<span>" + line + "</span></div>";
  }
  std::string s;
  for (const auto& src : sources_) {
    s += "<div class=\"schip\">";
    switch (src.state) {
      case SourceState::Pending:
        s += "<span>" + esc(src.name) + "</span><div class=\"spinner small\"/><span class=\"sword\">[[search.searching]]</span>";
        break;
      case SourceState::Answered:
        s += "<span>" + esc(src.name) + "</span>" + icon("check") + "<span>" + std::to_string(src.count) + "</span>";
        break;
      case SourceState::Failed:
        // the source and how it failed, as said (codes, http status)
        s += "<span>" + esc(src.name) + "</span>" + icon("x") + "<span class=\"sword\">" + esc(failure::reasonLine(src.failure)) +
             "</span>";
        break;
    }
    s += "</div>";
  }
  return s;
}

std::string SearchRoute::emptyRml() const {
  if (!done_ || hasResults() || failed_) {
    if (failed_) {
      ErrorCard e;
      e.title = "[[common.load_failed]]";
      failure::describe(e, failure_);
      e.actions = {Button{"[[common.retry]]", "refresh", "retry", true, retrying_, false, "s-retry", {}}};
      return errorCard(e);
    }
    return {};
  }
  std::string s = "<div class=\"sempty\">" + icon("search") + "<div class=\"stitle\">[[search.nothing_found]]</div>";
  if (view::cyrillic(query_)) s += chip("[[search.english]]", "globe", std::nullopt, "english");
  return s + "</div>";
}

std::string SearchRoute::resultsRml() const {
  std::string s;
  for (const auto& kind : order_) {
    for (const auto& r : rows_) {
      if (r.kind == kind) s += rowRml(r);
    }
  }
  if (!hasResults() && searching_ && !done_) {
    // the first row's skeleton while nothing has answered yet
    ui::rml::Row sk;
    sk.id = "s-loading";
    sk.title = "[[search.title]]";
    sk.state = RowState::Loading;
    sk.kind = CardKind::PosterJp;
    s += ui::rml::row(sk);
  }
  return s;
}

std::string SearchRoute::body() {
  std::string s = "<div class=\"safe search" + std::string(collapsed_ ? " collapsed" : "") + "\" id=\"s-root\">";
  s += "<div class=\"kbcol\" id=\"s-kbcol\"><div class=\"t-title-m stitle\">[[search.title]]</div>" + keyboard(layout_, query_) + "</div>";
  s += "<div class=\"results\" id=\"s-results\">";
  s += "<div class=\"qbar f\" id=\"s-qbar\" data-action=\"edit\">" + view::focusDecor() + icon("search") + "<span id=\"s-qtext\">" +
       esc(query_) + "</span></div>";
  s += "<div class=\"strip\" id=\"s-strip\">" + stripRml() + "</div>";
  s += "<div class=\"vscroll\" id=\"s-col\"><div id=\"s-rows\">" + resultsRml() + "</div><div id=\"s-empty\">" + emptyRml() +
       "</div></div>";
  return s + "</div></div>";
}

void SearchRoute::loaded(Rml::ElementDocument& doc, bool restoring) {
  if (restoring) return;
  Rml::ElementList keys;
  doc.QuerySelectorAll(keys, ".osk .key");
  if (!keys.empty()) s_.ui.focus().focus(keys[std::min<std::size_t>(static_cast<std::size_t>(keyIndex_), keys.size() - 1)]);
}

void SearchRoute::clearResults() {
  rows_.clear();
  order_.clear();
  sources_.clear();
  done_ = false;
  // a retry keeps the failure shown until its first answer
  if (!retrying_) failed_ = false;
  if (auto* r = view::byId(page(), "s-rows")) view::fill(r, resultsRml());
  if (!retrying_) {
    if (auto* e = view::byId(page(), "s-empty")) view::fill(e, "");
  }
  redrawStrip();
}

void SearchRoute::search() {
  cancel(callId_);
  clearResults();
  searching_ = true;
  searchedMs_ = s_.ui.nowMs();
  if (auto* r = view::byId(page(), "s-rows")) view::fill(r, resultsRml());
  callId_ = call(rpc::kCatalogSearch, Json{{"query", query_}, {"limit", 30}},
                 {[this](const std::string& status, const Json& json) {
                    const bool wasRetry = retrying_;
                    retrying_ = false;
                    if (status != rpc::kStatusOK) {
                      searching_ = false;
                      failed_ = true;
                      failure_ = failure::of(rpc::kCatalogSearch, status, json, s_.ui.nowMs() - searchedMs_);
                      callId_.clear();
                      if (auto* r = view::byId(page(), "s-rows")) view::fill(r, resultsRml());
                      if (auto* e = view::byId(page(), "s-empty")) view::fill(e, emptyRml());
                      return;
                    }
                    if (wasRetry) {
                      // the retry answered: the failure gives way
                      failed_ = false;
                      if (auto* e = view::byId(page(), "s-empty")) view::fill(e, "");
                    }
                    event(json);
                  },
                  [this](const Json& json, bool last) {
                    event(json);
                    if (last) {
                      callId_.clear();
                      if (!done_) event(Json{{"done", true}});
                    }
                  }},
                 true);
  changed();
}

SearchRoute::Row& SearchRoute::row(const std::string& kind) {
  for (auto& r : rows_) {
    if (r.kind == kind) return r;
  }
  rows_.push_back(Row{kind, {}, {}});
  return rows_.back();
}

bool SearchRoute::hasResults() const {
  return std::any_of(rows_.begin(), rows_.end(), [](const Row& r) { return !r.items.empty(); });
}

const catalog::Item* SearchRoute::itemOf(const std::string& ref) const {
  for (const auto& r : rows_) {
    for (const auto& it : r.items) {
      if (it.ref == ref) return &it;
    }
  }
  return nullptr;
}

bool SearchRoute::focusInResults() const {
  auto* f = s_.ui.focus().current();
  if (f == nullptr) return false;
  for (auto* p = f; p != nullptr; p = p->GetParentNode()) {
    if (p->GetId() == "s-col") return true;
  }
  return false;
}

void SearchRoute::placeRow(const Row& r) {
  auto* rows = view::byId(page(), "s-rows");
  if (rows == nullptr) return;
  if (auto* sk = view::byId(page(), "s-loading")) rows->RemoveChild(sk);
  // before focus is in the results, rows go in kind order; after, a new row joins below the others, so the row
  // under focus never moves
  Rml::Element* before = nullptr;
  std::size_t at = order_.size();
  if (!focusInResults()) {
    for (std::size_t i = 0; i < order_.size(); ++i) {
      if (kindRank(order_[i]) > kindRank(r.kind)) {
        before = view::byId(page(), "srow-" + order_[i]);
        at = i;
        break;
      }
    }
  }
  order_.insert(order_.begin() + static_cast<std::ptrdiff_t>(at), r.kind);
  view::append(rows, rowRml(r), before);
}

void SearchRoute::addItem(const catalog::Item& it) {
  if (itemOf(it.ref) != nullptr) {
    updateItem(it);
    return;
  }
  const auto kind = rowKindOf(it);
  const bool fresh = std::find(order_.begin(), order_.end(), kind) == order_.end();
  auto& r = row(kind);
  r.items.push_back(it);
  if (fresh) {
    placeRow(r);
    return;
  }
  // a row's new results append at its end
  if (auto* rowEl = view::byId(page(), "srow-" + kind)) view::append(rowEl->QuerySelector(".track"), cardRml(it));
}

void SearchRoute::updateItem(const catalog::Item& it) {
  for (auto& r : rows_) {
    for (auto& old : r.items) {
      if (old.ref != it.ref) continue;
      old = it;
      // a title group merged in place: only its meta line changes, never its position
      if (auto* el = view::byId(page(), catalog::idOf("s", it.ref))) {
        Rml::ElementList metas;
        el->QuerySelectorAll(metas, ".meta");
        if (metas.size() >= 2) {
          metas[0]->SetInnerRML(catalog::metaOf(it));
          metas[1]->SetInnerRML(catalog::sourcesLineOf(it, sourceName(it)));
        } else if (!metas.empty()) {
          metas[0]->SetInnerRML(catalog::metaOf(it));
        }
      }
      return;
    }
  }
}

void SearchRoute::event(const Json& ev) {
  if (!ev.is_object()) return;
  for (const auto& p : ev.value("pending", Json::array())) {
    Source src;
    src.provider = p.value("provider", "");
    src.source = p.value("source", "");
    src.name = p.value("name", "");
    if (src.name.empty()) src.name = catalog::providerName(src.provider);
    s_.sourceNames[src.provider + "/" + src.source] = src.name;
    const bool known = std::any_of(sources_.begin(), sources_.end(),
                                   [&](const Source& s) { return s.provider == src.provider && s.source == src.source; });
    if (!known) sources_.push_back(std::move(src));
  }
  const std::string provider = ev.value("provider", "");
  const std::string source = ev.value("source", "");
  const auto items = catalog::parseItems(ev.value("items", Json::array()));
  const auto updated = catalog::parseItems(ev.value("updated", Json::array()));
  if (!provider.empty()) {
    Source* src = nullptr;
    for (auto& s : sources_) {
      if (s.provider == provider && s.source == source) src = &s;
    }
    if (src == nullptr) {
      sources_.push_back(Source{provider, source, source.empty() ? catalog::providerName(provider) : source, SourceState::Pending, 0, {}});
      src = &sources_.back();
    }
    if (ev.contains("failure") && ev["failure"].is_object()) {
      src->state = SourceState::Failed;
      // the batch's failure ({code, httpStatus}) with its source, as the server named them
      Json f = ev["failure"];
      f["failure"] = f.value("code", "failed");
      f["provider"] = provider;
      if (!source.empty()) f["source"] = source;
      src->failure = failure::of(rpc::kCatalogSearch, "", f, s_.ui.nowMs() - searchedMs_);
    } else {
      if (src->state == SourceState::Pending) src->state = SourceState::Answered;
      src->count += static_cast<int>(items.size());
    }
    for (const auto& u : ev.value("unenforced", Json::array())) {
      if (!u.is_string()) continue;
      auto& r = row(provider == "torrplayer" ? "anime" : provider == "torrserver" ? "films" : "youtube");
      const auto name = src->name;
      if (std::find(r.unenforced.begin(), r.unenforced.end(), name) == r.unenforced.end()) {
        r.unenforced.push_back(name);
        if (auto* note = view::byId(page(), "snote-" + r.kind)) {
          std::string names;
          for (const auto& n : r.unenforced) names += (names.empty() ? "" : ", ") + esc(n);
          view::fill(note, icon("info") + "<span>[[search.unenforced]] · " + names + "</span>");
        }
      }
    }
  }
  for (const auto& it : items) addItem(it);
  for (const auto& it : updated) updateItem(it);
  // a row that only got a note is not a row of results yet
  rows_.erase(std::remove_if(rows_.begin(), rows_.end(),
                             [&](const Row& r) {
                               return r.items.empty() && std::find(order_.begin(), order_.end(), r.kind) == order_.end() &&
                                      r.unenforced.empty();
                             }),
              rows_.end());
  if (ev.value("done", false)) {
    done_ = true;
    searching_ = false;
    for (auto& s : sources_) {
      if (s.state == SourceState::Pending) s.state = SourceState::Answered;
    }
    if (auto* sk = view::byId(page(), "s-loading")) sk->GetParentNode()->RemoveChild(sk);
    if (auto* e = view::byId(page(), "s-empty")) view::fill(e, emptyRml());
  }
  redrawStrip();
  changed();
}

void SearchRoute::redrawStrip() {
  auto* strip = view::byId(page(), "s-strip");
  if (strip == nullptr) return;
  auto* f = s_.ui.focus().current();
  const bool focused = f != nullptr && f->GetId() == "s-sum";
  view::fill(strip, stripRml());
  if (focused) {
    if (auto* sum = view::byId(page(), "s-sum")) s_.ui.focus().focus(sum);
  }
}

void SearchRoute::redrawEntry() {
  if (auto* doc = page()) {
    if (auto* entry = doc->QuerySelector(".osk .entry")) view::fill(entry, esc(query_) + "<span class=\"caret\">|</span>");
  }
  if (auto* q = view::byId(page(), "s-qtext")) view::fill(q, esc(query_));
}

void SearchRoute::redrawKeyboard() {
  auto* col = view::byId(page(), "s-kbcol");
  if (col == nullptr) return;
  view::fill(col, "<div class=\"t-title-m stitle\">[[search.title]]</div>" + keyboard(layout_, query_));
  Rml::ElementList keys;
  col->QuerySelectorAll(keys, ".key");
  if (!keys.empty()) s_.ui.focus().focus(keys[std::min<std::size_t>(static_cast<std::size_t>(keyIndex_), keys.size() - 1)]);
}

void SearchRoute::type(const std::string& key) {
  if (key == "space") {
    if (!query_.empty() && query_.back() != ' ') query_ += ' ';
  } else if (key == "backspace") {
    query_ = view::dropLast(query_);
  } else if (key == "voice") {
    // dictation goes through the room's microphone: the assistant fills the query
    call(rpc::kAssistantInvoke, Json::object(), {});
    return;
  } else {
    if (query_.size() < 200) query_ += key;
  }
  redrawEntry();
  dueMs_ = s_.ui.nowMs() + kTypeDebounceMs;
  changed();
}

void SearchRoute::setCollapsed(bool on) {
  if (collapsed_ == on) return;
  collapsed_ = on;
  if (auto* root = view::byId(page(), "s-root")) root->SetClass("collapsed", on);
}

bool SearchRoute::activate(const ui::Activation& a) {
  const auto& act = a.action;
  if (act.starts_with("key:")) {
    if (auto* doc = page()) {
      Rml::ElementList keys;
      doc->QuerySelectorAll(keys, ".osk .key");
      const auto it = std::find(keys.begin(), keys.end(), a.element);
      if (it != keys.end()) keyIndex_ = static_cast<int>(it - keys.begin());
    }
    type(act.substr(4));
    return true;
  }
  if (act.starts_with("layout:")) {
    const auto l = act.substr(7);
    layout_ = l == "en" ? KeyLayout::Latin : l == "digits" ? KeyLayout::Digits : KeyLayout::Cyrillic;
    redrawKeyboard();
    return true;
  }
  if (act == "edit") {
    setCollapsed(false);
    redrawKeyboard();
    return true;
  }
  if (act == "english") {
    layout_ = KeyLayout::Latin;
    query_.clear();
    keyIndex_ = 0;
    clearResults();
    redrawEntry();
    redrawKeyboard();
    return true;
  }
  if (act == "retry") {
    if (retrying_) return true;
    retrying_ = true;
    if (auto* b = view::byId(page(), "s-retry")) b->SetClass("loading", true);
    search();
    return true;
  }
  if (act == "failures") {
    std::string body = "<div class=\"list\">";
    bool any = false;
    for (const auto& s : sources_) {
      if (s.state != SourceState::Failed) continue;
      any = true;
      ListItem li;
      li.leadIcon = "x";
      li.title = esc(failure::sourceLine(s.failure, s.name));
      const auto words = failure::wordsFor(s.failure);
      li.meta = (!words.reason.empty() ? words.reason + " · " : std::string()) + esc(failure::reasonLine(s.failure));
      li.focusable = true;
      body += listItem(li);
    }
    if (!any) {
      ListItem li;
      li.leadIcon = "check";
      li.title = "[[search.summary|answered=" + std::to_string(sources_.size()) + "|total=" + std::to_string(sources_.size()) + "]]";
      body += listItem(li);
    }
    s_.ui.sheet("[[search.failures]]", body + "</div>");
    return true;
  }
  if (act.starts_with("open:")) {
    const auto* it = itemOf(act.substr(5));
    if (it == nullptr) return true;
    if (it->unavailable) {
      s_.ui.toast({"s-na", ui::ToastTone::Info, "[[catalog.not_on_sources]]", "unavailable", {}, {}, 0});
      return true;
    }
    RouteArgs args;
    args.ref = it->ref;
    if (opensTitle(*it)) {
      s_.router.open(cadence_v2_ScreenRoute_SCREEN_ROUTE_TITLE, args);
    } else {
      args.data = Json{{"title", it->title}, {"art", it->artwork}, {"kind", it->kind}, {"provider", it->provider}};
      s_.router.open(cadence_v2_ScreenRoute_SCREEN_ROUTE_RESOLVING, args);
    }
    return true;
  }
  return false;
}

bool SearchRoute::back() {
  // BACK in the results brings the keyboard back; with the keyboard shown it leaves search
  if (collapsed_ || focusInResults()) {
    setCollapsed(false);
    redrawKeyboard();
    return true;
  }
  return false;
}

void SearchRoute::update(std::int64_t nowMs) {
  if (dueMs_ >= 0 && nowMs >= dueMs_) {
    dueMs_ = -1;
    if (view::codepoints(query_) >= kMinQuery) {
      search();
    } else {
      cancel(callId_);
      searching_ = false;
      clearResults();
    }
  }
  auto* f = s_.ui.focus().current();
  if (f == nullptr) return;
  if (focusInResults()) {
    setCollapsed(true);
  } else if (collapsed_ && f->GetId() == "s-qbar") {
    // ↑ from the first row onto the query bar: the keyboard comes back
    setCollapsed(false);
    redrawKeyboard();
  }
}

void SearchRoute::context(ScreenContextData& c) const { c.query = query_; }

}  // namespace cadence::screen::routes
