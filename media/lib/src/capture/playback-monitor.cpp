#include "capture/playback-monitor.h"

#include <algorithm>
#include <array>
#include <cmath>
#include <numbers>
#include <tuple>
#include <utility>
#include <vector>

namespace cadence {

namespace {

constexpr int kRate = 48000;
// one 20 ms measurement window
constexpr std::size_t kWindowFrames = 960;
// normalized pcm16
constexpr double kFullScale = 32768.0;

// second-order butterworth section, direct form ii transposed, with double state
struct Biquad {
  double b0 = 0, b1 = 0, b2 = 0, a1 = 0, a2 = 0;
  double z1 = 0, z2 = 0;

  static Biquad make(double frequency, bool highPass) {
    // rbj cookbook with q = 1/sqrt(2); every coefficient is normalized by a0
    const double omega = 2 * std::numbers::pi * frequency / kRate;
    const double alpha = std::sin(omega) * std::numbers::sqrt2 / 2;
    const double a0 = 1 + alpha;
    const double cosOmega = std::cos(omega);
    const double gain = (highPass ? 1 + cosOmega : 1 - cosOmega) / a0;
    return Biquad{gain * 0.5,
                  (highPass ? -2 : 2) * gain * 0.5,
                  gain * 0.5,
                  -2 * cosOmega / a0,
                  (1 - alpha) / a0};
  }

  void clear() {
    z1 = 0;
    z2 = 0;
  }

  double next(double input) {
    const double output = b0 * input + z1;
    z1 = b1 * input - a1 * output + z2;
    z2 = b2 * input - a2 * output;
    return output;
  }
};

// the perceptual mapping of led/audio-level.cpp, on a known rms rather than pcm
float levelOf(double rms) {
  if (!(rms > 0))
    return 0.0f;
  return static_cast<float>(std::clamp((20 * std::log10(rms) + 50) / 40, 0.0, 1.0));
}

}  // namespace

struct PlaybackMonitor::State {
  // per reference: one high-pass then one low-pass for each of the three bands
  struct Reference {
    std::array<Biquad, 6> filters{};
  };

  std::vector<std::size_t> references;
  std::size_t channels = 0;
  std::vector<Reference> bank;
  // squared input and squared band outputs, summed over references and frames
  std::array<double, 4> energy{};
  std::size_t windowFrames = 0;
};

PlaybackMonitor::PlaybackMonitor() = default;

PlaybackMonitor::~PlaybackMonitor() = default;

bool PlaybackMonitor::configure(const CaptureLayout& layout) {
  // any rejection disarms: monitoring never continues on a stale configuration
  state_ = nullptr;
  if (layout.sampleRate != kRate || layout.channels == 0 || layout.speakers.empty())
    return false;
  for (const auto index : layout.speakers)
    if (index >= layout.channels)
      return false;
  for (std::size_t left = 0; left < layout.speakers.size(); ++left)
    for (std::size_t right = left + 1; right < layout.speakers.size(); ++right)
      if (layout.speakers[left] == layout.speakers[right])
        return false;
  // this stream measures speaker references only, never a microphone channel
  for (const auto microphone : layout.microphones)
    if (std::find(layout.speakers.begin(), layout.speakers.end(), microphone) != layout.speakers.end())
      return false;

  auto state = std::make_unique<State>();
  state->references = layout.speakers;
  state->channels = layout.channels;
  state->bank.assign(layout.speakers.size(), State::Reference{});
  static constexpr std::array<std::tuple<std::size_t, double, bool>, 6> kSections{{
      {0, 40, true},
      {1, 250, false},
      {2, 250, true},
      {3, 2000, false},
      {4, 2000, true},
      {5, 12000, false},
  }};
  for (auto& reference : state->bank)
    for (const auto& [filter, cutoff, highPass] : kSections)
      reference.filters[filter] = Biquad::make(cutoff, highPass);
  state_ = std::move(state);
  return true;
}

void PlaybackMonitor::reset() {
  if (!state_)
    return;
  for (auto& reference : state_->bank)
    for (auto& filter : reference.filters)
      filter.clear();
  state_->energy = {};
  state_->windowFrames = 0;
}

std::optional<MusicLevels> PlaybackMonitor::process(std::span<const std::int16_t> interleaved) {
  if (!state_ || interleaved.empty())
    return std::nullopt;
  // a span that is not whole frames is a gap, never a partial frame: drop the partial window
  if (interleaved.size() % state_->channels != 0) {
    reset();
    return MusicLevels{};
  }

  auto& state = *state_;
  const auto frames = interleaved.size() / state.channels;
  std::optional<MusicLevels> newest;
  for (std::size_t frame = 0; frame < frames; ++frame) {
    const auto base = frame * state.channels;
    for (std::size_t index = 0; index < state.references.size(); ++index) {
      const double input = interleaved[base + state.references[index]] / kFullScale;
      state.energy[0] += input * input;
      auto& filters = state.bank[index].filters;
      for (std::size_t band = 0; band < 3; ++band) {
        const double filtered = filters[2 * band + 1].next(filters[2 * band].next(input));
        state.energy[band + 1] += filtered * filtered;
      }
    }
    if (++state.windowFrames == kWindowFrames) {
      // nonfinite filter state means the monitor has broken down: reset to silence for this window and after
      const bool sound = std::all_of(state.bank.begin(), state.bank.end(), [](const State::Reference& ref) {
        return std::all_of(ref.filters.begin(), ref.filters.end(), [](const Biquad& f) {
          return std::isfinite(f.z1) && std::isfinite(f.z2);
        });
      });
      newest = sound ? finishWindow(state) : MusicLevels{};
      if (!sound)
        reset();
      state.energy = {};
      state.windowFrames = 0;
    }
  }
  return newest;
}

MusicLevels PlaybackMonitor::finishWindow(State& state) {
  const double frames = static_cast<double>(kWindowFrames);
  const double references = static_cast<double>(state.references.size());
  MusicLevels levels;
  levels.level = levelOf(std::sqrt(state.energy[0] / frames / references));
  for (std::size_t band = 0; band < 3; ++band)
    levels.bands[band] = levelOf(std::sqrt(state.energy[band + 1] / frames / references));
  return levels;
}

}  // namespace cadence
