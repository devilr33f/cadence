#pragma once

#include "render/render-backend.h"

namespace cadence::screen::platform {

// the safe area inside a display of that size with display.screen's overscan (0, 2.5 or 5 percent a side, as many
// tvs crop that much): what the ui lays out in, and what android's video plane fits in (player/video-fit.h)
render::Rect safeArea(render::Size display, double overscanPercent);

// the ui's dp ratio: the design is drawn at 1920 x 1080 dp, so 1 on a 1080p screen, 2 on 4k, 2/3 at 720p
double dpRatio(render::Size display);

}  // namespace cadence::screen::platform
