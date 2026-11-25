#pragma once

#include <cstddef>

namespace cadence::dsp {

// the pcm backend's format: float32, interleaved stereo, 48 kHz
inline constexpr std::size_t kRate = 48000;
inline constexpr double kRateHz = static_cast<double>(kRate);
inline constexpr std::size_t kStereo = 2;

constexpr std::size_t framesFor(std::size_t ms) { return kRate * ms / 1000; }

}  // namespace cadence::dsp
