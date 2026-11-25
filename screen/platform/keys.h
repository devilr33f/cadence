#pragma once

#include "cadence/screen/v1/ui.pb.h"

#include <SDL3/SDL_events.h>

#include <cstdint>
#include <optional>
#include <unordered_map>

namespace cadence::screen::platform {

// the remote's keys as sdl gives them (android's keycodes through SDLActivity, a keyboard, cec through the os) as
// screen/v1 keys; nullopt for keys the screen ignores
std::optional<cadence_screen_v1_Key> keyOf(SDL_Keycode key);

struct KeyEvent {
  cadence_screen_v1_Key key = cadence_screen_v1_Key_KEY_UNSPECIFIED;
  cadence_screen_v1_KeyAction action = cadence_screen_v1_KeyAction_KEY_ACTION_UNSPECIFIED;
  cadence_screen_v1_InputSource source = cadence_screen_v1_InputSource_INPUT_SOURCE_REMOTE;
};

// down, repeat and up per key, with a long press (KEY_ACTION_LONG) once a key is held for longMs. a long BACK is the
// assistant (the remote's mic button is not a key on the stick: s4): it comes out as KEY_ASSISTANT LONG, and its
// up is swallowed, so the page behind never sees a BACK; held past hintMs it says so first as a KEY_BACK DOWN, so the
// ui can fade in the assistant's mic glyph (design r2 section 7) before the long press lands
class KeyTracker {
 public:
  explicit KeyTracker(std::int64_t longMs = 500, std::int64_t hintMs = 250) : longMs_(longMs), hintMs_(hintMs) {}

  // a key went down (repeat: the os's auto-repeat) at nowMs
  std::optional<KeyEvent> down(cadence_screen_v1_Key key, bool repeat, std::int64_t nowMs);
  std::optional<KeyEvent> up(cadence_screen_v1_Key key, std::int64_t nowMs);
  // keys held past longMs since the last call: their LONG events
  std::optional<KeyEvent> poll(std::int64_t nowMs);
  // ms until the next long press is due, or -1 when no key is held
  [[nodiscard]] std::int64_t nextDueMs(std::int64_t nowMs) const;

 private:
  struct Held {
    std::int64_t sinceMs = 0;
    bool longSent = false;
    bool hinted = false;
  };
  std::int64_t longMs_;
  std::int64_t hintMs_;
  std::unordered_map<int, Held> held_;
};

}  // namespace cadence::screen::platform
