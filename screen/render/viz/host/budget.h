#pragma once

namespace cadence::render::viz {

// S5 on the stick: a preset's frame costs 14 ms to 870 ms. one over budget for window_s on end is skipped (and kept
// in the local slow list); a frame within budget starts the window again
class Budget {
 public:
  explicit Budget(double budget_ms = 20, double window_s = 2) : budget_ms_(budget_ms), window_s_(window_s) {}

  // a frame that took frame_ms, at now_s; true once the preset has been over budget for the whole window
  bool over(double frame_ms, double now_s) {
    if (frame_ms <= budget_ms_) {
      since_ = -1;
      return false;
    }
    if (since_ < 0) since_ = now_s - frame_ms / 1000;
    return now_s - since_ >= window_s_;
  }
  // a new preset
  void reset() { since_ = -1; }

 private:
  double budget_ms_, window_s_;
  double since_ = -1;
};

}  // namespace cadence::render::viz
