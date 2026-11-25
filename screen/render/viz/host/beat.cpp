#include "beat.h"

#include <algorithm>
#include <cmath>
#include <numeric>

namespace cadence::render::viz {

namespace {

// band edges in hz: kick, snare, hat; the full band spans all three
constexpr double kEdges[4] = {30, 130, 5000, 16000};
// the decay of each band's pulse to a tenth, seconds
constexpr double kPulseDecay[BeatTracker::kBands] = {0.30, 0.25, 0.16, 0.25};
constexpr double kRefractory[BeatTracker::kBands] = {0.18, 0.12, 0.07, 0.15};
constexpr double kThresholdK[BeatTracker::kBands] = {2.0, 1.8, 1.8, 1.6};
// the full band stands in for a kick after this long without one (music without a kick), at a share of its
// strength: a breakdown's few kickless seconds are not to punch like the drop (the owner's vultures, 43-53 s)
constexpr double kNoKickS = 4.0;
constexpr float kFallbackShare = 0.5f;

double ease(double cur, double target, double dt, double tau) {
  return cur + (target - cur) * (1 - std::exp(-dt / std::max(tau, 1e-4)));
}

}  // namespace

bool BeatTracker::detect(Detector& d, float flux, double dt, double refractory, double k) {
  d.since += dt;
  // the onsets' reference eases down on its own (halving in ~20 s), so a quieter song or section finds its beats
  d.ref *= std::pow(0.966, dt);
  const std::size_t n = std::max<std::size_t>(d.count, 1);
  double mean = 0, var = 0;
  for (std::size_t i = 0; i < d.count; ++i) mean += d.history[i];
  mean /= static_cast<double>(n);
  for (std::size_t i = 0; i < d.count; ++i) var += (d.history[i] - mean) * (d.history[i] - mean);
  const double dev = std::sqrt(var / static_cast<double>(n));
  // the floor follows the band's typical flux slowly, so a quiet passage still has onsets and hiss has none
  d.floor = d.count == 0 ? flux : d.floor + (flux - d.floor) * (1 - std::exp(-dt / 6.0));
  const double threshold = mean + k * dev + 0.15 * d.floor + 1e-4;
  // the window keeps ~1 s whatever the frame rate
  const auto cap = static_cast<std::size_t>(std::clamp(std::lround(1.0 / dt), 16L, static_cast<long>(d.history.size())));
  d.history[d.pos % cap] = flux;
  d.pos = (d.pos + 1) % cap;
  d.count = std::min(d.count + 1, cap);
  // once the band has a reference, a rise well under its usual onsets is no onset, whatever the window says
  const bool strong = d.hits < 4 || flux >= 0.35 * d.ref;
  const bool hit = d.count >= 8 && flux > threshold && strong && flux >= d.prev && d.since >= refractory;
  d.prev = flux;
  if (hit) d.since = 0;
  return hit;
}

void BeatTracker::track_tempo(double now) {
  if (last_beat_ >= 0) {
    double iv = now - last_beat_;
    if (iv > 0.25 && iv < 2.0) {
      intervals_[interval_pos_] = iv;
      interval_pos_ = (interval_pos_ + 1) % intervals_.size();
      interval_count_ = std::min(interval_count_ + 1, intervals_.size());
    }
  }
  last_beat_ = now;
  if (interval_count_ < 4) return;
  std::array<double, 16> w{};
  std::copy_n(intervals_.begin(), interval_count_, w.begin());
  std::sort(w.begin(), w.begin() + static_cast<std::ptrdiff_t>(interval_count_));
  double median = w[interval_count_ / 2];
  // octave-fold into 70-180 bpm, so an off-beat kick pattern still reads its pulse
  while (median < 60.0 / 180.0) median *= 2;
  while (median > 60.0 / 70.0) median /= 2;
  period_ = period_ <= 0 ? median : period_ * 0.75 + median * 0.25;
}

void BeatTracker::update(std::span<const float> mag, double bin_hz, double dt) {
  dt = std::clamp(dt, 1e-3, 0.25);
  t_ += dt;
  const std::size_t n = std::min(mag.size(), prev_log_.size());

  std::array<float, kBands> flux{};
  std::array<double, kBands> lin{};
  std::array<double, 3> power{};
  double sub_power = 0;
  double total = 0, centroid = 0;
  for (std::size_t k = 1; k < n; ++k) {
    const double hz = static_cast<double>(k) * bin_hz;
    const auto lg = static_cast<float>(std::log1p(1000.0 * mag[k]));
    const float rise = primed_ ? std::max(0.0f, lg - prev_log_[k]) : 0.0f;
    prev_log_[k] = lg;
    const double lrise = primed_ ? std::max(0.0f, mag[k] - prev_mag_[k]) : 0.0;
    prev_mag_[k] = mag[k];
    const double p = static_cast<double>(mag[k]) * mag[k];
    if (hz >= kEdges[0] && hz < 80.0) sub_power += p;
    for (std::size_t b = 0; b < 3; ++b) {
      if (hz >= kEdges[b] && hz < kEdges[b + 1]) {
        flux[b] += rise;
        lin[b] += lrise;
        power[b] += p;
      }
    }
    if (hz >= kEdges[0] && hz < kEdges[3]) {
      flux[Full] += rise;
      lin[Full] += lrise;
      total += p;
      centroid += p * std::log2(std::max(hz, 40.0));
    }
  }
  primed_ = true;

  // the level now and the track's loud level: the gate that keeps a fade or a quiet bridge from punching like a drop.
  // now_db_ tracks the ~0.3 s mean (for track_db_ and detecting silence gaps between songs), while level_db_ rises
  // fast on each frame and falls at 15 db/s so a staccato hit separated by dead silence (ghost! 55-61 s) still reads
  // at its true loudness while a fade-out (-3 to -6 db/s) still falls cleanly with it
  const double rms_now = std::sqrt(std::max(total, 0.0));
  const double db_frame = rms_now > 1e-6 ? 20 * std::log10(rms_now) : -120.0;
  now_db_ = ease(now_db_, db_frame, dt, 0.3);
  level_db_ = db_frame > level_db_ ? ease(level_db_, db_frame, dt, 0.02)
                                   : std::max(db_frame, level_db_ - 15.0 * dt);
  // near silence (the gap between songs) lets it go fast, so a quieter next song is not held under the last one's
  const bool silent = track_db_ - now_db_ > 30;
  // only sink fast toward a quieter master if the song has never proven itself loud; once a track has hit loud
  // levels, a 15-20 s quiet interlude (ghost! 132-153 s) is an interlude, not a quieter master, so track_db_ and
  // bass_ref_db_ must not collapse onto ambient pads and fire a fake drop before the real one lands
  if (silent) heard_loud_ = false;
  else if (now_db_ > kLoudMasterDb - 3.0) heard_loud_ = true;
  song_s_ = silent ? 0.0 : song_s_ + dt;
  const bool settled = song_s_ > 30.0;
  const bool quiet_master = settled && !heard_loud_;
  const double sink = quiet_master && track_db_ - now_db_ > 6 ? 1.0 : 0.15;
  track_db_ = silent ? std::max({now_db_, track_db_ - 12.0 * dt, kLoudMasterDb})
                     : std::max(now_db_, track_db_ - sink * dt);
  // full within 1.5 db of it, nothing 10 db under (the owner: a fading outro still punched like the chorus at -14 db)
  const double drop = track_db_ - now_db_;
  const double gate = std::clamp((10.0 - drop) / 8.5, 0.0, 1.0);
  const double loud = gate * gate * (3 - 2 * gate);
  loud_ = loud;
  // peak-held loudness for transient drum hits: bridges silent gaps between staccato beats without lifting continuous
  // energy or build-up state, and scales staccato breaks under the full wall
  const double peak_drop = track_db_ - level_db_;
  const double peak_gate = std::clamp((10.0 - peak_drop) / 8.5, 0.0, 1.0);
  const double peak_loud = peak_gate * peak_gate * (3 - 2 * peak_gate);
  const double hit_loud = std::max(loud, 0.72 * peak_loud);

  // the bass against the track's own, and a build-up: the bass gone a while with the music still going
  const double bass_now = 10 * std::log10(power[0] + 1e-12);
  // its mean (~0.4 s) and its peaks held (falling 3 db a second): a held 808 under a drop fills both, a kick-only
  // pattern only the peaks (most of a presence), a build-up neither
  bass_db_ = ease(bass_db_, bass_now, dt, 0.4);
  bass_peak_db_ = bass_now > bass_peak_db_ ? ease(bass_peak_db_, bass_now, dt, 0.03)
                                           : std::max(bass_now, bass_peak_db_ - 3.0 * dt);
  // never under the track's level less 5 db (a master's bass sits ~5 db under its whole), so a long intro or
  // build-up cannot drag the bass reference down onto snare bleed, while a quieter master's reference still follows
  // track_db_ down once settled
  bass_ref_db_ = silent ? std::max({bass_db_, bass_ref_db_ - 12.0 * dt, kLoudBassDb})
                        : std::max({bass_db_, bass_ref_db_ - (quiet_master ? 0.5 : 0.15) * dt,
                                    track_db_ + (kLoudBassDb - kLoudMasterDb)});
  const auto smooth = [](double x) { return x * x * (3 - 2 * x); };
  const double mean_presence = smooth(std::clamp((bass_db_ - (bass_ref_db_ - 12.0)) / 7.0, 0.0, 1.0));
  const double peak_presence = smooth(std::clamp((bass_peak_db_ - (bass_ref_db_ - 6.0)) / 7.0, 0.0, 1.0));
  const double presence = std::max(mean_presence, 0.7 * peak_presence);
  // a build-up or breakdown: once the song has held real bass (4 s), any non-silent bassless section (loud build-up
  // or quiet atmospheric interlude like ghost! 132-153 s) arms the next drop; before the first drop of a song,
  // require loud energy (loud > 0.45) so a quiet intro stays calm while a real pre-drop build-up still arms it
  if (presence > 0.8) bass_held_s_ += dt;
  if (silent) {
    bass_held_s_ = 0;
    build_s_ = 0;
    drop_armed_ = false;
    drop_disarm_s_ = 0;
  } else if (presence < 0.5 && (bass_held_s_ > 4.0 || loud > 0.45)) {
    build_s_ += dt;
  } else if (presence > 0.7) {
    build_s_ = std::max(0.0, build_s_ - 4.0 * dt);
  }
  if (build_s_ >= 2.0 && !drop_open_) {
    drop_armed_ = true;
    drop_disarm_s_ = 0;
  } else if (drop_armed_ && presence > 0.7) {
    // stay armed for up to 1.5 s through a pre-drop beat-4 pickup hit (ghost! 208.4 -> 208.9 s), then disarm if
    // sustained bass holds without a real kick (a kickless synth pad like 202-205 s)
    drop_disarm_s_ += dt;
    if (drop_disarm_s_ > 1.5) drop_armed_ = false;
  }
  out_.loud = static_cast<float>(loud);
  out_.hit_loud = static_cast<float>(hit_loud);
  out_.level_gain = static_cast<float>(std::clamp(std::pow(10.0, (track_db_ - kLoudMasterDb) / 20.0), 0.01, 1.0));
  out_.bass_presence = static_cast<float>(presence);
  out_.build = static_cast<float>(std::clamp(build_s_ / 8.0, 0.0, 1.0));

  // a drop's arrival: the bass back after a build-up held. the build arms it only 2 s in (the held bass peak takes
  // ~2 s to fall away, and its still being up must not land the hit), then the bass's first peak back lands it at
  // once — waiting on the presence's mean would sit half a second into the drop (the owner: invasive's particles
  // rushed the 1:25 build-up and then sat slow in the 1:30 drop). both decay between frames, so the frame it lands
  // is the full hit: the pulse for the slam, the level for the section after it. a short build hits as hard as a
  // long one — scaling the hit by the build left invasive's drop quieter than its own chorus
  drop_pulse_ *= std::pow(0.1, dt / 0.75);
  // the section stays the fast part well after the hit. while the music still holds its bass and its level it
  // barely sinks (halving over ~60 s — a wall of a section lasts a hundred seconds, ghost!'s first ran 32-130 and
  // sagged to verse speed by its middle); once the section truly calms it falls in ~10 s, so the next one is a
  // fresh hit
  const bool in_section = presence > 0.6 && loud > 0.4;
  drop_level_ *= std::pow(0.5, dt / (in_section ? 60.0 : 10.0));
  // true sub-bass (30-80 Hz) tracks the real 808/kick weight separately from 90-130 Hz pickup fills (ghost! 208.47 vs
  // 209.20 s) so a drop fires when the actual sub-bass lands
  const double sub_now = 10 * std::log10(sub_power + 1e-12);
  sub_db_ = ease(sub_db_, sub_now, dt, 0.4);
  sub_peak_db_ = sub_now > sub_peak_db_ ? ease(sub_peak_db_, sub_now, dt, 0.03)
                                        : std::max(sub_now, sub_peak_db_ - 6.0 * dt);
  sub_ref_db_ = silent ? std::max({sub_db_, sub_ref_db_ - 12.0 * dt, kLoudBassDb - 3.0})
                       : std::max({sub_db_, sub_ref_db_ - (quiet_master ? 0.5 : 0.15) * dt,
                                   track_db_ + (kLoudBassDb - 3.0 - kLoudMasterDb)});
  const bool sub_hit = sub_peak_db_ >= sub_ref_db_ - 1.5;
  drop_ready_ = drop_armed_ && !drop_open_ && sub_hit && (peak_presence > 0.98 || presence > 0.80) &&
                std::max(loud, peak_loud) > 0.65;
  if (presence < 0.5) drop_open_ = false;
  out_.drop = false;
  out_.drop_pulse = static_cast<float>(drop_pulse_);
  out_.drop_level = static_cast<float>(drop_level_);

  // onsets per band
  for (std::size_t b = 0; b < kBands; ++b) {
    auto& d = det_[b];
    bool hit = detect(d, flux[b], dt, kRefractory[b], kThresholdK[b]);
    // far under the track's loud level (a fade's tail, a near-silent intro) nothing counts as a beat
    if (loud < 0.2) hit = false;
    // the log flux finds the onset's shape at any level, so a soft bass note in a breakdown rises as far as a kick
    // (the owner's vultures, 43-53 s); its linear rise against the band's usual hits tells them apart (kicks 0.2-1.4
    // there, the breakdown's notes 0.02-0.15). the reference halves in ~40 s, so a quieter song finds its own
    d.lin_ref *= std::pow(0.983, dt);
    // the gap between songs starts it over: the next one may be mastered quieter
    if (silent) d.lin_hits = 0;
    if (hit && d.lin_hits >= 4 && lin[b] < 0.33 * d.lin_ref) hit = false;
    out_.onset[b] = hit;
    const auto decay = static_cast<float>(std::pow(0.1, dt / kPulseDecay[b]));
    out_.pulse[b] *= decay;
    if (hit) {
      // its strength: its linear rise against the band's usual hits, and the music's level against the track's
      const double share = d.lin_hits < 4 ? 1.0 : std::clamp(lin[b] / std::max(d.lin_ref, 1e-6), 0.0, 1.0);
      d.ref = d.hits == 0 ? flux[b] : d.ref + (flux[b] - d.ref) * (d.hits < 4 ? 0.5 : 0.12);
      ++d.hits;
      // the strong hits' level: up fast, down slowly (weak hits passing must not drag it to themselves), and one
      // huge hit does not lift it past what the usual ones reach
      const double l = d.lin_hits == 0 ? lin[b] : std::min(lin[b], 2.5 * d.lin_ref);
      // (the first hit out of silence rises from nothing, many times a usual one: calibration comes down fast)
      const double rate = d.lin_hits < 4 ? (l > d.lin_ref ? 0.3 : 0.8) : (l > d.lin_ref ? 0.2 : 0.03);
      d.lin_ref = d.lin_hits == 0 ? l : d.lin_ref + (l - d.lin_ref) * rate;
      ++d.lin_hits;
      const double strength = std::clamp((0.15 + 0.85 * share) * loud, 0.0, 1.0);
      out_.pulse[b] = std::max(out_.pulse[b], static_cast<float>(strength));
    }
  }
  if (out_.onset[Kick]) last_kick_ = t_;
  const bool fallback = t_ - last_kick_ > kNoKickS;
  out_.beat = out_.onset[Kick] || (fallback && out_.onset[Full]);
  out_.beat_pulse = fallback ? std::max(out_.pulse[Kick], kFallbackShare * out_.pulse[Full]) : out_.pulse[Kick];
  if (out_.beat) track_tempo(t_);
  if (last_beat_ >= 0 && period_ > 0 && t_ - last_beat_ < 4 * period_) {
    out_.bpm = static_cast<float>(60.0 / period_);
    out_.phase = static_cast<float>(std::fmod((t_ - last_beat_) / period_, 1.0));
  } else {
    out_.bpm = 0;
    out_.phase = 0;
  }

  // loudness against the track's own range: the peak (from a loud master's, kLoudMasterDb) falls 0.3 db a second
  // and the floor rises 1, each jumping to a new extreme, and the window between them is never narrower than 30 db.
  // slow, so a fade reads as a fade (at 3 db a second the range chased the fade and a dying song looked as loud as
  // its chorus)
  const double db = db_frame;
  peak_db_ = silent ? std::max({db, peak_db_ - 12.0 * dt, kLoudMasterDb})
                    : std::max({db, peak_db_ - 0.3 * dt, track_db_});
  low_db_ = db < low_db_ ? std::max(db, -80.0) : low_db_ + 1.0 * dt;
  const double top = std::max(peak_db_, -70.0);
  const double bottom = std::min(low_db_, top - 30.0);
  // a fade or a quiet bridge reads as one: the level is held down with the gate, so the glow and speed calm with it
  // held down with the gate and the bass's presence too: a fade, a quiet bridge or an intro without its bass is calm,
  // the glow and the particles' speed with it
  const double level = db < -75 ? 0.0
                                : std::clamp((db - bottom) / (top - bottom), 0.0, 1.0) * (0.3 + 0.7 * loud) *
                                      (0.35 + 0.65 * presence);
  env_ = ease(env_, level, dt, level > env_ ? 0.04 : 0.25);
  env_slow_ = ease(env_slow_, level, dt, level > env_slow_ ? 0.8 : 2.0);
  env_long_ = ease(env_long_, level, dt, 8.0);
  out_.energy = static_cast<float>(env_);
  out_.energy_slow = static_cast<float>(env_slow_);
  const double surge = std::clamp((env_slow_ - env_long_) * 4.0, 0.0, 1.0);
  // the drop's hold: the intensity lifted the frame it lands and halving every ~6 s through the section, so the
  // drop reads as the loudest thing around however loud the part before its build-up was
  drop_hold_ *= std::pow(0.5, dt / 6.0);
  const double target = std::max(surge, drop_hold_);
  out_.intensity = static_cast<float>(ease(out_.intensity, target, dt, target > out_.intensity ? 0.15 : 2.0));

  // each band against its own recent peak (falling ~1.4 db a second), with a floor so silence stays dark.
  // bass also floors its peak at the track's own bass reference and gates on bass_presence so an intro or a
  // bassless build-up never reports full bass as its short-term peak decays
  const double bass_ref = std::pow(10.0, bass_ref_db_ / 20.0);
  for (std::size_t b = 0; b < 3; ++b) {
    const double e = std::sqrt(power[b]);
    const double floor = b == 0 ? std::max(bass_ref, 1e-3) : 1e-3;
    band_peak_[b] = std::max({e, band_peak_[b] * std::pow(0.85, dt), floor});
    const double rel = std::clamp(e / band_peak_[b], 0.0, 1.0);
    // a contrast curve: the top of the range is where the drive is
    const double band_gate = b == 0 ? presence : (0.4 + 0.6 * presence);
    const double v = db < -75 ? 0.0 : rel * rel * (0.3 + 0.7 * loud_) * band_gate;
    out_.band[b] = static_cast<float>(ease(out_.band[b], v, dt, v > out_.band[b] ? 0.02 : 0.18));
  }
  const double c = total > 1e-12 ? centroid / total : std::log2(40.0);
  out_.brightness = static_cast<float>(std::clamp((c - std::log2(150.0)) / (std::log2(6000.0) - std::log2(150.0)), 0.0, 1.0));
}

void BeatTracker::confirm_drop(bool has_kick) {
  if (!drop_ready_ || !has_kick) return;
  drop_ready_ = false;
  drop_armed_ = false;
  drop_disarm_s_ = 0;
  drop_open_ = true;
  drop_pulse_ = 1;
  drop_level_ = 1;
  drop_hold_ = std::max(drop_hold_, std::clamp(0.65 + 0.35 * build_s_ / 8.0, 0.0, 1.0));
  out_.drop = true;
  out_.drop_pulse = static_cast<float>(drop_pulse_);
  out_.drop_level = static_cast<float>(drop_level_);
}

void BeatTracker::reset_track() {
  for (auto& d : det_) {
    d.ref = 0;
    d.hits = 0;
    d.lin_ref = 0;
    d.lin_hits = 0;
  }
  last_beat_ = -1;
  last_kick_ = -1e9;
  interval_pos_ = 0;
  interval_count_ = 0;
  period_ = 0;
  peak_db_ = kLoudMasterDb;
  low_db_ = -60;
  track_db_ = kLoudMasterDb;
  now_db_ = -60;
  level_db_ = -60;
  heard_loud_ = false;
  bass_db_ = -120;
  bass_peak_db_ = -120;
  bass_ref_db_ = kLoudBassDb;
  sub_db_ = -120;
  sub_peak_db_ = -120;
  sub_ref_db_ = kLoudBassDb - 3.0;
  bass_held_s_ = 0;
  build_s_ = 0;
  song_s_ = 0;
  drop_pulse_ = 0;
  drop_level_ = 0;
  drop_hold_ = 0;
  drop_disarm_s_ = 0;
  drop_open_ = false;
  drop_armed_ = false;
  drop_ready_ = false;
  env_long_ = env_slow_;
  band_peak_ = {};
  out_.drop = false;
  out_.drop_pulse = 0;
  out_.drop_level = 0;
  out_.build = 0;
  out_.intensity = 0;
}

}  // namespace cadence::render::viz
