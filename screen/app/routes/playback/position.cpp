#include "app/routes/playback/position.h"

#include <algorithm>
#include <cstdlib>

namespace cadence::screen::routes {

void PositionModel::reset() {
  last_ = {};
  has_ = false;
  target_.reset();
  seq_.reset();
  seekAtMs_ = 0;
}

bool PositionModel::reading(const player::PositionSample& s, std::int64_t nowMs) {
  last_ = s;
  has_ = true;
  if (target_ && nowMs - seekAtMs_ < kHoldMs) {
    bool done = false;
    if (seq_) {
      done = s.seekSeq >= *seq_ && !s.seekPending;
    } else {
      // an echo read after the seek went, near its target (run on to the reading's time)
      const auto expected = static_cast<std::int64_t>(*target_) +
                            static_cast<std::int64_t>(static_cast<double>(std::max<std::int64_t>(0, s.atMs - seekAtMs_)) * s.rate);
      done = s.atMs >= seekAtMs_ && std::llabs(static_cast<std::int64_t>(s.positionMs) - expected) <= kNearMs;
    }
    if (!done) return false;
  }
  target_.reset();
  seq_.reset();
  return true;
}

void PositionModel::seek(std::uint64_t targetMs, std::int64_t nowMs, std::optional<std::uint32_t> seq) {
  target_ = targetMs;
  seekAtMs_ = nowMs;
  seq_ = seq;
}

bool PositionModel::holding(std::int64_t nowMs) const { return target_.has_value() && nowMs - seekAtMs_ < kHoldMs; }

std::uint64_t PositionModel::at(std::int64_t nowMs, std::uint64_t durationMs) const {
  std::uint64_t p = 0;
  if (holding(nowMs) || (target_ && !has_)) {
    p = *target_;
  } else if (has_) {
    p = last_.positionMs;
    const auto since = std::clamp<std::int64_t>(nowMs - last_.atMs, 0, kRunOnMs);
    if (last_.rate > 0) p += static_cast<std::uint64_t>(static_cast<double>(since) * last_.rate);
  }
  if (durationMs > 0) p = std::min(p, durationMs);
  return p;
}

}  // namespace cadence::screen::routes
