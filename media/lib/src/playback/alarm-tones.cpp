#include "playback/alarm-tones.h"

#include "playback/earcons.h"

#include <algorithm>
#include <array>
#include <cmath>
#include <cstddef>
#include <numbers>
#include <span>

namespace cadence {

namespace {

struct Tone {
  double hz;
  std::size_t start;
  std::size_t samples;
};

// 10 ms at 48 kHz
constexpr std::size_t kFadeSamples = 480;

// 150 ms tones 100 ms apart
constexpr std::size_t kAlarmSamples = 2 * kEarconRate;
constexpr std::array<Tone, 3> kAlarm{{{660, 0, 7200}, {880, 12000, 7200}, {1100, 24000, 7200}}};

// 120 ms beeps 100 ms apart
constexpr std::size_t kTimerSamples = kEarconRate;
constexpr std::array<Tone, 2> kTimer{{{880, 0, 5760}, {880, 10560, 5760}}};

// two notes 400 ms apart, each ringing out over 550 ms at about half the peak
constexpr std::size_t kReminderSamples = 57600;
constexpr std::array<Tone, 2> kReminder{{{784, 0, 26400}, {1047, 19200, 26400}}};
constexpr double kChimeLevel = 0.5;
// how fast a chime note dies away, per second
constexpr double kChimeDecay = 5.0;

// raised cosine, exactly 0 at the tone's first and last sample
double fade(std::size_t index, std::size_t samples) {
  const std::size_t edge = std::min(index, samples - 1 - index);
  if (edge >= kFadeSamples)
    return 1;
  return 0.5 * (1 - std::cos(std::numbers::pi * static_cast<double>(edge) / static_cast<double>(kFadeSamples)));
}

std::vector<std::int16_t> period(std::size_t samples, std::span<const Tone> tones) {
  std::vector<std::int16_t> pcm(samples);
  for (const auto& tone : tones) {
    for (std::size_t i = 0; i < tone.samples; ++i) {
      const double phase = 2 * std::numbers::pi * tone.hz * static_cast<double>(i) / kEarconRate;
      pcm[tone.start + i] =
          static_cast<std::int16_t>(std::lround(kEarconPeak * fade(i, tone.samples) * std::sin(phase)));
    }
  }
  return pcm;
}

// a bell's notes: each dies away, and the second sounds over the first's tail
std::vector<std::int16_t> chime(std::size_t samples, std::span<const Tone> tones) {
  std::vector<double> sum(samples, 0.0);
  for (const auto& tone : tones) {
    for (std::size_t i = 0; i < tone.samples && tone.start + i < samples; ++i) {
      const double t = static_cast<double>(i) / kEarconRate;
      const double phase = 2 * std::numbers::pi * tone.hz * t;
      sum[tone.start + i] += kChimeLevel * fade(i, tone.samples) * std::exp(-kChimeDecay * t) * std::sin(phase);
    }
  }
  std::vector<std::int16_t> pcm(samples);
  for (std::size_t i = 0; i < samples; ++i)
    pcm[i] = static_cast<std::int16_t>(std::lround(kEarconPeak * std::clamp(sum[i], -1.0, 1.0)));
  pcm.back() = 0;
  return pcm;
}

}  // namespace

std::vector<std::int16_t> ringPeriodPcm(RingKind kind) {
  switch (kind) {
    case RingKind::Alarm:
      return period(kAlarmSamples, kAlarm);
    case RingKind::Reminder:
      return chime(kReminderSamples, kReminder);
    default:
      return period(kTimerSamples, kTimer);
  }
}

}  // namespace cadence
