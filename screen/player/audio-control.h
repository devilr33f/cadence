#pragma once

#include <cstdint>

namespace cadence::screen::player {

// the gain a DIRECT route player (mpv's stock ao until ao_cadence lands) applies itself: level x duck x mute, with
// ramps stepped by time, behind the playback gate (rule 7). until cadenced sends a level the gain is 0, as a level
// the player cannot know must never be guessed upward
class AudioControl {
 public:
  void setGate(bool open) { gateOpen_ = open; }
  [[nodiscard]] bool gateOpen() const { return gateOpen_; }

  void setLevel(std::uint32_t level, std::uint32_t rampMs, std::int64_t nowMs);
  // active false ends the duck; level is the fraction of the level kept, clamped to 0..1
  void setDuck(bool active, float level, std::uint32_t rampMs, std::int64_t nowMs);
  void setMute(bool muted) { muted_ = muted; }

  // mpv's volume (0-100) at nowMs; 0 while the gate is shut, muted, or no level was given
  [[nodiscard]] double volume(std::int64_t nowMs) const;
  // a ramp is still moving
  [[nodiscard]] bool ramping(std::int64_t nowMs) const;
  [[nodiscard]] bool hasLevel() const { return hasLevel_; }

 private:
  struct Ramp {
    double from = 0;
    double to = 0;
    std::int64_t startMs = 0;
    std::int64_t durationMs = 0;
    [[nodiscard]] double at(std::int64_t nowMs) const;
    [[nodiscard]] bool moving(std::int64_t nowMs) const { return durationMs > 0 && nowMs < startMs + durationMs; }
  };

  bool gateOpen_ = false;
  bool muted_ = false;
  bool hasLevel_ = false;
  Ramp level_;
  Ramp duck_{1, 1, 0, 0};
};

}  // namespace cadence::screen::player
