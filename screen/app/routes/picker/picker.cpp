#include "app/routes/picker/picker.h"

#include "app/routes/core/failure-card.h"
#include "app/routes/core/methods.h"
#include "app/routes/core/view.h"
#include "i18n/locale.h"

#include <RmlUi/Core/ElementDocument.h>

#include <cstdlib>

namespace cadence::screen::routes {

using namespace ui::rml;

namespace {

int qualityOf(const catalog::Choice& q) {
  const int fromId = std::atoi(q.id.c_str());
  return fromId > 0 ? fromId : std::atoi(q.label.c_str());
}

std::string typeTag(const std::string& transType) {
  if (transType == "sub") return "<div class=\"ptag\">" + icon("subtitles") + "<span>[[picker.subtitles]]</span></div>";
  if (transType == "raw") return "<div class=\"ptag\">" + icon("globe") + "<span>[[picker.original]]</span></div>";
  return "<div class=\"ptag\">" + icon("mic") + "<span>[[picker.voiceover]]</span></div>";
}

}  // namespace

PickerRoute::PickerRoute(Services& s, const RouteArgs& args) : Route(s), title_(play::titleOf(args)) {
  if (args.data.contains("options") && args.data["options"].is_object()) {
    raw_ = args.data["options"];
    options_ = catalog::parseOptions(raw_);
    state_ = State::Ready;
  }
}

void PickerRoute::start() {
  if (state_ != State::Ready) load();
}

void PickerRoute::online() {
  if (state_ == State::Failed) load();
}

void PickerRoute::load() {
  if (state_ != State::Failed) state_ = State::Loading;
  askedMs_ = s_.ui.nowMs();
  call(rpc::kCatalogOptions, Json{{"ref", title_.ref}, {"episode", title_.episode}},
       {[this](const std::string& status, const Json& json) {
          retrying_ = false;
          if (status != rpc::kStatusOK) failure_ = failure::of(rpc::kCatalogOptions, status, json, askedMs_ > 0 ? s_.ui.nowMs() - askedMs_ : -1);
          if (status == rpc::kStatusOK) {
            raw_ = json;
            options_ = catalog::parseOptions(json);
            state_ = State::Ready;
          } else {
            state_ = State::Failed;
          }
          if (auto* host = view::byId(page(), "p-list")) view::fill(host, listRml());
          focusDefault();
          changed();
        },
        nullptr});
}

const catalog::Choice* PickerRoute::choice(const std::string& group, const std::string& id) const {
  if (const auto* g = options_.group(group)) {
    for (const auto& c : g->choices) {
      if (c.id == id) return &c;
    }
  }
  return nullptr;
}

std::string PickerRoute::voiceoverRow(const catalog::Choice& vo, const catalog::ChoiceGroup* quality) const {
  const auto selected = options_.selected.find("voiceover");
  const bool chosen = selected != options_.selected.end() && selected->second == vo.id;
  const auto rowId = catalog::idOf("pv", vo.id);
  std::string s = "<div class=\"prow" + std::string(vo.unavailable ? " unavailable" : "") + "\" id=\"" + rowId + "\">";
  s += "<div class=\"plabel\"><div class=\"pname\">" + esc(!vo.translator.empty() ? vo.translator : vo.label) + "</div><div class=\"pmeta\">" +
       typeTag(vo.transType);
  if (!vo.language.empty()) s += "<span class=\"plang\">" + esc(vo.language) + "</span>";
  if (vo.unavailable) {
    s += "<span class=\"pnote\">[[picker.episode_missing|n=" + catalog::argOf(title_.episode) + "]]</span>";
  } else if (chosen && options_.remembers("voiceover")) {
    s += "<span class=\"pnote\">" + icon("check") + " [[picker.remembered]]</span>";
  }
  s += "</div></div><div class=\"pchips\">";
  const auto selQ = options_.selected.find("quality");
  bool any = false;
  if (quality != nullptr) {
    for (const auto& q : quality->choices) {
      // a translator's chips stop at its best quality
      if (vo.quality > 0 && qualityOf(q) > vo.quality) continue;
      any = true;
      const bool on = chosen && selQ != options_.selected.end() && selQ->second == q.id;
      auto c = chip(esc(q.label), {}, on ? std::optional<bool>(true) : std::nullopt,
                    vo.unavailable || q.unavailable ? "na:" + vo.id : "q:" + vo.id + "|" + q.id);
      c.insert(c.find(' '), " id=\"" + catalog::idOf("pq", vo.id + "|" + q.id) + "\"");
      s += c;
    }
  }
  if (!any) {
    // no quality group: one chip plays the translator's own
    auto c = chip(vo.quality > 0 ? std::to_string(vo.quality) : "[[title.watch]]", "play", std::nullopt,
                  vo.unavailable ? "na:" + vo.id : "q:" + vo.id + "|");
    c.insert(c.find(' '), " id=\"" + catalog::idOf("pq", vo.id + "|") + "\"");
    s += c;
  }
  return s + "</div></div>";
}

std::string PickerRoute::torrentRow(const catalog::Choice& t) const {
  const auto lang = s_.ui.translator().language();
  std::string title;
  const auto add = [&](const std::string& part) {
    if (part.empty()) return;
    if (!title.empty()) title += " · ";
    title += part;
  };
  if (!t.episodes.empty()) add("[[picker.torrent_episodes|range=" + catalog::argOf(t.episodes) + "]]");
  if (t.quality > 0) add(std::to_string(t.quality) + "p");
  add(esc(t.codec));
  if (t.sizeBytes > 0) {
    add(i18n::decimal(lang, static_cast<double>(t.sizeBytes / 100000000) / 10.0) + (lang == i18n::Lang::Ru ? " ГБ" : " GB"));
  }
  if (title.empty()) title = esc(t.label);
  ListItem li;
  li.id = catalog::idOf("pt", t.id);
  li.leadIcon = "film";
  li.title = title;
  std::string meta = t.seeders > 0 ? "[[picker.seeds|count=" + std::to_string(t.seeders) + "]]" : "[[picker.no_seeds]]";
  if (t.hardsub) meta += " · [[picker.hardsub]]";
  li.meta = meta;
  li.action = t.seeders > 0 && !t.unavailable ? "t:" + t.id : "na-t:" + t.id;
  if (t.seeders <= 0 || t.unavailable) li.classes = "unavailable";
  return listItem(li);
}

std::string PickerRoute::listRml() const {
  if (state_ == State::Loading) {
    std::string s;
    for (int i = 0; i < 4; ++i) s += "<div class=\"prow\"><div class=\"plabel\"><div class=\"skel line\" style=\"width: 320dp;\"/></div></div>";
    return s;
  }
  if (state_ == State::Failed) {
    ErrorCard e;
    e.title = "[[common.load_failed]]";
    failure::describe(e, failure_, title_.provider);
    e.actions = {Button{"[[common.retry]]", "refresh", "retry", true, retrying_, false, "p-retry", "default-focus"},
                 Button{"[[common.back]]", "back", "back", false, false, false, "p-back", {}}};
    return errorCard(e);
  }
  std::string s;
  const auto* quality = options_.group("quality");
  int hidden = 0;
  if (const auto* vo = options_.group("voiceover")) {
    for (const auto& c : vo->choices) s += voiceoverRow(c, quality);
    hidden += vo->hidden;
  }
  if (const auto* tor = options_.group("torrent"); tor != nullptr && !tor->choices.empty()) {
    s += "<div class=\"pdivider\"><span>[[picker.torrents]]</span></div><div class=\"list\">";
    for (const auto& t : tor->choices) s += torrentRow(t);
    s += "</div>";
    hidden += tor->hidden;
  }
  if (options_.group("voiceover") == nullptr && options_.group("torrent") == nullptr) {
    // nothing to choose: one row plays with the server's defaults
    catalog::Choice only;
    only.id = "";
    only.label = "[[title.watch]]";
    s += voiceoverRow(only, quality);
  }
  if (hidden > 0) {
    ListItem li;
    li.id = "p-hidden";
    li.leadIcon = "info";
    li.title = "[[picker.hidden|count=" + std::to_string(hidden) + "]] · [[picker.show]]";
    li.action = "hidden";
    s += "<div class=\"list phidden\">" + listItem(li) + "</div>";
  }
  return s;
}

std::string PickerRoute::body() {
  std::string head = "<div class=\"pkhead\"><div class=\"pktitle\">[[picker.title]]</div><div class=\"pksub\">" + esc(title_.title);
  if (!title_.episode.empty()) head += " · [[episodes.episode|n=" + catalog::argOf(title_.episode) + "]]";
  head += "</div></div>";
  return "<div class=\"safe picker\"><div class=\"vscroll\" id=\"p-col\">" + head + "<div id=\"p-list\">" + listRml() +
         "</div></div></div>";
}

std::string PickerRoute::defaultFocusId() const {
  if (state_ == State::Failed) return "p-retry";
  const auto vo = options_.selected.find("voiceover");
  const auto* quality = options_.group("quality");
  std::string voId;
  if (vo != options_.selected.end()) {
    voId = vo->second;
  } else if (const auto* g = options_.group("voiceover")) {
    for (const auto& c : g->choices) {
      if (!c.unavailable) {
        voId = c.id;
        break;
      }
    }
  }
  if (const auto* g = options_.group("voiceover"); g == nullptr && options_.group("torrent") != nullptr) {
    const auto t = options_.selected.find("torrent");
    for (const auto& c : options_.group("torrent")->choices) {
      if (t == options_.selected.end() || c.id == t->second) return catalog::idOf("pt", c.id);
    }
  }
  const auto* v = choice("voiceover", voId);
  // the selected quality, else the best this translator has
  const auto q = options_.selected.find("quality");
  if (q != options_.selected.end()) return catalog::idOf("pq", voId + "|" + q->second);
  std::string best;
  int bestQ = -1;
  if (quality != nullptr) {
    for (const auto& c : quality->choices) {
      const int h = qualityOf(c);
      if (v != nullptr && v->quality > 0 && h > v->quality) continue;
      if (h > bestQ) {
        bestQ = h;
        best = c.id;
      }
    }
  }
  return catalog::idOf("pq", voId + "|" + best);
}

void PickerRoute::focusDefault() {
  auto* doc = page();
  if (doc == nullptr) return;
  if (auto* e = doc->GetElementById(defaultFocusId())) {
    s_.ui.focus().focus(e);
    focusLater(e->GetId());
    return;
  }
  if (auto* list = doc->GetElementById("p-list")) {
    if (auto* first = list->QuerySelector(".f")) s_.ui.focus().focus(first);
  }
}

void PickerRoute::loaded(Rml::ElementDocument&, bool restoring) {
  if (!restoring && state_ != State::Loading) focusDefault();
}

bool PickerRoute::activate(const ui::Activation& a) {
  const auto& act = a.action;
  if (act == "retry") {
    if (retrying_) return true;
    retrying_ = true;
    if (auto* b = view::byId(page(), "p-retry")) b->SetClass("loading", true);
    load();
    return true;
  }
  if (act == "back" && state_ == State::Failed) {
    s_.router.pop();
    return true;
  }
  if (act.starts_with("q:")) {
    const auto rest = act.substr(2);
    const auto bar = rest.find('|');
    const auto vo = rest.substr(0, bar);
    const auto q = bar == std::string::npos ? std::string() : rest.substr(bar + 1);
    Json choices = Json::object();
    std::map<std::string, std::string> sel;
    if (!vo.empty()) choices["voiceover"] = sel["voiceover"] = vo;
    if (!q.empty()) choices["quality"] = sel["quality"] = q;
    play::resolve(s_.router, title_, choices, play::selectionLabel(options_, sel));
    return true;
  }
  if (act.starts_with("t:")) {
    const auto id = act.substr(2);
    play::resolve(s_.router, title_, Json{{"torrent", id}}, play::selectionLabel(options_, {{"torrent", id}}));
    return true;
  }
  if (act.starts_with("na:") || act.starts_with("na-t:")) {
    // refused elements stay focusable and explain themselves (section 7)
    const bool torrent = act.starts_with("na-t:");
    s_.ui.toast({"p-na", ui::ToastTone::Info,
                 torrent ? "[[picker.no_seeds]]" : "[[picker.episode_missing|n=" + catalog::argOf(title_.episode) + "]]",
                 "unavailable", {}, {}, 0});
    return true;
  }
  if (act == "hidden") {
    s_.ui.toast({"p-hidden", ui::ToastTone::Info, "[[picker.hidden_hint]]", "info", {}, {}, 0});
    return true;
  }
  return false;
}

void PickerRoute::context(ScreenContextData& c) const {
  c.pageRef = title_.ref;
  c.episode = title_.episode;
  // the focused chip names the voiceover and quality it would play
  auto* f = s_.ui.focus().current();
  const auto act = f != nullptr ? f->GetAttribute<Rml::String>("data-action", "") : std::string();
  if (act.starts_with("q:")) {
    const auto rest = act.substr(2);
    const auto bar = rest.find('|');
    c.voiceover = rest.substr(0, bar);
    if (bar != std::string::npos) c.quality = rest.substr(bar + 1);
  } else if (act.starts_with("t:")) {
    c.source = act.substr(2);
  } else {
    if (const auto it = options_.selected.find("voiceover"); it != options_.selected.end()) c.voiceover = it->second;
    if (const auto it = options_.selected.find("quality"); it != options_.selected.end()) c.quality = it->second;
  }
}

}  // namespace cadence::screen::routes
