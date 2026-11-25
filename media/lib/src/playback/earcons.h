#pragma once

#include <cstdint>
#include <vector>

namespace cadence {

inline constexpr int kEarconRate = 48000;
inline constexpr int kEarconPeak = 8200;

enum class Earcon {
  // two rising notes: a session started
  Wake,
  // two falling notes: the request cannot be served
  Failure,
};

// 48 kHz mono samples of a short tone pair. pure: the same samples on every call
std::vector<std::int16_t> earconPcm(Earcon earcon);

}  // namespace cadence
