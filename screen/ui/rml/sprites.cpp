#include "ui/rml/sprites.h"

#include "qrcodegen.hpp"

#include <algorithm>
#include <charconv>
#include <cmath>
#include <numbers>
#include <string>

namespace cadence::screen::ui {

namespace {

constexpr int kSpriteSize = 96;
constexpr double kPi = std::numbers::pi;

Pixels blank(int w, int h) {
  Pixels p;
  p.width = w;
  p.height = h;
  p.rgba.assign(static_cast<std::size_t>(w) * static_cast<std::size_t>(h) * 4, 0);
  return p;
}

void put(Pixels& p, int x, int y, std::uint8_t r, std::uint8_t g, std::uint8_t b, double coverage) {
  const auto a = static_cast<double>(std::clamp(coverage, 0.0, 1.0));
  auto* px = p.rgba.data() + (static_cast<std::size_t>(y) * static_cast<std::size_t>(p.width) + static_cast<std::size_t>(x)) * 4;
  px[0] = static_cast<std::uint8_t>(std::lround(r * a));
  px[1] = static_cast<std::uint8_t>(std::lround(g * a));
  px[2] = static_cast<std::uint8_t>(std::lround(b * a));
  px[3] = static_cast<std::uint8_t>(std::lround(255 * a));
}

// an arc of a ring from start to start + sweep (radians, clockwise from 12 o'clock), 4x4 supersampled
Pixels arc(double start, double sweep, double thickness) {
  auto p = blank(kSpriteSize, kSpriteSize);
  const double c = kSpriteSize / 2.0;
  const double outer = c - 2;
  const double inner = outer - thickness;
  for (int y = 0; y < kSpriteSize; ++y) {
    for (int x = 0; x < kSpriteSize; ++x) {
      int hits = 0;
      for (int sy = 0; sy < 4; ++sy) {
        for (int sx = 0; sx < 4; ++sx) {
          const double dx = x + (sx + 0.5) / 4 - c;
          const double dy = y + (sy + 0.5) / 4 - c;
          const double r = std::sqrt(dx * dx + dy * dy);
          if (r < inner || r > outer) continue;
          double a = std::atan2(dx, -dy);
          if (a < 0) a += 2 * kPi;
          double rel = a - start;
          while (rel < 0) rel += 2 * kPi;
          while (rel >= 2 * kPi) rel -= 2 * kPi;
          if (rel <= sweep) ++hits;
        }
      }
      if (hits > 0) put(p, x, y, 255, 255, 255, hits / 16.0);
    }
  }
  return p;
}

std::uint64_t fnv(std::string_view s) {
  std::uint64_t h = 1469598103934665603ull;
  for (const char sc : s) {
    const auto ch = static_cast<unsigned char>(sc);
    h ^= ch;
    h *= 1099511628211ull;
  }
  return h;
}

// the gallery's stand-in art: a diagonal gradient between two muted colours from the seed and a sun disc, so a
// poster has shape and tone without any third-party image
Pixels art(std::string_view seed) {
  constexpr int w = 134;
  constexpr int h = 201;
  auto p = blank(w, h);
  const auto hsh = fnv(seed);
  const auto hue = [](double t, double sat, double val, double out[3]) {
    const double hh = std::fmod(t, 1.0) * 6;
    const int i = static_cast<int>(hh);
    const double f = hh - i;
    const double pp = val * (1 - sat), q = val * (1 - sat * f), tt = val * (1 - sat * (1 - f));
    const double table[6][3] = {{val, tt, pp}, {q, val, pp}, {pp, val, tt}, {pp, q, val}, {tt, pp, val}, {val, pp, q}};
    for (int k = 0; k < 3; ++k) out[k] = table[i % 6][k];
  };
  double a[3], b[3], s[3];
  hue(static_cast<double>(hsh % 360) / 360, 0.45, 0.55, a);
  hue(static_cast<double>((hsh >> 9) % 360) / 360, 0.55, 0.22, b);
  hue(static_cast<double>((hsh >> 18) % 360) / 360, 0.35, 0.92, s);
  const double cx = 30 + static_cast<double>((hsh >> 27) % 74);
  const double cy = 40 + static_cast<double>((hsh >> 35) % 90);
  const double rad = 18 + static_cast<double>((hsh >> 43) % 22);
  for (int y = 0; y < h; ++y) {
    for (int x = 0; x < w; ++x) {
      const double t = (static_cast<double>(x) / w + static_cast<double>(y) / h) / 2;
      double c[3];
      for (int k = 0; k < 3; ++k) c[k] = a[k] * (1 - t) + b[k] * t;
      const double d = std::hypot(x - cx, y - cy) - rad;
      const double sun = std::clamp(0.5 - d, 0.0, 1.0);
      for (int k = 0; k < 3; ++k) c[k] = c[k] * (1 - sun) + s[k] * sun;
      put(p, x, y, static_cast<std::uint8_t>(c[0] * 255), static_cast<std::uint8_t>(c[1] * 255),
          static_cast<std::uint8_t>(c[2] * 255), 1.0);
    }
  }
  return p;
}

std::optional<std::string> unhex(std::string_view hex) {
  if (hex.size() % 2 != 0) return std::nullopt;
  std::string out;
  for (std::size_t i = 0; i < hex.size(); i += 2) {
    unsigned v = 0;
    const auto r = std::from_chars(hex.data() + i, hex.data() + i + 2, v, 16);
    if (r.ec != std::errc{} || r.ptr != hex.data() + i + 2) return std::nullopt;
    out.push_back(static_cast<char>(v));
  }
  return out;
}

std::optional<Pixels> qr(std::string_view hex) {
  const auto payload = unhex(hex);
  if (!payload || payload->size() > 512) return std::nullopt;
  try {
    const auto code = qrcodegen::QrCode::encodeBinary(std::vector<std::uint8_t>(payload->begin(), payload->end()),
                                                      qrcodegen::QrCode::Ecc::MEDIUM);
    const int n = code.getSize();
    // 4 modules of quiet zone; each module 4 pixels, scaled to 240 dp by the element with nearest-looking edges
    constexpr int quiet = 4;
    constexpr int scale = 4;
    const int size = (n + quiet * 2) * scale;
    auto p = blank(size, size);
    for (int y = 0; y < size; ++y) {
      for (int x = 0; x < size; ++x) {
        const int mx = x / scale - quiet;
        const int my = y / scale - quiet;
        const bool dark = mx >= 0 && my >= 0 && mx < n && my < n && code.getModule(mx, my);
        // ink #0C0E13 on paper #F2EDE4 (tokens.json)
        if (dark) {
          put(p, x, y, 0x0C, 0x0E, 0x13, 1.0);
        } else {
          put(p, x, y, 0xF2, 0xED, 0xE4, 1.0);
        }
      }
    }
    return p;
  } catch (const std::exception&) {
    return std::nullopt;
  }
}

// a sparkline: points (bytes 0-255, low to high) as a 2 dp paper polyline in 240 x 48 dp, drawn at 2x
std::optional<Pixels> spark(std::string_view hex) {
  const auto bytes = unhex(hex);
  if (!bytes || bytes->size() < 2 || bytes->size() > 64) return std::nullopt;
  constexpr int w = 480, h = 96;
  constexpr double half = 2.0;
  auto p = blank(w, h);
  const auto n = bytes->size();
  std::vector<double> xs(n), ys(n);
  for (std::size_t i = 0; i < n; ++i) {
    xs[i] = 4 + (w - 8) * static_cast<double>(i) / static_cast<double>(n - 1);
    ys[i] = 4 + (h - 8) * (1 - static_cast<unsigned char>((*bytes)[i]) / 255.0);
  }
  for (int y = 0; y < h; ++y) {
    for (int x = 0; x < w; ++x) {
      double best = 1e9;
      for (std::size_t i = 0; i + 1 < n; ++i) {
        const double ax = xs[i], ay = ys[i], bx = xs[i + 1], by = ys[i + 1];
        const double dx = bx - ax, dy = by - ay;
        const double len = dx * dx + dy * dy;
        double t = len > 0 ? ((x + 0.5 - ax) * dx + (y + 0.5 - ay) * dy) / len : 0;
        t = std::clamp(t, 0.0, 1.0);
        best = std::min(best, std::hypot(x + 0.5 - (ax + t * dx), y + 0.5 - (ay + t * dy)));
      }
      const double c = std::clamp(half + 0.5 - best, 0.0, 1.0);
      if (c > 0) put(p, x, y, 0xF2, 0xED, 0xE4, c);
    }
  }
  return p;
}

// the focus glow (design section 7: ember-glow 0 0 32 dp around the ring): a static, pre-blurred nine-patch, so no
// box-shadow blur layer is ever rendered; the inside stays clear so it never tints the art. it follows the ring's
// radius (a card's 24 dp, a pill's or a circle's half height), else its corners stand off a round ring as a square
// halo: 32 dp of glow, the ring's corner, then a 16 dp middle the nine-patch stretches (the cards' sprite keeps 48)
Pixels glow(int ringRadius) {
  const int size = ringRadius == 24 ? 160 : 2 * (32 + ringRadius) + 16;
  const double lo = 32, hi = size - 32, radius = ringRadius, sigma = 12;
  auto p = blank(size, size);
  for (int y = 0; y < size; ++y) {
    for (int x = 0; x < size; ++x) {
      const double px = x + 0.5, py = y + 0.5;
      const double cx = std::clamp(px, lo + radius, hi - radius);
      const double cy = std::clamp(py, lo + radius, hi - radius);
      const double d = std::hypot(px - cx, py - cy) - radius;
      if (d <= 0) continue;
      const double a = 0.45 * std::exp(-(d * d) / (2 * sigma * sigma));
      put(p, x, y, 0xFF, 0x9A, 0x3C, a);
    }
  }
  return p;
}

// a glow's ring radius in dp: 4 to 96
std::optional<int> radiusOf(std::string_view s) {
  int v = -1;
  const auto r = std::from_chars(s.data(), s.data() + s.size(), v);
  if (r.ec != std::errc{} || r.ptr != s.data() + s.size() || v < 4 || v > 96) return std::nullopt;
  return v;
}

std::optional<int> frameOf(std::string_view s) {
  int v = -1;
  const auto r = std::from_chars(s.data(), s.data() + s.size(), v);
  if (r.ec != std::errc{} || r.ptr != s.data() + s.size() || v < 0 || v > 7) return std::nullopt;
  return v;
}

}  // namespace

std::string hexOf(std::string_view bytes) {
  static constexpr char kHex[] = "0123456789abcdef";
  std::string out;
  out.reserve(bytes.size() * 2);
  for (const char sc : bytes) {
    const auto c = static_cast<unsigned char>(sc);
    out.push_back(kHex[c >> 4]);
    out.push_back(kHex[c & 15]);
  }
  return out;
}

std::optional<Pixels> generateSprite(std::string_view source) {
  if (!source.starts_with("gen:")) return std::nullopt;
  const auto rest = source.substr(4);
  if (rest == "glow") return glow(24);
  if (rest.starts_with("glow:")) {
    const auto r = radiusOf(rest.substr(5));
    if (!r) return std::nullopt;
    return glow(*r);
  }
  if (rest == "white") {
    auto p = blank(1, 1);
    put(p, 0, 0, 255, 255, 255, 1.0);
    return p;
  }
  if (rest.starts_with("spinner:")) {
    const auto f = frameOf(rest.substr(8));
    if (!f) return std::nullopt;
    return arc(*f * kPi / 4, 1.5 * kPi, 8);
  }
  if (rest.starts_with("arc:")) {
    const auto f = frameOf(rest.substr(4));
    if (!f) return std::nullopt;
    return arc(0, (*f + 1) * kPi / 4, 6);
  }
  if (rest.starts_with("qr:")) return qr(rest.substr(3));
  if (rest.starts_with("art:")) return art(rest.substr(4));
  if (rest.starts_with("spark:")) return spark(rest.substr(6));
  return std::nullopt;
}

}  // namespace cadence::screen::ui
