#pragma once

#include <SDL3/SDL_events.h>

namespace cadence::screen::platform {

enum class Lifecycle {
  None,
  // android: the activity goes to the background; stop drawing, keep state (the surface may go)
  Pause,
  // back in the foreground: redraw everything
  Resume,
  // the window's pixel size changed (or a display was attached)
  Resize,
  // the window was exposed: owes a frame
  Expose,
  LowMemory,
  // the os ends the app, or the window was closed
  Quit,
};

Lifecycle lifecycleOf(const SDL_Event& event);
const char* lifecycleName(Lifecycle lifecycle);

}  // namespace cadence::screen::platform
