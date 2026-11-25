#include "playback/earcons.h"

#include <algorithm>
#include <array>
#include <cmath>
#include <cstddef>
#include <numbers>

namespace cadence {

namespace {

struct Note {
  double hz;
  std::size_t samples;
};

// 5 ms at 48 kHz
constexpr std::size_t kFadeSamples = 240;

constexpr std::array<Note, 2> kWake{{{880, 3360}, {1319, 3360}}};
constexpr std::array<Note, 2> kFailure{{{659, 4320}, {440, 4320}}};

// raised cosine, exactly 0 at the note's first and last sample so the notes join without a click
double fade(std::size_t index, std::size_t samples) {
  const std::size_t edge = std::min(index, samples - 1 - index);
  if (edge >= kFadeSamples)
    return 1;
  return 0.5 * (1 - std::cos(std::numbers::pi * static_cast<double>(edge) / static_cast<double>(kFadeSamples)));
}

void appendNote(std::vector<std::int16_t>& pcm, const Note& note) {
  for (std::size_t i = 0; i < note.samples; ++i) {
    const double phase = 2 * std::numbers::pi * note.hz * static_cast<double>(i) / kEarconRate;
    pcm.push_back(static_cast<std::int16_t>(std::lround(kEarconPeak * fade(i, note.samples) * std::sin(phase))));
  }
}

}  // namespace

std::vector<std::int16_t> earconPcm(Earcon earcon) {
  const auto& notes = earcon == Earcon::Wake ? kWake : kFailure;
  std::vector<std::int16_t> pcm;
  pcm.reserve(notes[0].samples + notes[1].samples);
  for (const auto& note : notes)
    appendNote(pcm, note);
  return pcm;
}

}  // namespace cadence
