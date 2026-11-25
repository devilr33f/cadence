#pragma once

#include <optional>
#include <string>
#include <string_view>

namespace cadence::url {

// the StreamTitle of an icy metadata packet ("StreamTitle='Artist - Title';StreamUrl='...';"), when it carries one;
// the title may hold apostrophes of its own, so it runs to the "';" that closes it
std::optional<std::string> icyStreamTitle(std::string_view packet);

struct IcyTitle {
  std::string artist;
  std::string title;
};

// "Artist - Title" split on the first " - "; without one it is all title and no artist
IcyTitle splitIcyTitle(std::string_view streamTitle);

}  // namespace cadence::url
