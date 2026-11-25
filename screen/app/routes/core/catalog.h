#pragma once

#include "app/routes/core/rpc.h"
#include "i18n/lang.h"
#include "ui/core/rml.h"

#include <map>
#include <optional>
#include <string>
#include <vector>

// the catalog's answers (server/internal/catalog's views as json) as the screens read them, and how they look on a
// card. every text field is third-party data: it reaches rml only through esc()
namespace cadence::screen::routes::catalog {

struct Badge {
  std::string kind;
  std::string label;
};

// a title Shikimori relates to another: its relation («Продолжение»), name, kind and year
struct Related {
  std::string relation;
  std::string title;
  std::string kind;
  int year = 0;
};

struct Item {
  std::string ref;
  std::string provider;
  std::string source;
  std::string kind;
  std::string title;
  std::string subtitle;
  std::string artwork;
  std::string aspect;
  int durationS = 0;
  std::vector<Badge> badges;
  bool watched = false;
  int positionS = 0;
  int watchedCount = 0;
  int watchedTotal = 0;
  bool hasProgress = false;
  bool playable = false;
  bool hasChildren = false;
  bool unavailable = false;
  // show
  std::string nameRu;
  std::string nameEn;
  int year = 0;
  std::string format;
  int episodesAired = 0;
  int episodesTotal = 0;
  std::string status;
  std::string season;
  std::vector<std::string> genres;
  double score = 0;
  std::string ageRating;
  std::string description;
  std::vector<std::string> voiceovers;
  int sourcesWithIt = 0;
  // Shikimori's and MyAnimeList's facts, on a title's page
  int rank = 0;
  int popularity = 0;
  std::string airedOn;
  std::string releasedOn;
  std::vector<std::string> studios;
  std::vector<Related> related;
  // the owner's own list entry: hasMine says there is one
  bool hasMine = false;
  std::string mineStatus;
  int mineScore = 0;
  int mineWatched = 0;
  // episode
  std::string label;
  double ordinal = 0;
  std::string episodeName;
  std::string episodeType;
  // music
  std::string artist;
  // torrent
  long long sizeBytes = 0;
  int seeders = 0;
};
Item parseItem(const Json& j);
// a title's rank, popularity and the owner's own list status for a hero's line («#148 на MAL · Смотрю»), "" for none
std::string factsOf(const Item& it);
// a yyyy-mm-dd day as dd.mm.yyyy, "" for anything else
std::string dayOf(const std::string& ymd);

// another source of a title (catalog.alternatives)
struct Alternative {
  std::string ref;
  std::string provider;
  std::string source;
  std::string sourceName;
  std::string title;
  std::string match;
  int episodesAired = 0;
  int episodesTotal = 0;
  std::vector<std::string> voiceovers;
  int quality = 0;
  bool current = false;
  bool unavailable = false;
};
std::vector<Alternative> parseAlternatives(const Json& j);
std::vector<Item> parseItems(const Json& j);

struct SourceName {
  std::string provider;
  std::string source;
  std::string name;
};

struct ChildGroup {
  std::string id;
  std::string label;
  int count = 0;
};

struct Action {
  std::string id;
  std::string kind;
  std::string label;
  std::string icon;
  bool toggled = false;
  bool enabled = true;
  std::string value;
  std::vector<std::string> values;
};

struct Detail {
  Item item;
  std::vector<Item> children;
  std::string next;
  std::vector<ChildGroup> childGroups;
  std::vector<Item> variants;
  std::string selected;
  std::string continueEpisode;
  int continuePositionS = 0;
  std::vector<Action> actions;
};
Detail parseDetail(const Json& j);
std::vector<Action> parseActions(const Json& j);

struct Choice {
  std::string id;
  std::string label;
  std::string detail;
  std::vector<Badge> badges;
  bool unavailable = false;
  std::string reason;
  // a stream's
  bool stream = false;
  std::string translator;
  std::string transType;
  std::string language;
  int quality = 0;
  // a torrent's
  bool torrent = false;
  std::string episodes;
  long long sizeBytes = 0;
  int seeders = 0;
  std::string codec;
  bool hardsub = false;
};

struct ChoiceGroup {
  std::string kind;
  std::string label;
  std::vector<Choice> choices;
  int hidden = 0;
};

struct Options {
  std::vector<ChoiceGroup> groups;
  std::map<std::string, std::string> selected;
  std::vector<std::string> remembered;
  [[nodiscard]] const ChoiceGroup* group(const std::string& kind) const;
  [[nodiscard]] bool remembers(const std::string& kind) const;
  // the picker is skipped: the remembered choice plays straight away (design r2 section 18.4)
  [[nodiscard]] bool skipPicker() const { return remembers("voiceover") || remembers("torrent"); }
};
Options parseOptions(const Json& j);

// the provider badge: a neutral pill with a two-letter monogram and the plain name (design r2 section 6)
std::string providerBadge(const std::string& provider);
std::string providerMonogram(const std::string& provider);
std::string providerName(const std::string& provider);

// the card shape a row of these items takes: torrplayer's anime 1:1.414, films 2:3, youtube 16:9, music square
ui::rml::CardKind cardKindOf(const Item& item);

// the meta line under a card: «2023 · TV · 24 эп.» for a show; and the second one, «tp · на 5 источниках»
std::string metaOf(const Item& item);
std::string sourcesLineOf(const Item& item, const std::string& sourceName);

// an item as a card: id is its element's id (stable across rebuilds), action what OK does
ui::rml::Card cardOf(const Item& item, const std::string& id, const std::string& action,
                     const std::string& sourceName = {});

// "img:<id>" for an item's artwork, "" without one
std::string artOf(const Item& item);

// an element id from a ref (ids take [A-Za-z0-9_-] only)
std::string idOf(const std::string& prefix, const std::string& ref);
// a value safe inside [[key|name=value]] (no |, [ or ])
std::string argOf(const std::string& value);

// an episode label as the episodes grid and the buttons print it ("5.5" stays "5.5")
std::string episodeLabel(const Item& episode);


// seconds as «1 ч 4 мин»
std::string durationText(i18n::Lang lang, int seconds);

}  // namespace cadence::screen::routes::catalog
