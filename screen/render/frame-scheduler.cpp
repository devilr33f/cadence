#include "render/frame-scheduler.h"

#include <algorithm>

namespace cadence::screen::render {

FrameDecision FrameScheduler::decide(const FrameInputs& in) {
  FrameDecision out;
  out.renderVideo = in.videoInGl && in.videoFrameDue;
  if (in.videoFrameDue || in.uiDirty || in.exposed) owed_ = true;

  // a video session through our gl began: hold until both layers have something
  if (in.videoInGl && !wasVideo_) {
    holding_ = true;
    holdSince_ = in.nowMs;
  }
  wasVideo_ = in.videoInGl;
  if (holding_) {
    const bool ready = (in.firstVideoFrame || out.renderVideo) && in.uiReady;
    if (ready || !in.videoInGl || in.nowMs - holdSince_ >= holdMs_) {
      holding_ = false;
    } else {
      return out;
    }
  }
  // before anything was shown, wait for the ui's first layout as well
  if (!presentedOnce_ && !in.uiReady) return out;

  out.present = owed_;
  if (out.present) owed_ = false;
  return out;
}

int loopWaitMs(const WaitInputs& in) {
  std::int64_t wait = std::max(0, in.maxWaitMs);
  if (in.keysDueMs >= 0) wait = std::min(wait, in.keysDueMs);
  if (in.hidden) return static_cast<int>(wait);
  if (in.uiDueMs >= 0) wait = std::min(wait, in.uiDueMs);
  if (in.frameOwed || in.animating) wait = 0;
  if (in.holding) wait = std::max<std::int64_t>(wait, std::min(in.maxWaitMs, kHoldPollMs));
  return static_cast<int>(wait);
}

}  // namespace cadence::screen::render
