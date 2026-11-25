#include "analyzer.h"
#include "classic.h"
#include "fixture.h"
#include "raster.h"

#include <catch2/catch_approx.hpp>
#include <catch2/catch_test_macros.hpp>

#include <cmath>
#include <cstdlib>
#include <cstring>
#include <filesystem>
#include <fstream>
#include <iterator>
#include <numbers>
#include <string>

namespace viz = cadence::render::viz;
using cadence::render::Color;
using cadence::render::Rect;

namespace {

constexpr double kDt = 1.0 / 60;

// the analyzer after the fixture's first `frames` snapshots, one a frame
viz::Analyzer fed(int frames, viz::AnalyzerConfig c = {}) {
  viz::Analyzer a(c);
  for (int k = 0; k < frames; ++k) {
    const auto s = viz::fixture::snapshot(k);
    a.push_pcm(s, 2, viz::fixture::kRate);
    a.step(kDt);
  }
  return a;
}

std::vector<std::uint8_t> render(const viz::Analyzer& a, viz::Style style) {
  viz::RasterCanvas c(640, 180);
  c.clear(Color{0.05f, 0.04f, 0.05f, 1});
  viz::Classic(viz::ClassicLook{}).draw(c, Rect{16, 12, 608, 156}, a, style);
  return c.png();
}

// compares with tests/golden/<name>.png; CADENCE_UPDATE_GOLDEN=1 rewrites it, a mismatch writes <name>.actual.png
// beside the build
void golden(const std::string& name, const std::vector<std::uint8_t>& png) {
  const std::filesystem::path file = std::filesystem::path(CADENCE_VIZ_GOLDEN) / (name + ".png");
  if (const char* u = std::getenv("CADENCE_UPDATE_GOLDEN"); u && std::string(u) == "1") {
    std::ofstream(file, std::ios::binary).write(reinterpret_cast<const char*>(png.data()),
                                                static_cast<std::streamsize>(png.size()));
    return;
  }
  std::ifstream in(file, std::ios::binary);
  const std::vector<std::uint8_t> want((std::istreambuf_iterator<char>(in)), std::istreambuf_iterator<char>());
  if (want != png) {
    std::ofstream(name + ".actual.png", std::ios::binary)
        .write(reinterpret_cast<const char*>(png.data()), static_cast<std::streamsize>(png.size()));
  }
  INFO(name << ": differs from " << file.string() << " (CADENCE_UPDATE_GOLDEN=1 after an intended change)");
  CHECK(want == png);
}

}  // namespace

TEST_CASE("golden: bars, mirror and scope while playing") {
  const auto a = fed(20);
  golden("bars", render(a, viz::Style::Bars));
  golden("mirror", render(a, viz::Style::Mirror));
  golden("scope", render(a, viz::Style::Scope));
}

TEST_CASE("golden: bars falling with their peaks after the sound stops") {
  golden("bars-falling", render(fed(36), viz::Style::Bars));
}

TEST_CASE("golden: 16 bars from audiod's bands") {
  viz::Analyzer a(viz::AnalyzerConfig{.bars = 16});
  const std::uint8_t bands[16] = {250, 230, 200, 180, 160, 140, 120, 100, 90, 80, 70, 60, 50, 40, 30, 20};
  a.push_bands(bands);
  a.step(kDt);
  golden("bands16", render(a, viz::Style::Bars));
}

TEST_CASE("a full-scale sine reads full scale in its bar") {
  viz::Analyzer a(viz::AnalyzerConfig{.bars = 32});
  std::vector<float> s(512);
  for (std::size_t i = 0; i < s.size(); ++i) {
    s[i] = static_cast<float>(std::sin(2 * std::numbers::pi * 1000.0 * static_cast<double>(i) / 48000.0));
  }
  a.push_pcm(s, 1, 48000);
  a.step(kDt);
  double top = 0;
  for (double b : a.bars()) top = std::max(top, b);
  CHECK(top > 0.95);
  CHECK(top <= 1.0);
}

TEST_CASE("bars rise at once and fall at most fall_per_s; peaks hold, then fall") {
  viz::Analyzer a(viz::AnalyzerConfig{.bars = 4, .fall_per_s = 1.0, .peak_hold_s = 0.5});
  const std::uint8_t loud[4] = {255, 255, 255, 255};
  a.push_bands(loud);
  a.step(0.01);
  CHECK(a.bars()[0] == 1.0);
  a.silence();
  a.step(0.25);
  CHECK(a.bars()[0] == Catch::Approx(0.75));
  CHECK(a.peaks()[0] == 1.0);
  a.step(0.3);
  CHECK(a.peaks()[0] < 1.0);
  for (int i = 0; i < 200; ++i) a.step(0.05);
  CHECK(a.bars()[0] == 0.0);
  CHECK(a.peaks()[0] == 0.0);
  CHECK_FALSE(a.moving());
}

TEST_CASE("reduced motion jumps to the levels") {
  viz::Analyzer a(viz::AnalyzerConfig{.bars = 2});
  const std::uint8_t loud[2] = {255, 255};
  a.push_bands(loud);
  a.step(0.01, true);
  a.silence();
  a.step(0.01, true);
  CHECK(a.bars()[0] == 0.0);
  CHECK(a.peaks()[0] == 0.0);
}

TEST_CASE("snapshot samples decode as little-endian f32") {
  const std::uint8_t bytes[9] = {0x00, 0x00, 0x80, 0x3f, 0x00, 0x00, 0x00, 0xbf, 0x01};
  const auto s = viz::decode_samples(bytes);
  REQUIRE(s.size() == 2);
  CHECK(s[0] == 1.0f);
  CHECK(s[1] == -0.5f);
}

TEST_CASE("garbage pcm stays in range") {
  viz::Analyzer a;
  std::vector<float> s(1024, std::nanf(""));
  s[3] = 1e30f;
  a.push_pcm(s, 2, 48000);
  a.push_pcm({}, 0, 48000);
  a.step(kDt);
  for (double b : a.bars()) {
    CHECK(b >= 0.0);
    CHECK(b <= 1.0);
  }
}

TEST_CASE("the png encoder writes a valid header and is deterministic") {
  viz::RasterCanvas c(3, 2);
  c.clear(Color{1, 0, 0, 1});
  const auto p = c.png();
  REQUIRE(p.size() > 33);
  CHECK(std::memcmp(p.data(), "\x89PNG\r\n\x1a\n", 8) == 0);
  CHECK(p == c.png());
}
