#pragma once

#include <cstddef>
#include <cstdint>
#include <vector>

namespace cadence::screen::render {

struct FrameStatsSummary {
  std::size_t frames = 0;
  // presented frames a second over the window
  double fps = 0;
  // the work of a frame (compose to swap), milliseconds
  double workP50Ms = 0;
  double workP95Ms = 0;
  double workMaxMs = 0;
  // frames that followed the one before at once (an animation's, UiLayer's next frame asked for), and their rate
  // over their own intervals: render-on-demand leaves idle gaps that say nothing about smoothness
  std::size_t chained = 0;
  double chainedFps = 0;
  // chained frames whose interval was longer than 1.5 refresh periods (a missed vsync)
  std::size_t late = 0;
};

// frame timing over a rolling window of the last frames; numbers only, so a summary is a safe log line (rule 8)
class FrameStats {
 public:
  explicit FrameStats(std::size_t window = 300, double refreshHz = 60.0);

  // one presented frame: when its work began and when the swap returned, in nanoseconds of one clock; chained when
  // the loop drew it straight after the one before (an animation), not after an idle wait
  void add(std::int64_t startNs, std::int64_t endNs, bool chained = false);
  [[nodiscard]] FrameStatsSummary summary() const;
  void reset();

 private:
  struct Sample {
    std::int64_t startNs;
    std::int64_t endNs;
    bool chained;
  };
  std::size_t window_;
  double periodNs_;
  std::vector<Sample> samples_;
  std::size_t next_ = 0;
};

}  // namespace cadence::screen::render
