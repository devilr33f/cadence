#pragma once

#include "app/routes/ambient/drift.h"
#include "app/routes/ambient/idle-renderer.h"
#include "app/routes/playback/context.h"
#include "kenburns.h"
#include "screen_backend.h"
#include "render/render-backend.h"

#include <cstdint>
#include <functional>
#include <memory>
#include <optional>
#include <string>
#include <vector>

namespace cadence::screen::routes {

// idle's timer (plan item 15): idle after idleAfterS with no key while nothing plays, or after pausedAfterS while a
// video is paused (ambient.pausedAfterS: -1, absent, is idleAfterS; 0 never covers a paused one); never while a turn,
// a toast or a cast holds the screen; sleep after sleepAfterS more (0 = never)
class IdleTimer {
 public:
  void configure(int idleAfterS, int sleepAfterS, int pausedAfterS = -1) {
    idleAfterMs_ = static_cast<std::int64_t>(idleAfterS) * 1000;
    sleepAfterMs_ = static_cast<std::int64_t>(sleepAfterS) * 1000;
    pausedAfterMs_ = pausedAfterS < 0 ? -1 : static_cast<std::int64_t>(pausedAfterS) * 1000;
  }
  // a key, a turn, a toast, a cast: the screen is in use
  void touch(std::int64_t nowMs) { lastMs_ = nowMs; }
  // due to go idle: no key for idleAfterS and nothing playing; paused: what plays is a paused video
  [[nodiscard]] bool idle(std::int64_t nowMs, bool playing, bool paused = false) const {
    if (idleAfterMs_ <= 0 || playing) return false;
    const auto after = paused && pausedAfterMs_ >= 0 ? pausedAfterMs_ : idleAfterMs_;
    return after > 0 && nowMs - lastMs_ >= after;
  }
  [[nodiscard]] bool sleep(std::int64_t nowMs) const {
    return sleepAfterMs_ > 0 && idleAfterMs_ > 0 && nowMs - lastMs_ >= idleAfterMs_ + sleepAfterMs_;
  }

 private:
  std::int64_t idleAfterMs_ = 600000;
  std::int64_t sleepAfterMs_ = 3600000;
  std::int64_t pausedAfterMs_ = -1;
  std::int64_t lastMs_ = 0;
};

// an image for the ken burns drift, decoded off the ui thread at 960 x 540 at most
struct DecodedImage {
  int width = 0;
  int height = 0;
  std::vector<std::uint8_t> rgba;
};
// where ambient images come from: the gallery's generated ones; on a device, the image proxy once the screen fetches
// images (stream b's follow-up) — until then none, and an image pick is skipped for the next
class ImageSource {
 public:
  virtual ~ImageSource() = default;
  // calls done (on the ui thread, from poll) with the image, or nullopt when it cannot be had
  virtual void request(const std::string& url, std::function<void(std::optional<DecodedImage>)> done) = 0;
  virtual void poll() {}
};
std::unique_ptr<ImageSource> makeNoImages();

// one ambient pick, ambient.next's answer
struct AmbientPick {
  // video (an aerial clip, played muted) or image (ken burns)
  std::string kind;
  std::string id;
  std::string url;
  struct Caption {
    int atS = 0;
    std::string text;
  };
  std::vector<Caption> captions;
  std::string category;
  int durationS = 0;
  // the overlay's weather line, formatted by the server; empty when it has none
  std::string weather;
};
std::optional<AmbientPick> ambientPickFrom(const std::string& json);
// the caption showing at second s of the clip, or empty
std::string captionAt(const std::vector<AmbientPick::Caption>& captions, double s);

// idle (SCREEN_ROUTE_AMBIENT): aerial clips through the player, muted (purpose ambient: route NONE, never a session
// the server sees as playing), captions at their times, a clock and the weather; images with the ken burns drift
// drawn under the layer (one textured quad, never rmlui). the overlay drifts 8 dp a minute against burn-in, clips
// change through black, and nothing here ever makes sound or starts playback of content. the first key only wakes
// (the routes consume it and put back what was shown)
class AmbientPage final : public Page {
 public:
  AmbientPage(PlaybackContext& c, std::unique_ptr<ImageSource> images);
  ~AmbientPage() override;

  void enter() override;
  std::string body() override;
  void loaded(bool restoring) override;
  void leave() override;
  bool key(const platform::KeyEvent& event) override;
  void tick(std::int64_t nowMs) override;
  void playerChanged() override;
  void renderUnder(render::RenderBackend& backend, render::Rect screen) override;
  [[nodiscard]] bool dirty() const override;
  [[nodiscard]] cadence_v2_ScreenRoute route() const override { return cadence_v2_ScreenRoute_SCREEN_ROUTE_AMBIENT; }

  // a pick as if the server had answered it (the gallery, the tests)
  void show(const AmbientPick& pick);
  // the overlay's drift at tS of idle (the gallery)
  void driftTo(double tS);
  [[nodiscard]] const std::optional<AmbientPick>& pick() const { return pick_; }
  [[nodiscard]] const std::string& clipSession() const { return clipSession_; }
  // asleep: the screen blanked (sleepAfterS)
  void setAsleep(bool asleep);
  // the gl context goes (or went first)
  void releaseGraphics() { aurora_.release(); }
  void abandonGraphics() { aurora_.abandon(); }

 private:
  void next();
  void refreshOverlay();
  [[nodiscard]] bool clipPlaying() const;

  std::unique_ptr<ImageSource> images_;
  std::optional<AmbientPick> pick_;
  std::string clipSession_;
  int clips_ = 0;
  std::int64_t enteredMs_ = 0;
  std::int64_t pickedMs_ = 0;
  std::int64_t fadeUntilMs_ = 0;
  std::int64_t retryAtMs_ = -1;
  bool asking_ = false;
  DriftOffset drift_{-1, -1};
  std::string caption_;
  // the image waiting for gl, then on screen
  std::optional<DecodedImage> pending_;
  std::int64_t pendingAtMs_ = 0;
  render::Texture texture_;
  render::RenderBackend* backend_ = nullptr;
  cadence::render::KenBurns kb_;
  bool kbStarted_ = false;
  bool asleep_ = false;
  std::int64_t lastClock_ = -1;
  std::unique_ptr<cadence::render::BackendCanvas> canvas_;
  // the aurora while nothing else shows (no pick, an image still decoding, ambient.source aurora), ~20 frames a second
  [[nodiscard]] bool auroraShown() const;
  IdleRenderer aurora_;
  mutable std::int64_t auroraDrawnMs_ = -1;
};

}  // namespace cadence::screen::routes
