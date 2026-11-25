#pragma once

#include "app/routes/core/catalog.h"
#include "app/routes/core/router.h"

#include <string>

// opening a title's episode (design r2 sections 18.4-18.5): with a remembered voiceover (or torrent) the picker is
// skipped and resolving starts at once; else the picker opens on the options already fetched. and the episode tile
// the title page and the episodes grid share
namespace cadence::screen::routes::play {

// what the picker and resolving show of the title
struct Title {
  std::string ref;
  std::string episode;
  std::string title;
  std::string art;
  std::string provider;
  std::string kind;
};

Json titleJson(const Title& t);
Title titleOf(const RouteArgs& args);

// the picker, or resolving when the options remember a choice
void open(Router& router, const Title& t, const Json& options);
// resolving with choices; the picker's own path (OK on a quality chip)
void resolve(Router& router, const Title& t, const Json& choices, const std::string& label, bool replace = false);

// «AniLibria · 1080p»: the selection's words
std::string selectionLabel(const catalog::Options& o, const std::map<std::string, std::string>& selection);

// an episode as a wide card: a numbered tile when it has no still (section 18.3), watched at 60 % with its word,
// tags for new, ova, special, recap and film
std::string episodeCard(const catalog::Item& e, const std::string& id, const std::string& action, i18n::Lang lang);

}  // namespace cadence::screen::routes::play
