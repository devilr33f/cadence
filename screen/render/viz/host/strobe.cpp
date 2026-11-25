#include "strobe.h"

#include <algorithm>
#include <cmath>
#include <vector>

namespace cadence::render::viz {

StrobeResult check_strobe(std::span<const double> luma, double fps) {
  StrobeResult out;
  if (luma.size() < 2 || fps <= 0) return out;
  // the frames where a swing completes: the series' turning points that moved at least 0.1 from the last one with a
  // dark side below 0.8
  std::vector<std::size_t> swings;
  std::size_t anchor = 0;
  int dir = 0;
  std::size_t extreme = 0;
  auto qualifies = [&](std::size_t a, std::size_t b) {
    return std::abs(luma[b] - luma[a]) >= 0.1 && std::min(luma[a], luma[b]) < 0.8;
  };
  for (std::size_t i = 1; i < luma.size(); ++i) {
    const double d = luma[i] - luma[extreme];
    const int now = d > 0 ? 1 : (d < 0 ? -1 : 0);
    if (dir == 0) {
      if (now != 0) {
        dir = now;
        extreme = i;
      }
      continue;
    }
    if (now == dir) {
      extreme = i;
    } else if (now == -dir) {
      // a turn at extreme: count the swing from anchor to it
      if (qualifies(anchor, extreme)) swings.push_back(extreme);
      anchor = extreme;
      extreme = i;
      dir = now;
    }
  }
  if (dir != 0 && qualifies(anchor, extreme)) swings.push_back(extreme);
  // two swings make a flash; slide a one-second window over the swings
  const auto window = static_cast<std::size_t>(std::lround(fps));
  std::size_t lo = 0;
  for (std::size_t hi = 0; hi < swings.size(); ++hi) {
    while (swings[hi] - swings[lo] >= window) ++lo;
    out.max_flashes_per_s = std::max(out.max_flashes_per_s, (hi - lo + 1) / 2);
  }
  out.strobing = out.max_flashes_per_s > 3;
  return out;
}

}  // namespace cadence::render::viz
