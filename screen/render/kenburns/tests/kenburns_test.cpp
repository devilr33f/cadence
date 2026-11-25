#include "kenburns.h"

#include <catch2/catch_approx.hpp>
#include <catch2/catch_test_macros.hpp>

using cadence::render::cover_uv;
using cadence::render::KenBurns;
using cadence::render::KenBurnsConfig;
using cadence::render::Rect;

namespace {

constexpr Rect kScreen{0, 0, 1920, 1080};

bool inside(const Rect& r) {
  constexpr float eps = 1e-5f;
  return r.x >= -eps && r.y >= -eps && r.x + r.w <= 1 + eps && r.y + r.h <= 1 + eps && r.w > 0 && r.h > 0;
}

}  // namespace

TEST_CASE("cover crop keeps the viewport's aspect") {
  const Rect wide = cover_uv(2.0, 16.0 / 9, 1, 0, 0);
  CHECK(wide.h == 1.0f);
  CHECK(wide.w == Catch::Approx((16.0 / 9) / 2.0));
  CHECK(wide.x == Catch::Approx((1 - wide.w) / 2));
  const Rect tall = cover_uv(0.75, 16.0 / 9, 1, 0, 0);
  CHECK(tall.w == 1.0f);
  CHECK(tall.h == Catch::Approx(0.75 / (16.0 / 9)));
  const Rect zoomed = cover_uv(16.0 / 9, 16.0 / 9, 1.25, 1, -1);
  CHECK(zoomed.w == Catch::Approx(0.8));
  CHECK(zoomed.x + zoomed.w == Catch::Approx(1.0));
  CHECK(zoomed.y == Catch::Approx(0.0).margin(1e-6));
}

TEST_CASE("the drift never shows past the image's edges, for any seed and shape") {
  for (std::uint64_t seed = 0; seed < 200; ++seed) {
    for (auto [w, h] : {std::pair{960, 540}, std::pair{540, 960}, std::pair{1200, 400}, std::pair{500, 500}}) {
      KenBurns kb;
      kb.start(7, w, h, seed, 100);
      for (double t = 100; t <= 131; t += 0.5) {
        const auto q = kb.quad(kScreen, t);
        INFO(seed << " " << w << "x" << h << " t " << t);
        REQUIRE(inside(q.uv));
        REQUIRE(q.texture == 7);
      }
    }
  }
}

TEST_CASE("a seed moves alike; the drift is slow and smooth") {
  KenBurns a, b;
  a.start(1, 960, 540, 42, 0);
  b.start(1, 960, 540, 42, 0);
  CHECK(a.quad(kScreen, 12.3).uv.x == b.quad(kScreen, 12.3).uv.x);
  // at rest at both ends (ease), moving in the middle
  const auto s0 = a.quad(kScreen, 0).uv, s1 = a.quad(kScreen, 0.1).uv;
  const auto m0 = a.quad(kScreen, 15).uv, m1 = a.quad(kScreen, 15.1).uv;
  CHECK(std::abs(s1.x - s0.x) < std::abs(m1.x - m0.x) + 1e-7f);
  CHECK(std::abs(m1.x - m0.x) < 0.01f);
}

TEST_CASE("fades in, holds, fades out, done after its time") {
  KenBurns kb(KenBurnsConfig{.duration_s = 20, .fade_s = 2});
  kb.start(1, 960, 540, 1, 10);
  CHECK(kb.quad(kScreen, 10).opacity == 0.0f);
  CHECK(kb.quad(kScreen, 11).opacity == Catch::Approx(0.5));
  CHECK(kb.quad(kScreen, 20).opacity == 1.0f);
  CHECK(kb.quad(kScreen, 29).opacity == Catch::Approx(0.5));
  CHECK_FALSE(kb.done(29.9));
  CHECK(kb.done(30));
}

TEST_CASE("reduced motion holds the crop still") {
  KenBurns kb;
  kb.set_reduced_motion(true);
  kb.start(1, 1200, 400, 9, 0);
  const auto a = kb.quad(kScreen, 3).uv, b = kb.quad(kScreen, 25).uv;
  CHECK(a.x == b.x);
  CHECK(a.w == b.w);
}

TEST_CASE("freeze stops the drift where it is, resume carries on") {
  KenBurns kb(KenBurnsConfig{.duration_s = 30});
  kb.start(1, 960, 540, 5, 0);
  kb.freeze(10);
  const auto at = kb.quad(kScreen, 10).uv;
  CHECK(kb.quad(kScreen, 50).uv.x == at.x);
  CHECK_FALSE(kb.done(50));
  CHECK(kb.quad(kScreen, 50).opacity == 1.0f);
  kb.resume(50);
  CHECK(kb.quad(kScreen, 50).uv.x == at.x);
  CHECK(kb.progress(60) == Catch::Approx(20.0 / 30));
  CHECK(kb.done(70));
}
