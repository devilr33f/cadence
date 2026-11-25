#pragma once

#include <array>
#include <cstddef>
#include <cstdint>
#include <span>

namespace cadence::render::viz {

// the visualizer's drive (owner's issue #1: "more drive"): onsets per band from spectral flux, and loudness and band
// energies normalised to the track itself, so a quiet song moves as much as a loud one. it works frame by frame on
// the spectrum the features already took (no look-ahead: a kick shows on the frame it is heard), at any frame rate
//
// flux: the rise of each bin's log magnitude since the last frame, summed per band (kick 40-160 hz, snare 160 hz-
// 5 khz, hat 5-16 khz). a band's onset is its flux over an adaptive threshold (the mean plus 1.6 deviations of the
// last ~1 s, and a floor against silence) that is rising, with a refractory. the tempo comes from the kicks'
// intervals (their median, octave-folded into 70-180 bpm), and the phase runs on it between kicks
class BeatTracker {
 public:
  enum Band : std::size_t { Kick = 0, Snare = 1, Hat = 2, Full = 3 };
  static constexpr std::size_t kBands = 4;

  struct Out {
    // an onset this frame, per band
    std::array<bool, kBands> onset{};
    // 1 on an onset (scaled by how far over the threshold it was), decaying: kick ~130 ms, snare ~110, hat ~70
    std::array<float, kBands> pulse{};
    // the beat the scenes follow: a kick, or at half strength the full band's onset while no kick came for 4 s (music
    // without one)
    bool beat = false;
    float beat_pulse = 0;
    // loudness against the track's own range, 0..1 (fast: ~40 ms up, ~250 ms down; slow: ~2 s)
    float energy = 0, energy_slow = 0;
    // band energies against their own recent peaks, 0..1 (bass, mid, treble), fast up and slow down
    std::array<float, 3> band{};
    // how far the energy is over its last ~8 s, and a drop's hold once the bass returns after a build-up: 0 calm,
    // 1 a drop (rises fast, eases out over ~2 s; the hold halves over ~6 s)
    float intensity = 0;
    // 0..1 of the spectrum's centroid on a log scale (dark to bright)
    float brightness = 0;
    float bpm = 0;
    // 0 at each beat, rising to 1 at the next one the tempo expects (0 while no tempo holds)
    float phase = 0;
    // the music's level against the track's loud level: 1 within 1.5 db of it, 0 10 db under (a fade, a quiet bridge)
    float loud = 1;
    // loudness gate for transient drum hits: follows loud, or a gentler share of the fast peak-held level so
    // staccato hits separated by dead silence (ghost! 55-61 s) still register proportionally instead of muting
    float hit_loud = 1;
    // the bass against the track's own (its level held, falling 0.2 db a second): 1 within 5 db, 0 12 db under. a
    // build-up takes the bass out, so the beat's effects there stay small
    float bass_presence = 1;
    // a build-up: the bass gone while the music goes on, growing over 8 s of it (0..1), gone within ~2 s of the bass
    float build = 0;
    // the drop's arrival: the bass back after a build-up held. the pulse is the hit (full on that frame, gone in
    // ~0.75 s); the level is the section — held while the bass and the level hold (halving over ~60 s), falling in
    // ~10 s once they go — so the picture stays faster than the chorus after the hit has faded. the hold lifts the
    // intensity, for the surge alone leans on what came before the build and after a loud section never rises
    bool drop = false;
    float drop_pulse = 0;
    float drop_level = 0;
    // the song's loud level against a loud master's, as an amplitude (1 for a loud master, 0.1 for one 20 db under)
    float level_gain = 1;
  };

  // one frame: the magnitudes of bins 0..n-1 at bin_hz apart (a 1024-point fft's 512), dt seconds after the last
  void update(std::span<const float> magnitude, double bin_hz, double dt);
  // confirms a pending drop arrival once Impact sees a real drum hit, so a kickless synth swell never fires a fake drop
  void confirm_drop(bool has_kick);
  // clears per-track references (loudness, bass reference, drop state, onset calibration) on a metadata track switch
  // so gapless or skipped tracks start fresh without waiting for a silence gap
  void reset_track();
  [[nodiscard]] const Out& out() const { return out_; }

  // where a song's references start, and come back to across the silence between songs: a loud modern master's level
  // and its bass (on the tap's pre-volume pcm). a song's quiet intro then reads as quiet, not as its own loudest
  // (the owner's invasive: its first 26 s punched like the drop); they sink 0.2 db a second, so a song mastered
  // quieter finds its own level in under a minute
  static constexpr double kLoudMasterDb = -7.0, kLoudBassDb = -12.0;

 private:
  struct Detector {
    std::array<float, 64> history{};
    std::size_t pos = 0, count = 0;
    double since = 1e9;
    float prev = 0;
    double floor = 0;
    // how strong this band's real onsets are, over ~10 s of them: a rise must reach a share of it to count, and its
    // pulse is its share (a fading song's small rises make small pulses or none)
    double ref = 0;
    int hits = 0;
    // the linear rise of its real onsets (the log flux is level-blind): a rise must reach a share of it
    double lin_ref = 0;
    int lin_hits = 0;
  };

  bool detect(Detector& d, float flux, double dt, double refractory, double k);
  void track_tempo(double now);

  std::array<float, 512> prev_log_{};
  std::array<float, 512> prev_mag_{};
  bool primed_ = false;
  std::array<Detector, kBands> det_{};
  double t_ = 0;
  double last_beat_ = -1;
  double last_kick_ = -1e9;
  std::array<double, 16> intervals_{};
  std::size_t interval_pos_ = 0, interval_count_ = 0;
  double period_ = 0;
  // loudness in db: its running peak and floor, the fast and slow envelopes
  double peak_db_ = kLoudMasterDb, low_db_ = -60;
  // the track's loud level (falling 0.2 db a second), the smoothed level (for track_db_ and silence gaps), and a
  // fast-attack peak-held level (for loud_, so silent gaps between staccato hits don't mute real beats)
  double track_db_ = kLoudMasterDb, now_db_ = -60, level_db_ = -60;
  bool heard_loud_ = false;
  double loud_ = 1;
  double bass_db_ = -120, bass_peak_db_ = -120, bass_ref_db_ = kLoudBassDb, build_s_ = 0, bass_held_s_ = 0, song_s_ = 0;
  // true sub-bass (30-80 Hz) mean, peak, and reference: drops require sub-bass to land, not just 90-130 Hz fills
  double sub_db_ = -120, sub_peak_db_ = -120, sub_ref_db_ = kLoudBassDb - 3.0;
  double drop_pulse_ = 0, drop_level_ = 0, drop_hold_ = 0, drop_disarm_s_ = 0;
  bool drop_open_ = false, drop_armed_ = false, drop_ready_ = false;
  double env_ = 0, env_slow_ = 0, env_long_ = 0;
  std::array<double, 3> band_peak_{};
  Out out_;
};

}  // namespace cadence::render::viz
