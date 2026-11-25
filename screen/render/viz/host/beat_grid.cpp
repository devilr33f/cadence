#include "beat_grid.h"

#include <algorithm>
#include <cmath>
#include <complex>
#include <numbers>

namespace cadence::render::viz {

namespace {

constexpr double kLowHz = 30, kHighHz = 8000;
// the tempo's range and its prior (librosa's: a log-normal around 120 bpm, an octave wide)
constexpr double kMinBpm = 60, kMaxBpm = 200, kPriorBpm = 120;
// how far behind the newest hop a beat is confirmed (its peak must be seen), seconds: well inside the lead the
// screen's timeline has (150 ms and more)
constexpr double kConfirmS = 0.07;
// the tempo's look-back and how often it is estimated, seconds
constexpr double kTempoWindowS = 6.0, kTempoEveryS = 0.5, kTempoFirstS = 3.0;
// the envelope (the mean rise of 40 bands, db a hop) of music with onsets in it, and the least a peak standing for
// a beat with no tempo must reach: a held tone's is about 0.06, music's about 2
constexpr double kMinEnv = 0.4, kMinOnset = 1.5;
// silence this long starts the grid and its references over (the gap between songs)
constexpr double kSilenceS = 1.0;

}  // namespace

BeatGrid::BeatGrid(std::uint32_t rate) : rate_(rate == 0 ? 48000 : rate) {
  hop_ = std::max<std::size_t>(64, static_cast<std::size_t>(std::lround(512.0 * rate_ / 48000.0)));
  for (std::size_t n = 0; n < kWindow; ++n)
    window_[n] = 0.5 - 0.5 * std::cos(2.0 * std::numbers::pi * static_cast<double>(n) / (kWindow - 1));
  // log-spaced bands, each at least one bin and above the last
  const double nyq = rate_ / 2.0;
  const double hi = std::min(kHighHz, nyq * 0.95);
  for (std::size_t b = 0; b <= kBands; ++b) {
    const double hz = kLowHz * std::pow(hi / kLowHz, static_cast<double>(b) / kBands);
    auto bin = static_cast<std::size_t>(std::lround(hz * kWindow / rate_));
    if (b > 0) bin = std::max(bin, edges_[b - 1] + 1);
    edges_[b] = std::min(bin, kWindow / 2 - 1);
  }
  prev_db_.fill(-100);
}

void BeatGrid::feed(std::span<const float> mono) {
  for (const float s : mono) {
    ring_[ring_pos_] = std::isfinite(s) ? s : 0.0f;
    ring_pos_ = (ring_pos_ + 1) % kWindow;
    ++fed_;
    if (++since_hop_ >= hop_) {
      since_hop_ = 0;
      hop();
    }
  }
}

double BeatGrid::env(std::int64_t h) const {
  if (h < 0 || h >= hops_ || hops_ - h > static_cast<std::int64_t>(kHistory)) return 0;
  return env_[static_cast<std::size_t>(h % static_cast<std::int64_t>(kHistory))];
}

double BeatGrid::lin(std::int64_t h) const {
  if (h < 0 || h >= hops_ || hops_ - h > static_cast<std::int64_t>(kHistory)) return 0;
  return lin_[static_cast<std::size_t>(h % static_cast<std::int64_t>(kHistory))];
}

double BeatGrid::body(std::int64_t h) const {
  if (h < 0 || h >= hops_ || hops_ - h > static_cast<std::int64_t>(kHistory)) return 0;
  return body_[static_cast<std::size_t>(h % static_cast<std::int64_t>(kHistory))];
}

void BeatGrid::hop() {
  std::array<std::complex<double>, kWindow> x{};
  for (std::size_t n = 0; n < kWindow; ++n) x[n] = static_cast<double>(ring_[(ring_pos_ + n) % kWindow]) * window_[n];
  fft_.forward(x);
  const double scale = 4.0 / kWindow;
  double rise = 0, body = 0, loudest = -200;
  std::array<double, kBands> db{};
  for (std::size_t b = 0; b < kBands; ++b) {
    double p = 0;
    for (std::size_t k = edges_[b]; k < edges_[b + 1]; ++k) {
      const double m = std::abs(x[k]) * scale;
      p += m * m;
      const double d = primed_ ? std::max(0.0, m - prev_mag_[k]) : 0.0;
      rise += d;
      // the kick and the snare outweigh a hat: 1 near 100 Hz, about a half at 400, under a tenth at 5 kHz.
      // the plain rise is the opposite — most bins are the top — so the ticks around a beat were punching and the
      // beat one hears was not (the owner)
      const double hz = static_cast<double>(k) * static_cast<double>(rate_) / static_cast<double>(kWindow);
      body += d / (1.0 + std::pow(hz / 280.0, 1.4));
      prev_mag_[k] = m;
    }
    db[b] = std::max(10 * std::log10(p + 1e-12), -100.0);
    loudest = std::max(loudest, db[b]);
  }
  // each band at most 60 db under the loudest (librosa's top_db): a band with next to nothing in it jitters by
  // tens of db between hops, and a held tone read as a stream of onsets
  double flux = 0;
  for (std::size_t b = 0; b < kBands; ++b) {
    const double d = std::max(db[b], loudest - 60.0);
    if (primed_) flux += std::max(0.0, d - prev_db_[b]);
    prev_db_[b] = d;
  }
  primed_ = true;
  flux /= kBands;
  const auto slot = static_cast<std::size_t>(hops_ % static_cast<std::int64_t>(kHistory));
  env_[slot] = flux;
  lin_[slot] = rise;
  body_[slot] = body;
  ++hops_;

  const double hop_s = static_cast<double>(hop_) / rate_;
  // near silence a while: a new song starts everything over
  silent_ = loudest < -70 ? silent_ + 1 : 0;
  if (static_cast<double>(silent_) * hop_s > kSilenceS) {
    period_ = 0;
    confidence_ = 0;
    next_ = -1;
    refs_ = 0;
    ref_env_ = kPriorEnv;
    ref_lin_ = kPriorLin;
    ref_body_ = kPriorBody;
    disagree_ = 0;
    first_hop_ = hops_;
  }
  // the usual beat's level sinks 0.2 db a second on its own: a song mastered quieter than kPrior* finds its beats
  ref_lin_ *= std::pow(10.0, -0.2 * hop_s / 20);
  ref_env_ *= std::pow(10.0, -0.2 * hop_s / 20);
  ref_body_ *= std::pow(10.0, -0.2 * hop_s / 20);
  // the fallback's threshold: the envelope's mean and spread over ~1 s
  const double a = 1 - std::exp(-hop_s / 1.0);
  mean_ += (flux - mean_) * a;
  var_ += ((flux - mean_) * (flux - mean_) - var_) * a;

  if (static_cast<double>(hops_ - first_hop_) * hop_s >= kTempoFirstS &&
      static_cast<double>(hops_ - last_tempo_) * hop_s >= kTempoEveryS) {
    last_tempo_ = hops_;
    estimate_tempo();
  }
  place_beats();
}

void BeatGrid::estimate_tempo() {
  const double hop_s = static_cast<double>(hop_) / rate_;
  const auto n = static_cast<std::int64_t>(std::min<double>(
      {static_cast<double>(hops_ - first_hop_), kTempoWindowS / hop_s, static_cast<double>(kHistory)}));
  const auto lo = static_cast<std::int64_t>(std::floor(60.0 / (kMaxBpm * hop_s)));
  const auto hi = static_cast<std::int64_t>(std::ceil(60.0 / (kMinBpm * hop_s)));
  if (n < hi * 2) return;
  std::vector<double> e(static_cast<std::size_t>(n));
  double mean = 0;
  for (std::int64_t i = 0; i < n; ++i) {
    e[static_cast<std::size_t>(i)] = env(hops_ - n + i);
    mean += e[static_cast<std::size_t>(i)];
  }
  mean /= static_cast<double>(n);
  // no onsets to speak of (a held tone, a pad): no tempo either, whatever the noise's autocorrelation says
  if (mean < kMinEnv) {
    confidence_ *= 0.5;
    return;
  }
  double zero = 0;
  for (auto& v : e) {
    v -= mean;
    zero += v * v;
  }
  zero /= static_cast<double>(n);
  if (zero <= 1e-9) return;
  // up to twice the slowest beat: a tempo is scored with its bar's support too
  const std::int64_t top = std::min<std::int64_t>(2 * hi + 2, n - 1);
  std::vector<double> ac(static_cast<std::size_t>(top + 1), 0.0);
  for (std::int64_t lag = lo - 1; lag <= top; ++lag) {
    double s = 0;
    for (std::int64_t i = lag; i < n; ++i) s += e[static_cast<std::size_t>(i)] * e[static_cast<std::size_t>(i - lag)];
    ac[static_cast<std::size_t>(lag)] = s / static_cast<double>(n - lag) / zero;
  }
  const auto score = [&](std::int64_t lag) {
    const double twice = 2 * lag <= top ? ac[static_cast<std::size_t>(2 * lag)] : 0.0;
    return ac[static_cast<std::size_t>(lag)] + 0.0 * std::max(twice, 0.0);
  };
  double best = -1e9;
  std::int64_t at = 0;
  for (std::int64_t lag = lo; lag <= hi; ++lag) {
    const double bpm = 60.0 / (static_cast<double>(lag) * hop_s);
    const double w = std::exp(-0.5 * std::pow(std::log2(bpm / kPriorBpm), 2));
    // a beat whose double also repeats (two beats, half a bar) over one that does not: a triplet hat's 3:2 pulse
    // scores close to the beat alone
    const double v = score(lag) * w;
    if (v > best) {
      best = v;
      at = lag;
    }
  }
  if (at <= 0) return;
  // the peak between hops
  const double y0 = ac[static_cast<std::size_t>(at - 1)], y1 = ac[static_cast<std::size_t>(at)],
               y2 = ac[static_cast<std::size_t>(at + 1)];
  const double d = y0 - 2 * y1 + y2;
  const double lag = static_cast<double>(at) + (std::abs(d) > 1e-12 ? std::clamp(0.5 * (y0 - y2) / d, -0.5, 0.5) : 0.0);
  confidence_ += (std::clamp(y1 * 2.0, 0.0, 1.0) - confidence_) * 0.4;

  if (period_ <= 0) {
    period_ = lag;
    disagree_ = 0;
    anchor();
    return;
  }
  if (std::abs(lag / period_ - 1) < 0.04) {
    period_ += (lag - period_) * 0.2;
    disagree_ = 0;
    anchor();
    return;
  }
  // another tempo must clearly beat the one held (a third more of the weighted autocorrelation: a song's half or
  // double tempo scores close, and flipping between them throws the picture off the beat) and hold for three
  // estimates (1.5 s) before the grid moves to it
  const auto held = static_cast<std::int64_t>(std::lround(period_));
  if (held >= lo && held <= hi) {
    const double bpm = 60.0 / (period_ * hop_s);
    const double w = std::exp(-0.5 * std::pow(std::log2(bpm / kPriorBpm), 2));
    if (best < 1.3 * std::max(score(held), 0.0) * w) {
      disagree_ = 0;
      anchor();
      return;
    }
  }
  if (std::abs(lag / std::max(candidate_, 1e-9) - 1) < 0.04) {
    ++disagree_;
  } else {
    candidate_ = lag;
    disagree_ = 1;
  }
  if (disagree_ >= 3) {
    period_ = lag;
    disagree_ = 0;
    next_ = -1;
    anchor();
  }
}

void BeatGrid::anchor() {
  if (period_ <= 0) return;
  const double hop_s = static_cast<double>(hop_) / rate_;
  const auto confirm = static_cast<std::int64_t>(std::ceil(kConfirmS / hop_s));
  const std::int64_t newest = hops_ - 1 - confirm;
  const auto p = static_cast<std::int64_t>(std::lround(period_));
  if (p < 2 || newest < p * 2) return;
  // the phase whose comb of beats back through the envelope gathers the most of it
  double best = -1;
  std::int64_t at = 0;
  for (std::int64_t phi = 0; phi < p; ++phi) {
    double s = 0, w = 1;
    for (int k = 0; k < 8; ++k, w *= 0.85) {
      const auto h = static_cast<std::int64_t>(std::lround(static_cast<double>(newest - phi) - k * period_));
      s += w * std::max({env(h - 1), env(h), env(h + 1)});
    }
    if (s > best) {
      best = s;
      at = phi;
    }
  }
  const double last = static_cast<double>(newest - at);
  double next = last + period_;
  while (next <= static_cast<double>(newest)) next += period_;
  if (next_ < 0) {
    next_ = next;
    return;
  }
  // a grid that drifted off the music's phase by more than a sixth of a beat is put back on it
  double diff = std::fmod(next - next_, period_);
  if (diff > period_ / 2) diff -= period_;
  if (diff < -period_ / 2) diff += period_;
  if (std::abs(diff) > period_ / 6) next_ += diff;
}

void BeatGrid::place_beats() {
  const double hop_s = static_cast<double>(hop_) / rate_;
  const auto confirm = static_cast<std::int64_t>(std::ceil(kConfirmS / hop_s));
  const std::int64_t newest = hops_ - 1;
  if (period_ > 0 && confidence_ > 0.15 && next_ >= 0) {
    while (static_cast<double>(newest) >= next_ + static_cast<double>(confirm)) {
      // the hit nearest where the grid expects it, within an eighth of a beat
      const double w = std::max(2.0, period_ * 0.125);
      std::int64_t at_env = std::lround(next_);
      std::int64_t at_body = at_env;
      double peak = -1, peak_body = -1;
      for (auto h = static_cast<std::int64_t>(std::floor(next_ - w)); h <= static_cast<std::int64_t>(std::ceil(next_ + w)); ++h) {
        if (env(h) > peak) {
          peak = env(h);
          at_env = h;
        }
        if (body(h) > peak_body) {
          peak_body = body(h);
          at_body = h;
        }
      }
      // land on the kick or the snare when one is there; a hat's peak in the same window is not the beat
      const bool body_hit = peak_body >= 0.35 * ref_body_;
      const bool hit = body_hit || peak >= 0.3 * ref_env_;
      const std::int64_t beat = body_hit ? at_body : hit ? at_env : std::lround(next_);
      emit(beat, hit ? std::max(peak, env(beat)) : env(beat), std::max({lin(beat - 1), lin(beat), lin(beat + 1)}),
           std::max({body(beat - 1), body(beat), body(beat + 1)}), hit);
      // the grid follows the music's hits, gently
      // the period follows the gap between two hits in a row (the autocorrelation's lag is whole hops: 120 bpm read
      // for 117.5 drifted a beat off in a few bars)
      if (hit && last_hit_ >= 0) {
        const double gap = static_cast<double>(beat - last_hit_);
        if (std::abs(gap / period_ - 1) < 0.08) period_ += (gap - period_) * 0.1;
      }
      last_hit_ = hit ? beat : -1;
      next_ = (hit ? 0.6 * static_cast<double>(beat) + 0.4 * next_ : next_) + period_;
    }
    return;
  }
  // no tempo yet: the envelope's own peaks, three hops after them, over its mean and 1.5 spreads, 0.2 s apart
  const std::int64_t c = newest - 3;
  if (c < 3) return;
  const double v = env(c);
  for (std::int64_t h = c - 3; h <= c + 3; ++h)
    if (h != c && env(h) > v) return;
  if (v <= mean_ + 1.5 * std::sqrt(std::max(var_, 0.0)) || v < kMinOnset) return;
  if (static_cast<double>(c - last_beat_hop_) * hop_s < 0.2) return;
  emit(c, v, std::max({lin(c - 1), lin(c), lin(c + 1)}), std::max({body(c - 1), body(c), body(c + 1)}), true);
}

void BeatGrid::emit(std::int64_t h, double e, double l, double b, bool hit) {
  last_beat_hop_ = h;
  // how hard it hits against the usual beat. the body is the kick and the snare; the whole-spectrum rise only
  // keeps a beat that has neither, and then only as a tick. a half-size hit stays small (the curve), so the beat
  // one hears is the one that moves the picture
  const double share_body = std::clamp(b / std::max(ref_body_ * level_gain_, 1e-12), 0.0, 1.5);
  const double share_lin = std::clamp(l / std::max(ref_lin_ * level_gain_, 1e-12), 0.0, 1.0);
  const double share = std::max(share_body, 0.35 * share_lin);
  const double curved = std::pow(std::min(share, 1.35), 1.55);
  const double strength = std::clamp(curved * (hit ? 1.0 : 0.28), 0.0, 1.0);
  // the usual beat: raised by the hits over it (one huge hit at most 2.5 times), from a loud master's (kPrior*);
  // weak beats never pull it down (a quiet intro is not the song's measure), time does (hop())
  if (e > ref_env_) ref_env_ += (std::min(e, 2.5 * ref_env_) - ref_env_) * 0.2;
  if (l > ref_lin_ * level_gain_) ref_lin_ += (std::min(l / level_gain_, 2.5 * ref_lin_) - ref_lin_) * 0.2;
  if (b > ref_body_ * level_gain_) ref_body_ += (std::min(b / level_gain_, 2.5 * ref_body_) - ref_body_) * 0.25;
  ++refs_;
  // the hop's window ends at (h + 1) * hop; its rise is its middle's
  const std::int64_t frame = (h + 1) * static_cast<std::int64_t>(hop_) - static_cast<std::int64_t>(kWindow / 2);
  beats_.push_back({frame, static_cast<float>(std::clamp(strength, 0.0, 1.0))});
  while (beats_.size() > 64) beats_.pop_front();
}

std::vector<BeatGrid::Beat> BeatGrid::take(std::int64_t heard) {
  std::vector<Beat> out;
  while (!beats_.empty() && beats_.front().frame <= heard) {
    out.push_back(beats_.front());
    last_beat_frame_ = beats_.front().frame;
    beats_.pop_front();
  }
  return out;
}

double BeatGrid::phase(std::int64_t heard) const {
  if (bpm() <= 0 || last_beat_frame_ < 0) return 0;
  const double p = period_ * static_cast<double>(hop_);
  return std::fmod(std::max(0.0, static_cast<double>(heard - last_beat_frame_)) / p, 1.0);
}

}  // namespace cadence::render::viz
