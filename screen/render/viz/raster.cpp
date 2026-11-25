#include "raster.h"

#include <algorithm>
#include <array>
#include <cmath>

namespace cadence::render::viz {

namespace {

std::uint8_t to8(float v) { return static_cast<std::uint8_t>(std::lround(std::clamp(v, 0.0f, 1.0f) * 255.0f)); }

std::uint8_t blend(std::uint8_t dst, std::uint8_t src, int a) {
  return static_cast<std::uint8_t>((src * a + dst * (255 - a) + 127) / 255);
}

class BitWriter {
 public:
  explicit BitWriter(std::vector<std::uint8_t>& out) : out_(out) {}
  // n bits of v, lsb first
  void bits(std::uint32_t v, int n) {
    for (int i = 0; i < n; ++i) put((v >> i) & 1U);
  }
  // a huffman code, msb first
  void code(std::uint32_t c, int n) {
    for (int i = n - 1; i >= 0; --i) put((c >> i) & 1U);
  }
  void flush() {
    if (n_ > 0) out_.push_back(cur_);
    cur_ = 0;
    n_ = 0;
  }

 private:
  void put(std::uint32_t b) {
    cur_ = static_cast<std::uint8_t>(cur_ | (b << n_));
    if (++n_ == 8) flush();
  }
  std::vector<std::uint8_t>& out_;
  std::uint8_t cur_ = 0;
  int n_ = 0;
};

void literal(BitWriter& w, std::uint32_t v) {
  if (v < 144) w.code(0x30 + v, 8);
  else if (v < 256) w.code(0x190 + v - 144, 9);
  else if (v < 280) w.code(v - 256, 7);
  else w.code(0xc0 + v - 280, 8);
}

constexpr std::array<std::uint16_t, 29> kLenBase{3,  4,  5,  6,  7,  8,  9,  10, 11,  13,  15,  17,  19,  23, 27,
                                                 31, 35, 43, 51, 59, 67, 83, 99, 115, 131, 163, 195, 227, 258};
constexpr std::array<std::uint8_t, 29> kLenExtra{0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 2, 2, 2,
                                                 2, 3, 3, 3, 3, 4, 4, 4, 4, 5, 5, 5, 5, 0};
constexpr std::array<std::uint16_t, 30> kDistBase{1,   2,   3,   4,   5,   7,    9,    13,   17,   25,
                                                  33,  49,  65,  97,  129, 193,  257,  385,  513,  769,
                                                  1025, 1537, 2049, 3073, 4097, 6145, 8193, 12289, 16385, 24577};
constexpr std::array<std::uint8_t, 30> kDistExtra{0, 0, 0, 0, 1, 1, 2, 2,  3,  3,  4,  4,  5,  5,  6,
                                                  6, 7, 7, 8, 8, 9, 9, 10, 10, 11, 11, 12, 12, 13, 13};

void match(BitWriter& w, std::size_t len, std::size_t dist) {
  std::size_t li = kLenBase.size() - 1;
  while (kLenBase[li] > len) --li;
  literal(w, static_cast<std::uint32_t>(257 + li));
  w.bits(static_cast<std::uint32_t>(len - kLenBase[li]), kLenExtra[li]);
  std::size_t di = kDistBase.size() - 1;
  while (kDistBase[di] > dist) --di;
  w.code(static_cast<std::uint32_t>(di), 5);
  w.bits(static_cast<std::uint32_t>(dist - kDistBase[di]), kDistExtra[di]);
}

// zlib stream of data: one fixed-huffman block, matching runs at distance 1 or one row back
std::vector<std::uint8_t> zlib(const std::vector<std::uint8_t>& data, std::size_t row) {
  std::vector<std::uint8_t> out{0x78, 0x01};
  BitWriter w(out);
  w.bits(1, 1);
  w.bits(1, 2);
  std::size_t i = 0;
  while (i < data.size()) {
    std::size_t best = 0, best_dist = 0;
    for (std::size_t dist : {std::size_t{1}, row}) {
      if (dist == 0 || dist > i || dist > 32768) continue;
      std::size_t n = 0;
      while (n < 258 && i + n < data.size() && data[i + n] == data[i + n - dist]) ++n;
      if (n > best) {
        best = n;
        best_dist = dist;
      }
    }
    if (best >= 3) {
      match(w, best, best_dist);
      i += best;
    } else {
      literal(w, data[i++]);
    }
  }
  literal(w, 256);
  w.flush();
  std::uint32_t a = 1, b = 0;
  for (std::uint8_t c : data) {
    a = (a + c) % 65521;
    b = (b + a) % 65521;
  }
  const std::uint32_t adler = (b << 16) | a;
  for (int s = 24; s >= 0; s -= 8) out.push_back(static_cast<std::uint8_t>(adler >> s));
  return out;
}

std::uint32_t crc32(const std::uint8_t* p, std::size_t n, std::uint32_t crc = 0xffffffffU) {
  for (std::size_t i = 0; i < n; ++i) {
    crc ^= p[i];
    for (int k = 0; k < 8; ++k) crc = (crc >> 1) ^ (0xedb88320U & (0U - (crc & 1U)));
  }
  return crc;
}

void be32(std::vector<std::uint8_t>& out, std::uint32_t v) {
  for (int s = 24; s >= 0; s -= 8) out.push_back(static_cast<std::uint8_t>(v >> s));
}

void chunk(std::vector<std::uint8_t>& out, const char* type, const std::vector<std::uint8_t>& body) {
  be32(out, static_cast<std::uint32_t>(body.size()));
  const std::size_t start = out.size();
  out.insert(out.end(), type, type + 4);
  out.insert(out.end(), body.begin(), body.end());
  be32(out, crc32(out.data() + start, out.size() - start) ^ 0xffffffffU);
}

}  // namespace

RasterCanvas::RasterCanvas(int width, int height)
    : w_(width), h_(height), px_(static_cast<std::size_t>(width) * static_cast<std::size_t>(height) * 4, 0) {}

void RasterCanvas::clear(Color c) {
  const std::array<std::uint8_t, 4> v{to8(c.r), to8(c.g), to8(c.b), to8(c.a)};
  for (std::size_t i = 0; i < px_.size(); i += 4) std::copy(v.begin(), v.end(), px_.begin() + static_cast<std::ptrdiff_t>(i));
}

void RasterCanvas::fill_rects(std::span<const Rect> rects, Color c) {
  const std::array<std::uint8_t, 3> src{to8(c.r), to8(c.g), to8(c.b)};
  const int a = to8(c.a);
  for (const Rect& r : rects) {
    const int x0 = std::clamp(static_cast<int>(std::lround(r.x)), 0, w_);
    const int x1 = std::clamp(static_cast<int>(std::lround(r.x + r.w)), 0, w_);
    const int y0 = std::clamp(static_cast<int>(std::lround(r.y)), 0, h_);
    const int y1 = std::clamp(static_cast<int>(std::lround(r.y + r.h)), 0, h_);
    for (int y = y0; y < y1; ++y) {
      for (int x = x0; x < x1; ++x) {
        auto* p = &px_[(static_cast<std::size_t>(y) * static_cast<std::size_t>(w_) + static_cast<std::size_t>(x)) * 4];
        for (std::size_t k = 0; k < 3; ++k) p[k] = blend(p[k], src[k], a);
        p[3] = static_cast<std::uint8_t>(a + (p[3] * (255 - a) + 127) / 255);
      }
    }
  }
}

std::vector<std::uint8_t> RasterCanvas::png() const { return encode_png(w_, h_, px_); }

std::vector<std::uint8_t> encode_png(int width, int height, const std::vector<std::uint8_t>& rgba) {
  const auto w = static_cast<std::size_t>(width), h = static_cast<std::size_t>(height);
  // filter 1 (sub) on every row: flat runs become zeros
  std::vector<std::uint8_t> raw;
  raw.reserve(h * (w * 4 + 1));
  for (std::size_t y = 0; y < h; ++y) {
    raw.push_back(1);
    for (std::size_t i = 0; i < w * 4; ++i) {
      const std::uint8_t cur = rgba[y * w * 4 + i];
      const std::uint8_t left = i >= 4 ? rgba[y * w * 4 + i - 4] : 0;
      raw.push_back(static_cast<std::uint8_t>(cur - left));
    }
  }
  std::vector<std::uint8_t> out{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'};
  std::vector<std::uint8_t> ihdr;
  be32(ihdr, static_cast<std::uint32_t>(width));
  be32(ihdr, static_cast<std::uint32_t>(height));
  ihdr.insert(ihdr.end(), {8, 6, 0, 0, 0});
  chunk(out, "IHDR", ihdr);
  chunk(out, "IDAT", zlib(raw, w * 4 + 1));
  chunk(out, "IEND", {});
  return out;
}

}  // namespace cadence::render::viz
