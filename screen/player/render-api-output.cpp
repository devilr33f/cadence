#include "player/video-output.h"

#include "player/mpv-options.h"

#include "ipc/log.h"

#include "render/gl.h"

#include <mpv/render_gl.h>

namespace cadence::screen::player {

RenderApiOutput::RenderApiOutput(render::RenderBackend& backend, render::Size size, GetProcAddress getProcAddress,
                                 std::function<void()> wake)
    : backend_(backend), size_(size), getProcAddress_(getProcAddress), wake_(std::move(wake)) {}

RenderApiOutput::~RenderApiOutput() { detach(); }

bool RenderApiOutput::attach(mpv_handle* mpv) {
  if (context_ != nullptr) return true;
  target_ = backend_.createRenderTarget(size_, false);
  if (!target_) {
    CADENCE_LOG(Error, "video_target_failed", {{"width", size_.width}, {"height", size_.height}});
    return false;
  }
  mpv_opengl_init_params gl{&RenderApiOutput::procAddress, this};
  int advanced = 1;
  mpv_render_param params[] = {
      {MPV_RENDER_PARAM_API_TYPE, const_cast<char*>(MPV_RENDER_API_TYPE_OPENGL)},
      {MPV_RENDER_PARAM_OPENGL_INIT_PARAMS, &gl},
      {MPV_RENDER_PARAM_ADVANCED_CONTROL, &advanced},
      {MPV_RENDER_PARAM_INVALID, nullptr},
  };
  const int err = mpv_render_context_create(&context_, mpv, params);
  if (err < 0) {
    CADENCE_LOG(Error, "video_render_context_failed", {{"code", err}});
    context_ = nullptr;
    backend_.destroyRenderTarget(target_);
    return false;
  }
  mpv_render_context_set_update_callback(context_, &RenderApiOutput::onUpdate, this);
  backend_.resetState();
  return true;
}

void RenderApiOutput::detach() {
  if (context_ != nullptr) {
    mpv_render_context_set_update_callback(context_, nullptr, nullptr);
    mpv_render_context_free(context_);
    context_ = nullptr;
  }
  backend_.destroyRenderTarget(target_);
  rendered_ = false;
}

namespace {
// mpv's per-frame fence, never made (mpvStubbedGlFunction)
GLsync noFence(GLenum /*condition*/, GLbitfield /*flags*/) { return nullptr; }
}  // namespace

void* RenderApiOutput::procAddress(void* self, const char* name) {
  if (mpvWithheldGlFunction(name)) return nullptr;
  if (mpvStubbedGlFunction(name)) return reinterpret_cast<void*>(&noFence);
  return static_cast<RenderApiOutput*>(self)->getProcAddress_(nullptr, name);
}

// mpv's thread: never call mpv or gl here
void RenderApiOutput::onUpdate(void* self) {
  auto* out = static_cast<RenderApiOutput*>(self);
  out->updated_ = true;
  if (out->wake_) out->wake_();
}

bool RenderApiOutput::frameDue() {
  if (context_ == nullptr || !updated_.exchange(false)) return false;
  const std::uint64_t flags = mpv_render_context_update(context_);
  return (flags & MPV_RENDER_UPDATE_FRAME) != 0;
}

bool RenderApiOutput::render() {
  if (context_ == nullptr) return false;
  mpv_opengl_fbo fbo{static_cast<int>(target_.fbo), size_.width, size_.height, GL_RGBA8};
  // flip_y: mpv writes gl's orientation (top row at high y), as every render target of ours holds it
  int flipY = 1;
  // pacing is the main loop's: never wait for the frame's target time in here
  int block = 0;
  mpv_render_param params[] = {
      {MPV_RENDER_PARAM_OPENGL_FBO, &fbo},
      {MPV_RENDER_PARAM_FLIP_Y, &flipY},
      {MPV_RENDER_PARAM_BLOCK_FOR_TARGET_TIME, &block},
      {MPV_RENDER_PARAM_INVALID, nullptr},
  };
  const int err = mpv_render_context_render(context_, params);
  // mpv leaves its own gl state behind
  backend_.resetState();
  if (err < 0) return false;
  rendered_ = true;
  return true;
}

std::optional<render::Texture> RenderApiOutput::texture() const {
  if (!rendered_ || !target_) return std::nullopt;
  return target_.color;
}

void RenderApiOutput::reportSwap() {
  if (context_ != nullptr && rendered_) mpv_render_context_report_swap(context_);
}

}  // namespace cadence::screen::player
