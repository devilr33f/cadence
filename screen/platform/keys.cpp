#include "platform/keys.h"

#include <SDL3/SDL_keycode.h>

namespace cadence::screen::platform {

std::optional<cadence_screen_v1_Key> keyOf(SDL_Keycode key) {
  switch (key) {
    case SDLK_UP: return cadence_screen_v1_Key_KEY_UP;
    case SDLK_DOWN: return cadence_screen_v1_Key_KEY_DOWN;
    case SDLK_LEFT: return cadence_screen_v1_Key_KEY_LEFT;
    case SDLK_RIGHT: return cadence_screen_v1_Key_KEY_RIGHT;
    // the remote's ok arrives as return (s4), a d-pad centre as select
    case SDLK_RETURN:
    case SDLK_KP_ENTER:
    case SDLK_SELECT:
      return cadence_screen_v1_Key_KEY_OK;
    case SDLK_ESCAPE:
    case SDLK_AC_BACK:
    case SDLK_BACKSPACE:
      return cadence_screen_v1_Key_KEY_BACK;
    case SDLK_AC_HOME: return cadence_screen_v1_Key_KEY_HOME;
    case SDLK_MENU: return cadence_screen_v1_Key_KEY_MENU;
    // a keyboard's g stands for the remote's assistant button
    case SDLK_G: return cadence_screen_v1_Key_KEY_ASSISTANT;
    case SDLK_MEDIA_PLAY_PAUSE: return cadence_screen_v1_Key_KEY_PLAY_PAUSE;
    case SDLK_MEDIA_PLAY: return cadence_screen_v1_Key_KEY_PLAY;
    case SDLK_MEDIA_PAUSE: return cadence_screen_v1_Key_KEY_PAUSE;
    case SDLK_MEDIA_STOP: return cadence_screen_v1_Key_KEY_STOP;
    case SDLK_MEDIA_NEXT_TRACK: return cadence_screen_v1_Key_KEY_NEXT;
    case SDLK_MEDIA_PREVIOUS_TRACK: return cadence_screen_v1_Key_KEY_PREVIOUS;
    case SDLK_MEDIA_FAST_FORWARD: return cadence_screen_v1_Key_KEY_FAST_FORWARD;
    case SDLK_MEDIA_REWIND: return cadence_screen_v1_Key_KEY_REWIND;
    case SDLK_VOLUMEUP: return cadence_screen_v1_Key_KEY_VOLUME_UP;
    case SDLK_VOLUMEDOWN: return cadence_screen_v1_Key_KEY_VOLUME_DOWN;
    case SDLK_MUTE: return cadence_screen_v1_Key_KEY_VOLUME_MUTE;
    case SDLK_POWER: return cadence_screen_v1_Key_KEY_POWER;
    default: return std::nullopt;
  }
}

std::optional<KeyEvent> KeyTracker::down(cadence_screen_v1_Key key, bool repeat, std::int64_t nowMs) {
  auto it = held_.find(key);
  if (it == held_.end()) {
    held_[key] = Held{nowMs, false};
    // back waits: a short one is a back at its up, a long one the assistant
    if (key == cadence_screen_v1_Key_KEY_BACK) return std::nullopt;
    return KeyEvent{key, cadence_screen_v1_KeyAction_KEY_ACTION_DOWN};
  }
  if (!repeat || key == cadence_screen_v1_Key_KEY_BACK) return std::nullopt;
  if (auto l = poll(nowMs)) return l;
  return KeyEvent{key, cadence_screen_v1_KeyAction_KEY_ACTION_REPEAT};
}

std::optional<KeyEvent> KeyTracker::up(cadence_screen_v1_Key key, std::int64_t nowMs) {
  auto it = held_.find(key);
  if (it == held_.end()) return std::nullopt;
  const Held held = it->second;
  held_.erase(it);
  if (key == cadence_screen_v1_Key_KEY_BACK) {
    if (held.longSent) return std::nullopt;
    if (nowMs - held.sinceMs >= longMs_) {
      return KeyEvent{cadence_screen_v1_Key_KEY_ASSISTANT, cadence_screen_v1_KeyAction_KEY_ACTION_LONG};
    }
    return KeyEvent{key, cadence_screen_v1_KeyAction_KEY_ACTION_PRESS};
  }
  return KeyEvent{key, cadence_screen_v1_KeyAction_KEY_ACTION_UP};
}

std::optional<KeyEvent> KeyTracker::poll(std::int64_t nowMs) {
  for (auto& [key, held] : held_) {
    if (key == cadence_screen_v1_Key_KEY_BACK && !held.longSent && !held.hinted && nowMs - held.sinceMs >= hintMs_ &&
        nowMs - held.sinceMs < longMs_) {
      held.hinted = true;
      return KeyEvent{cadence_screen_v1_Key_KEY_BACK, cadence_screen_v1_KeyAction_KEY_ACTION_DOWN};
    }
    if (held.longSent || nowMs - held.sinceMs < longMs_) continue;
    held.longSent = true;
    const auto k = static_cast<cadence_screen_v1_Key>(key);
    if (k == cadence_screen_v1_Key_KEY_BACK) {
      return KeyEvent{cadence_screen_v1_Key_KEY_ASSISTANT, cadence_screen_v1_KeyAction_KEY_ACTION_LONG};
    }
    return KeyEvent{k, cadence_screen_v1_KeyAction_KEY_ACTION_LONG};
  }
  return std::nullopt;
}

std::int64_t KeyTracker::nextDueMs(std::int64_t nowMs) const {
  std::int64_t best = -1;
  for (const auto& [key, held] : held_) {
    if (held.longSent) continue;
    const bool hint = key == cadence_screen_v1_Key_KEY_BACK && !held.hinted;
    const std::int64_t due = held.sinceMs + (hint ? hintMs_ : longMs_) - nowMs;
    const std::int64_t clamped = due < 0 ? 0 : due;
    if (best < 0 || clamped < best) best = clamped;
  }
  return best;
}

}  // namespace cadence::screen::platform
