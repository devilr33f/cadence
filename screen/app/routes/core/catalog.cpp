#include "app/routes/core/catalog.h"

#include "i18n/locale.h"
#include "ui/images/image-cache.h"

#include <cstdio>

namespace cadence::screen::routes::catalog {

using ui::rml::esc;

namespace {

std::string str(const Json& j, const char* k) {
  const auto it = j.find(k);
  return it != j.end() && it->is_string() ? it->get<std::string>() : std::string();
}

int num(const Json& j, const char* k) {
  const auto it = j.find(k);
  return it != j.end() && it->is_number() ? it->get<int>() : 0;
}

long long big(const Json& j, const char* k) {
  const auto it = j.find(k);
  return it != j.end() && it->is_number() ? it->get<long long>() : 0;
}

double real(const Json& j, const char* k) {
  const auto it = j.find(k);
  return it != j.end() && it->is_number() ? it->get<double>() : 0;
}

bool flag(const Json& j, const char* k) {
  const auto it = j.find(k);
  return it != j.end() && it->is_boolean() && it->get<bool>();
}

const Json& obj(const Json& j, const char* k) {
  static const Json empty = Json::object();
  const auto it = j.find(k);
  return it != j.end() && it->is_object() ? *it : empty;
}

const Json& arr(const Json& j, const char* k) {
  static const Json empty = Json::array();
  const auto it = j.find(k);
  return it != j.end() && it->is_array() ? *it : empty;
}

std::vector<std::string> strings(const Json& j, const char* k) {
  std::vector<std::string> out;
  for (const auto& v : arr(j, k)) {
    if (v.is_string()) out.push_back(v.get<std::string>());
  }
  return out;
}

std::vector<Badge> badges(const Json& j) {
  std::vector<Badge> out;
  for (const auto& b : arr(j, "badges")) {
    if (b.is_object()) out.push_back({str(b, "kind"), str(b, "label")});
  }
  return out;
}

}  // namespace

Item parseItem(const Json& j) {
  Item it;
  if (!j.is_object()) return it;
  it.ref = str(j, "ref");
  it.provider = str(j, "provider");
  it.source = str(j, "source");
  it.kind = str(j, "kind");
  it.title = str(j, "title");
  it.subtitle = str(j, "subtitle");
  const auto& art = obj(j, "artwork");
  it.artwork = str(art, "hash");
  it.aspect = str(art, "aspect");
  it.durationS = num(j, "durationS");
  it.badges = badges(j);
  if (const auto& p = obj(j, "progress"); !p.empty()) {
    it.hasProgress = true;
    it.watched = flag(p, "watched");
    it.positionS = num(p, "positionS");
    it.watchedCount = num(p, "count");
    it.watchedTotal = num(p, "total");
  }
  it.playable = flag(j, "playable");
  it.hasChildren = flag(j, "hasChildren");
  it.unavailable = flag(j, "unavailable");
  if (const auto& s = obj(j, "show"); !s.empty()) {
    const auto& names = obj(s, "names");
    it.nameRu = str(names, "ru");
    it.nameEn = str(names, "en");
    it.year = num(s, "year");
    it.format = str(s, "format");
    it.episodesAired = num(s, "episodesAired");
    it.episodesTotal = num(s, "episodesTotal");
    it.status = str(s, "status");
    it.season = str(s, "season");
    it.genres = strings(s, "genres");
    it.score = real(s, "score");
    it.ageRating = str(s, "ageRating");
    it.description = str(s, "description");
    it.voiceovers = strings(s, "voiceovers");
    it.sourcesWithIt = num(s, "sourcesWithIt");
    it.rank = num(s, "rank");
    it.popularity = num(s, "popularity");
    it.airedOn = str(s, "airedOn");
    it.releasedOn = str(s, "releasedOn");
    it.studios = strings(s, "studios");
    if (const auto r = s.find("related"); r != s.end() && r->is_array()) {
      for (const auto& x : *r) {
        Related rel{str(x, "relation"), str(x, "title"), str(x, "kind"), num(x, "year")};
        if (!rel.title.empty()) it.related.push_back(rel);
      }
    }
    if (const auto& m = obj(s, "mine"); !m.empty()) {
      it.hasMine = true;
      it.mineStatus = str(m, "status");
      it.mineScore = num(m, "score");
      it.mineWatched = num(m, "watched");
    }
  }
  if (const auto& e = obj(j, "episode"); !e.empty()) {
    it.label = str(e, "label");
    it.ordinal = real(e, "ordinal");
    it.episodeName = str(e, "name");
    it.episodeType = str(e, "type");
  }
  if (const auto& m = obj(j, "music"); !m.empty()) it.artist = str(m, "artist");
  if (const auto& t = obj(j, "torrent"); !t.empty()) {
    it.sizeBytes = big(t, "sizeBytes");
    it.seeders = num(t, "seeders");
  }
  return it;
}

std::string factsOf(const Item& it) {
  std::string out;
  if (it.rank > 0) out = "[[title.rank|rank=" + std::to_string(it.rank) + "]]";
  if (it.hasMine && !it.mineStatus.empty()) {
    out += (out.empty() ? "" : " · ") + std::string("[[title.list.") + it.mineStatus + "]]";
  }
  return out;
}

std::string dayOf(const std::string& ymd) {
  if (ymd.size() < 10 || ymd[4] != '-' || ymd[7] != '-') return {};
  for (const std::size_t i : {std::size_t{0}, std::size_t{1}, std::size_t{2}, std::size_t{3}, std::size_t{5}, std::size_t{6}, std::size_t{8}, std::size_t{9}}) {
    if (ymd[i] < '0' || ymd[i] > '9') return {};
  }
  return ymd.substr(8, 2) + "." + ymd.substr(5, 2) + "." + ymd.substr(0, 4);
}

std::vector<Alternative> parseAlternatives(const Json& j) {
  std::vector<Alternative> out;
  const auto items = j.is_object() ? j.find("items") : j.end();
  if (!j.is_object() || items == j.end() || !items->is_array()) return out;
  for (const auto& x : *items) {
    Alternative a;
    a.ref = str(x, "ref");
    a.provider = str(x, "provider");
    a.source = str(x, "source");
    a.sourceName = str(x, "sourceName");
    a.title = str(x, "title");
    a.match = str(x, "match");
    a.episodesAired = num(x, "episodesAired");
    a.episodesTotal = num(x, "episodesTotal");
    a.quality = num(x, "quality");
    a.current = flag(x, "current");
    a.unavailable = flag(x, "unavailable");
    if (const auto v = x.find("voiceovers"); v != x.end() && v->is_array()) {
      for (const auto& o : *v) {
        const auto l = str(o, "label");
        if (!l.empty()) a.voiceovers.push_back(l);
      }
    }
    if (!a.ref.empty()) out.push_back(std::move(a));
  }
  return out;
}

std::vector<Item> parseItems(const Json& j) {
  std::vector<Item> out;
  if (!j.is_array()) return out;
  for (const auto& i : j) out.push_back(parseItem(i));
  return out;
}

std::vector<Action> parseActions(const Json& j) {
  std::vector<Action> out;
  if (!j.is_array()) return out;
  for (const auto& a : j) {
    if (!a.is_object()) continue;
    Action x;
    x.id = str(a, "id");
    x.kind = str(a, "kind");
    x.label = str(a, "label");
    x.icon = str(a, "icon");
    x.toggled = flag(a, "toggled");
    x.enabled = !a.contains("enabled") || flag(a, "enabled");
    x.value = str(a, "value");
    x.values = strings(a, "values");
    out.push_back(std::move(x));
  }
  return out;
}

Detail parseDetail(const Json& j) {
  Detail d;
  d.item = parseItem(obj(j, "item"));
  d.children = parseItems(arr(j, "children"));
  d.next = str(j, "next");
  for (const auto& g : arr(j, "childGroups")) {
    if (g.is_object()) d.childGroups.push_back({str(g, "id"), str(g, "label"), num(g, "count")});
  }
  d.variants = parseItems(arr(j, "variants"));
  d.selected = str(j, "selected");
  if (const auto& c = obj(j, "continue"); !c.empty()) {
    d.continueEpisode = str(c, "episode");
    d.continuePositionS = num(c, "positionS");
  }
  d.actions = parseActions(arr(j, "actions"));
  return d;
}

Options parseOptions(const Json& j) {
  Options o;
  for (const auto& g : arr(j, "groups")) {
    if (!g.is_object()) continue;
    ChoiceGroup group;
    group.kind = str(g, "kind");
    group.label = str(g, "label");
    group.hidden = num(g, "hidden");
    for (const auto& c : arr(g, "choices")) {
      if (!c.is_object()) continue;
      Choice ch;
      ch.id = str(c, "id");
      ch.label = str(c, "label");
      ch.detail = str(c, "detail");
      ch.badges = badges(c);
      ch.unavailable = flag(c, "unavailable");
      ch.reason = str(c, "reason");
      if (const auto& s = obj(c, "stream"); !s.empty()) {
        ch.stream = true;
        ch.translator = str(s, "translator");
        ch.transType = str(s, "transType");
        ch.language = str(s, "language");
        ch.quality = num(s, "quality");
      }
      if (const auto& t = obj(c, "torrent"); !t.empty() || c.contains("torrent")) {
        ch.torrent = c.contains("torrent");
        ch.episodes = str(t, "episodes");
        ch.sizeBytes = big(t, "sizeBytes");
        ch.seeders = num(t, "seeders");
        ch.quality = ch.quality != 0 ? ch.quality : num(t, "quality");
        ch.codec = str(t, "codec");
        ch.hardsub = flag(t, "hardsub");
      }
      group.choices.push_back(std::move(ch));
    }
    o.groups.push_back(std::move(group));
  }
  if (const auto& s = obj(j, "selected"); !s.empty()) {
    for (auto it = s.begin(); it != s.end(); ++it) {
      if (it->is_string()) o.selected[it.key()] = it->get<std::string>();
    }
  }
  o.remembered = strings(j, "remembered");
  return o;
}

const ChoiceGroup* Options::group(const std::string& kind) const {
  for (const auto& g : groups) {
    if (g.kind == kind) return &g;
  }
  return nullptr;
}

bool Options::remembers(const std::string& kind) const {
  for (const auto& r : remembered) {
    if (r == kind) return true;
  }
  return false;
}

std::string providerMonogram(const std::string& provider) {
  if (provider == "torrplayer") return "tp";
  if (provider == "torrserver") return "TS";
  if (provider == "ytmusic") return "YM";
  if (provider == "spotify") return "SP";
  return provider.substr(0, 2);
}

std::string providerName(const std::string& provider) {
  if (provider == "torrplayer") return "torrplayer";
  if (provider == "torrserver") return "TorrServer";
  if (provider == "ytmusic") return "YouTube Music";
  if (provider == "spotify") return "Spotify";
  return provider;
}

std::string providerBadge(const std::string& provider) {
  return ui::rml::badge(ui::rml::BadgeKind::Provider, esc(providerName(provider)), esc(providerMonogram(provider)));
}

ui::rml::CardKind cardKindOf(const Item& item) {
  using K = ui::rml::CardKind;
  if (item.aspect == "poster-jp") return K::PosterJp;
  if (item.aspect == "poster") return K::Poster;
  if (item.aspect == "wide") return K::Wide;
  if (item.aspect == "square") return K::Square;
  const auto& k = item.kind;
  if (k == "track" || k == "album" || k == "artist" || k == "playlist") return K::Square;
  if (k == "video" || k == "live" || k == "channel" || k == "episode") return K::Wide;
  if (item.provider == "torrplayer") return K::PosterJp;
  if (item.provider == "ytmusic" || item.provider == "spotify") return K::Square;
  return K::Poster;
}

std::string metaOf(const Item& it) {
  std::string out;
  const auto add = [&](const std::string& part) {
    if (part.empty()) return;
    if (!out.empty()) out += " · ";
    out += part;
  };
  if (it.kind == "title_group" || it.kind == "series" || it.kind == "season" || it.kind == "movie") {
    if (it.year > 0) add(std::to_string(it.year));
    add(esc(it.format));
    const int eps = it.episodesTotal > 0 ? it.episodesTotal : it.episodesAired;
    if (eps > 0 && it.kind != "movie") add("[[catalog.episodes_short|count=" + std::to_string(eps) + "]]");
    if (out.empty()) add(esc(it.subtitle));
    return out;
  }
  if (it.kind == "track" || it.kind == "album") {
    add(esc(!it.artist.empty() ? it.artist : it.subtitle));
    return out;
  }
  add(esc(it.subtitle));
  if (it.durationS > 0 && (it.kind == "video" || it.kind == "episode")) {
    char buf[16];
    std::snprintf(buf, sizeof buf, "%d:%02d", it.durationS / 60, it.durationS % 60);
    add(buf);
  }
  return out;
}

std::string sourcesLineOf(const Item& it, const std::string& sourceName) {
  if (it.unavailable) return "[[catalog.not_on_sources]]";
  if (it.provider != "torrplayer") return {};
  const std::string mono = esc(providerMonogram(it.provider));
  if (it.kind == "title_group" && it.sourcesWithIt > 1) {
    return mono + " · [[search.on_sources|count=" + std::to_string(it.sourcesWithIt) + "]]";
  }
  if (!sourceName.empty()) return mono + " · " + esc(sourceName);
  if (!it.source.empty()) return mono + " · " + esc(it.source);
  return {};
}

std::string artOf(const Item& item) {
  return ui::images::validId(item.artwork) ? "img:" + item.artwork : std::string();
}

ui::rml::Card cardOf(const Item& item, const std::string& id, const std::string& action, const std::string& sourceName) {
  ui::rml::Card c;
  c.kind = cardKindOf(item);
  c.id = id;
  c.ref = item.ref;
  c.title = esc(!item.nameRu.empty() ? item.nameRu : item.title);
  c.meta = metaOf(item);
  c.meta2 = sourcesLineOf(item, sourceName);
  c.art = artOf(item);
  c.action = action;
  c.unavailable = false;
  if (item.hasProgress && item.positionS > 0 && item.durationS > 0) {
    c.progress = static_cast<double>(item.positionS) / item.durationS;
  }
  return c;
}

std::string idOf(const std::string& prefix, const std::string& ref) {
  // fnv-1a: refs are opaque and may hold anything; an id may not
  std::uint64_t h = 1469598103934665603ULL;
  for (char ch : ref) {
    h ^= static_cast<unsigned char>(ch);
    h *= 1099511628211ULL;
  }
  char buf[24];
  std::snprintf(buf, sizeof buf, "%016llx", static_cast<unsigned long long>(h));
  return prefix + "-" + buf;
}

std::string argOf(const std::string& value) {
  std::string out;
  for (char c : value) {
    if (c == '|' || c == '[' || c == ']' || c == '<' || c == '>' || c == '&' || c == '"') continue;
    out += c;
  }
  return out;
}

std::string episodeLabel(const Item& e) {
  if (!e.label.empty()) return e.label;
  if (e.ordinal > 0) return i18n::plain_number(e.ordinal);
  return {};
}

std::string durationText(i18n::Lang lang, int seconds) { return i18n::duration(lang, seconds); }

}  // namespace cadence::screen::routes::catalog
