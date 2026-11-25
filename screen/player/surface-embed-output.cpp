#include "player/video-output.h"

#include "ipc/log.h"

#include <string>

namespace cadence::screen::player {

namespace {

// the activity's surface outlives any one player: kept here, and handed to whichever output is attached
std::mutex registryMu;
SurfaceEmbedOutput* attached = nullptr;
std::int64_t surfaceWid = 0;
int surfaceWidth = 0;
int surfaceHeight = 0;

}  // namespace

bool SurfaceEmbedOutput::attach(mpv_handle* mpv) {
  std::lock_guard registry(registryMu);
  attached = this;
  std::lock_guard lock(mu_);
  mpv_ = mpv;
  appliedWid_ = 0;
  appliedWidth_ = appliedHeight_ = 0;
  if (surfaceWid != 0) {
    wid_ = surfaceWid;
    applyLocked(surfaceWidth, surfaceHeight);
  }
  return true;
}

void SurfaceEmbedOutput::detach() {
  std::lock_guard registry(registryMu);
  if (attached == this) attached = nullptr;
  std::lock_guard lock(mu_);
  releaseLocked();
  mpv_ = nullptr;
  videoLayout().setContent(VideoLayout::Source::Player, std::nullopt);
}

void SurfaceEmbedOutput::contentChanged(std::optional<VideoContent> content) {
  videoLayout().setContent(VideoLayout::Source::Player, content);
}

void SurfaceEmbedOutput::surfaceChanged(std::int64_t wid, int width, int height) {
  std::lock_guard lock(mu_);
  wid_ = wid;
  applyLocked(width, height);
}

void SurfaceEmbedOutput::surfaceDestroyed() {
  std::lock_guard lock(mu_);
  releaseLocked();
}

bool SurfaceEmbedOutput::hasSurface() const {
  std::lock_guard lock(mu_);
  return wid_ != 0;
}

void SurfaceEmbedOutput::applyLocked(int width, int height) {
  if (width > 0 && height > 0) {
    width_ = width;
    height_ = height;
  }
  if (mpv_ == nullptr || wid_ == 0) return;
  // the plane's own resizes (VideoLayout) change the size alone: a vo set again would rebuild mpv's video chain
  const bool newSurface = wid_ != appliedWid_;
  if (newSurface) {
    std::int64_t value = wid_;
    mpv_set_property(mpv_, "wid", MPV_FORMAT_INT64, &value);
  }
  if (width_ > 0 && height_ > 0 && (newSurface || width_ != appliedWidth_ || height_ != appliedHeight_)) {
    const std::string size = std::to_string(width_) + "x" + std::to_string(height_);
    mpv_set_property_string(mpv_, "android-surface-size", size.c_str());
    appliedWidth_ = width_;
    appliedHeight_ = height_;
  }
  if (!newSurface) return;
  mpv_set_property_string(mpv_, "vo", "mediacodec_embed");
  appliedWid_ = wid_;
  CADENCE_LOG(Info, "video_surface_attached", {{"width", width_}, {"height", height_}});
}

void SurfaceEmbedOutput::releaseLocked() {
  if (mpv_ != nullptr && wid_ != 0) {
    // before the surface goes: mpv must not touch it again (s1)
    mpv_set_property_string(mpv_, "vo", "null");
    std::int64_t zero = 0;
    mpv_set_property(mpv_, "wid", MPV_FORMAT_INT64, &zero);
    CADENCE_LOG(Info, "video_surface_detached");
  }
  wid_ = 0;
  appliedWid_ = 0;
}

void androidVideoSurfaceChanged(std::int64_t wid, int width, int height) {
  std::lock_guard registry(registryMu);
  surfaceWid = wid;
  surfaceWidth = width;
  surfaceHeight = height;
  if (attached != nullptr) attached->surfaceChanged(wid, width, height);
}

void androidVideoSurfaceDestroyed() {
  std::lock_guard registry(registryMu);
  if (attached != nullptr) attached->surfaceDestroyed();
  surfaceWid = 0;
}

}  // namespace cadence::screen::player
