#include "player/video-fit.h"

#include "ipc/log.h"

#include <algorithm>
#include <cmath>
#include <cstdint>
#include <utility>

namespace cadence::screen::player {

render::Size displaySize(const VideoContent& c) {
  if (c.width <= 0 || c.height <= 0) return {};
  std::int64_t w = c.width;
  std::int64_t h = c.height;
  // as mpv: the sample aspect only ever grows a side, so no source pixel is thrown away
  if (c.sarNum > 0 && c.sarDen > 0 && c.sarNum != c.sarDen) {
    if (c.sarNum > c.sarDen)
      w = (w * c.sarNum + c.sarDen / 2) / c.sarDen;
    else
      h = (h * c.sarDen + c.sarNum / 2) / c.sarNum;
  }
  const int turn = ((c.rotation % 360) + 360) % 360;
  if (turn == 90 || turn == 270) std::swap(w, h);
  constexpr std::int64_t kMax = 1 << 20;
  return render::Size{static_cast<int>(std::clamp<std::int64_t>(w, 1, kMax)),
                      static_cast<int>(std::clamp<std::int64_t>(h, 1, kMax))};
}

render::Rect fitRect(const VideoContent& content, render::Rect area) {
  const auto d = displaySize(content);
  if (d.width <= 0 || d.height <= 0 || area.width <= 0 || area.height <= 0) return area;
  const double scale = std::min(static_cast<double>(area.width) / d.width, static_cast<double>(area.height) / d.height);
  int w = std::clamp(static_cast<int>(std::lround(d.width * scale)), 1, area.width);
  int h = std::clamp(static_cast<int>(std::lround(d.height * scale)), 1, area.height);
  if (area.width - w <= area.width * kFitSnap) w = area.width;
  if (area.height - h <= area.height * kFitSnap) h = area.height;
  return render::Rect{area.x + (area.width - w) / 2, area.y + (area.height - h) / 2, w, h};
}

void VideoLayout::setSink(Sink sink) {
  std::lock_guard lock(mu_);
  sink_ = std::move(sink);
  sent_.reset();
  publishLocked();
}

void VideoLayout::setArea(render::Size display, render::Rect area) {
  std::lock_guard lock(mu_);
  display_ = display;
  area_ = area.width > 0 && area.height > 0 ? area : render::Rect{0, 0, display.width, display.height};
  publishLocked();
}

void VideoLayout::setContent(Source source, std::optional<VideoContent> content) {
  std::lock_guard lock(mu_);
  auto& slot = source == Source::Mirror ? mirror_ : player_;
  if (slot == content) return;
  slot = content;
  if (content) {
    const auto d = displaySize(*content);
    CADENCE_LOG(Info, "video_content", {{"source", source == Source::Mirror ? "mirror" : "player"},
                                        {"width", content->width},
                                        {"height", content->height},
                                        {"display_width", d.width},
                                        {"display_height", d.height},
                                        {"rotation", content->rotation}});
  }
  publishLocked();
}

std::optional<VideoContent> VideoLayout::content(Source source) const {
  std::lock_guard lock(mu_);
  return source == Source::Mirror ? mirror_ : player_;
}

render::Rect VideoLayout::rect() const {
  std::lock_guard lock(mu_);
  return rectLocked();
}

render::Rect VideoLayout::rectLocked() const {
  if (display_.width <= 0 || display_.height <= 0) return {};
  const auto& shown = mirror_ ? mirror_ : player_;
  if (!shown) return render::Rect{0, 0, display_.width, display_.height};
  return fitRect(*shown, area_);
}

void VideoLayout::publishLocked() {
  const auto r = rectLocked();
  if (r.width <= 0 || r.height <= 0 || sent_ == r) return;
  sent_ = r;
  if (!sink_) return;
  CADENCE_LOG(Info, "video_plane_placed", {{"x", r.x}, {"y", r.y}, {"width", r.width}, {"height", r.height}});
  sink_(r);
}

VideoLayout& videoLayout() {
  static VideoLayout layout;
  return layout;
}

}  // namespace cadence::screen::player
