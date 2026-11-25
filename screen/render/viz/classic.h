#pragma once

#include "analyzer.h"
#include "backend.h"

#include <vector>

namespace cadence::render::viz {

enum class Style {
  // winamp 2-style bars from a baseline with falling peaks
  Bars,
  // the bars mirrored about the middle
  Mirror,
  // the oscilloscope
  Scope,
};

struct ClassicLook {
  // design §13: paper at 70 % (ember means focus)
  Color bar{0.96f, 0.94f, 0.90f, 0.7f};
  Color peak{0.96f, 0.94f, 0.90f, 1.0f};
  Color scope{0.96f, 0.94f, 0.90f, 0.85f};
  // the gap between bars as a share of a bar's slot
  float gap = 0.4f;
  float peak_px = 3;
  float scope_px = 2;
  // the height a bar keeps at 0, so a silent screen still shows the bars (design §13: 8 dp of 120)
  float rest = 0.066f;
};

// draws an Analyzer's state into area of a canvas: rects alone, snapped to whole pixels, in one fill_rects call per
// colour, so A's backend draws it with one shader. drawn at the caller's ~30 fps, never more than the area
class Classic {
 public:
  explicit Classic(ClassicLook look = {}) : look_(look) {}

  void draw(Canvas& canvas, Rect area, const Analyzer& a, Style style);

 private:
  void bars(Rect area, const Analyzer& a, bool mirror);
  void scope(Rect area, const Analyzer& a);

  ClassicLook look_;
  std::vector<Rect> fill_, peaks_;
};

}  // namespace cadence::render::viz
