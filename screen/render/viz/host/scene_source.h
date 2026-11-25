#pragma once

#include "gl.h"
#include "source.h"

#include <array>
#include <atomic>
#include <cstdint>
#include <memory>
#include <mutex>
#include <string>
#include <vector>

namespace cadence::render::viz {

// the look every feedback scene shares, set from the ui thread: bloom's strength and the artwork's dominant colour
// (the server's, per track) as a bias over the palette
struct SceneLook {
  std::atomic<float> bloom{0.75f};
  std::atomic<float> tint_r{1}, tint_g{1}, tint_b{1};
  std::atomic<float> tint_amount{0};

  void set_tint(float r, float g, float b, float amount) {
    tint_r = r;
    tint_g = g;
    tint_b = b;
    tint_amount = amount;
  }

  // the track's cover (straight rgba, rows top down), which the drive scenes show; empty pixels clear it. the viz
  // thread uploads it when its generation moved
  void set_artwork(std::vector<std::uint8_t> rgba, int width, int height) {
    std::lock_guard lock(art_mu);
    art_rgba = std::move(rgba);
    art_w = width;
    art_h = height;
    ++art_generation;
  }
  // the viz thread: the cover when it changed since generation (which it then advances); false when unchanged
  bool take_artwork(std::uint64_t& generation, std::vector<std::uint8_t>& rgba, int& width, int& height) {
    std::lock_guard lock(art_mu);
    if (generation == art_generation) return false;
    generation = art_generation;
    rgba = art_rgba;
    width = art_w;
    height = art_h;
    return true;
  }
  // the track's seed (its title's hash): what a drive scene's shape follows
  std::atomic<std::uint64_t> track{0};
  // the owner's shape for the drive scenes (DriveSource::shape_for's codes), kAutoShape to follow the track
  static constexpr int kAutoShape = -100;
  std::atomic<int> shape{kAutoShape};
  // the drive scenes' scene pass against the host's size (0.5: half; 1: full), read when they start
  std::atomic<float> scene_scale{0.5f};
  // the drive scenes' latest punch (1 + the kick's scale), for the ui's text over the shape
  std::atomic<float> punch{1};

 private:
  std::mutex art_mu;
  std::vector<std::uint8_t> art_rgba;
  int art_w = 0, art_h = 0;
  std::uint64_t art_generation = 0;
};

// the gl the scenes share on the viz thread: the 256 × 8 palette lut (eight cosine gradients eased in from black:
// fire, ice, spectrum, sunset, toxic, ocean, ember, mono), the bloom chain at quarter size (a bright-pass
// downsample and a separable 9-tap blur, three draws) and the composite (scene + bloom, tinted)
class ScenePost {
 public:
  explicit ScenePost(std::shared_ptr<SceneLook> look) : look_(std::move(look)) {}
  bool init(int width, int height);
  void release();
  // the bloom of scene (a w × h texture) and the composite of both into fbo (bound by the caller)
  // bloom < 0 takes the look's
  void finish(unsigned scene, unsigned fbo, float bloom = -1, bool keep_alpha = false);
  // the bloom of scene alone (its quarter-size texture), for a caller that composites it in its own pass
  unsigned bloom_only(unsigned scene);
  [[nodiscard]] unsigned palette() const { return palette_; }
  [[nodiscard]] bool ready() const { return composite_ != 0; }

 private:
  std::shared_ptr<SceneLook> look_;
  int w_ = 0, h_ = 0;
  unsigned palette_ = 0;
  unsigned bright_ = 0, blur_ = 0, composite_ = 0;
  gl::Target a_, b_;
  int users_ = 0;
};

// the palette rows by name, as a scene names its own ("// palette: fire")
int palette_row(const std::string& name);
// the lut's pixels (rgba8, 256 × 8): the cpu half, so a test can check that every row starts from black
std::vector<std::uint8_t> palette_pixels();

// a feedback scene (the milkdrop mechanism, iteration 2 of the viz poc): each frame samples the scene's previous
// frame through its own warp (zoom punch on bass and onsets, rotation by mids, swirl by treble), fades it, remaps it
// through its palette (whose offset steps on every onset), and draws its 2-3 elements on top in the same draw; then
// bloom and the tint. one scene draw and four post draws a frame
//
// a scene is glsl es 3.00 defining main() and writing fragColor, with these in scope: vUv, uRes, uTime, uBass,
// uMid, uTreble, uLevel (absolute band levels 0..1), uBeatPulse (1 on an onset, decaying), uBeatCount, uPrev (its
// previous frame), fft(u), fftLog(u), wave(u), wave2(u) (a later window of the same channel), palette(x), fb(uv,
// decay) (the faded, palette-remapped feedback sample), beatAge(i) (seconds since the i-th last onset, -1 none),
// hash12 and rot2
class SceneSource final : public VizSource {
 public:
  SceneSource(std::string name, std::string glsl, int palette, std::shared_ptr<ScenePost> post)
      : name_(std::move(name)), glsl_(std::move(glsl)), palette_row_(palette), post_(std::move(post)) {}

  [[nodiscard]] std::string id() const override { return "scene:" + name_; }
  bool init(int width, int height) override;
  // fresh trails: both feedback buffers back to black
  bool activate() override;
  void render(const FrameInputs& in, unsigned fbo) override;
  void release() override;
  [[nodiscard]] const std::string& error() const { return error_; }

 private:
  std::string name_, glsl_, error_;
  int palette_row_ = 0;
  std::shared_ptr<ScenePost> post_;
  int w_ = 0, h_ = 0;
  unsigned program_ = 0;
  gl::Target ping_[2];
  int front_ = 0;
  float shift_ = 0;
  int u_res_ = -1, u_time_ = -1, u_audio_ = -1, u_prev_ = -1, u_pal_ = -1, u_pal_row_ = -1, u_pal_shift_ = -1,
      u_bass_ = -1, u_mid_ = -1, u_treble_ = -1, u_level_ = -1, u_pulse_ = -1, u_count_ = -1, u_rings_ = -1;
};

// every *.frag in dir as a SceneSource named by its stem, sorted, sharing one post chain over look
std::vector<std::unique_ptr<SceneSource>> load_scenes(const std::string& dir, std::shared_ptr<SceneLook> look);

// the scenes compiled into the binary (scenes/embed.cmake), for a screen that has no asset dir
struct EmbeddedScene {
  const char* name;
  const char* glsl;
};
const std::vector<EmbeddedScene>& embedded_scene_sources();
std::vector<std::unique_ptr<SceneSource>> embedded_scenes(std::shared_ptr<SceneLook> look);

}  // namespace cadence::render::viz
