#pragma once

#include "features.h"

#include <string>

namespace cadence::render::viz {

// one visualizer the host can show: a shader preset, a milkdrop preset through projectM, or a classic style. every
// call comes on the viz thread with the host's gl context current (never the ui's: S5's projectM starved the ui
// loop); the host times each render() (with a glFinish) against its budget, so a source reports its cost by being
// measured, and one over budget for its window is skipped
class VizSource {
 public:
  virtual ~VizSource() = default;

  // stable and loggable: "shader:ember-pulse", "milkdrop:<preset path>", "classic:bars"
  [[nodiscard]] virtual std::string id() const = 0;
  // once, at the internal resolution; false drops the source
  virtual bool init(int width, int height) = 0;
  // it is about to be shown (a transition into it starts); heavy state (a projectM instance) lives from here
  virtual bool activate() { return true; }
  // a frame into fbo (bound, viewport set, width × height)
  virtual void render(const FrameInputs& in, unsigned fbo) = 0;
  // no longer shown
  virtual void deactivate() {}
  // before the context goes
  virtual void release() {}
};

}  // namespace cadence::render::viz
