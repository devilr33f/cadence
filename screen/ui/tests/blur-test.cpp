#include "ui/images/blur.h"

#include <catch2/catch_test_macros.hpp>

#include <cstdint>
#include <cstdlib>

using namespace cadence::screen::ui;

namespace {

Pixels solid(int w, int h, std::uint8_t r, std::uint8_t g, std::uint8_t b) {
  Pixels p;
  p.width = w;
  p.height = h;
  p.rgba.resize(static_cast<std::size_t>(w * h * 4));
  for (std::size_t i = 0; i < p.rgba.size(); i += 4) {
    p.rgba[i] = r;
    p.rgba[i + 1] = g;
    p.rgba[i + 2] = b;
    p.rgba[i + 3] = 255;
  }
  return p;
}

const std::uint8_t* at(const Pixels& p, int x, int y) { return p.rgba.data() + static_cast<std::size_t>((y * p.width + x) * 4); }

}  // namespace

TEST_CASE("a blur keeps a flat colour flat (the dither moves it a step at most)") {
  auto p = solid(64, 40, 120, 60, 200);
  images::blur(p, 8);
  for (int y = 0; y < p.height; ++y) {
    for (int x = 0; x < p.width; ++x) {
      CHECK(std::abs(at(p, x, y)[0] - 120) <= 1);
      CHECK(std::abs(at(p, x, y)[2] - 200) <= 1);
      CHECK(at(p, x, y)[3] == 255);
    }
  }
}

TEST_CASE("a blur softens a hard edge and keeps premultiplied colour under its alpha") {
  auto p = solid(64, 8, 0, 0, 0);
  for (int y = 0; y < p.height; ++y) {
    for (int x = 32; x < 64; ++x) {
      auto* d = p.rgba.data() + static_cast<std::size_t>((y * p.width + x) * 4);
      d[0] = d[1] = d[2] = 255;
    }
  }
  images::blur(p, 8);
  // the step became a ramp: dark well left, light well right, between in the middle
  CHECK(at(p, 20, 4)[0] < 20);
  CHECK(at(p, 44, 4)[0] > 235);
  CHECK(at(p, 31, 4)[0] > 60);
  CHECK(at(p, 31, 4)[0] < 200);
  auto q = solid(16, 16, 255, 255, 255);
  for (std::size_t i = 0; i < q.rgba.size(); i += 4) q.rgba[i] = q.rgba[i + 1] = q.rgba[i + 2] = q.rgba[i + 3] = 100;
  images::blur(q, 4);
  for (std::size_t i = 0; i < q.rgba.size(); i += 4) CHECK(q.rgba[i] <= q.rgba[i + 3]);
}

TEST_CASE("radius 0 or an empty image changes nothing") {
  auto p = solid(4, 4, 10, 20, 30);
  const auto before = p.rgba;
  images::blur(p, 0);
  CHECK(p.rgba == before);
  Pixels empty;
  images::blur(empty, 5);
  CHECK(empty.rgba.empty());
}

TEST_CASE("fitCover crops to the slot's shape about the centre and resamples to its size") {
  // a portrait poster: left half red, right half blue, top to bottom
  auto p = solid(40, 60, 255, 0, 0);
  for (int y = 0; y < 60; ++y) {
    for (int x = 20; x < 40; ++x) {
      auto* d = p.rgba.data() + static_cast<std::size_t>((y * 40 + x) * 4);
      d[0] = 0;
      d[2] = 255;
    }
  }
  REQUIRE(images::fitCover(p, 160, 90));
  CHECK(p.width == 160);
  CHECK(p.height == 90);
  CHECK(at(p, 10, 45)[0] == 255);
  CHECK(at(p, 150, 45)[2] == 255);
  Pixels none;
  CHECK_FALSE(images::fitCover(none, 10, 10));
  CHECK_FALSE(images::fitCover(p, 0, 10));
}
