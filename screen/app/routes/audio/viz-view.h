#pragma once

#include "render/render-backend.h"

#include <cstdint>
#include <functional>
#include <memory>
#include <span>
#include <string>
#include <vector>

namespace cadence::screen::routes {

// audio mode's full-screen visualizer (plan item 16): the feedback scenes and the classic styles in P's host, under
// the ui's layer, upscaled from 960 × 540. pcm comes from the tap's thread; the rest is the ui thread's
class VizView {
 public:
  virtual ~VizView() = default;
  // the tap's thread: a PcmSnapshot as heard, never waits
  virtual void pcm(std::span<const float> interleaved, std::uint32_t channels, std::uint32_t rate, std::int64_t monoNs) = 0;
  // rendering on (audio mode shows the scenes) or off (another page, the classic bars, paused for long)
  virtual void setRunning(bool on) = 0;
  virtual void setPaused(bool paused) = 0;
  virtual void setReducedMotion(bool on) = 0;
  // ← → while the controls are hidden; a new track picks another scene
  virtual void next() = 0;
  virtual void prev() = 0;
  virtual void onTrack(std::uint64_t seed) = 0;
  // the artwork's dominant colour as a tint (0xRRGGBB), amount 0..1
  virtual void setTint(std::uint32_t rgb, float amount) = 0;
  // the family of sources the owner picked, by id prefix ("drive:", "scene:", "milkdrop:")
  virtual void setFamily(std::vector<std::string> prefixes) { (void)prefixes; }
  // the track's cover (straight rgba, rows top down) for the drive scenes; empty clears it
  virtual void setArtwork(std::vector<std::uint8_t> rgba, int width, int height) {
    (void)rgba;
    (void)width;
    (void)height;
  }
  // one source shown and kept ("drive:cover"; empty: each track picks), and the drive scenes' shape (their codes;
  // SceneLook::kAutoShape follows the track)
  virtual void setPin(const std::string& id) { (void)id; }
  virtual void setShape(int code) { (void)code; }
  // the render size: low (640 x 360), medium (960 x 540), high (1280 x 720, the drive scenes at full size), max
  // (1920 x 1080), native (the screen's own output size); a change restarts the host at its next draw
  virtual void setQuality(const std::string& quality) { (void)quality; }
  // how far ahead of the sound the picture looks, ms (display.screen's vizOffsetMs); applies at the next start
  virtual void setLead(int ms) { (void)ms; }
  // the drive scenes' shape scale this frame (1 at rest): the ui's title over the shape follows it
  [[nodiscard]] virtual float punch() const { return 1; }
  // draws the newest frame over dst (gl current, the ui thread); false when there is none yet
  virtual bool draw(render::RenderBackend& backend, render::Rect dst) = 0;
  // a new frame since the last draw
  [[nodiscard]] virtual bool frameReady() const = 0;
  // the shown source's id ("scene:vortex")
  [[nodiscard]] virtual std::string current() const = 0;
  // the host's thread wakes the main loop with it after each frame (VizFeed::setWake); before the first draw
  virtual void setWake(std::function<void()> wake) { (void)wake; }
  virtual void release() {}
  // the gl context is gone (the ui went first): its objects are dropped without a call
  virtual void abandon() {}
};

// the device's: the host on a thread of its own with a gl context shared with the ui's (screen_backend's egl
// context), started the first time it is shown with the ui's context current
std::unique_ptr<VizView> makeHostVizView();
// the gallery's and the tests': the renderer on the ui thread, a frame per draw from the pcm it was handed, its
// clock stepped by the caller (deterministic golden images); the first draw after a start renders warmupFrames
std::unique_ptr<VizView> makeSyncVizView(double fps = 30, int warmupFrames = 45);

}  // namespace cadence::screen::routes
