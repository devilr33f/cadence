#pragma once

#include "cadence/screen/v1/media.pb.h"
#include "cadence/v2/media.pb.h"

#include <cstdint>
#include <vector>

namespace cadence::screen::player {

// where mpv's sound goes (plan item 19)
enum class AoMode {
  // mpv's own output (alsa, pipewire, aaudio): the stopgap until ao_cadence lands; the player levels it itself
  Stock,
  // the patched mpv's ao_cadence into a ring audiod's mixer reads: audiod levels, ducks and equalizes it
  Cadence,
};

struct AudioRouteInfo {
  cadence_v2_AudioRoute route = cadence_v2_AudioRoute_AUDIO_ROUTE_UNSPECIFIED;
  std::vector<cadence_v2_AudioControl> controls;
  std::uint32_t controlLatencyMs = 0;
  // the player applies level, duck, mute and eq itself (mpv's volume and af)
  bool playerControls = false;
  // mpv plays no audio track at all
  bool silent = false;
};

// the route a session gets: ambient (and anything muted by purpose) is NONE, with no audio track; content is MIXER
// through ao_cadence, or DIRECT on the stock ao
AudioRouteInfo audioRouteFor(AoMode mode, cadence_screen_v1_MediaPurpose purpose);

// the build's mode (CADENCE_SCREEN_AO)
AoMode buildAoMode();

}  // namespace cadence::screen::player
