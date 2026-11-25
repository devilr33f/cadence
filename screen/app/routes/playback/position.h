#pragma once

#include "player/player.h"

#include <cstdint>
#include <optional>

namespace cadence::screen::routes {

// where the bar shows playback: the player's (or cadenced's) last reading run on at its rate, and, once the screen
// asked for a seek, that seek's target held until a reading of it arrives, so no reading taken before the seek ever
// moves the bar back. rapid presses add up from the held target, never from a stale reading
class PositionModel {
 public:
  // the screen's own player marks each reading with the seek it is of: a seek counts as done once a reading of it
  // (or a later one) is no longer pending. cadenced's echoes carry no seek: one counts once it was read after the
  // seek went and lands near its target. either way the hold ends after kHoldMs, whatever comes
  static constexpr std::int64_t kHoldMs = 8000;
  // how near the target an echo must land (a keyframe seek, the time the echo took)
  static constexpr std::int64_t kNearMs = 3000;
  // a reading is run on at most this long (cadenced's echoes come on changes alone, so a minute, as before)
  static constexpr std::int64_t kRunOnMs = 60000;

  void reset();
  // a reading; false when the held seek dropped it
  bool reading(const player::PositionSample& s, std::int64_t nowMs);
  // the screen asked for a seek to targetMs; seq is the player's seek it became (PlayerState::seekSeq), none for a
  // seek cadenced carries out
  void seek(std::uint64_t targetMs, std::int64_t nowMs, std::optional<std::uint32_t> seq);
  // the position at nowMs, clamped to durationMs when known
  [[nodiscard]] std::uint64_t at(std::int64_t nowMs, std::uint64_t durationMs = 0) const;
  // a seek is held (its target is what at() says)
  [[nodiscard]] bool holding(std::int64_t nowMs) const;
  // a relative step's base: the held target, else the position now
  [[nodiscard]] std::uint64_t base(std::int64_t nowMs, std::uint64_t durationMs = 0) const { return at(nowMs, durationMs); }

 private:
  // the newest reading, taken or not: once a hold ran out it is what the bar shows
  player::PositionSample last_;
  bool has_ = false;
  std::optional<std::uint64_t> target_;
  std::int64_t seekAtMs_ = 0;
  std::optional<std::uint32_t> seq_;
};

}  // namespace cadence::screen::routes
