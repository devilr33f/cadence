#include "audiod/airplay-volume.h"

#include <algorithm>
#include <cmath>

namespace cadence::audiod::airplay {

namespace {

// the slider's position, 0..1, that the echo walk steps on
std::optional<double> unit(double db) {
  if (!toPercent(db))
    return std::nullopt;
  if (db == kMuteDb)
    return 0.0;
  return (db - kLowestDb) / -kLowestDb;
}

int rounded(double position) { return static_cast<int>(std::clamp(std::round(position * 100.0), 0.0, 100.0)); }

}  // namespace

std::optional<std::uint8_t> toPercent(double db) {
  if (!std::isfinite(db))
    return std::nullopt;
  if (db == kMuteDb)
    return 0;
  if (db < kLowestDb || db > 0.0)
    return std::nullopt;
  return static_cast<std::uint8_t>(std::round((db - kLowestDb) * 100.0 / -kLowestDb));
}

double toDb(std::uint8_t percent) {
  const auto level = std::min(percent, kMaxSenderLevel);
  if (level == 0)
    return kMuteDb;
  return 0.3 * static_cast<double>(level) + kLowestDb;
}

void EchoTable::mark(int percent, Clock::time_point now) {
  expires_[static_cast<std::size_t>(std::clamp(percent, 0, static_cast<int>(kMaxSenderLevel)))] = now + kEchoWindow;
}

void EchoTable::markNeighbours(int percent, Clock::time_point now) {
  mark(percent, now);
  if (percent > 0)
    mark(percent - 1, now);
  if (percent < static_cast<int>(kMaxSenderLevel))
    mark(percent + 1, now);
}

// models the receiver stepping towards what it is set to; the caller marks what comes while the write is in flight
// too, and keeps the table whether the write succeeds, fails or times out
void EchoTable::dispatched(std::uint8_t target, std::optional<double> observedDb, Clock::time_point now) {
  const int end = std::min(target, kMaxSenderLevel);
  const auto start = observedDb ? unit(*observedDb) : std::nullopt;
  if (!start) {
    expires_.fill(now + kEchoWindow);
    return;
  }
  const double goal = end / 100.0;
  markNeighbours(rounded(*start), now);
  double present = *start;
  for (int step = 0; step < kEchoMaxSteps; ++step) {
    if (std::abs(goal - present) <= kEchoEpsilon)
      break;
    if (std::abs(goal - present) < kEchoFinalStep)
      present = goal;
    else if (goal > present)
      present += kEchoStep;
    else
      present -= kEchoStep;
    markNeighbours(rounded(present), now);
  }
  markNeighbours(end, now);
}

void EchoTable::reportDuringWrite(std::uint8_t percent, Clock::time_point now) { markNeighbours(percent, now); }

bool EchoTable::isEcho(std::uint8_t percent, Clock::time_point now) const {
  const auto& expiry = expires_[std::min(percent, kMaxSenderLevel)];
  return expiry && now < *expiry;
}

}  // namespace cadence::audiod::airplay
