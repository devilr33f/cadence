#pragma once

#include "../fft.h"
#include "beat.h"
#include "beat_grid.h"
#include "impact.h"
#include "onset.h"

#include <array>
#include <cstddef>
#include <cstdint>
#include <span>

namespace cadence::render::viz {

inline constexpr std::size_t kFftBins = 512;
inline constexpr std::size_t kWaveSamples = 512;

// what every source renders a frame from (the shader presets as uniforms and one 512 × 2 texture: row 0 the
// spectrum, row 1 the waveform)
struct FrameInputs {
  double time_s = 0;
  double dt = 0;
  // 0..1 per bin (bin k is k * rate / 1024 Hz): 54 db under the recent loudest bin, falling smoothly
  std::array<float, kFftBins> fft{};
  // the last 512 frames heard, mono, -1..1
  std::array<float, kWaveSamples> wave{};
  // each band's level against its own running average (1 is average; milkdrop's bass/mid/treb), and smoothed
  float bass = 0, mid = 0, treble = 0;
  float bass_att = 0, mid_att = 0, treble_att = 0;
  // a strong beat (the drive's, with a pulse of 0.45 or more): what a shader or a ring draws whole
  bool beat = false;
  // seconds since the last beat
  float since_beat = 0;
  // iteration 2's onset detector (host/onset.h): a kick on the raw bass energy, with a decaying pulse, a count, the
  // tempo once it holds, the ages of the last 8 onsets (-1 none) and whether this one starts a bar of four
  bool onset = false;
  bool bar = false;
  float beat_pulse = 0;
  std::uint32_t beat_count = 0;
  float bpm = 0;
  std::array<float, 8> beat_ages{-1, -1, -1, -1, -1, -1, -1, -1};
  // absolute band levels 0..1 (the rms of each band's bins, scaled), smoothed fast up and slow down, and their mean:
  // what the feedback scenes are tuned to (bass/mid/treble above are each band against its own average)
  float bass_level = 0, mid_level = 0, treble_level = 0, level = 0;
  // the drive (host/beat.h): onsets and decaying pulses per band, loudness and band energies against the track's own
  // range, a drop's intensity, the spectrum's brightness and the beat's phase (0 at a beat, 1 at the next expected)
  bool kick = false, snare = false, hat = false;
  float kick_pulse = 0, snare_pulse = 0, hat_pulse = 0;
  float energy = 0, energy_slow = 0, intensity = 0, brightness = 0, beat_phase = 0;
  // the beat on the tempo grid (host/beat_grid.h): its strength the frame one is placed (0 otherwise), and a
  // pulse that fades
  float grid_strength = 0;
  float grid_pulse = 0;
  // a spectral onset in any band (kick, snare, or hat): its strength that frame, 0 otherwise
  float spec_strength = 0;
  // the bass against the track's own (0 in a build-up or a breakdown, host/beat.h), and a build-up's growth, 0..1
  float bass_presence = 1, build = 0;
  // the drop's arrival (host/beat.h): one frame when the bass returns, a pulse for the hit (~0.75 s) and a level
  // for the section (held while the bass and the level hold, falling in ~10 s once they go)
  bool drop = false;
  float drop_pulse = 0, drop_level = 0;
  // the raw interleaved stereo frames of this frame's slice, for projectM, which keeps its own analysis
  std::span<const float> pcm_stereo;
  // set by the renderer: a 512 × 2 GL_R8 texture of fft (row 0) and wave mapped to 0..1 (row 1)
  unsigned audio_texture = 0;
};

// FrameInputs from the frames heard: 1024 mono frames ending now (a PcmTimeline window), at rate
class AudioFeatures {
 public:
  AudioFeatures();

  // heard: the frame heard now on the stream fed (feed()); -1 when nothing feeds ahead, so the window's newest
  // frames feed the beat grid themselves (a beat then shows ~70 ms late: the offline tools and tests)
  void update(std::span<const float> mono_1024, std::uint32_t rate, double dt, FrameInputs& out,
              std::int64_t heard = -1);
  // the stream ahead of what is heard, in order (the host's timeline holds 150 ms and more of it): the beat grid
  // confirms each beat on its peak before it sounds
  void feed(std::span<const float> mono) { grid_.feed(mono); impact_.push(mono); }
  [[nodiscard]] std::int64_t fed() const { return grid_.fed(); }
  // a new stream (the timeline started over): the grid starts afresh
  void restart(std::uint32_t rate) {
    grid_ = BeatGrid(rate);
    impact_ = Impact();
    self_ = 0;
    grid_pulse_ = 0;
  }
  // a new track announced by metadata (gapless playback, crossfade, skip): resets per-track loudness, bass, drop,
  // and kick calibration without desyncing the fed audio timeline
  void on_track() {
    beat_.reset_track();
    impact_.reset_track();
    spec_peak_db_ = -42;
  }

 private:
  Fft<1024> fft_;
  std::array<double, 1024> window_{};
  std::array<float, kFftBins> smooth_{};
  double avg_[3] = {0, 0, 0};
  double att_[3] = {0, 0, 0};
  double since_beat_ = 1e9;
  bool primed_ = false;
  BeatTracker beat_;
  BeatGrid grid_;
  Impact impact_;
  // the frames heard so far when nothing feeds ahead
  std::int64_t self_ = 0;
  float grid_pulse_ = 0;
  std::uint32_t beats_ = 0;
  double t_ = 0;
  std::array<double, 8> beat_times_{-1, -1, -1, -1, -1, -1, -1, -1};
  std::array<float, kFftBins> mag_{};
  double spec_peak_db_ = -42;
};

}  // namespace cadence::render::viz
