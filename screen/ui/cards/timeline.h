#pragma once

#include <cstdint>
#include <optional>
#include <string>

// a slider's timeline (feature slider.timeline): a playback position the device runs from its anchor, so a playing
// session's card needs no revision a second. pure: no rmlui, no nanopb
namespace cadence::screen::ui::cards {

// seconds as the player's own bar shows them: "m:ss", "h:mm:ss" past an hour
std::string clockText(double seconds);
// a timeline slider's label: "1:23 / 3:45"
std::string timelineText(double value, double max);

// a timeline's state on the device: the server's value at its anchor, and the user's choice while the remote moves
// it (and after, until the next revision brings a new anchor or a refusal rolls it back)
struct Timeline {
  double value = 0;
  double min = 0;
  double max = 0;
  // epoch ms at which value held; 0 is none (the value stands)
  std::int64_t anchorAtMs = 0;
  // seconds of value per second of time: 1 playing, 0 paused or buffering
  double rate = 0;
  std::optional<double> held;

  // where it stands at nowMs: the held choice, else value + (now - anchor) / 1000 * rate within [min, max]
  [[nodiscard]] double at(std::int64_t nowMs) const;
  // whether it moves on its own now (not held, a rate, short of its end at nowMs)
  [[nodiscard]] bool running(std::int64_t nowMs) const;
  // the remote moves it: it stops at v (within [min, max])
  void hold(double v);
  // a refusal: back to the running value
  void release() { held.reset(); }
};

}  // namespace cadence::screen::ui::cards
