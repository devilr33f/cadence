#pragma once

#include "audiod/sender-volume.h"

#include <array>
#include <cstdint>
#include <optional>

// the airplay sender's volume (M4d-D6, reference §5; v1's airplay/volume.rs ported exactly): shairport's AirplayVolume
// is the sender's slider in dB, and the receiver steps towards a volume it is set to, reporting each step
namespace cadence::audiod::airplay {

inline constexpr double kMuteDb = -144.0;
inline constexpr double kLowestDb = -30.0;
// the receiver's steps as the echo table models them, on the 0..1 slider
inline constexpr double kEchoStep = 0.09;
inline constexpr double kEchoFinalStep = 0.1;
inline constexpr double kEchoEpsilon = 1e-3;
// a whole slider takes twelve steps: the rest guards against floating-point damage, it is no second policy
inline constexpr int kEchoMaxSteps = 14;

// 0-100 for a slider position: exactly -144 (mute) is 0, [-30, 0] maps linearly; anything else (not finite, between
// mute and -30, above 0) is nullopt and never reaches cadenced
std::optional<std::uint8_t> toPercent(double db);
// 0 is -144 (mute), else 0.3·p − 30; p above 100 counts as 100
double toDb(std::uint8_t percent);

// the levels a SetAirplayVolume may still come back as, each within kEchoWindow: one expiry per percent, not a queue,
// so a late intermediate echo never evicts a live final one
class EchoTable {
 public:
  using Clock = SenderClock;

  // a write of target, sent while the receiver last reported observedDb: where it starts and each step the receiver
  // takes from there towards it, each with its neighbours; with no usable start, every level
  void dispatched(std::uint8_t target, std::optional<double> observedDb, Clock::time_point now);
  // a report that came while a write was in flight, with its neighbours
  void reportDuringWrite(std::uint8_t percent, Clock::time_point now);
  [[nodiscard]] bool isEcho(std::uint8_t percent, Clock::time_point now) const;
  void clear() { expires_.fill(std::nullopt); }

 private:
  void mark(int percent, Clock::time_point now);
  void markNeighbours(int percent, Clock::time_point now);

  std::array<std::optional<Clock::time_point>, kMaxSenderLevel + 1> expires_{};
};

}  // namespace cadence::audiod::airplay
