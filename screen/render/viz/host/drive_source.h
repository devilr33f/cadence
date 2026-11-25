#pragma once

#include "gl.h"
#include "scene_source.h"
#include "source.h"

#include <cstdint>
#include <memory>
#include <string>
#include <vector>

namespace cadence::render::viz {

// the drive scenes (owner's issue #1, after the "trap nation" look he pointed at): a shape in the middle (a polygon,
// a circle, a star or a diamond, picked per track) whose outline throws the spectrum out as a comb of spikes, over a
// background that moves with the music (the cover blurred and zooming, a warp of particles whose speed is the energy,
// a tunnel of the shape's outlines), then a post pass: bloom, a shake and zoom punch and an rgb split on each kick, an
// anamorphic flare across the middle, a vignette and grain. the ui writes the song's title, artist and album over the
// shape. three passes of ours and ScenePost's bloom chain a frame; the scene pass draws at half the size (the stick's
// mali-g31 took 50-94 ms a frame at full size), the post at full size from it
//
// a frame flashes the whole screen only through the slow intensity (a drop), never per kick: the strobe check holds
enum class DriveVariant : int { Cover = 0, Warp = 1, Tunnel = 2 };

class DriveSource final : public VizSource {
 public:
  DriveSource(DriveVariant v, std::shared_ptr<SceneLook> look, std::shared_ptr<ScenePost> post);

  [[nodiscard]] std::string id() const override;
  bool init(int width, int height) override;
  bool activate() override;
  void render(const FrameInputs& in, unsigned fbo) override;
  void release() override;
  [[nodiscard]] const std::string& error() const { return error_; }

  // the shape a seed picks: sides (3-8), 0 a circle, -1 a star, -2 a diamond
  static int shape_for(std::uint64_t seed, DriveVariant v);

 private:
  void sync_artwork();
  [[nodiscard]] float scene_scale() const;

  DriveVariant v_;
  std::shared_ptr<SceneLook> look_;
  std::shared_ptr<ScenePost> post_;
  std::string error_;
  int w_ = 0, h_ = 0;
  unsigned scene_ = 0, post_program_ = 0;
  // a: the soft layer (half size), c: the sharp one (full size)
  gl::Target a_, c_;
  unsigned art_ = 0;
  unsigned spikes_ = 0;
  bool has_art_ = false;
  int art_w_ = 1, art_h_ = 1;
  std::uint64_t art_generation_ = 0;
  // the motion the music drives: the particles' travel, the shape's turn, the camera's punch and shake
  double travel_ = 0, spin_ = 0, jolt_ = 0, sway_ = 0, punch_ = 0, shake_ = 0, flare_ = 0, hue_ = 0, warp_ = 0;
  // exponentially shaped envelopes for snare godrays and hi-hat sparkle
  double snare_ = 0, hat_ = 0;
  // 0..1 progress of an expanding shockwave ring launched on a drop hit (1 when idle)
  double drop_wave_ = 1;
  std::uint64_t seen_track_ = ~0ULL;
  int shape_ = 3;
  double shape_change_ = 0;
  int prev_shape_ = 3;
};

// the three drive scenes over one look, sharing a post chain
std::vector<std::unique_ptr<VizSource>> drive_sources(std::shared_ptr<SceneLook> look);

}  // namespace cadence::render::viz
