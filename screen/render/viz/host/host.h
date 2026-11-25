#pragma once

#include "../backend.h"
#include "features.h"
#include "gl.h"
#include "pcm_timeline.h"
#include "renderer.h"

#include <array>
#include <atomic>
#include <cstdint>
#include <functional>
#include <memory>
#include <mutex>
#include <span>
#include <thread>
#include <vector>

namespace cadence::render::viz {

// a gl context sharing objects with the ui's, made by the platform layer (stream A: SDL_GL_CreateContext with
// SDL_GL_SHARE_WITH_CURRENT_CONTEXT, or eglCreateContext with the ui's as share_context and a 1×1 pbuffer)
class SharedContext {
 public:
  virtual ~SharedContext() = default;
  // binds it to the calling thread
  virtual bool make_current() = 0;
  virtual void release() = 0;
};

struct HostConfig {
  RendererConfig renderer;
  // the frame rate it aims at; a device whose frames cost more than ~13 ms falls back to half of it, and comes back
  // once they are cheap again
  int fps = 60;
  // the board's audio delay trim on top of the tap's own timing (audio.output delayMs): the picture follows the
  // sound as heard
  double delay_ms = 0;
  // how far ahead of the sound the picture looks, ms: a frame takes the host's render and the ui's composite (two
  // or three frames) to reach the screen, so the window is taken that much after now
  double lead_ms = 0;
  // a log line per beat drawn (viz_beat): a diagnosis's, off by default
  bool log_beats = false;
  // the host's thread, after each finished frame: wakes the ui's loop to show it (Platform::wake). a loop that only
  // looked when something else woke it showed the scenes at a few frames a second, faster while the ui changed
  std::function<void()> on_frame;
};

// the visualizer host: a thread of its own with a shared gl context (never the ui's loop), fed the tap's snapshots
// on the timeline of when they are heard. each frame takes the 1024 frames heard up to now for the features and the
// frames heard since the last frame (contiguous, no overlap) for projectM, renders through the Renderer, and copies
// the result into one of three textures the ui draws from (the newest finished one, so it never samples a texture
// being drawn)
class Host {
 public:
  Host(HostConfig c, std::unique_ptr<SharedContext> context, LogFn log = {});
  ~Host();
  Host(const Host&) = delete;
  Host& operator=(const Host&) = delete;

  // add sources and transitions here before start()
  Renderer& renderer() { return renderer_; }

  bool start();
  void stop();

  // the tap reader's thread: a PcmSnapshot (interleaved f32, its first frame sounding at mono_ns, CLOCK_MONOTONIC);
  // never waits
  void push_pcm(std::span<const float> interleaved, std::uint32_t channels, std::uint32_t rate,
                std::int64_t mono_ns) {
    timeline_.push(interleaved, channels, rate, mono_ns);
  }

  // the ui thread
  void next();
  void prev();
  void on_track(std::uint64_t seed);
  // the family of sources to show (Renderer::set_family)
  void set_family(std::vector<std::string> prefixes);
  // Renderer::set_pin
  void set_pin(std::string id);
  // paused playback: rendering stops (the ui fades the layer out); reduced motion turns auto switching off
  void set_paused(bool paused) { paused_ = paused; }
  void set_reduced_motion(bool on);
  // the newest finished frame over dst (texture 0 until the first)
  [[nodiscard]] TexturedQuad frame(Rect dst) const;

 private:
  void run();

  HostConfig c_;
  std::unique_ptr<SharedContext> context_;
  LogFn log_;
  PcmTimeline timeline_;
  Renderer renderer_;
  AudioFeatures features_;

  std::thread thread_;
  std::atomic<bool> running_{false}, paused_{false};
  std::mutex mu_;
  int moves_ = 0;
  bool track_ = false, reduced_changed_ = false, reduced_ = false, family_changed_ = false;
  std::vector<std::string> family_;
  std::string pin_;
  bool pin_changed_ = false;
  std::uint64_t seed_ = 0;
  bool has_track_ = false;
  std::uint64_t last_track_ = 0;
  std::array<gl::Target, 3> present_;
  std::atomic<unsigned> front_{0};
};

}  // namespace cadence::render::viz
