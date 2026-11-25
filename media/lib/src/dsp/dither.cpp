#include "dsp/dither.h"

#include <algorithm>
#include <cmath>
#include <cstddef>

namespace cadence::dsp {

namespace {

// splitmix64, so that neighbouring seeds start far apart
std::uint64_t mix(std::uint64_t seed) {
  std::uint64_t z = seed + 0x9e3779b97f4a7c15ull;
  z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9ull;
  z = (z ^ (z >> 27)) * 0x94d049bb133111ebull;
  z ^= z >> 31;
  // xorshift never leaves zero
  return z == 0 ? 0x9e3779b97f4a7c15ull : z;
}

constexpr double kScale = 32768;
constexpr double kUnit = 1.0 / 4294967296.0;

}  // namespace

Rng::Rng(std::uint64_t seed) : state_(mix(seed)) {}

std::uint32_t Rng::next() {
  state_ ^= state_ << 13;
  state_ ^= state_ >> 7;
  state_ ^= state_ << 17;
  return static_cast<std::uint32_t>(state_ >> 32);
}

void ditherToS16(std::span<const float> in, std::span<std::int16_t> out, Rng& rng) {
  const std::size_t count = std::min(in.size(), out.size());
  for (std::size_t i = 0; i < count; ++i) {
    const float x = std::isnan(in[i]) ? 0.0f : in[i];
    // a clipped sample stays at the limit: dither would only pull it in
    if (x >= 1) {
      out[i] = 32767;
      continue;
    }
    if (x <= -1) {
      out[i] = -32768;
      continue;
    }
    const double noise = static_cast<double>(rng.next()) * kUnit + static_cast<double>(rng.next()) * kUnit - 1;
    const double value = std::nearbyint(static_cast<double>(x) * kScale + noise);
    out[i] = static_cast<std::int16_t>(std::clamp(value, -32768.0, 32767.0));
  }
}

}  // namespace cadence::dsp
