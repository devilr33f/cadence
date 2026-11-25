#pragma once

#include <cstdint>
#include <vector>

namespace cadence {

enum class RingKind { Alarm, Timer, Reminder };

// one period of a ring at 48 kHz mono, to play in a loop: an alarm's is 2 s, three rising tones then silence; a
// timer's is 1 s, two beeps then silence; a reminder's is 1.2 s, a soft two-note chime that fades as a bell does. our own synthesis, never louder than kEarconPeak, and zero on the first
// and last sample so the loop joins without a click. pure: the same samples on every call
std::vector<std::int16_t> ringPeriodPcm(RingKind kind);

}  // namespace cadence
