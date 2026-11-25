#pragma once

#include "playback/jitter-buffer.h"

#include <cstddef>
#include <cstdint>
#include <vector>

namespace cadence::test_support {

// 100 ms and 500 ms of 48 kHz mono
inline constexpr std::size_t kTarget = 4800;
inline constexpr std::size_t kMaximum = 24000;
inline constexpr std::size_t kRead = 480;

// reads count samples in playback-sized pieces
inline std::vector<std::int16_t> readSamples(JitterBuffer& buffer, std::size_t count) {
  std::vector<std::int16_t> heard;
  std::vector<std::int16_t> piece(kRead);
  while (heard.size() < count) {
    buffer.read(piece);
    heard.insert(heard.end(), piece.begin(), piece.end());
  }
  heard.resize(count);
  return heard;
}

}  // namespace cadence::test_support
