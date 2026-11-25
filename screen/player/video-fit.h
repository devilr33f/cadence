#pragma once

#include "render/render-backend.h"

#include <functional>
#include <mutex>
#include <optional>

namespace cadence::screen::player {

// a decoded picture as the decoder reports it: its visible size (the crop applied), the sample aspect ratio and a
// clockwise rotation (0, 90, 180, 270) the surface applies
struct VideoContent {
  int width = 0;
  int height = 0;
  int sarNum = 1;
  int sarDen = 1;
  int rotation = 0;

  bool operator==(const VideoContent&) const = default;
};

// the content's shape on screen: the sample aspect widens (or heightens) it, a quarter turn swaps it; empty for a
// picture without a size
render::Size displaySize(const VideoContent& content);

// the largest rect of the content's shape inside the area, centred: letterboxed or pillarboxed, never stretched. a
// shape within kFitSnap of the area's fills it (a 1920x1088 coded frame on a 1080p screen, not a 7 px pillar); no
// size fills the area
render::Rect fitRect(const VideoContent& content, render::Rect area);
inline constexpr double kFitSnap = 0.01;

// what the video plane shows: android's video SurfaceView, which mpv (vo=mediacodec_embed) and the mirror's
// MediaCodec scale to fit, so it is sized to the content's shape itself (the bars around it are black). the screen's
// area (the overscan's safe area, in the display's pixels) and each producer's content come from their threads; the
// sink hears each new rect, under the lock, so rects arrive in order
class VideoLayout {
 public:
  enum class Source { Player, Mirror };
  using Sink = std::function<void(const render::Rect&)>;

  void setSink(Sink sink);
  // the display and the area inside it the video fits in
  void setArea(render::Size display, render::Rect area);
  // a producer's content, or none as it stops; the mirror's wins over the player's (the app stops one for the other)
  void setContent(Source source, std::optional<VideoContent> content);
  [[nodiscard]] std::optional<VideoContent> content(Source source) const;
  // the plane's rect now: the whole display while nothing shows (or nothing has a size), empty before the display
  [[nodiscard]] render::Rect rect() const;

 private:
  [[nodiscard]] render::Rect rectLocked() const;
  void publishLocked();

  mutable std::mutex mu_;
  Sink sink_;
  render::Size display_;
  render::Rect area_;
  std::optional<VideoContent> player_;
  std::optional<VideoContent> mirror_;
  std::optional<render::Rect> sent_;
};

// the process's plane (one video surface)
VideoLayout& videoLayout();

}  // namespace cadence::screen::player
