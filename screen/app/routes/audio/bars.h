#pragma once

#include "render/render-backend.h"

#include <array>
#include <cstdint>

namespace cadence::screen::routes {

// the audio page's background bars, fed by audiod's 16 bands (0-255 over -60..0 dbfs, ~30 a second). the owner's
// issue #1 found them slow and without drive, so each band is shown against the track's own range (its recent
// peak, and the loudest band's, falling 9 db a second: a quiet master fills the bars too) through a contrast curve;
// a bar jumps up at once and falls under gravity, and a kick (the low bands' rise over their recent average) bounces
// the row. paused bars fall to the floor, then active() goes false and the page stops asking for frames. reduced
// motion keeps its 10 hz updates without the fall or the bounce. heights are dp, independent of the output's size
class BarSmoother {
 public:
  static constexpr int kBars = 16;
  static constexpr double kMinDp = 8;
  static constexpr double kMaxDp = 120;

  BarSmoother();

  // the newest analysis frame, stamped on the ui thread when it is taken from the tap's queue
  void bands(const std::array<std::uint8_t, 16>& b, std::int64_t nowMs);
  void setPaused(bool paused) { paused_ = paused; }
  void setReducedMotion(bool on) { reduced_ = on; }
  // advances to nowMs: true when a height changed and the background needs a redraw
  bool step(std::int64_t nowMs);
  // a fall or a bounce still owes a frame at display rate; false once the bars rest
  [[nodiscard]] bool active(std::int64_t nowMs) const;
  [[nodiscard]] const std::array<double, kBars>& heights() const { return h_; }
  [[nodiscard]] std::int64_t intervalMs() const { return reduced_ ? 100 : 16; }
  // the kick's bounce, 0..1 (the renderer may brighten the row with it)
  [[nodiscard]] double bounce() const { return bounce_; }

 private:
  [[nodiscard]] double want(std::size_t i) const;

  std::array<double, kBars> target_{};
  std::array<double, kBars> peak_{};
  std::array<double, kBars> h_{};
  std::array<double, kBars> v_{};
  std::array<double, 4> low_{};
  double peakAll_ = 0;
  double fluxAvg_ = 0;
  double bounce_ = 0;
  std::int64_t lastBandsMs_ = -1;
  std::int64_t lastKickMs_ = -1000;
  std::int64_t lastMs_ = -1;
  bool paused_ = false;
  bool reduced_ = false;
};

// one integral row for the shader: a full-width band in the bottom third, equal widths and gaps, and exactly equal
// outer margins. every coordinate is a device pixel, so a changing height never introduces sub-pixel placement
struct BarLayout {
  render::Rect band{};
  int firstX = 0;
  int pitch = 0;
  int barWidth = 0;
  int gap = 0;
  int baseline = 0;
  int maxHeight = 0;

  [[nodiscard]] bool valid() const { return band.width > 0 && band.height > 0 && barWidth > 0; }
};

[[nodiscard]] BarLayout barLayout(render::Rect screen);
[[nodiscard]] render::Rect barRect(const BarLayout& layout, int index, double heightDp);

}  // namespace cadence::screen::routes
