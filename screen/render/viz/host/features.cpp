#include "features.h"

#include <algorithm>
#include <cmath>
#include <numbers>
#include <vector>

namespace cadence::render::viz {

namespace {
// a beat at least this strong (its pulse, 0..1) counts as a whole beat
constexpr float kStrongBeat = 0.45f;
}  // namespace

namespace {

// band edges in hz: bass, mid, treble
constexpr double kEdges[4] = {20, 250, 4000, 16000};

}  // namespace

AudioFeatures::AudioFeatures() {
  for (std::size_t n = 0; n < window_.size(); ++n) {
    window_[n] = 0.5 - 0.5 * std::cos(2.0 * std::numbers::pi * static_cast<double>(n) / 1023.0);
  }
}

void AudioFeatures::update(std::span<const float> mono, std::uint32_t rate, double dt, FrameInputs& out,
                           std::int64_t heard) {
  dt = std::clamp(dt, 0.0, 0.5);
  std::array<std::complex<double>, 1024> x{};
  double wsum = 0;
  const std::size_t pad = 1024 - std::min<std::size_t>(mono.size(), 1024);
  for (std::size_t n = 0; n < 1024; ++n) {
    const double s = n >= pad ? static_cast<double>(mono[n - pad]) : 0.0;
    x[n] = std::clamp(s, -1.0, 1.0) * window_[n];
    wsum += window_[n];
  }
  for (std::size_t i = 0; i < kWaveSamples; ++i) {
    const double s = 512 + i >= pad ? static_cast<double>(mono[512 + i - pad]) : 0.0;
    out.wave[i] = static_cast<float>(std::clamp(s, -1.0, 1.0));
  }
  fft_.forward(x);
  const double scale = 2.0 / wsum;
  // a bin falls at most 3.5 levels a second (fast enough to dance, slow enough not to flicker), and the 54 db shown
  // hang under the loudest bin of the last seconds (falling 1.5 db a second), so a quiet master fills the spectrum too
  const double fall = 3.5 * dt;
  double band[3] = {0, 0, 0};
  const double bin_hz = static_cast<double>(rate) / 1024.0;
  double loudest = -120;
  for (std::size_t k = 0; k < kFftBins; ++k) {
    const double amp = std::sqrt(std::norm(x[k])) * scale;
    if (k > 0) loudest = std::max(loudest, amp > 0 ? 20 * std::log10(amp) : -120.0);
  }
  spec_peak_db_ = std::clamp(std::max(loudest, spec_peak_db_ - 1.5 * dt), -42.0, 0.0);
  const double top = spec_peak_db_ + 3, span = 54;
  for (std::size_t k = 0; k < kFftBins; ++k) {
    const double p = std::norm(x[k]);
    const double amp = std::sqrt(p) * scale;
    mag_[k] = static_cast<float>(amp);
    const double db = amp > 0 ? 20 * std::log10(amp) : -120;
    const auto level = static_cast<float>(std::clamp((db - (top - span)) / span, 0.0, 1.0));
    smooth_[k] = std::max(level, smooth_[k] - static_cast<float>(fall));
    out.fft[k] = smooth_[k];
    const double hz = static_cast<double>(k) * bin_hz;
    for (int b = 0; b < 3; ++b) {
      if (hz >= kEdges[b] && hz < kEdges[b + 1]) band[b] += p;
    }
  }
  // each band against its own running average over about 4 s; the attenuated one follows over about 0.3 s
  float* now[3] = {&out.bass, &out.mid, &out.treble};
  float* att[3] = {&out.bass_att, &out.mid_att, &out.treble_att};
  const double avg_k = 1 - std::exp(-dt / 4.0), att_k = 1 - std::exp(-dt / 0.3);
  for (int b = 0; b < 3; ++b) {
    const double e = std::sqrt(band[b]) * scale;
    avg_[b] = primed_ ? avg_[b] + (e - avg_[b]) * avg_k : e;
    const double rel = avg_[b] > 1e-6 ? e / avg_[b] : 0.0;
    att_[b] = primed_ ? att_[b] + (rel - att_[b]) * att_k : rel;
    *now[b] = static_cast<float>(std::min(rel, 4.0));
    *att[b] = static_cast<float>(std::min(att_[b], 4.0));
  }
  primed_ = true;
  out.dt = dt;

  // the drive: onsets per band from spectral flux, and every level against the track's own range (host/beat.h)
  t_ += dt;
  beat_.update(mag_, bin_hz, dt);
  const auto& b = beat_.out();
  // the beat on the song's grid (host/beat_grid.h): fed ahead by the host, or here from the window's newest frames
  impact_.set_rate(rate);
  if (heard < 0) {
    self_ += std::llround(dt * rate);
    const std::int64_t need = self_ - grid_.fed();
    if (need > 0) {
      const auto n = static_cast<std::int64_t>(mono.size());
      if (need > n) {
        const std::vector<float> zeros(static_cast<std::size_t>(need - n), 0.0f);
        grid_.feed(zeros);
        impact_.push(zeros);
      }
      const auto tail = mono.subspan(static_cast<std::size_t>(n - std::min(need, n)));
      grid_.feed(tail);
      impact_.push(tail);
    }
    heard = self_ - 1;
  }
  grid_.set_level_gain(b.level_gain);
  // the grid keeps the tempo and the phase. its beat is one impulse a frame, for the plot and the phase
  float beat = -1;
  for (const auto& bt : grid_.take(heard)) beat = std::max(beat, bt.strength);
  grid_pulse_ *= static_cast<float>(std::pow(0.1, dt / 0.2));
  if (beat >= 0) grid_pulse_ = std::max(grid_pulse_, beat);
  out.grid_strength = beat >= 0 ? beat : 0;
  float hit = 0;
  if (b.onset[BeatTracker::Kick]) hit = std::max(hit, b.pulse[BeatTracker::Kick]);
  if (b.onset[BeatTracker::Snare]) hit = std::max(hit, b.pulse[BeatTracker::Snare]);
  if (b.onset[BeatTracker::Hat]) hit = std::max(hit, b.pulse[BeatTracker::Hat]);
  out.spec_strength = hit;
  impact_.update(heard, dt, b.hit_loud, b.level_gain);
  beat_.confirm_drop(impact_.hit() || impact_.pulse() > 0.55f);
  out.bass_presence = b.bass_presence;
  out.build = b.build;
  out.drop = b.drop;
  out.drop_pulse = b.drop_pulse;
  out.drop_level = b.drop_level;
  out.kick = impact_.hit();
  out.snare = b.onset[BeatTracker::Snare];
  out.hat = b.onset[BeatTracker::Hat];
  out.kick_pulse = impact_.pulse();
  out.snare_pulse = b.pulse[BeatTracker::Snare];
  out.hat_pulse = b.pulse[BeatTracker::Hat];
  out.energy = b.energy;
  out.energy_slow = b.energy_slow;
  out.intensity = b.intensity;
  out.brightness = b.brightness;
  out.beat_phase = static_cast<float>(grid_.phase(heard));
  out.grid_pulse = grid_pulse_;
  out.bass_level = b.band[0];
  out.mid_level = b.band[1];
  out.treble_level = b.band[2];
  out.level = b.energy;
  out.onset = impact_.hit();
  out.beat_pulse = impact_.pulse();
  // the flags and rings the shaders draw whole (uBeat, uSinceBeat, a scene's rings) take the strong kicks alone: a
  // weak one (a fade's, a breakdown's) moves only what scales with its pulse
  since_beat_ += dt;
  out.beat = impact_.hit() && impact_.pulse() >= kStrongBeat;
  if (out.beat) since_beat_ = 0;
  out.since_beat = static_cast<float>(std::min(since_beat_, 1e6));
  if (impact_.hit()) ++beats_;
  if (out.beat) {
    std::rotate(beat_times_.begin(), beat_times_.begin() + 1, beat_times_.end());
    beat_times_.back() = t_;
  }
  out.beat_count = beats_;
  out.bpm = static_cast<float>(grid_.bpm());
  out.bar = impact_.hit() && (out.bpm <= 0 || beats_ % 4 == 0);
  for (std::size_t i = 0; i < 8; ++i) {
    const double a = beat_times_[i] < 0 ? -1 : t_ - beat_times_[i];
    out.beat_ages[i] = a < 0 || a > 3.5 ? -1.0f : static_cast<float>(a);
  }
}

}  // namespace cadence::render::viz
