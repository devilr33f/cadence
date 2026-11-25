#pragma once

#include <cstddef>

namespace cadence {

inline constexpr int kOpusSampleRate = 16000;
inline constexpr int kOpusFrameMs = 20;
// each binary frame is one opus packet of 1 to this many bytes
inline constexpr std::size_t kMaxAudioFrameBytes = 65536;

}  // namespace cadence
