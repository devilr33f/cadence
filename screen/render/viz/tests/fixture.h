#pragma once

#include <cmath>
#include <cstdint>
#include <numbers>
#include <vector>

// the fixed pcm the golden images come from: stereo 48 kHz, a chord at 110, 440, 1760 and 5000 Hz with a seeded
// noise floor, the right channel a fifth up; snapshot k is the 512 frames ending at k * 800 (60 a second), and
// from snapshot `silent_from` on the sound stops
namespace cadence::render::viz::fixture {

inline constexpr std::uint32_t kRate = 48000;
inline constexpr int kHop = 800;
inline constexpr int kSilentFrom = 30;

// frame n of the sound (n < 0 is silence): left and right
inline void frame(long n, double& l, double& r) {
  if (n < 0) {
    l = r = 0;
    return;
  }
  const double t = static_cast<double>(n) / kRate;
  // a 32-bit lcg from the frame index, so any frame is the same alone
  const auto seed = static_cast<std::uint32_t>(n) * 1664525U + 1013904223U;
  const double noise = (static_cast<double>(seed >> 8) / 16777216.0 - 0.5) * 0.004;
  const double tw = 2 * std::numbers::pi * t;
  const double env = 0.6 + 0.4 * std::sin(2 * std::numbers::pi * 1.5 * t);
  // a kick every half second
  const double kick = std::exp(-14 * std::fmod(t, 0.5)) * 0.5 * std::sin(tw * 55);
  l = env * (0.35 * std::sin(tw * 110) + 0.2 * std::sin(tw * 440) + 0.08 * std::sin(tw * 1760) +
             0.02 * std::sin(tw * 5000)) + noise + kick;
  r = env * (0.3 * std::sin(tw * 165) + 0.15 * std::sin(tw * 660) + 0.05 * std::sin(tw * 2640)) - noise + kick;
}

// count contiguous stereo frames from frame start
inline std::vector<float> stream(long start, long count) {
  std::vector<float> out;
  out.reserve(static_cast<std::size_t>(count) * 2);
  for (long i = 0; i < count; ++i) {
    double l = 0, r = 0;
    frame(start + i, l, r);
    out.push_back(static_cast<float>(l));
    out.push_back(static_cast<float>(r));
  }
  return out;
}

inline std::vector<float> snapshot(int k) {
  std::vector<float> out;
  out.reserve(512 * 2);
  for (int i = 0; i < 512; ++i) {
    const long n = static_cast<long>(k) * kHop - 512 + i;
    if (k >= kSilentFrom || n < 0) {
      out.push_back(0);
      out.push_back(0);
      continue;
    }
    const double t = static_cast<double>(n) / kRate;
    // a 32-bit lcg from the frame index, so any snapshot is the same alone
    const auto seed = static_cast<std::uint32_t>(n) * 1664525U + 1013904223U;
    const double noise = (static_cast<double>(seed >> 8) / 16777216.0 - 0.5) * 0.004;
    const double tw = 2 * std::numbers::pi * t;
    const double env = 0.6 + 0.4 * std::sin(2 * std::numbers::pi * 1.5 * t);
    const double l = env * (0.35 * std::sin(tw * 110) + 0.2 * std::sin(tw * 440) + 0.08 * std::sin(tw * 1760) +
                            0.02 * std::sin(tw * 5000)) + noise;
    const double r = env * (0.3 * std::sin(tw * 165) + 0.15 * std::sin(tw * 660) + 0.05 * std::sin(tw * 2640)) - noise;
    out.push_back(static_cast<float>(l));
    out.push_back(static_cast<float>(r));
  }
  return out;
}

}  // namespace cadence::render::viz::fixture
