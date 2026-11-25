#pragma once

#include "app/routes/playback/session.h"
#include "player/player.h"

#include <string>
#include <string_view>
#include <vector>

namespace cadence::screen::routes {

// a codec as the osd names it: mpv's h264 reads H.264, hevc HEVC, eac3 E-AC-3; an unknown name in capitals
std::string codecName(std::string_view mpvCodec);

// the selected video's and audio's codecs, «HEVC · AAC»; either alone, or "" with neither
std::string codecLine(const std::vector<player::Track>& tracks);

// the provider badge's name: a torrplayer play names the source it came from (anime365, aniliberty), as the
// continue row's captions do; any other its provider's name
std::string providerLabel(const Session& s);

}  // namespace cadence::screen::routes
