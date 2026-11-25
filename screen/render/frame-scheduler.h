#pragma once

#include <cstdint>

namespace cadence::screen::render {

// what changed since the last decision, as the main loop sees it
struct FrameInputs {
  std::int64_t nowMs = 0;
  // mpv's update callback said a new video frame is due (the render api path only)
  bool videoFrameDue = false;
  // a video session draws through our gl (render api); false for none and for android's surface embed
  bool videoInGl = false;
  // the player rendered a first frame of the current session
  bool firstVideoFrame = false;
  // the ui changed (layout, text, focus) or is animating
  bool uiDirty = false;
  // the ui laid out its first page; until then there is nothing worth showing
  bool uiReady = false;
  // the window was exposed, resized or came back from the background
  bool exposed = false;
};

struct FrameDecision {
  // render mpv's frame into its texture
  bool renderVideo = false;
  // compose and swap
  bool present = false;
};

// render-when-dirty with s1's startup fix: nothing is swapped while a video session starts until its first frame and
// the ui's first layout are both ready (or the hold times out), so the screen never alternates between ui on black,
// black and video + ui; otherwise a frame is composed only when the video or the ui changed. one thread
class FrameScheduler {
 public:
  explicit FrameScheduler(std::int64_t startupHoldMs = 3000) : holdMs_(startupHoldMs) {}

  FrameDecision decide(const FrameInputs& in);
  // the swap happened
  void presented() { presentedOnce_ = true; }
  // a frame is owed (exposed) but held back by the startup hold
  [[nodiscard]] bool holding() const { return holding_; }

 private:
  std::int64_t holdMs_;
  bool wasVideo_ = false;
  bool holding_ = false;
  std::int64_t holdSince_ = 0;
  bool owed_ = true;
  bool presentedOnce_ = false;
};

// what the main loop's next event wait depends on
struct WaitInputs {
  // the cap: the link's messages, mpv and the tap wake the loop, so it only bounds a missed wake
  int maxWaitMs = 250;
  // ms until a held key's hint or long press, -1 for none
  std::int64_t keysDueMs = -1;
  // ms until the ui has something due (UiLayer::nextUpdateMs), -1 for none
  std::int64_t uiDueMs = -1;
  // a frame is owed: the window was exposed, or the ui changed since its last render
  bool frameOwed = false;
  // the last turn presented a change of the ui (an animation's frame): the next turn looks again at once, and the
  // swap's vsync paces it, so an animation runs at the display's rate instead of at the cap's
  bool animating = false;
  // in the background, or with the display off, the ui is not drawn: owed frames wait (an expose still presents one)
  bool hidden = false;
  // the startup hold keeps the screen (FrameScheduler::holding): a frame owed polls rather than spins
  bool holding = false;
};

// ms the main loop may sleep in its event wait: 0 when a frame is owed or the ui animates, else until the earliest
// due, at most the cap. never 0 unless something will be drawn, so an idle or a background screen sleeps
int loopWaitMs(const WaitInputs& in);

// the startup hold's poll while a frame is owed but held back
inline constexpr int kHoldPollMs = 10;

}  // namespace cadence::screen::render
