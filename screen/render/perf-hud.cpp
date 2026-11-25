#include "render/perf-hud.h"

#include <algorithm>
#include <cmath>
#include <cstdio>

namespace cadence::screen::render::perf {

namespace {

// a 3 x 5 font: five rows of three bits, the top row first
struct Glyph {
  char c;
  const char* rows;
};
constexpr Glyph kFont[] = {
    {'0', "111101101101111"}, {'1', "010110010010111"}, {'2', "111001111100111"}, {'3', "111001111001111"},
    {'4', "101101111001001"}, {'5', "111100111001111"}, {'6', "111100111101111"}, {'7', "111001010010010"},
    {'8', "111101111101111"}, {'9', "111101111001111"}, {'.', "000000000000010"}, {'/', "001001010100100"},
    {':', "000010000010000"}, {'-', "000000111000000"}, {'A', "010101111101101"}, {'B', "110101110101110"},
    {'C', "011100100100011"}, {'D', "110101101101110"}, {'E', "111100110100111"}, {'F', "111100110100100"},
    {'G', "011100101101011"}, {'H', "101101111101101"}, {'I', "111010010010111"}, {'K', "101101110101101"},
    {'L', "100100100100111"}, {'M', "101111111101101"}, {'N', "110101101101101"}, {'O', "010101101101010"},
    {'P', "110101110100100"}, {'R', "110101110101101"}, {'S', "011100010001110"}, {'T', "111010010010010"},
    {'U', "101101101101111"}, {'V', "101101101101010"}, {'W', "101101111111101"}, {'X', "101101010101101"},
    {'Y', "101101010010010"},
};

struct Rgba {
  std::uint8_t r, g, b, a;
};

void put(std::vector<std::uint8_t>& px, int x, int y, Rgba c) {
  if (x < 0 || y < 0 || x >= Hud::kWidth || y >= Hud::kHeight) return;
  auto* p = px.data() + (static_cast<std::size_t>(y) * Hud::kWidth + static_cast<std::size_t>(x)) * 4;
  p[0] = c.r;
  p[1] = c.g;
  p[2] = c.b;
  p[3] = c.a;
}

void fill(std::vector<std::uint8_t>& px, int x0, int y0, int x1, int y1, Rgba c) {
  for (int y = std::max(0, y0); y < std::min(Hud::kHeight, y1); ++y) {
    for (int x = std::max(0, x0); x < std::min(Hud::kWidth, x1); ++x) put(px, x, y, c);
  }
}

// scale 2: a glyph is 6 x 10 px, 8 px a cell
void text(std::vector<std::uint8_t>& px, int x, int y, const std::string& s, Rgba c) {
  for (const char ch : s) {
    for (const auto& g : kFont) {
      if (g.c != ch) continue;
      for (int row = 0; row < 5; ++row) {
        for (int col = 0; col < 3; ++col) {
          if (g.rows[row * 3 + col] == '1') fill(px, x + col * 2, y + row * 2, x + col * 2 + 2, y + row * 2 + 2, c);
        }
      }
    }
    x += 8;
  }
}

std::string num(double v) {
  char buf[16];
  std::snprintf(buf, sizeof buf, v >= 100 ? "%.0f" : "%.1f", v);
  return buf;
}

}  // namespace

std::vector<std::string> hudLines(const Summary& s) {
  const auto& ph = s.phaseMs;
  const auto& sp = s.spanMs;
  const auto phase = [&](Phase p) { return ph[static_cast<std::size_t>(p)]; };
  const auto span = [&](Span x) { return sp[static_cast<std::size_t>(x)]; };
  return {
      "FPS " + num(s.fps) + " ANIM " + num(s.chainedFps) + " LATE " + std::to_string(s.late),
      "CPU " + num(s.cpuMs.p50) + "/" + num(s.cpuMs.p95) + " FRAME " + num(s.frameMs.p95),
      "UPD " + num(phase(Phase::Update).p95) + " RML " + num(span(Span::RmlUpdate).p95) + " DRAW " +
          num(phase(Phase::Draw).p95),
      "SWAP " + num(phase(Phase::Swap).p95) + " GPU " + num(phase(Phase::Gpu).p95) + " UP " +
          std::to_string(s.spanCount[static_cast<std::size_t>(Span::Upload)]),
  };
}

void rasterHud(std::vector<std::uint8_t>& px, const std::vector<Recorder::Bar>& bars, const Summary& window,
               double periodMs) {
  px.assign(static_cast<std::size_t>(Hud::kWidth) * Hud::kHeight * 4, 0);
  // a dark panel (premultiplied: 80% black)
  fill(px, 0, 0, Hud::kWidth, Hud::kHeight, {0, 0, 0, 204});
  const auto lines = hudLines(window);
  for (std::size_t i = 0; i < lines.size(); ++i) text(px, 4, 4 + static_cast<int>(i) * 13, lines[i], {255, 255, 255, 255});
  // the graph: 2 px a bar, 2 ms a pixel row up to 2 periods
  const int top = 4 + static_cast<int>(lines.size()) * 13 + 2;
  const int bottom = Hud::kHeight - 2;
  const int h = bottom - top;
  const double range = std::max(1.0, periodMs * 2);
  const auto rowOf = [&](double ms) { return bottom - static_cast<int>(std::lround(std::min(ms, range) / range * h)); };
  const int maxBars = Hud::kWidth / 2;
  const int first = std::max(0, static_cast<int>(bars.size()) - maxBars);
  for (int i = first; i < static_cast<int>(bars.size()); ++i) {
    const auto& b = bars[static_cast<std::size_t>(i)];
    const int x = (i - first) * 2;
    const int cpuTop = rowOf(b.cpuMs);
    const int allTop = rowOf(static_cast<double>(b.cpuMs) + static_cast<double>(b.swapMs));
    const bool over = static_cast<double>(b.cpuMs) + static_cast<double>(b.swapMs) > periodMs * 1.5 || b.late;
    fill(px, x, allTop, x + 2, cpuTop, {70, 70, 90, 255});
    fill(px, x, cpuTop, x + 2, bottom, over ? Rgba{230, 60, 60, 255} : Rgba{80, 200, 120, 255});
  }
  // the refresh period
  const int line = rowOf(periodMs);
  for (int x = 0; x < Hud::kWidth; x += 2) put(px, x, line, {255, 210, 0, 255});
}

Hud::~Hud() = default;

void Hud::draw(RenderBackend& backend, Size screen, const Recorder& recorder, const Summary& window) {
  const double periodMs = recorder.periodMs();
  rasterHud(pixels_, recorder.recent(Hud::kWidth / 2), window, periodMs);
  const Size size{kWidth, kHeight};
  if (!texture_) {
    texture_ = backend.createTexture(size, PixelFormat::Rgba8, pixels_.data(), kWidth * 4);
  } else {
    backend.updateTexture(texture_, Rect{0, 0, kWidth, kHeight}, pixels_.data(), kWidth * 4);
  }
  if (!texture_) return;
  // 1.5x on a 1080p screen, 1x at 720p
  const int scale2 = std::max(2, screen.height * 2 / 720);
  QuadParams q;
  q.dst = Rect{16, 16, kWidth * scale2 / 2, kHeight * scale2 / 2};
  q.blend = Blend::Premultiplied;
  backend.drawTexturedQuad(texture_, q);
}

void Hud::release(RenderBackend& backend) {
  if (texture_) backend.destroyTexture(texture_);
  texture_ = {};
}

}  // namespace cadence::screen::render::perf
