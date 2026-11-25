#pragma once

#include <algorithm>
#include <cstdint>

namespace cadence::dsp {

// the one curve from a cadence level (0-100, what people see and set) to the mixer's linear gain. every software
// gain audiod applies for a level goes through it, and no level is ever handed to a player's own volume on the mixer
// route (mpv's volume is cubic: 30 is about -31 dB), so a level sounds the same whoever renders it
constexpr float levelGain(std::uint32_t level) { return static_cast<float>(std::min<std::uint32_t>(level, 100)) / 100.0f; }

}  // namespace cadence::dsp
