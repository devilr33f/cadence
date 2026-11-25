#include "ui/images/blur.h"

#include <algorithm>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <utility>
#include <vector>

namespace cadence::screen::ui::images {

namespace {

// interleaved gradient noise in [0, 1) for pixel (x, y): the last pass rounds with it, so a smooth blurred field
// quantised to 8 bits dithers instead of stepping into bands (a dark backdrop shows every step)
double noiseAt(int x, int y) {
  const double v = 0.06711056 * x + 0.00583715 * y;
  const double f = 52.9829189 * (v - std::floor(v));
  return f - std::floor(f);
}

// one box pass along a line of n pixels, stride apart, through a scratch copy: a running sum per channel. dither
// (the last pass): the pixel's coordinates are (line, i) when vertical, (i, line) when not
void boxLine(std::uint8_t* p, int n, std::size_t stride, int r, std::vector<std::uint32_t>& line, bool dither = false,
             int lineIndex = 0, bool vertical = false) {
  line.resize(static_cast<std::size_t>(n) * 4);
  for (int i = 0; i < n; ++i) {
    for (std::size_t c = 0; c < 4; ++c) line[static_cast<std::size_t>(i) * 4 + c] = p[static_cast<std::size_t>(i) * stride + c];
  }
  const auto at = [&](int i, std::size_t c) { return line[static_cast<std::size_t>(std::clamp(i, 0, n - 1)) * 4 + c]; };
  const std::uint32_t span = static_cast<std::uint32_t>(2 * r + 1);
  for (std::size_t c = 0; c < 4; ++c) {
    std::uint32_t sum = 0;
    for (int i = -r; i <= r; ++i) sum += at(i, c);
    for (int i = 0; i < n; ++i) {
      std::uint8_t v = static_cast<std::uint8_t>((sum + span / 2) / span);
      if (dither && c < 3) {
        const double exact = static_cast<double>(sum) / span;
        const double n01 = vertical ? noiseAt(lineIndex, i) : noiseAt(i, lineIndex);
        v = static_cast<std::uint8_t>(std::clamp(std::floor(exact + n01), 0.0, 255.0));
      }
      p[static_cast<std::size_t>(i) * stride + c] = v;
      sum += at(i + r + 1, c);
      sum -= at(i - r, c);
    }
  }
}

}  // namespace

void blur(Pixels& image, int radius) {
  if (radius <= 0 || image.width <= 0 || image.height <= 0) return;
  if (image.rgba.size() < static_cast<std::size_t>(image.width) * static_cast<std::size_t>(image.height) * 4) return;
  // three passes of a box a third as wide: close to a gaussian of that radius
  const int r = std::max(1, radius / 2);
  std::vector<std::uint32_t> line;
  const std::size_t row = static_cast<std::size_t>(image.width) * 4;
  for (int pass = 0; pass < 3; ++pass) {
    for (int y = 0; y < image.height; ++y) boxLine(image.rgba.data() + static_cast<std::size_t>(y) * row, image.width, 4, r, line);
    for (int x = 0; x < image.width; ++x) {
      boxLine(image.rgba.data() + static_cast<std::size_t>(x) * 4, image.height, row, r, line, pass == 2, x, true);
    }
  }
  // premultiplied stays premultiplied: the dither never lifts a colour past its alpha
  for (std::size_t i = 0; i + 3 < image.rgba.size(); i += 4) {
    for (std::size_t c = 0; c < 3; ++c) image.rgba[i + c] = std::min(image.rgba[i + c], image.rgba[i + 3]);
  }
}

bool fitCover(Pixels& image, int width, int height) {
  if (width <= 0 || height <= 0 || image.width <= 0 || image.height <= 0) return false;
  if (image.rgba.size() < static_cast<std::size_t>(image.width) * static_cast<std::size_t>(image.height) * 4) return false;
  // the source rect with the target's shape, centred
  const double sw = image.width, sh = image.height;
  double cw = sw, ch = sw * height / width;
  if (ch > sh) {
    ch = sh;
    cw = sh * width / height;
  }
  const double x0 = (sw - cw) / 2, y0 = (sh - ch) / 2;
  Pixels out;
  out.width = width;
  out.height = height;
  out.rgba.resize(static_cast<std::size_t>(width) * static_cast<std::size_t>(height) * 4);
  const auto px = [&](int x, int y, std::size_t c) {
    x = std::clamp(x, 0, image.width - 1);
    y = std::clamp(y, 0, image.height - 1);
    return static_cast<double>(
        image.rgba[(static_cast<std::size_t>(y) * static_cast<std::size_t>(image.width) + static_cast<std::size_t>(x)) * 4 + c]);
  };
  for (int y = 0; y < height; ++y) {
    const double fy = y0 + (y + 0.5) * ch / height - 0.5;
    const int iy = static_cast<int>(std::floor(fy));
    const double ty = fy - iy;
    for (int x = 0; x < width; ++x) {
      const double fx = x0 + (x + 0.5) * cw / width - 0.5;
      const int ix = static_cast<int>(std::floor(fx));
      const double tx = fx - ix;
      auto* d = out.rgba.data() + (static_cast<std::size_t>(y) * static_cast<std::size_t>(width) + static_cast<std::size_t>(x)) * 4;
      for (std::size_t c = 0; c < 4; ++c) {
        const double top = px(ix, iy, c) * (1 - tx) + px(ix + 1, iy, c) * tx;
        const double bottom = px(ix, iy + 1, c) * (1 - tx) + px(ix + 1, iy + 1, c) * tx;
        d[c] = static_cast<std::uint8_t>(std::clamp(top * (1 - ty) + bottom * ty + 0.5, 0.0, 255.0));
      }
    }
  }
  image = std::move(out);
  return true;
}

void roundCorners(Pixels& image, int radius) {
  const int w = image.width, h = image.height;
  radius = std::min({radius, w / 2, h / 2});
  if (radius <= 0 || image.rgba.size() < static_cast<std::size_t>(w) * static_cast<std::size_t>(h) * 4) return;
  const double r = radius;
  for (int y = 0; y < radius; ++y) {
    for (int x = 0; x < radius; ++x) {
      // the pixel's centre against the corner circle's, its edge a pixel wide
      const double dx = r - (x + 0.5), dy = r - (y + 0.5);
      const double cover = std::clamp(r - std::sqrt(dx * dx + dy * dy) + 0.5, 0.0, 1.0);
      if (cover >= 1.0) continue;
      for (const auto& [cx, cy] : {std::pair{x, y}, std::pair{w - 1 - x, y}, std::pair{x, h - 1 - y}, std::pair{w - 1 - x, h - 1 - y}}) {
        auto* p = image.rgba.data() + (static_cast<std::size_t>(cy) * static_cast<std::size_t>(w) + static_cast<std::size_t>(cx)) * 4;
        for (int c = 0; c < 4; ++c) p[c] = static_cast<std::uint8_t>(std::lround(p[c] * cover));
      }
    }
  }
}

}  // namespace cadence::screen::ui::images
