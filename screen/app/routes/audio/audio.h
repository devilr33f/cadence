#pragma once

#include "app/routes/ambient/drift.h"
#include "app/routes/audio/bar-renderer.h"
#include "app/routes/audio/bars.h"
#include "app/routes/audio/queue-sidebar.h"
#include "app/routes/audio/viz-view.h"
#include "app/routes/panels/side-sheet.h"
#include "app/routes/playback/context.h"

#include <array>
#include <atomic>
#include <memory>

namespace cadence::screen::routes {

// audio mode (design §13, SCREEN_ROUTE_AUDIO): a record sleeve, art on the left and type on the right. the classic
// visualizer is a soft, full-width band in the background's bottom third, never a block in the text column. lean-back
// is the default: no controls until a key, which is consumed and shows them with focus on play/pause; 8 s without a
// key goes back to lean-back; 2 min of lean-back while playing dims to the ambient sleeve that drifts 1 dp every 2 s.
// «Визуализация» picks off, the classic background bars, or the full-screen feedback scenes (the host's texture under
// the layer; ← → change scene while the controls are hidden)
class AudioPage final : public Page {
 public:
  enum class State { Lean, Controls, Ambient };

  AudioPage(PlaybackContext& c, SideSheet& sheet, std::unique_ptr<VizView> viz);
  ~AudioPage() override;

  void enter() override;
  std::string body() override;
  void loaded(bool restoring) override;
  void leave() override;
  bool key(const platform::KeyEvent& event) override;
  bool activate(const ui::Activation& a) override;
  void tick(std::int64_t nowMs) override;
  void playerChanged() override;
  void sessionChanged() override;
  void renderUnder(render::RenderBackend& backend, render::Rect screen) override;
  [[nodiscard]] bool dirty() const override;
  [[nodiscard]] cadence_v2_ScreenRoute route() const override { return cadence_v2_ScreenRoute_SCREEN_ROUTE_AUDIO; }

  [[nodiscard]] State state() const { return state_; }
  void setState(State s);
  // the tap's thread: the bands and the pcm
  void bands(const std::array<std::uint8_t, 16>& b);
  void pcm(std::span<const float> s, std::uint32_t ch, std::uint32_t rate, std::int64_t monoNs) {
    if (viz_) viz_->pcm(s, ch, rate, monoNs);
  }
  [[nodiscard]] VizView* viz() const { return viz_.get(); }
  void setVisualizer(VizFamily f);
  // display.screen's config came again: the family, the pinned scene and shape
  void settingsChanged();
  // the tap's wake also serves a new classic-band analysis frame; active false while paused or off the page
  void setWake(std::function<void()> wake) { wake_ = std::move(wake); }
  void releaseGraphics();
  void abandonGraphics();
  // the route tests read the model without inspecting gl objects
  [[nodiscard]] const std::array<double, BarSmoother::kBars>& barHeights() const { return bars_.heights(); }
  // the session's track changed in place (a cast's next song): the page is built again, its bars from the floor
  void trackChanged();
  // the server sent this session's fields again: its queue may have changed without a new play
  void queueChanged() { queue_.load(true); }
  // the panel and queue chips
  std::function<void(const std::string& panelId, Rml::Element* opener)> openPanel;
  std::function<void(Rml::Element* opener)> openQueue;
  // the gallery: the drift as it stands at tS of ambient
  void driftTo(double tS);

 private:
  std::string page() const;
  std::string controls() const;
  void refresh();
  void applyBars();
  [[nodiscard]] bool classicBars() const;
  // a host family is drawn under the page now (not a handoff, not ambient)
  [[nodiscard]] bool hostShown() const;
  void updateBarsActivity();
  // the host's family from the setting, and the track's cover for the drive scenes (the image cache's, at 256 px)
  void applyFamily();
  void requestArtwork();
  void requestArt(const std::string& id);
  void saveVisualizer();
  void takeArtwork();

  SideSheet& sheet_;
  std::unique_ptr<VizView> viz_;
  AudioQueueSidebar queue_;
  BarSmoother bars_;
  BarRenderer barRenderer_;
  std::array<std::atomic<std::uint8_t>, 16> bands_{};
  std::atomic<bool> bandsNew_{false};
  std::atomic<bool> barsActive_{false};
  std::function<void()> wake_;
  State state_ = State::Lean;
  std::int64_t lastKeyMs_ = 0;
  std::int64_t leanSinceMs_ = 0;
  std::int64_t ambientSinceMs_ = 0;
  std::int64_t lastRefresh_ = -1;
  bool okDown_ = false;
  DriftOffset lastDrift_{-1, -1};
  std::string artKey_;
  std::string artFor_;
  bool artPending_ = false;
  float punchShown_ = 1;
  // the sleeve's gradient and the background band's tints (built once per track)
  mutable std::string background_;
  mutable std::uint32_t barColorA_ = 0x4a3a5c;
  mutable std::uint32_t barColorB_ = 0x1f2a44;
};

// the placeholder's duotone (8 muted pairs) for an album, stable per album
std::pair<std::uint32_t, std::uint32_t> placeholderColors(const std::string& album);
// a colour darkened to at most 18 % luminance, as #rrggbb
std::string darkened(std::uint32_t rgb, double maxLuma = 0.18);

}  // namespace cadence::screen::routes
