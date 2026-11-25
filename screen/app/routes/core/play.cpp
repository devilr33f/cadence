#include "app/routes/core/play.h"

#include "app/routes/core/view.h"

namespace cadence::screen::routes::play {

using ui::rml::esc;

Json titleJson(const Title& t) {
  return Json{{"title", t.title}, {"art", t.art}, {"provider", t.provider}, {"kind", t.kind}};
}

Title titleOf(const RouteArgs& args) {
  Title t;
  t.ref = args.ref;
  t.episode = args.episode;
  t.title = args.data.value("title", "");
  t.art = args.data.value("art", "");
  t.provider = args.data.value("provider", "");
  t.kind = args.data.value("kind", "");
  return t;
}

std::string selectionLabel(const catalog::Options& o, const std::map<std::string, std::string>& selection) {
  std::string out;
  const auto add = [&](const std::string& kind) {
    const auto it = selection.find(kind);
    if (it == selection.end()) return;
    if (const auto* g = o.group(kind)) {
      for (const auto& c : g->choices) {
        if (c.id != it->second) continue;
        if (!out.empty()) out += " · ";
        out += esc(c.label);
      }
    }
  };
  add("voiceover");
  add("quality");
  add("torrent");
  return out;
}

void resolve(Router& router, const Title& t, const Json& choices, const std::string& label, bool replace) {
  RouteArgs args;
  args.ref = t.ref;
  args.episode = t.episode;
  args.data = titleJson(t);
  args.data["choices"] = choices;
  args.data["label"] = label;
  if (replace) {
    router.replace(cadence_v2_ScreenRoute_SCREEN_ROUTE_RESOLVING, args);
  } else {
    router.open(cadence_v2_ScreenRoute_SCREEN_ROUTE_RESOLVING, args);
  }
}

void open(Router& router, const Title& t, const Json& options) {
  const auto o = catalog::parseOptions(options);
  if (o.skipPicker()) {
    // the remembered choice plays straight away (section 18.4)
    Json choices = Json::object();
    for (const auto& [k, v] : o.selected) choices[k] = v;
    resolve(router, t, choices, selectionLabel(o, o.selected));
    return;
  }
  RouteArgs args;
  args.ref = t.ref;
  args.episode = t.episode;
  args.data = titleJson(t);
  args.data["options"] = options;
  router.open(cadence_v2_ScreenRoute_SCREEN_ROUTE_PICKER, args);
}

std::string episodeCard(const catalog::Item& e, const std::string& id, const std::string& action, i18n::Lang lang) {
  const auto label = catalog::episodeLabel(e);
  ui::rml::Card c;
  c.kind = ui::rml::CardKind::Wide;
  c.id = id;
  c.ref = e.ref;
  c.action = action;
  c.art = catalog::artOf(e);
  c.title = e.episodeName.empty() ? "[[episodes.episode|n=" + catalog::argOf(label) + "]]"
                                  : esc(label) + " · " + esc(e.episodeName);
  std::string meta;
  const auto add = [&](const std::string& part) {
    if (part.empty()) return;
    if (!meta.empty()) meta += " · ";
    meta += part;
  };
  if (e.watched) add(ui::rml::icon("check") + " [[episodes.watched]]");
  if (e.durationS > 0) add(esc(catalog::durationText(lang, e.durationS)));
  c.meta = meta;
  for (const auto& b : e.badges) {
    if (b.kind == "new") c.badges.push_back(ui::rml::badge(ui::rml::BadgeKind::New, {}));
  }
  const auto& type = e.episodeType;
  if (type == "ova" || type == "ona") c.badges.push_back(ui::rml::badge(ui::rml::BadgeKind::Quality, "[[episodes.tag.ova]]"));
  if (type == "special") c.badges.push_back(ui::rml::badge(ui::rml::BadgeKind::Quality, "[[episodes.tag.special]]"));
  if (type == "recap") c.badges.push_back(ui::rml::badge(ui::rml::BadgeKind::Quality, "[[episodes.tag.recap]]"));
  if (type == "movie") c.badges.push_back(ui::rml::badge(ui::rml::BadgeKind::Quality, "[[title.badge.film]]"));
  if (!c.badges.empty() && !meta.empty()) c.badges.insert(c.badges.begin(), "<span class=\"bmeta\">" + meta + "</span>");
  if (e.hasProgress && !e.watched && e.positionS > 0 && e.durationS > 0) {
    c.progress = static_cast<double>(e.positionS) / e.durationS;
  }
  auto out = ui::rml::card(c);
  if (c.art.empty()) {
    // no still: the ordinal in inter display 96 dp on ink-raised, an 8 dp stripe at the top, no fake image
    const std::string art = "<div class=\"art\">";
    if (const auto at = out.find(art); at != std::string::npos) {
      out.replace(at, art.size(), "<div class=\"art eptile\"><div class=\"stripe\"/><div class=\"num\">" + esc(label) + "</div>");
    }
  }
  if (e.watched) out.replace(out.find("class=\"card "), 12, "class=\"card watched ");
  return out;
}

}  // namespace cadence::screen::routes::play
