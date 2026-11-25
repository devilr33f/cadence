#include "platform/lifecycle.h"

namespace cadence::screen::platform {

Lifecycle lifecycleOf(const SDL_Event& e) {
  switch (e.type) {
    case SDL_EVENT_QUIT:
    case SDL_EVENT_TERMINATING:
    case SDL_EVENT_WINDOW_CLOSE_REQUESTED:
      return Lifecycle::Quit;
    case SDL_EVENT_WILL_ENTER_BACKGROUND:
    case SDL_EVENT_DID_ENTER_BACKGROUND:
      return Lifecycle::Pause;
    case SDL_EVENT_WILL_ENTER_FOREGROUND:
      return Lifecycle::None;
    case SDL_EVENT_DID_ENTER_FOREGROUND:
      return Lifecycle::Resume;
    case SDL_EVENT_LOW_MEMORY:
      return Lifecycle::LowMemory;
    case SDL_EVENT_WINDOW_PIXEL_SIZE_CHANGED:
    case SDL_EVENT_WINDOW_RESIZED:
    case SDL_EVENT_WINDOW_DISPLAY_CHANGED:
      return Lifecycle::Resize;
    case SDL_EVENT_WINDOW_EXPOSED:
    case SDL_EVENT_WINDOW_SHOWN:
    case SDL_EVENT_WINDOW_RESTORED:
      return Lifecycle::Expose;
    default:
      return Lifecycle::None;
  }
}

const char* lifecycleName(Lifecycle l) {
  switch (l) {
    case Lifecycle::None: return "none";
    case Lifecycle::Pause: return "pause";
    case Lifecycle::Resume: return "resume";
    case Lifecycle::Resize: return "resize";
    case Lifecycle::Expose: return "expose";
    case Lifecycle::LowMemory: return "low_memory";
    case Lifecycle::Quit: return "quit";
  }
  return "none";
}

}  // namespace cadence::screen::platform
