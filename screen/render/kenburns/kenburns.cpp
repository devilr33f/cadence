#include "kenburns.h"

#include <algorithm>
#include <cmath>
#include <numbers>

namespace cadence::render {

namespace {

std::uint64_t splitmix(std::uint64_t& s) {
  std::uint64_t z = (s += 0x9e3779b97f4a7c15ULL);
  z = (z ^ (z >> 30U)) * 0xbf58476d1ce4e5b9ULL;
  z = (z ^ (z >> 27U)) * 0x94d049bb133111ebULL;
  return z ^ (z >> 31U);
}

double unit(std::uint64_t& s) { return static_cast<double>(splitmix(s) >> 11U) / 9007199254740992.0; }

// design §8's ambient token: linear in-out, a sine ease that starts and ends at rest
double ease(double t) { return 0.5 - 0.5 * std::cos(std::numbers::pi * std::clamp(t, 0.0, 1.0)); }

}  // namespace

Rect cover_uv(double image_aspect, double viewport_aspect, double zoom, double px, double py) {
  double w = 1, h = 1;
  if (image_aspect > viewport_aspect) {
    w = viewport_aspect / image_aspect;
  } else if (image_aspect < viewport_aspect) {
    h = image_aspect / viewport_aspect;
  }
  zoom = std::max(zoom, 1.0);
  w /= zoom;
  h /= zoom;
  const double cx = 0.5 + std::clamp(px, -1.0, 1.0) * (1 - w) / 2;
  const double cy = 0.5 + std::clamp(py, -1.0, 1.0) * (1 - h) / 2;
  return Rect{static_cast<float>(cx - w / 2), static_cast<float>(cy - h / 2), static_cast<float>(w),
              static_cast<float>(h)};
}

void KenBurns::start(TextureId texture, int image_w, int image_h, std::uint64_t seed, double now_s) {
  texture_ = texture;
  image_w_ = std::max(image_w, 1);
  image_h_ = std::max(image_h, 1);
  start_s_ = now_s;
  frozen_at_ = -1;
  frozen_total_ = 0;
  std::uint64_t s = seed;
  // the pan runs across the slack between two points on opposite sides of the centre
  const double angle = 2 * std::numbers::pi * unit(s);
  const double reach = 0.6 + 0.4 * unit(s);
  from_x_ = std::cos(angle) * reach;
  from_y_ = std::sin(angle) * reach;
  to_x_ = -from_x_;
  to_y_ = -from_y_;
  zoom_out_ = unit(s) < 0.5;
}

void KenBurns::freeze(double now_s) {
  if (frozen_at_ < 0) frozen_at_ = now_s;
}

void KenBurns::resume(double now_s) {
  if (frozen_at_ < 0) return;
  frozen_total_ += std::max(now_s - frozen_at_, 0.0);
  frozen_at_ = -1;
}

double KenBurns::progress(double now_s) const {
  const double at = frozen_at_ >= 0 ? frozen_at_ : now_s;
  const double t = (at - start_s_ - frozen_total_) / std::max(c_.duration_s, 1e-3);
  return std::clamp(t, 0.0, 1.0);
}

bool KenBurns::done(double now_s) const {
  return frozen_at_ < 0 && now_s - start_s_ - frozen_total_ >= c_.duration_s;
}

TexturedQuad KenBurns::quad(Rect viewport, double now_s) const {
  const double image_aspect = static_cast<double>(image_w_) / static_cast<double>(image_h_);
  const double vp_aspect = static_cast<double>(viewport.w) / std::max(static_cast<double>(viewport.h), 1.0);
  TexturedQuad q;
  q.texture = texture_;
  q.dst = viewport;
  const double p = progress(now_s);
  if (reduced_) {
    q.uv = cover_uv(image_aspect, vp_aspect, 1.0, 0, 0);
  } else {
    const double e = ease(p);
    double zoom = c_.zoom_from + (c_.zoom_to - c_.zoom_from) * e;
    if (zoom_out_) zoom = c_.zoom_to - (c_.zoom_to - c_.zoom_from) * e;
    q.uv = cover_uv(image_aspect, vp_aspect, zoom, from_x_ + (to_x_ - from_x_) * e, from_y_ + (to_y_ - from_y_) * e);
  }
  // the fades count wall time on screen, frozen included, so a frozen image stays visible
  const double elapsed = (frozen_at_ >= 0 ? frozen_at_ : now_s) - start_s_ - frozen_total_;
  const double fade = std::max(c_.fade_s, 1e-3);
  const double in = std::clamp(elapsed / fade, 0.0, 1.0);
  const double out = std::clamp((c_.duration_s - elapsed) / fade, 0.0, 1.0);
  q.opacity = static_cast<float>(std::min(in, out));
  return q;
}

}  // namespace cadence::render
