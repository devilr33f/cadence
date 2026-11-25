#pragma once

#include <cstdint>

namespace cadence::audiod {

// 1, 10, 100, ...: a fault that repeats per packet is logged as it grows, not per packet
inline bool sparse(std::uint64_t count) {
  while (count >= 10 && count % 10 == 0)
    count /= 10;
  return count == 1;
}

}  // namespace cadence::audiod
