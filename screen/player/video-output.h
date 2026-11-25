#pragma once

#include "player/video-fit.h"
#include "render/render-backend.h"

#include <mpv/client.h>

#include <atomic>
#include <cstdint>
#include <functional>
#include <mutex>
#include <optional>

struct mpv_render_context;

namespace cadence::screen::player {

// where mpv's frames go; attach after mpv_initialize, detach before mpv_terminate_destroy (with the gl context
// current: the render context frees gl objects)
class VideoOutput {
 public:
  virtual ~VideoOutput() = default;
  virtual bool attach(mpv_handle* mpv) = 0;
  virtual void detach() = 0;
  [[nodiscard]] virtual bool inGl() const = 0;
  // a frame is due since the last call (consumes the update)
  virtual bool frameDue() = 0;
  virtual bool render() = 0;
  [[nodiscard]] virtual std::optional<render::Texture> texture() const = 0;
  virtual void reportSwap() = 0;
  // the picture's shape (mpv's video-params), none once there is no video: a plane mpv cannot letterbox itself is
  // placed to it
  virtual void contentChanged(std::optional<VideoContent> /*content*/) {}
};

using GetProcAddress = void* (*)(void* ctx, const char* name);


// linux: mpv's render api (opengl, on our gles context) into a render target of ours; its update callback only flags
// and wakes the main loop, which renders when mpv says a frame is due (s1)
class RenderApiOutput final : public VideoOutput {
 public:
  RenderApiOutput(render::RenderBackend& backend, render::Size size, GetProcAddress getProcAddress,
                  std::function<void()> wake);
  ~RenderApiOutput() override;

  bool attach(mpv_handle* mpv) override;
  void detach() override;
  [[nodiscard]] bool inGl() const override { return context_ != nullptr; }
  bool frameDue() override;
  bool render() override;
  [[nodiscard]] std::optional<render::Texture> texture() const override;
  void reportSwap() override;

 private:
  static void onUpdate(void* self);
  static void* procAddress(void* self, const char* name);

  render::RenderBackend& backend_;
  render::Size size_;
  GetProcAddress getProcAddress_;
  std::function<void()> wake_;
  mpv_render_context* context_ = nullptr;
  render::RenderTarget target_;
  std::atomic<bool> updated_{false};
  bool rendered_ = false;
};

// android: mpv decodes to its own SurfaceView below the translucent sdl surface (vo=mediacodec_embed). the activity
// hands the surface over (a jni global ref as wid) when it exists, and takes it back before it is destroyed
class SurfaceEmbedOutput final : public VideoOutput {
 public:
  bool attach(mpv_handle* mpv) override;
  void detach() override;
  [[nodiscard]] bool inGl() const override { return false; }
  bool frameDue() override { return false; }
  bool render() override { return false; }
  [[nodiscard]] std::optional<render::Texture> texture() const override { return std::nullopt; }
  void reportSwap() override {}
  // the plane (VideoLayout's player content): mediacodec_embed scales to the surface, so the surface takes the shape
  void contentChanged(std::optional<VideoContent> content) override;

  // surfaceChanged: the surface (its jni global ref) and its size; mpv shows video from now on
  void surfaceChanged(std::int64_t wid, int width, int height);
  // surfaceDestroyed, before it returns: mpv lets go of the surface
  void surfaceDestroyed();
  [[nodiscard]] bool hasSurface() const;

 private:
  void applyLocked(int width, int height);
  void releaseLocked();

  mutable std::mutex mu_;
  mpv_handle* mpv_ = nullptr;
  std::int64_t wid_ = 0;
  // what mpv was last given: the plane's resizes (the same surface, a new size) set only what changed
  std::int64_t appliedWid_ = 0;
  int width_ = 0;
  int height_ = 0;
  int appliedWidth_ = 0;
  int appliedHeight_ = 0;
};

// the activity's surface callbacks (android/native/android-main.cpp, on its ui thread): the surface reaches the
// SurfaceEmbedOutput attached now, or the next one to attach; destroyed returns once mpv let go of it
void androidVideoSurfaceChanged(std::int64_t wid, int width, int height);
void androidVideoSurfaceDestroyed();

}  // namespace cadence::screen::player
