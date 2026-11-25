#include "platform/overscan.h"

#include <algorithm>
#include <cmath>

namespace cadence::screen::platform {

render::Rect safeArea(render::Size display, double percent) {
  if (!std::isfinite(percent)) percent = 0;
  percent = std::clamp(percent, 0.0, 10.0);
  const auto inset = [&](int length) { return static_cast<int>(std::lround(length * percent / 100.0)); };
  const int x = inset(display.width);
  const int y = inset(display.height);
  return render::Rect{x, y, std::max(0, display.width - 2 * x), std::max(0, display.height - 2 * y)};
}

double dpRatio(render::Size display) {
  if (display.width <= 0 || display.height <= 0) return 1.0;
  return std::min(display.width / 1920.0, display.height / 1080.0);
}

}  // namespace cadence::screen::platform
