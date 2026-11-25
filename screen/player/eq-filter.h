#pragma once

#include "player/player.h"

#include <optional>
#include <string>

namespace cadence::screen::player {

// SetEq as mpv's af (a lavfi chain of ffmpeg's biquads, labelled so a new eq replaces the old): the empty string
// clears it; nullopt when a band or the preset is invalid (an unknown preset, a frequency out of 20-20000 Hz, gain
// beyond +-24 dB, q outside 0.1-10)
std::optional<std::string> eqFilter(const EqRequest& eq);

// the label the eq's filter carries in mpv's af list
inline constexpr const char* kEqLabel = "cadence_eq";

}  // namespace cadence::screen::player
