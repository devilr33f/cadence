#include "player/audio-control.h"

#include <algorithm>
#include <cmath>

namespace cadence::screen::player {

double AudioControl::Ramp::at(std::int64_t nowMs) const {
  if (durationMs <= 0 || nowMs >= startMs + durationMs) return to;
  if (nowMs <= startMs) return from;
  const double t = static_cast<double>(nowMs - startMs) / static_cast<double>(durationMs);
  return from + (to - from) * t;
}

void AudioControl::setLevel(std::uint32_t level, std::uint32_t rampMs, std::int64_t nowMs) {
  const double target = std::min<double>(level, 100.0);
  // the first level applies at once: the gain was 0 before it, so there is no audible level to ramp from
  const double from = hasLevel_ ? level_.at(nowMs) : target;
  level_ = Ramp{from, target, nowMs, hasLevel_ ? rampMs : 0};
  hasLevel_ = true;
}

void AudioControl::setDuck(bool active, float level, std::uint32_t rampMs, std::int64_t nowMs) {
  double target = 1.0;
  if (active) target = std::isnan(level) ? 0.0 : std::clamp(static_cast<double>(level), 0.0, 1.0);
  duck_ = Ramp{duck_.at(nowMs), target, nowMs, rampMs};
}

double AudioControl::volume(std::int64_t nowMs) const {
  if (!gateOpen_ || muted_ || !hasLevel_) return 0;
  return std::clamp(level_.at(nowMs) * duck_.at(nowMs), 0.0, 100.0);
}

bool AudioControl::ramping(std::int64_t nowMs) const { return level_.moving(nowMs) || duck_.moving(nowMs); }

}  // namespace cadence::screen::player
