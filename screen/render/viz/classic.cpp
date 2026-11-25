#include "classic.h"

#include <algorithm>
#include <cmath>

namespace cadence::render::viz {

namespace {

// a rect with its edges on whole pixels; nothing when it covers none
void push_snapped(std::vector<Rect>& out, double x0, double y0, double x1, double y1) {
  const double l = std::round(std::min(x0, x1)), r = std::round(std::max(x0, x1));
  const double t = std::round(std::min(y0, y1)), b = std::round(std::max(y0, y1));
  if (r <= l || b <= t) return;
  out.push_back(Rect{static_cast<float>(l), static_cast<float>(t), static_cast<float>(r - l), static_cast<float>(b - t)});
}

}  // namespace

void Classic::draw(Canvas& canvas, Rect area, const Analyzer& a, Style style) {
  fill_.clear();
  peaks_.clear();
  if (style == Style::Scope) {
    scope(area, a);
    canvas.fill_rects(fill_, look_.scope);
    return;
  }
  bars(area, a, style == Style::Mirror);
  canvas.fill_rects(fill_, look_.bar);
  canvas.fill_rects(peaks_, look_.peak);
}

void Classic::bars(Rect area, const Analyzer& a, bool mirror) {
  const auto levels = a.bars();
  const auto peaks = a.peaks();
  const double n = static_cast<double>(levels.size());
  const double slot = static_cast<double>(area.w) / n;
  const double bar_w = slot * (1.0 - static_cast<double>(look_.gap));
  const double rest = static_cast<double>(look_.rest);
  const double peak_px = static_cast<double>(look_.peak_px);
  for (std::size_t i = 0; i < levels.size(); ++i) {
    const double x0 = static_cast<double>(area.x) + slot * static_cast<double>(i) + (slot - bar_w) / 2;
    const double x1 = x0 + bar_w;
    const double level = rest + (1.0 - rest) * levels[i];
    const double peak = rest + (1.0 - rest) * peaks[i];
    if (mirror) {
      const double mid = static_cast<double>(area.y) + static_cast<double>(area.h) / 2;
      const double half = static_cast<double>(area.h) / 2;
      push_snapped(fill_, x0, mid - half * level, x1, mid + half * level);
      if (peaks[i] > levels[i]) {
        push_snapped(peaks_, x0, mid - half * peak - peak_px, x1, mid - half * peak);
        push_snapped(peaks_, x0, mid + half * peak, x1, mid + half * peak + peak_px);
      }
      continue;
    }
    const double base = static_cast<double>(area.y + area.h);
    const double h = static_cast<double>(area.h) - peak_px;
    push_snapped(fill_, x0, base - h * level, x1, base);
    if (peaks[i] > levels[i]) push_snapped(peaks_, x0, base - h * peak - peak_px, x1, base - h * peak);
  }
}

// a column per point, from the previous point's height to this one's, at least scope_px thick: a joined line from
// rects alone
void Classic::scope(Rect area, const Analyzer& a) {
  const auto pts = a.scope();
  const double mid = static_cast<double>(area.y) + static_cast<double>(area.h) / 2;
  const double half = static_cast<double>(area.h) / 2 - static_cast<double>(look_.scope_px);
  const double col = static_cast<double>(area.w) / static_cast<double>(pts.size());
  const double thick = static_cast<double>(look_.scope_px) / 2;
  double prev = mid - half * pts[0];
  for (std::size_t i = 0; i < pts.size(); ++i) {
    const double y = mid - half * pts[i];
    const double x0 = static_cast<double>(area.x) + col * static_cast<double>(i);
    push_snapped(fill_, x0, std::min(prev, y) - thick, x0 + col, std::max(prev, y) + thick);
    prev = y;
  }
}

}  // namespace cadence::render::viz
