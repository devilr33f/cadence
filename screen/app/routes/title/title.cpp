#include "app/routes/title/title.h"

#include "app/routes/core/failure-card.h"
#include "app/routes/core/methods.h"
#include "app/routes/core/view.h"
#include "i18n/locale.h"

#include <RmlUi/Core/ElementDocument.h>

#include <algorithm>

namespace cadence::screen::routes {

using namespace ui::rml;

namespace {

// the episodes the page shows below the fold, from the one before the next unwatched
constexpr std::size_t kPageEpisodes = 12;

std::string seasonWord(const std::string& s) {
  if (s == "winter" || s == "spring" || s == "summer" || s == "autumn") return "[[catalog.season." + s + "]]";
  return {};
}

}  // namespace

TitleRoute::TitleRoute(Services& s, const RouteArgs& args) : Route(s), ref_(args.ref) {}

void TitleRoute::start() { load(); }

void TitleRoute::online() {
  if (state_ == State::Failed) load();
}

void TitleRoute::load() {
  // a retry keeps the failure on screen until it is answered
  if (state_ != State::Failed) state_ = State::Loading;
  askedMs_ = s_.ui.nowMs();
  call(rpc::kCatalogItem, Json{{"ref", ref_}},
       {[this](const std::string& status, const Json& json) {
          retrying_ = false;
          if (status != rpc::kStatusOK) {
            state_ = State::Failed;
            failure_ = failure::of(rpc::kCatalogItem, status, json, askedMs_ > 0 ? s_.ui.nowMs() - askedMs_ : -1);
          } else {
            state_ = State::Ready;
            detail_ = catalog::parseDetail(json);
          }
          if (auto* host = view::byId(page(), "t-content")) {
            // the skeleton cross-fades into the page (the frame as it was fades out over the new one)
            s_.ui.crossfade();
            view::fill(host, headRml() + episodesRml());
            if (auto* f = view::byId(page(), state_ == State::Ready ? "t-play" : "t-retry")) s_.ui.focus().focus(f);
          }
          if (state_ == State::Ready && !primaryEpisode().empty()) loadOptions(primaryEpisode());
          changed();
        },
        nullptr});
}

std::string TitleRoute::variantRef() const {
  if (!detail_.selected.empty()) return detail_.selected;
  return !detail_.item.ref.empty() ? detail_.item.ref : ref_;
}

std::string TitleRoute::primaryEpisode() const {
  if (!detail_.continueEpisode.empty()) return detail_.continueEpisode;
  for (const auto& c : detail_.children) {
    if (!c.watched) return catalog::episodeLabel(c);
  }
  return detail_.children.empty() ? std::string() : catalog::episodeLabel(detail_.children.front());
}

std::string TitleRoute::sourceName(const catalog::Item& v) const {
  const auto it = s_.sourceNames.find(v.provider + "/" + v.source);
  if (it != s_.sourceNames.end()) return it->second;
  return v.source.empty() ? catalog::providerName(v.provider) : v.source;
}

void TitleRoute::loadOptions(const std::string& episode) {
  cancel(optionsCall_);
  optionsLoading_ = true;
  optionsEpisode_ = episode;
  optionsCall_ = call(rpc::kCatalogOptions, Json{{"ref", variantRef()}, {"episode", episode}},
                      {[this, episode](const std::string& status, const Json& json) {
                         optionsCall_.clear();
                         optionsLoading_ = false;
                         if (status == rpc::kStatusOK) {
                           options_ = json;
                           parsed_ = catalog::parseOptions(json);
                         } else {
                           options_ = Json();
                           parsed_ = {};
                         }
                         redrawButtons();
                         changed();
                         if (waitingEpisode_ == episode) {
                           waitingEpisode_.clear();
                           play(episode);
                         }
                       },
                       nullptr});
}

play::Title TitleRoute::titleFor(const std::string& ref, const std::string& episode) const {
  const auto& it = detail_.item;
  return play::Title{ref, episode, !it.nameRu.empty() ? it.nameRu : it.title, it.artwork, it.provider, it.kind};
}

void TitleRoute::play(const std::string& episode) {
  if (episode != optionsEpisode_ || optionsLoading_) {
    // the press waits for this episode's options, the button showing it is busy
    waitingEpisode_ = episode;
    if (episode != optionsEpisode_ || !optionsLoading_) loadOptions(episode);
    if (auto* b = view::byId(page(), "t-play")) b->SetClass("loading", true);
    return;
  }
  if (options_.is_null()) {
    // no options (a provider without them): resolve with the server's defaults
    play::resolve(s_.router, titleFor(variantRef(), episode), Json::object(), {});
    return;
  }
  play::open(s_.router, titleFor(variantRef(), episode), options_);
}

std::string TitleRoute::buttonsRml() const {
  std::string s;
  const auto ep = primaryEpisode();
  std::string label = "[[title.watch]]";
  if (!detail_.continueEpisode.empty()) {
    label = "[[title.continue_episode|n=" + catalog::argOf(detail_.continueEpisode) + "]]";
  } else if (!ep.empty()) {
    label = "[[title.watch_label|n=" + catalog::argOf(ep) + "]]";
  }
  s += button(Button{label, "play", "play", true, optionsLoading_ && !waitingEpisode_.empty(), false, "t-play", "default-focus"});
  // the switcher reads the voiceover the next play would take, else the source
  std::string sw;
  if (const auto it = parsed_.selected.find("voiceover"); it != parsed_.selected.end()) {
    sw = "[[title.voiceover]] · " + play::selectionLabel(parsed_, {{"voiceover", it->second}});
  } else if (!detail_.variants.empty()) {
    for (const auto& v : detail_.variants) {
      if (v.ref == detail_.selected) sw = "[[title.sources]] · " + esc(sourceName(v));
    }
    if (sw.empty()) sw = "[[title.sources]]";
  }
  if (!sw.empty()) s += button(Button{sw, "audio", "switcher", false, false, false, "t-source", {}});
  // the same title on the other providers and sources
  s += button(Button{"[[title.alternatives]]", "search", "alts", false, altsLoading_, false, "t-alts", {}});
  for (const auto& a : detail_.actions) {
    if (a.values.empty()) continue;
    // the my-list status: its value, opening its choices
    s += button(Button{esc(!a.value.empty() ? a.value : a.label), "bookmark", "act:" + a.id, false, false, false, "t-list", {}});
    break;
  }
  return s;
}

void TitleRoute::redrawButtons() {
  auto* host = view::byId(page(), "t-buttons");
  if (host == nullptr) return;
  auto* f = s_.ui.focus().current();
  const std::string focused = f != nullptr ? f->GetId() : "";
  view::fill(host, buttonsRml());
  if (!focused.empty()) {
    if (auto* e = view::byId(page(), focused)) s_.ui.focus().focus(e);
  }
}

std::string TitleRoute::headRml() const {
  if (state_ == State::Loading) {
    // the page's own shape, block for block (poster, name, original name, meta, genres, the buttons, three lines of
    // description), so the content cross-fades in over it with nothing jumping
    return "<div class=\"thead\"><div class=\"tposter skel\"/><div class=\"tinfo\">"
           "<div class=\"skel tsk-title\"/><div class=\"skel tsk-line\" style=\"width: 34%;\"/>"
           "<div class=\"skel tsk-line tsk-meta\" style=\"width: 48%;\"/><div class=\"skel tsk-line\" style=\"width: 36%;\"/>"
           "<div class=\"tbuttons\"><div class=\"skel tsk-btn\" style=\"width: 330dp;\"/>"
           "<div class=\"skel tsk-btn\" style=\"width: 260dp;\"/><div class=\"skel tsk-btn\" style=\"width: 170dp;\"/></div>"
           "<div class=\"skel tsk-line tsk-desc\" style=\"width: 94%;\"/><div class=\"skel tsk-line\" style=\"width: 90%;\"/>"
           "<div class=\"skel tsk-line\" style=\"width: 62%;\"/></div></div>";
  }
  if (state_ == State::Failed) {
    // what failed, as the server said it; «Повторить» spins while a retry runs
    ErrorCard e;
    e.title = "[[common.load_failed]]";
    failure::describe(e, failure_);
    e.actions = {Button{"[[common.retry]]", "refresh", "retry", true, retrying_, false, "t-retry", "default-focus"},
                 Button{"[[common.back]]", "back", "back", false, false, false, "t-back", {}}};
    return "<div class=\"thead\">" + errorCard(e) + "</div>";
  }
  const auto& it = detail_.item;
  const auto lang = s_.ui.translator().language();
  std::string s = "<div class=\"thead\"><div class=\"tposter\" data-ref=\"" + esc(it.ref) + "\"><div class=\"clip\">";
  if (const auto art = catalog::artOf(it); !art.empty()) {
    s += "<div class=\"img netimg\" data-img=\"" + esc(art.substr(4)) + "\"/>";
  }
  s += "</div><div class=\"hair\"/></div><div class=\"tinfo\">";
  s += "<div class=\"ttitle\">" + esc(!it.nameRu.empty() ? it.nameRu : it.title) + "</div>";
  if (!it.nameEn.empty() && it.nameEn != it.nameRu) s += "<div class=\"ten\">" + esc(it.nameEn) + "</div>";
  std::string meta;
  const auto add = [&](const std::string& part) {
    if (part.empty()) return;
    if (!meta.empty()) meta += " · ";
    meta += part;
  };
  if (it.year > 0) add(std::to_string(it.year));
  add(esc(it.format));
  add(seasonWord(it.season));
  if (it.episodesTotal > 0 || it.episodesAired > 0) {
    const int aired = it.episodesAired > 0 ? it.episodesAired : it.episodesTotal;
    const int total = it.episodesTotal > 0 ? it.episodesTotal : it.episodesAired;
    add("[[title.of_episodes|aired=" + std::to_string(aired) + "|total=" + std::to_string(total) + "]]");
  }
  // the subset has no ★: the word says it
  if (it.score > 0) add("[[title.score|score=" + i18n::decimal(lang, it.score) + "]]");
  if (it.rank > 0) add("[[title.rank|rank=" + std::to_string(it.rank) + "]]");
  add(esc(it.ageRating));
  s += "<div class=\"tmeta\"><span>" + meta + "</span>";
  if (it.status == "ongoing") s += badge(BadgeKind::New, "[[title.badge.ongoing]]");
  s += "</div>";
  if (!it.genres.empty()) {
    std::string g;
    for (const auto& x : it.genres) g += (g.empty() ? "" : ", ") + esc(x);
    s += "<div class=\"tgenres\">" + g + "</div>";
  }
  if (const auto facts = factsRml(); !facts.empty()) s += "<div class=\"tfacts\">" + facts + "</div>";
  if (it.hasMine && !it.mineStatus.empty()) {
    s += "<div class=\"tmine\">" + icon("bookmark") + "<span>" + mineRml() + "</span></div>";
  }
  s += "<div class=\"tbuttons\" id=\"t-buttons\">" + buttonsRml() + "</div>";
  if (!it.description.empty()) {
    // long_text, 3 lines collapsed: OK expands it, ↑/↓ scroll it once open
    s += "<div class=\"tdesc f\" id=\"t-desc\" data-kind=\"expand\" data-collapsed=\"108dp\">" + view::focusDecor() +
         esc(it.description) + "</div>";
  }
  if (!it.related.empty()) {
    // the franchise's other titles, as names: search one to play it
    s += "<div class=\"trelated\"><div class=\"tsub\">[[title.related]]</div>";
    for (std::size_t i = 0; i < it.related.size() && i < 6; ++i) {
      const auto& r = it.related[i];
      s += "<div class=\"trel\"><span class=\"trelk\">" + esc(r.relation) + "</span><span>" + esc(r.title) +
           (r.year > 0 ? " · " + std::to_string(r.year) : std::string()) + "</span></div>";
    }
    s += "</div>";
  }
  return s + "</div></div>";
}

std::string TitleRoute::factsRml() const {
  const auto& it = detail_.item;
  std::string out;
  const auto add = [&](const std::string& part) {
    if (part.empty()) return;
    if (!out.empty()) out += " · ";
    out += part;
  };
  std::string studios;
  for (std::size_t i = 0; i < it.studios.size() && i < 2; ++i) studios += (i ? ", " : "") + esc(it.studios[i]);
  add(studios);
  const auto from = catalog::dayOf(it.airedOn), to = catalog::dayOf(it.releasedOn);
  if (it.status == "announced") {
    add("[[title.announced]]");
  } else if (!from.empty() && !to.empty() && it.status != "ongoing") {
    add("[[title.aired|from=" + from + "|to=" + to + "]]");
  } else if (!from.empty()) {
    add("[[title.airing|from=" + from + "]]");
  }
  if (it.popularity > 0) add("[[title.popularity|n=" + std::to_string(it.popularity) + "]]");
  return out;
}

std::string TitleRoute::mineRml() const {
  const auto& it = detail_.item;
  // a key can not stand in another's argument: the status word is translated first
  std::string out = "[[title.mine|status=" + catalog::argOf(s_.ui.translator().tr("title.list." + it.mineStatus)) + "]]";
  if (it.mineWatched > 0) {
    const int total = it.episodesTotal > 0 ? it.episodesTotal : it.episodesAired;
    out += " · [[title.of_episodes|aired=" + std::to_string(it.mineWatched) + "|total=" +
           std::to_string(std::max(total, it.mineWatched)) + "]]";
  }
  if (it.mineScore > 0) out += " · [[title.mine_score|score=" + std::to_string(it.mineScore) + "]]";
  return out;
}

std::string TitleRoute::episodesRml() const {
  if (state_ == State::Loading) {
    // the episodes row's place: its header and the first wide cards
    std::string s = "<div class=\"row\"><div class=\"row-header\"><div class=\"skel tsk-head\"/></div><div class=\"track\">";
    for (int i = 0; i < 5; ++i) s += skeletonCard(CardKind::Wide);
    return s + "</div></div>";
  }
  if (state_ != State::Ready || detail_.children.empty()) return {};
  const auto lang = s_.ui.translator().language();
  const auto next = primaryEpisode();
  std::size_t at = 0;
  for (std::size_t i = 0; i < detail_.children.size(); ++i) {
    if (catalog::episodeLabel(detail_.children[i]) == next) at = i;
  }
  const std::size_t from = at > 0 ? at - 1 : 0;
  std::string s = "<div class=\"row\" id=\"t-eps\"><div class=\"row-header\"><div class=\"name\">[[episodes.title]]</div>";
  if (detail_.item.episodesTotal > 0) s += "<span class=\"rcount\">[[episodes.count|count=" + std::to_string(detail_.item.episodesTotal) + "]]</span>";
  s += "</div><div class=\"track\">";
  for (std::size_t i = from; i < detail_.children.size() && i < from + kPageEpisodes; ++i) {
    const auto& e = detail_.children[i];
    s += play::episodeCard(e, catalog::idOf("tep", e.ref + "#" + catalog::episodeLabel(e)), "ep:" + catalog::episodeLabel(e), lang);
  }
  s += "<div class=\"card f all wide\" id=\"t-alleps\" data-action=\"episodes\"><div class=\"art\">" + icon("chevron_right") +
       view::focusDecor() + "</div><div class=\"title\">[[episodes.all]]</div></div>";
  return s + "</div><div class=\"cline\"/></div>";
}

std::string TitleRoute::body() {
  return "<div class=\"safe title-page\"><div class=\"vscroll\" id=\"t-col\"><div id=\"t-content\">" + headRml() +
         episodesRml() + "</div></div></div>";
}

void TitleRoute::loaded(Rml::ElementDocument& doc, bool restoring) {
  if (restoring) return;
  if (auto* f = doc.GetElementById(state_ == State::Ready ? "t-play" : "t-retry")) s_.ui.focus().focus(f);
}

void TitleRoute::openSwitcher() {
  if (detail_.variants.empty()) {
    // one source: the switcher is its voiceover picker
    const auto ep = primaryEpisode();
    if (!options_.is_null() && ep == optionsEpisode_) {
      RouteArgs args;
      args.ref = variantRef();
      args.episode = ep;
      args.data = play::titleJson(titleFor(variantRef(), ep));
      args.data["options"] = options_;
      s_.router.open(cadence_v2_ScreenRoute_SCREEN_ROUTE_PICKER, args);
    }
    return;
  }
  int most = 0;
  for (const auto& v : detail_.variants) most = std::max(most, v.episodesAired);
  std::string body = "<div class=\"list sources\">";
  for (const auto& v : detail_.variants) {
    ListItem li;
    li.id = catalog::idOf("tsrc", v.ref);
    li.ref = v.ref;
    li.leadIcon = v.unavailable ? "unavailable" : "film";
    li.title = esc(sourceName(v));
    std::string meta;
    if (v.unavailable) {
      meta = "[[title.source_failed]]";
      li.classes = "unavailable";
    } else {
      if (v.episodesAired > 0) meta = "[[catalog.episodes_short|count=" + std::to_string(v.episodesAired) + "]]";
      // one episode ahead of every other source
      int others = 0;
      for (const auto& o : detail_.variants) {
        if (o.ref != v.ref) others = std::max(others, o.episodesAired);
      }
      if (v.episodesAired > others && others > 0 && v.episodesAired == most) {
        meta += " · [[title.source_ahead|count=" + std::to_string(v.episodesAired - others) + "]]";
      }
      if (!v.voiceovers.empty()) {
        std::string vo;
        for (std::size_t i = 0; i < v.voiceovers.size() && i < 2; ++i) vo += (i ? ", " : "") + esc(v.voiceovers[i]);
        if (v.voiceovers.size() > 2) vo += " +" + std::to_string(v.voiceovers.size() - 2);
        meta += (meta.empty() ? "" : " · ") + vo;
      }
      for (const auto& b : v.badges) {
        if (b.kind == "hd" || b.kind == "4k" || b.kind == "hdr") meta += " · " + esc(b.label.empty() ? b.kind : b.label);
      }
    }
    li.meta = meta;
    if (v.ref == detail_.selected) {
      li.trailIcon = "check";
      li.trail = "[[picker.selected]]";
    }
    li.action = "src:" + v.ref;
    body += listItem(li);
  }
  s_.ui.sheet("[[title.sources]]", body + "</div>", "right");
  // focus starts on the selected source
  if (auto* overlay = s_.ui.overlay()) {
    for (const auto& v : detail_.variants) {
      if (v.ref != detail_.selected) continue;
      if (auto* e = overlay->GetElementById(catalog::idOf("tsrc", v.ref))) s_.ui.focus().focus(e);
    }
  }
}

void TitleRoute::findAlternatives() {
  if (altsLoading_) return;
  altsLoading_ = true;
  redrawButtons();
  call(rpc::kCatalogAlternatives, Json{{"ref", ref_}},
       {[this](const std::string& status, const Json& json) {
          altsLoading_ = false;
          redrawButtons();
          if (status != rpc::kStatusOK) {
            s_.ui.toast({"t-alts", ui::ToastTone::Warning, "[[common.load_failed]]", "octagon", {}, {}, 0});
            return;
          }
          alts_ = catalog::parseAlternatives(json);
          openAlternatives();
          changed();
        },
        nullptr});
}

void TitleRoute::openAlternatives() {
  const bool others = std::any_of(alts_.begin(), alts_.end(), [](const catalog::Alternative& a) { return !a.current; });
  if (!others) {
    s_.ui.toast({"t-alts", ui::ToastTone::Info, "[[title.alternatives_none]]", "search", {}, {}, 0});
    return;
  }
  std::string body = "<div class=\"list sources\">";
  for (const auto& v : alts_) {
    ListItem li;
    li.id = catalog::idOf("talt", v.ref);
    li.ref = v.ref;
    li.leadIcon = v.unavailable ? "unavailable" : "film";
    li.title = esc(!v.sourceName.empty() ? v.sourceName : catalog::providerName(v.provider));
    std::string meta;
    const auto add = [&](const std::string& part) {
      if (part.empty()) return;
      if (!meta.empty()) meta += " · ";
      meta += part;
    };
    if (v.unavailable) {
      add("[[title.source_failed]]");
      li.classes = "unavailable";
    } else {
      if (v.episodesAired > 0) add("[[catalog.episodes_short|count=" + std::to_string(v.episodesAired) + "]]");
      std::string vo;
      for (std::size_t i = 0; i < v.voiceovers.size() && i < 3; ++i) vo += (i ? ", " : "") + esc(v.voiceovers[i]);
      if (v.voiceovers.size() > 3) vo += " +" + std::to_string(v.voiceovers.size() - 3);
      add(vo);
      if (v.quality > 0) add(std::to_string(v.quality) + "p");
      if (v.match == "name") add("[[title.alt_by_name]]");
    }
    li.meta = meta;
    if (v.current) {
      li.trailIcon = "check";
      li.trail = "[[title.alt_current]]";
    }
    li.action = "alt:" + v.ref;
    body += listItem(li);
  }
  s_.ui.sheet("[[title.alternatives_title]]", body + "</div>", "right");
  // focus starts on the first other source
  if (auto* overlay = s_.ui.overlay()) {
    for (const auto& v : alts_) {
      if (v.current) continue;
      if (auto* e = overlay->GetElementById(catalog::idOf("talt", v.ref))) s_.ui.focus().focus(e);
      break;
    }
  }
}

void TitleRoute::openMyList() {
  for (const auto& a : detail_.actions) {
    if (a.values.empty()) continue;
    std::string body = "<div class=\"list\">";
    for (const auto& v : a.values) {
      ListItem li;
      li.title = esc(v);
      li.leadIcon = v == a.value ? "check" : "bookmark";
      li.action = "set:" + a.id + "|" + v;
      body += listItem(li);
    }
    s_.ui.sheet("[[title.my_list]]", body + "</div>");
    return;
  }
}

bool TitleRoute::activate(const ui::Activation& a) {
  const auto& act = a.action;
  if (act == "retry") {
    if (retrying_) return true;
    retrying_ = true;
    if (auto* b = view::byId(page(), "t-retry")) b->SetClass("loading", true);
    load();
    return true;
  }
  if (act == "back") {
    s_.router.pop();
    return true;
  }
  if (act == "play") {
    play(primaryEpisode());
    return true;
  }
  if (act.starts_with("ep:")) {
    play(act.substr(3));
    return true;
  }
  if (act == "episodes") {
    RouteArgs args;
    args.ref = variantRef();
    args.episode = primaryEpisode();
    args.data = play::titleJson(titleFor(variantRef(), primaryEpisode()));
    s_.router.open(cadence_v2_ScreenRoute_SCREEN_ROUTE_EPISODES, args);
    return true;
  }
  if (act == "switcher") {
    openSwitcher();
    return true;
  }
  if (act == "alts") {
    findAlternatives();
    return true;
  }
  if (act.starts_with("alt:")) {
    const auto ref = act.substr(4);
    for (const auto& v : alts_) {
      if (v.ref != ref) continue;
      if (v.unavailable) {
        s_.ui.toast({"t-src", ui::ToastTone::Info, "[[title.source_failed]]", "unavailable", {}, {}, 0});
        return true;
      }
      s_.ui.closeLevel();
      // OK on another source opens its voiceover picker for the episode the page would play
      const auto ep = primaryEpisode();
      RouteArgs args;
      args.ref = ref;
      args.episode = ep;
      args.data = play::titleJson(titleFor(ref, ep));
      s_.router.open(cadence_v2_ScreenRoute_SCREEN_ROUTE_PICKER, args);
    }
    return true;
  }
  if (act.starts_with("src:")) {
    const auto ref = act.substr(4);
    for (const auto& v : detail_.variants) {
      if (v.ref != ref) continue;
      if (v.unavailable) {
        s_.ui.toast({"t-src", ui::ToastTone::Info, "[[title.source_failed]]", "unavailable", {}, {}, 0});
        return true;
      }
      s_.ui.closeLevel();
      // OK on a source opens its voiceover picker for the episode the page would play
      const auto ep = primaryEpisode();
      RouteArgs args;
      args.ref = ref;
      args.episode = ep;
      args.data = play::titleJson(titleFor(ref, ep));
      s_.router.open(cadence_v2_ScreenRoute_SCREEN_ROUTE_PICKER, args);
    }
    return true;
  }
  if (act.starts_with("act:")) {
    openMyList();
    return true;
  }
  if (act.starts_with("set:")) {
    const auto rest = act.substr(4);
    const auto bar = rest.find('|');
    const auto id = rest.substr(0, bar);
    const auto value = bar == std::string::npos ? std::string() : rest.substr(bar + 1);
    s_.ui.closeLevel();
    call(rpc::kCatalogAct, Json{{"ref", ref_}, {"action", id}, {"value", value}},
         {[this](const std::string& status, const Json& json) {
            if (status != rpc::kStatusOK) {
              s_.ui.toast({"t-act", ui::ToastTone::Warning, "[[common.load_failed]]", "octagon", {}, {}, 0});
              return;
            }
            // the server's answer is the truth (section 9)
            detail_.actions = catalog::parseActions(json.value("actions", Json::array()));
            redrawButtons();
          },
          nullptr});
    return true;
  }
  return false;
}

void TitleRoute::context(ScreenContextData& c) const {
  c.pageRef = ref_;
  for (const auto& v : detail_.variants) {
    if (v.ref == detail_.selected) c.source = v.source;
  }
  if (const auto it = parsed_.selected.find("voiceover"); it != parsed_.selected.end()) c.voiceover = it->second;
  if (const auto it = parsed_.selected.find("quality"); it != parsed_.selected.end()) c.quality = it->second;
  c.episode = primaryEpisode();
}

}  // namespace cadence::screen::routes
