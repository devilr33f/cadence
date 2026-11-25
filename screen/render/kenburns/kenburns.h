#pragma once

// the ken burns drift of ambient images and Home's backdrop (tv plan item 15, design §8 and §25): one textured quad
// whose uv rect pans and zooms over a cover crop, drawn by the backend with a transform; rmlui never animates a
// full-screen image. images are decoded off the ui thread at 960 × 540 at most and scaled up
#include "../viz/backend.h"

#include <cstdint>

namespace cadence::render {

struct KenBurnsConfig {
  // one image's time on screen
  double duration_s = 30;
  // zoom over the cover crop at the start and the end (1 = the crop itself)
  double zoom_from = 1.0;
  double zoom_to = 1.12;
  // fade in at the start and out before the end, through the ink beneath
  double fade_s = 1.5;
};

class KenBurns {
 public:
  explicit KenBurns(KenBurnsConfig c = {}) : c_(c) {}

  // a new image at now_s; seed picks the pan's direction and which way the zoom runs, so a given seed moves alike
  void start(TextureId texture, int image_w, int image_h, std::uint64_t seed, double now_s);
  // reduced motion: no drift, the crop held still (the caller swaps images every 10 min with a 2 s fade)
  void set_reduced_motion(bool on) { reduced_ = on; }
  // Home's first key stops the drift where it is; resume carries on from there
  void freeze(double now_s);
  void resume(double now_s);

  // the quad to draw over viewport at now_s
  [[nodiscard]] TexturedQuad quad(Rect viewport, double now_s) const;
  // the image has had its time
  [[nodiscard]] bool done(double now_s) const;
  // 0..1 through the image's time, frozen time left out
  [[nodiscard]] double progress(double now_s) const;

 private:
  KenBurnsConfig c_;
  TextureId texture_ = 0;
  int image_w_ = 0, image_h_ = 0;
  double start_s_ = 0;
  double frozen_at_ = -1;
  double frozen_total_ = 0;
  bool reduced_ = false;
  // pan from and to, in the slack the zoomed crop leaves: -1..1 each axis
  double from_x_ = 0, from_y_ = 0, to_x_ = 0, to_y_ = 0;
  bool zoom_out_ = false;
};

// the uv rect of a cover crop of an image in a viewport, zoomed by zoom >= 1 and panned by px, py in -1..1 of the
// slack left on each axis
Rect cover_uv(double image_aspect, double viewport_aspect, double zoom, double px, double py);

}  // namespace cadence::render
