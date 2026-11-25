#include "ui/cards/timeline.h"

#include <algorithm>
#include <cmath>
#include <cstdio>

namespace cadence::screen::ui::cards {

std::string clockText(double seconds) {
  const auto s = std::isfinite(seconds) && seconds > 0 ? static_cast<long long>(std::floor(seconds)) : 0LL;
  char buf[32];
  if (s >= 3600) {
    std::snprintf(buf, sizeof buf, "%lld:%02lld:%02lld", s / 3600, s / 60 % 60, s % 60);
  } else {
    std::snprintf(buf, sizeof buf, "%lld:%02lld", s / 60, s % 60);
  }
  return buf;
}

std::string timelineText(double value, double max) { return clockText(value) + " / " + clockText(max); }

double Timeline::at(std::int64_t nowMs) const {
  const double lo = min, hi = std::max(min, max);
  if (held) return std::clamp(*held, lo, hi);
  double v = value;
  if (anchorAtMs > 0 && rate > 0 && std::isfinite(rate)) {
    v += static_cast<double>(std::max<std::int64_t>(0, nowMs - anchorAtMs)) / 1000.0 * rate;
  }
  return std::clamp(v, lo, hi);
}

bool Timeline::running(std::int64_t nowMs) const {
  return !held && anchorAtMs > 0 && rate > 0 && std::isfinite(rate) && at(nowMs) < max;
}

void Timeline::hold(double v) { held = std::clamp(v, min, std::max(min, max)); }

}  // namespace cadence::screen::ui::cards
