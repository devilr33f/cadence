#include "app/routes/episodes/episodes.h"

#include "app/routes/core/failure-card.h"
#include "app/routes/core/methods.h"
#include "app/routes/core/view.h"
#include "ui/core/focus.h"

#include <RmlUi/Core/ElementDocument.h>

#include <algorithm>

namespace cadence::screen::routes {

using namespace ui::rml;

EpisodesRoute::EpisodesRoute(Services& s, const RouteArgs& args) : Route(s), title_(play::titleOf(args)) {}

void EpisodesRoute::start() { load({}, {}); }

void EpisodesRoute::online() {
  if (state_ == State::Failed) load({}, {});
}

void EpisodesRoute::load(const std::string& group, const std::string& cursor) {
  Json args{{"ref", title_.ref}};
  if (!group.empty()) args["group"] = group;
  if (!cursor.empty()) args["cursor"] = cursor;
  loadingMore_ = !cursor.empty();
  askedMs_ = s_.ui.nowMs();
  call(rpc::kCatalogItem, args,
       {[this, group, cursor](const std::string& status, const Json& json) {
          loadingMore_ = false;
          retrying_ = false;
          if (status != rpc::kStatusOK) {
            failure_ = failure::of(rpc::kCatalogItem, status, json, askedMs_ > 0 ? s_.ui.nowMs() - askedMs_ : -1);
            if (cursor.empty()) state_ = State::Failed;
            redrawGrid(false);
            return;
          }
          auto d = catalog::parseDetail(json);
          next_ = d.next;
          const bool first = state_ != State::Ready && group.empty() && cursor.empty();
          if (cursor.empty()) {
            episodes_ = d.children;
          } else {
            for (auto& e : d.children) episodes_.push_back(std::move(e));
          }
          // fractional ordinals sort between their neighbours; the label stays as given
          std::stable_sort(episodes_.begin(), episodes_.end(),
                           [](const catalog::Item& a, const catalog::Item& b) { return a.ordinal < b.ordinal; });
          if (first) {
            detail_ = std::move(d);
            state_ = State::Ready;
            buildRanges();
            // the range holding the next unwatched episode
            const auto want = !title_.episode.empty() ? title_.episode : nextUnwatched();
            for (std::size_t i = 0; i < ranges_.size(); ++i) {
              if (ranges_[i].server) continue;
              for (std::size_t k = ranges_[i].from; k < ranges_[i].to && k < episodes_.size(); ++k) {
                if (catalog::episodeLabel(episodes_[k]) == want) range_ = i;
              }
            }
            if (range_ < ranges_.size()) ranges_[range_].focused = want;
          } else if (!cursor.empty() && ranges_.size() == 1 && !ranges_[0].server) {
            ranges_[0].to = episodes_.size();
          }
          redrawGrid(first || !group.empty());
          changed();
        },
        nullptr});
}

void EpisodesRoute::buildRanges() {
  ranges_.clear();
  if (!detail_.childGroups.empty()) {
    for (const auto& g : detail_.childGroups) ranges_.push_back(Range{g.id, esc(g.label), true, 0, 0, {}});
    // the first group is what the read answered
    return;
  }
  if (episodes_.size() <= kRange) {
    ranges_.push_back(Range{"all", {}, false, 0, episodes_.size(), {}});
    return;
  }
  for (std::size_t from = 0; from < episodes_.size(); from += kRange) {
    const auto to = std::min(episodes_.size(), from + kRange);
    const auto label = esc(catalog::episodeLabel(episodes_[from])) + "–" + esc(catalog::episodeLabel(episodes_[to - 1]));
    ranges_.push_back(Range{"r" + std::to_string(from), label, false, from, to, {}});
  }
}

std::string EpisodesRoute::nextUnwatched() const {
  if (!detail_.continueEpisode.empty()) return detail_.continueEpisode;
  for (const auto& e : episodes_) {
    if (!e.watched) return catalog::episodeLabel(e);
  }
  return episodes_.empty() ? std::string() : catalog::episodeLabel(episodes_.back());
}

std::vector<const catalog::Item*> EpisodesRoute::visible() const {
  std::vector<const catalog::Item*> out;
  if (ranges_.empty()) return out;
  const auto& r = ranges_[std::min(range_, ranges_.size() - 1)];
  const std::size_t from = r.server ? 0 : r.from;
  const std::size_t to = r.server ? episodes_.size() : std::min(r.to, episodes_.size());
  for (std::size_t i = from; i < to; ++i) out.push_back(&episodes_[i]);
  return out;
}

std::string EpisodesRoute::chipsRml() const {
  if (ranges_.size() <= 1) return {};
  std::string s = "<div class=\"ranges\" id=\"e-ranges\">";
  for (std::size_t i = 0; i < ranges_.size(); ++i) {
    auto c = chip(ranges_[i].label, {}, i == range_, "range:" + std::to_string(i));
    c.insert(c.find(' '), " id=\"e-range-" + std::to_string(i) + "\"");
    s += c;
  }
  return s + "</div>";
}

std::string EpisodesRoute::gridRml() const {
  if (state_ == State::Loading) {
    std::string s;
    for (int r = 0; r < 2; ++r) {
      s += "<div class=\"epgrid-row\">";
      for (std::size_t c = 0; c < kColumns; ++c) s += skeletonCard(CardKind::Wide);
      s += "</div>";
    }
    return s;
  }
  if (state_ == State::Failed) {
    ErrorCard e;
    e.title = "[[common.load_failed]]";
    failure::describe(e, failure_, title_.provider);
    e.actions = {Button{"[[common.retry]]", "refresh", "retry", true, retrying_, false, "e-retry", "default-focus"}};
    return errorCard(e);
  }
  const auto eps = visible();
  if (eps.empty()) {
    return "<div class=\"row-state f\" id=\"e-empty\"><div class=\"glow\"/><div class=\"ring\"/>" + icon("info") +
           "<span class=\"title\">[[ui.empty]]</span></div>";
  }
  const auto lang = s_.ui.translator().language();
  std::string s;
  for (std::size_t i = 0; i < eps.size(); i += kColumns) {
    s += "<div class=\"epgrid-row\">";
    for (std::size_t k = i; k < i + kColumns && k < eps.size(); ++k) {
      const auto label = catalog::episodeLabel(*eps[k]);
      s += play::episodeCard(*eps[k], catalog::idOf("ep", label), "ep:" + label, lang);
    }
    s += "</div>";
  }
  if (!next_.empty()) s += "<div class=\"epmore\">" + spinner() + "</div>";
  return s;
}

std::string EpisodesRoute::body() {
  std::string s = "<div class=\"safe episodes\"><div class=\"vscroll\" id=\"e-col\"><div class=\"ehead\"><div class=\"ttitle\">" +
                  esc(title_.title) + "</div><div class=\"esub\">[[episodes.title]]";
  if (state_ == State::Ready) s += " · [[episodes.count|count=" + std::to_string(episodes_.size()) + "]]";
  s += "</div></div><div id=\"e-chips\">" + chipsRml() + "</div><div class=\"epgrid\" id=\"e-grid\">" + gridRml() + "</div>";
  return s + "</div></div>";
}

void EpisodesRoute::loaded(Rml::ElementDocument& doc, bool restoring) {
  if (restoring) return;
  if (state_ == State::Ready && range_ < ranges_.size()) {
    if (auto* e = doc.GetElementById(catalog::idOf("ep", ranges_[range_].focused))) s_.ui.focus().focus(e);
  }
}

void EpisodesRoute::redrawGrid(bool focusRemembered) {
  if (auto* chips = view::byId(page(), "e-chips")) {
    auto* f = s_.ui.focus().current();
    const std::string focused = f != nullptr ? f->GetId() : "";
    view::fill(chips, chipsRml());
    if (focused.starts_with("e-range-")) {
      if (auto* e = view::byId(page(), focused)) s_.ui.focus().focus(e);
    }
  }
  auto* grid = view::byId(page(), "e-grid");
  if (grid == nullptr) return;
  view::fill(grid, gridRml());
  if (auto* head = page() != nullptr ? page()->QuerySelector(".esub") : nullptr) {
    view::fill(head, "[[episodes.title]] · [[episodes.count|count=" + std::to_string(episodes_.size()) + "]]");
  }
  if (!focusRemembered) return;
  std::string want = range_ < ranges_.size() ? ranges_[range_].focused : std::string();
  if (want.empty()) want = nextUnwatched();
  if (auto* e = view::byId(page(), catalog::idOf("ep", want))) {
    s_.ui.focus().focus(e);
    // again once laid out, so the page scrolls to it
    focusLater(e->GetId());
  } else if (auto* first = grid->QuerySelector(".card.f")) {
    s_.ui.focus().focus(first);
  } else if (auto* retry = view::byId(page(), "e-retry")) {
    s_.ui.focus().focus(retry);
  }
}

std::string EpisodesRoute::focusedEpisode() const {
  auto* f = s_.ui.focus().current();
  if (f == nullptr) return {};
  const auto act = f->GetAttribute<Rml::String>("data-action", "");
  return act.starts_with("ep:") ? act.substr(3) : std::string();
}

void EpisodesRoute::update(std::int64_t) {
  const auto ep = focusedEpisode();
  if (!ep.empty() && range_ < ranges_.size() && ranges_[range_].focused != ep) {
    ranges_[range_].focused = ep;
    changed();
  }
  // the last grid row in focus: the next page joins the grid
  if (!next_.empty() && !loadingMore_ && !ep.empty() && ranges_.size() == 1) {
    const auto eps = visible();
    for (std::size_t i = eps.size() >= kColumns ? eps.size() - kColumns : 0; i < eps.size(); ++i) {
      if (catalog::episodeLabel(*eps[i]) == ep) {
        load({}, next_);
        break;
      }
    }
  }
}

void EpisodesRoute::play(const std::string& episode) {
  cancel(optionsCall_);
  pendingEpisode_ = episode;
  if (auto* e = view::byId(page(), catalog::idOf("ep", episode))) e->SetClass("pressed", true);
  optionsCall_ = call(rpc::kCatalogOptions, Json{{"ref", title_.ref}, {"episode", episode}},
                      {[this, episode](const std::string& status, const Json& json) {
                         optionsCall_.clear();
                         if (auto* e = view::byId(page(), catalog::idOf("ep", episode))) e->SetClass("pressed", false);
                         auto t = title_;
                         t.episode = episode;
                         if (status != rpc::kStatusOK) {
                           // no options: the server's defaults decide
                           play::resolve(s_.router, t, Json::object(), {});
                           return;
                         }
                         play::open(s_.router, t, json);
                       },
                       nullptr});
}

bool EpisodesRoute::activate(const ui::Activation& a) {
  const auto& act = a.action;
  if (act == "retry") {
    // the failure stays, «Повторить» spinning, until the answer
    if (retrying_) return true;
    retrying_ = true;
    if (auto* b = view::byId(page(), "e-retry")) b->SetClass("loading", true);
    load({}, {});
    return true;
  }
  if (act.starts_with("range:")) {
    const auto i = static_cast<std::size_t>(std::stoul(act.substr(6)));
    if (i >= ranges_.size() || i == range_) return true;
    range_ = i;
    if (ranges_[i].server) {
      state_ = State::Ready;
      episodes_.clear();
      load(ranges_[i].id, {});
      redrawGrid(false);
    } else {
      redrawGrid(false);
    }
    changed();
    return true;
  }
  if (act.starts_with("ep:")) {
    play(act.substr(3));
    return true;
  }
  return false;
}

void EpisodesRoute::context(ScreenContextData& c) const {
  c.pageRef = title_.ref;
  c.episode = focusedEpisode();
  if (c.episode.empty() && range_ < ranges_.size()) c.episode = ranges_[range_].focused;
}

}  // namespace cadence::screen::routes
