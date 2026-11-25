#pragma once

#include <RmlUi/Core/Context.h>

#include <string>
#include <vector>

namespace cadence::screen::ui {

// design r2 section 25's per-screen budget, as far as the document says it: the full-screen translucent layers
// (an element covering 90 % of the screen with a translucent fill, a decorator or opacity below 1; the budget is 2)
// and the text that overflows its box in a single-line slot. the animated area and the frame time come from the
// gallery's harness, which renders and reads frames back
struct LayerReport {
  int translucentLayers = 0;
  // ids or classes of the layers, for the report
  std::vector<std::string> layers;
  // single-line text (.line1, card titles, buttons) that wraps, and boxes that leave the screen on the right
  int overflows = 0;
  std::vector<std::string> overflowing;
};

LayerReport analyseLayers(Rml::Context& context);

}  // namespace cadence::screen::ui
