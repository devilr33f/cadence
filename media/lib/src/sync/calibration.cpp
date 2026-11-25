#include "sync/calibration.h"

#include <algorithm>
#include <cmath>
#include <limits>
#include <map>
#include <numbers>

namespace cadence::sync::calibration {

namespace {

constexpr double kPi = std::numbers::pi;
// the windowed sinc's half width, in lags
constexpr int kSincHalf = 16;
// the fine search around the picked lag: steps a lag is split into
constexpr int kFineSteps = 32;
// a rayleigh envelope's median is this many sigmas of either component
constexpr double kRayleighMedian = 1.1774100225154747;
constexpr double kMaxPsrDb = 200;

std::size_t nextPow2(std::size_t n) {
  std::size_t p = 1;
  while (p < n)
    p <<= 1;
  return p;
}

double psrDb(double peak, double sigma) {
  if (sigma <= 0)
    return peak > 0 ? kMaxPsrDb : 0;
  return std::min(kMaxPsrDb, 20.0 * std::log10(peak / sigma));
}

double envelopeAt(std::span<const std::complex<double>> corr, double lag) { return std::abs(interpolate(corr, lag)); }

// the analytic correlation against one reference, its spectrum kept per transform size (a calibration correlates the
// same chirp against every slot window)
class LegacyCorrelator {
 public:
  explicit LegacyCorrelator(std::vector<float> ref) : ref_(std::move(ref)) {}

  [[nodiscard]] std::size_t refSize() const { return ref_.size(); }

  std::vector<std::complex<double>> operator()(std::span<const float> signal) {
    if (ref_.empty() || ref_.size() > signal.size())
      return {};
    const auto n = nextPow2(signal.size() + ref_.size());
    auto& r = spectra_[n];
    if (r.empty()) {
      r.assign(n, {});
      for (std::size_t i = 0; i < ref_.size(); ++i)
        r[i] = ref_[i];
      fft(r, false);
    }
    std::vector<std::complex<double>> s(n);
    for (std::size_t i = 0; i < signal.size(); ++i)
      s[i] = signal[i];
    fft(s, false);
    // S * conj(R) is the cross-correlation's spectrum; zeroing its negative frequencies (and doubling the positive)
    // makes it analytic: the real part the correlation, the imaginary its hilbert transform
    for (std::size_t k = 0; k < n; ++k) {
      auto x = s[k] * std::conj(r[k]);
      if (k == 0 || k == n / 2)
        s[k] = x;
      else if (k < n / 2)
        s[k] = 2.0 * x;
      else
        s[k] = 0;
    }
    fft(s, true);
    const auto lags = signal.size() - ref_.size() + 1;
    s.resize(lags);
    const double scale = 1.0 / static_cast<double>(n);
    for (auto& v : s)
      v *= scale;
    return s;
  }

 private:
  std::vector<float> ref_;
  std::map<std::size_t, std::vector<std::complex<double>>> spectra_;
};

double median(std::vector<double> v) {
  if (v.empty())
    return 0;
  std::sort(v.begin(), v.end());
  const auto mid = v.size() / 2;
  return v.size() % 2 == 1 ? v[mid] : 0.5 * (v[mid - 1] + v[mid]);
}

}  // namespace

ChirpShape legacyChirp() {
  ChirpShape shape;
  shape.f0Hz = 1000;
  shape.f1Hz = 6000;
  shape.durationS = 0.1;
  shape.fadeS = 0.005;
  return shape;
}

ChirpShape withGain(ChirpShape shape, std::uint32_t gainDb) {
  shape.amplitude *= std::pow(10.0, static_cast<double>(std::min(gainDb, kMaxGainDb)) / 20.0);
  return shape;
}

double chirpAt(const ChirpShape& shape, double tS) {
  if (tS < 0 || tS >= shape.durationS)
    return 0;
  const double k = shape.f1Hz / shape.f0Hz;
  const double ln = std::log(k);
  // exponential sweep: the instantaneous frequency f0 * k^(t / T)
  const double phase = ln > 0 ? 2.0 * kPi * shape.f0Hz * shape.durationS / ln * (std::exp(tS / shape.durationS * ln) - 1.0)
                              : 2.0 * kPi * shape.f0Hz * tS;
  double gain = 1.0;
  if (shape.fadeS > 0) {
    const double edge = std::min(tS, shape.durationS - tS);
    if (edge < shape.fadeS)
      gain = 0.5 - 0.5 * std::cos(kPi * edge / shape.fadeS);
  }
  return shape.amplitude * gain * std::sin(phase);
}

std::vector<float> chirpSamples(const ChirpShape& shape, double rate) {
  const auto n = static_cast<std::size_t>(std::ceil(shape.durationS * rate));
  std::vector<float> out(n);
  for (std::size_t i = 0; i < n; ++i)
    out[i] = static_cast<float>(chirpAt(shape, static_cast<double>(i) / rate));
  return out;
}

void fft(std::vector<std::complex<double>>& data, bool inverse) {
  const auto n = data.size();
  for (std::size_t i = 1, j = 0; i < n; ++i) {
    std::size_t bit = n >> 1;
    for (; (j & bit) != 0; bit >>= 1)
      j ^= bit;
    j ^= bit;
    if (i < j)
      std::swap(data[i], data[j]);
  }
  for (std::size_t len = 2; len <= n; len <<= 1) {
    const double angle = (inverse ? 2.0 : -2.0) * kPi / static_cast<double>(len);
    const std::complex<double> step(std::cos(angle), std::sin(angle));
    for (std::size_t i = 0; i < n; i += len) {
      std::complex<double> w(1.0, 0.0);
      for (std::size_t k = 0; k < len / 2; ++k) {
        const auto u = data[i + k];
        const auto v = data[i + k + len / 2] * w;
        data[i + k] = u + v;
        data[i + k + len / 2] = u - v;
        // a fresh twiddle every 64 steps keeps the recurrence's rounding from growing over a long transform
        w = (k + 1) % 64 == 0 ? std::polar(1.0, angle * static_cast<double>(k + 1)) : w * step;
      }
    }
  }
}

std::vector<std::complex<double>> correlate(std::span<const float> signal, std::span<const float> ref) {
  return LegacyCorrelator(std::vector<float>(ref.begin(), ref.end()))(signal);
}

std::complex<double> interpolate(std::span<const std::complex<double>> corr, double lag) {
  const auto base = static_cast<long>(std::floor(lag));
  const auto size = static_cast<long>(corr.size());
  std::complex<double> sum(0, 0);
  for (long k = base - kSincHalf + 1; k <= base + kSincHalf; ++k) {
    if (k < 0 || k >= size)
      continue;
    const double x = lag - static_cast<double>(k);
    const double sinc = std::abs(x) < 1e-12 ? 1.0 : std::sin(kPi * x) / (kPi * x);
    // blackman over the half width
    const double u = x / static_cast<double>(kSincHalf);
    if (std::abs(u) >= 1)
      continue;
    const double window = 0.42 + 0.5 * std::cos(kPi * u) + 0.08 * std::cos(2 * kPi * u);
    sum += corr[static_cast<std::size_t>(k)] * (sinc * window);
  }
  return sum;
}

const char* token(ArrivalCode code) {
  switch (code) {
    case ArrivalCode::Ok:
      return "ok";
    case ArrivalCode::NotCovered:
      return "not_covered";
    case ArrivalCode::Weak:
      return "weak";
    case ArrivalCode::Unstable:
      return "unstable";
  }
  return "weak";
}

Arrival findArrival(std::span<const std::complex<double>> corr, double from, double to,
                    const DetectorConfig& config, std::optional<double> givenSigma) {
  Arrival out;
  if (from < 0 || to > static_cast<double>(corr.size()) || to - from < 3) {
    out.code = ArrivalCode::NotCovered;
    return out;
  }
  const auto first = static_cast<std::size_t>(std::ceil(from));
  const auto last = static_cast<std::size_t>(std::floor(to)) - 1;
  std::vector<double> envelope(last - first + 1);
  for (std::size_t k = first; k <= last; ++k)
    envelope[k - first] = std::abs(corr[k]);
  const double highest = *std::max_element(envelope.begin(), envelope.end());
  auto sorted = envelope;
  std::nth_element(sorted.begin(), sorted.begin() + static_cast<long>(sorted.size() / 2), sorted.end());
  const double sigma = givenSigma.value_or(sorted[sorted.size() / 2] / kRayleighMedian);

  // the direct path: the earliest envelope peak near the highest (a later, louder one is a reflection)
  std::size_t pick = 0;
  for (std::size_t i = 0; i < envelope.size(); ++i) {
    const bool peak = (i == 0 || envelope[i] >= envelope[i - 1]) &&
                      (i + 1 == envelope.size() || envelope[i] >= envelope[i + 1]);
    if (peak && envelope[i] >= config.earliestFraction * highest) {
      pick = i;
      break;
    }
  }
  const double peakLag = static_cast<double>(first + pick);
  out.psrDb = psrDb(envelope[pick], sigma);
  out.confidence = std::clamp((out.psrDb - config.zeroConfidenceDb) / (config.fullConfidenceDb - config.zeroConfidenceDb),
                              0.0, 1.0);
  if (out.psrDb < config.minPsrDb) {
    out.code = ArrivalCode::Weak;
    out.lag = peakLag;
    return out;
  }

  // the envelope's peak to a fraction of a lag: a fine grid over the neighbours by sinc interpolation, then a parabola
  // through the grid's best and its two neighbours
  const double lo = std::max(peakLag - 1.0, 0.0);
  const double hi = std::min(peakLag + 1.0, static_cast<double>(corr.size() - 1));
  const double step = 1.0 / kFineSteps;
  double bestLag = peakLag;
  double best = -1;
  for (double x = lo; x <= hi + 1e-9; x += step) {
    const double e = envelopeAt(corr, x);
    if (e > best) {
      best = e;
      bestLag = x;
    }
  }
  const double left = envelopeAt(corr, bestLag - step);
  const double right = envelopeAt(corr, bestLag + step);
  const double denominator = left - 2 * best + right;
  if (denominator < 0)
    bestLag += step * 0.5 * (left - right) / denominator;
  out.code = ArrivalCode::Ok;
  out.lag = bestLag;
  return out;
}

SampleClock fitSampleClock(std::span<const ReadStamp> stamps, double nominalRate, double maxPpm) {
  SampleClock clock;
  const double nominal = 1e9 / nominalRate;
  clock.nsPerFrame = nominal;
  if (stamps.empty())
    return clock;
  const double x0 = static_cast<double>(stamps.front().endFrame);
  const double y0 = static_cast<double>(stamps.front().monoNs);
  std::vector<std::pair<double, double>> points;
  points.reserve(stamps.size());
  for (const auto& s : stamps)
    points.emplace_back(static_cast<double>(s.endFrame) - x0, static_cast<double>(s.monoNs) - y0);
  std::sort(points.begin(), points.end());
  double slope = nominal;
  // a read returns late, never early: the line is the lower convex hull's edge over the stamps' mean frame, which
  // leaves the least total gap above it (a stall or a late wake never moves it)
  std::vector<std::pair<double, double>> hull;
  for (const auto& p : points) {
    while (hull.size() >= 2) {
      const auto& a = hull[hull.size() - 2];
      const auto& b = hull.back();
      const double cross = (b.first - a.first) * (p.second - a.second) - (b.second - a.second) * (p.first - a.first);
      if (cross > 0)
        break;
      hull.pop_back();
    }
    if (!hull.empty() && hull.back().first == p.first) {
      if (p.second < hull.back().second)
        hull.back() = p;
      continue;
    }
    hull.push_back(p);
  }
  if (hull.size() >= 2) {
    double mean = 0;
    for (const auto& p : points)
      mean += p.first;
    mean /= static_cast<double>(points.size());
    std::size_t edge = 0;
    while (edge + 2 < hull.size() && hull[edge + 1].first < mean)
      ++edge;
    const auto& a = hull[edge];
    const auto& b = hull[edge + 1];
    double candidate = (b.second - a.second) / (b.first - a.first);
    const double base = a.second - candidate * a.first;
    // two hull points can sit close together: the slope is refit by least squares over the tenth of the stamps
    // nearest that edge, whose own spread then averages out
    std::vector<std::pair<double, double>> gaps;
    for (const auto& p : points)
      gaps.emplace_back(p.second - (base + candidate * p.first), p.first);
    const auto keep = std::max<std::size_t>(points.size() / 10, std::min<std::size_t>(points.size(), 3));
    std::nth_element(gaps.begin(), gaps.begin() + static_cast<long>(keep - 1), gaps.end());
    double sx = 0, sy = 0;
    for (std::size_t i = 0; i < keep; ++i) {
      sx += gaps[i].second;
      sy += gaps[i].first;
    }
    const double mx = sx / static_cast<double>(keep), my = sy / static_cast<double>(keep);
    double sxx = 0, sxy = 0;
    for (std::size_t i = 0; i < keep; ++i) {
      sxx += (gaps[i].second - mx) * (gaps[i].second - mx);
      sxy += (gaps[i].second - mx) * (gaps[i].first - my);
    }
    if (sxx > 0)
      candidate += sxy / sxx;
    if (std::abs(candidate / nominal - 1.0) <= maxPpm * 1e-6)
      slope = candidate;
  }
  double intercept = points.front().second - slope * points.front().first;
  for (const auto& [x, y] : points)
    intercept = std::min(intercept, y - slope * x);
  clock.nsPerFrame = slope;
  clock.originNs = y0 + intercept - slope * x0;
  return clock;
}

namespace {

// copies frames [from, from + count) of a recording's channel as floats
using Reader = std::function<void(std::size_t channel, std::size_t from, std::size_t count, std::vector<float>& out)>;

// what one channel made of one round's window
struct Detected {
  ArrivalCode code = ArrivalCode::NotCovered;
  // arrival less the slot, leader ns
  double xNs = 0;
  double confidence = 0;
  // the wide window's floor, which the narrow pass reuses (a narrow window is all peak)
  std::optional<double> sigma;
};

// one round, its channels combined: the median of those that heard it
struct Round {
  ArrivalCode code = ArrivalCode::NotCovered;
  double xNs = 0;
  double confidence = 0;
};

Round combine(const std::vector<Detected>& channels) {
  Round out;
  std::vector<double> xs, confidences;
  bool weak = false;
  for (const auto& d : channels) {
    if (d.code == ArrivalCode::Ok) {
      xs.push_back(d.xNs);
      confidences.push_back(d.confidence);
    } else if (d.code == ArrivalCode::Weak) {
      weak = true;
      out.confidence = std::max(out.confidence, d.confidence);
    }
  }
  if (xs.empty()) {
    out.code = weak ? ArrivalCode::Weak : ArrivalCode::NotCovered;
    return out;
  }
  out.code = ArrivalCode::Ok;
  out.xNs = median(xs);
  out.confidence = median(confidences);
  return out;
}

double sampleStd(const std::vector<double>& v) {
  if (v.size() < 2)
    return 0;
  double mean = 0;
  for (auto x : v)
    mean += x;
  mean /= static_cast<double>(v.size());
  double sum = 0;
  for (auto x : v)
    sum += (x - mean) * (x - mean);
  return std::sqrt(sum / static_cast<double>(v.size() - 1));
}

std::vector<Heard> analyseWith(const Plan& plan, std::size_t frames, std::size_t channels, const Reader& read,
                               double rate, const SampleClock& clock,
                               const std::function<double(double localMonoNs)>& toLeader,
                               const AnalysisConfig& config) {
  const auto n = plan.members.size();
  std::vector<Heard> out(n);
  const std::size_t rounds = std::max<std::uint32_t>(plan.rounds, 1);
  if (n == 0 || channels == 0 || plan.slotNs.size() < rounds * n)
    return out;
  LegacyCorrelator correlator(chirpSamples(plan.chirp, rate));
  const auto refSize = correlator.refSize();
  // the leader's time back on the local clock: the mapping's slope is within ppm of one, so a few steps converge
  auto toLocal = [&](double leaderNs) {
    double local = leaderNs;
    for (int i = 0; i < 4; ++i)
      local += leaderNs - toLeader(local);
    return local;
  };
  // the sinc interpolation and the fine search reach this many lags past a window's ends
  constexpr double kMargin = 20;
  std::vector<float> segment;
  // channel ch's arrival within [fromNs, toNs) (leader time) of the slot at slot: only that window is correlated
  auto detect = [&](std::size_t ch, double slot, double fromNs, double toNs, std::optional<double> sigma) {
    Detected d;
    const double from = clock.frameAt(toLocal(fromNs));
    const double to = clock.frameAt(toLocal(toNs));
    const double first = std::floor(from - kMargin);
    const double last = std::ceil(to + kMargin) + static_cast<double>(refSize);
    if (first < 0 || last > static_cast<double>(frames) || to - from < 3)
      return d;
    const auto s0 = static_cast<std::size_t>(first);
    read(ch, s0, static_cast<std::size_t>(last) - s0, segment);
    const auto corr = correlator(segment);
    const auto a = findArrival(corr, from - first, to - first, config.detector, sigma);
    d.code = a.code;
    d.confidence = a.confidence;
    d.sigma = sigma;
    if (!sigma && a.code != ArrivalCode::NotCovered) {
      // the window's floor, as findArrival took it
      std::vector<double> envelope;
      const auto lo = static_cast<std::size_t>(std::ceil(from - first));
      const auto hi = std::min(static_cast<std::size_t>(std::floor(to - first)), corr.size());
      for (std::size_t k = lo; k < hi; ++k)
        envelope.push_back(std::abs(corr[k]));
      if (!envelope.empty()) {
        std::nth_element(envelope.begin(), envelope.begin() + static_cast<long>(envelope.size() / 2), envelope.end());
        d.sigma = envelope[envelope.size() / 2] / kRayleighMedian;
      }
    }
    if (a.code == ArrivalCode::Ok)
      d.xNs = toLeader(clock.monoAt(first + a.lag)) - slot;
    return d;
  };

  for (std::size_t i = 0; i < n; ++i) {
    // pass 1: each round's wide window, every channel
    std::vector<std::vector<Detected>> wide(rounds, std::vector<Detected>(channels));
    std::vector<Round> firstPass(rounds);
    std::vector<double> xs;
    for (std::size_t r = 0; r < rounds; ++r) {
      const auto slot = static_cast<double>(plan.slot(r, i));
      for (std::size_t ch = 0; ch < channels; ++ch)
        wide[r][ch] = detect(ch, slot, slot - static_cast<double>(plan.searchBeforeNs),
                             slot + static_cast<double>(plan.searchAfterNs), std::nullopt);
      firstPass[r] = combine(wide[r]);
      if (firstPass[r].code == ArrivalCode::Ok)
        xs.push_back(firstPass[r].xNs);
    }
    auto& heard = out[i];
    if (xs.empty()) {
      const bool weak = std::any_of(firstPass.begin(), firstPass.end(),
                                    [](const Round& r) { return r.code == ArrivalCode::Weak; });
      heard.code = weak ? ArrivalCode::Weak : ArrivalCode::NotCovered;
      for (const auto& r : firstPass)
        heard.confidence = std::max(heard.confidence, r.confidence);
      continue;
    }
    // pass 2: every round again in a narrow window around the rounds' median, so a round whose louder reflection won
    // its wide window takes its direct path there
    const double m = median(xs);
    std::vector<Round> secondPass(rounds);
    std::vector<double> narrow;
    for (std::size_t r = 0; r < rounds; ++r) {
      const auto slot = static_cast<double>(plan.slot(r, i));
      std::vector<Detected> heardBy(channels);
      for (std::size_t ch = 0; ch < channels; ++ch) {
        if (!wide[r][ch].sigma)
          continue;
        heardBy[ch] = detect(ch, slot, slot + m - static_cast<double>(config.narrowNs),
                             slot + m + static_cast<double>(config.narrowNs), wide[r][ch].sigma);
      }
      secondPass[r] = combine(heardBy);
      if (secondPass[r].code == ArrivalCode::Ok)
        narrow.push_back(secondPass[r].xNs);
    }
    // the outlier gate around the narrow pass's median
    const double centre = median(narrow);
    std::vector<double> kept, confidences;
    for (const auto& r : secondPass)
      if (r.code == ArrivalCode::Ok && std::abs(r.xNs - centre) <= static_cast<double>(config.gateNs)) {
        kept.push_back(r.xNs);
        confidences.push_back(r.confidence);
      }
    const auto minRounds = std::max<std::size_t>(1, rounds - 1);
    heard.rounds = static_cast<std::uint32_t>(kept.size());
    heard.spreadNs = sampleStd(kept);
    if (kept.empty()) {
      std::vector<double> c;
      for (const auto& r : firstPass)
        if (r.code == ArrivalCode::Ok)
          c.push_back(r.confidence);
      heard.code = ArrivalCode::Unstable;
      heard.tNs = std::llround(static_cast<double>(plan.slot(0, i)) + m);
      heard.confidence = median(c);
      continue;
    }
    heard.tNs = std::llround(static_cast<double>(plan.slot(0, i)) + median(kept));
    heard.confidence = median(confidences);
    heard.code = kept.size() >= minRounds && heard.spreadNs <= config.maxSpreadNs ? ArrivalCode::Ok
                                                                                 : ArrivalCode::Unstable;
  }
  return out;
}

}  // namespace

std::vector<Heard> analyse(const Plan& plan, std::span<const std::int16_t> interleaved, std::size_t channels,
                           double rate, const SampleClock& clock,
                           const std::function<double(double localMonoNs)>& toLeader, const AnalysisConfig& config) {
  if (channels == 0)
    return std::vector<Heard>(plan.members.size());
  const auto frames = interleaved.size() / channels;
  const Reader read = [&](std::size_t ch, std::size_t from, std::size_t count, std::vector<float>& out) {
    out.resize(count);
    for (std::size_t k = 0; k < count; ++k)
      out[k] = static_cast<float>(interleaved[(from + k) * channels + ch]) / 32768.0f;
  };
  return analyseWith(plan, frames, channels, read, rate, clock, toLeader, config);
}

std::vector<Heard> analyse(const Plan& plan, std::span<const float> pcm, double rate, const SampleClock& clock,
                           const std::function<double(double localMonoNs)>& toLeader, const AnalysisConfig& config) {
  const Reader read = [&](std::size_t, std::size_t from, std::size_t count, std::vector<float>& out) {
    out.assign(pcm.begin() + static_cast<long>(from), pcm.begin() + static_cast<long>(from + count));
  };
  return analyseWith(plan, pcm.size(), 1, read, rate, clock, toLeader, config);
}

const char* token(ResultCode code) {
  switch (code) {
    case ResultCode::Ok:
      return "ok";
    case ResultCode::Incomplete:
      return "incomplete";
    case ResultCode::Inconsistent:
      return "inconsistent";
    case ResultCode::TooFew:
      return "too_few";
  }
  return "incomplete";
}

const char* token(MemberCode code) {
  switch (code) {
    case MemberCode::Ok:
      return "ok";
    case MemberCode::NoSelf:
      return "no_self";
    case MemberCode::Isolated:
      return "isolated";
    case MemberCode::Unstable:
      return "unstable";
  }
  return "isolated";
}

Solution solve(const Plan& plan, const std::vector<std::vector<Heard>>& heard, const SolverConfig& config) {
  Solution result;
  const auto n = plan.members.size();
  result.members.resize(n);
  for (std::size_t i = 0; i < n; ++i)
    result.members[i].id = plan.members[i];
  if (n < 2) {
    result.code = ResultCode::TooFew;
    return result;
  }
  auto at = [&](std::size_t j, std::size_t i) -> const Heard* {
    if (j >= heard.size() || i >= heard[j].size() || heard[j][i].code != ArrivalCode::Ok)
      return nullptr;
    return &heard[j][i];
  };
  auto unstable = [&](std::size_t j, std::size_t i) {
    return j < heard.size() && i < heard[j].size() && heard[j][i].code == ArrivalCode::Unstable;
  };
  // a member heard, but only by arrivals whose rounds disagreed, is unstable rather than isolated
  auto isolated = [&](std::size_t i) {
    for (std::size_t j = 0; j < n; ++j)
      if (j != i && (unstable(j, i) || unstable(i, j)))
        return MemberCode::Unstable;
    return MemberCode::Isolated;
  };
  std::vector<bool> self(n);
  for (std::size_t i = 0; i < n; ++i) {
    self[i] = at(i, i) != nullptr;
    if (!self[i])
      result.members[i].code = unstable(i, i) ? MemberCode::Unstable : MemberCode::NoSelf;
  }
  struct Pair {
    std::size_t i, j;
    double d, w;
  };
  std::vector<Pair> pairs;
  for (std::size_t i = 0; i < n; ++i)
    for (std::size_t j = i + 1; j < n; ++j) {
      const auto* ij = at(j, i);
      const auto* ji = at(i, j);
      if (!self[i] || !self[j] || ij == nullptr || ji == nullptr)
        continue;
      const double aij = static_cast<double>(ij->tNs - plan.slotNs[i]);
      const double aji = static_cast<double>(ji->tNs - plan.slotNs[j]);
      const double si = static_cast<double>(at(i, i)->tNs - plan.slotNs[i]);
      const double sj = static_cast<double>(at(j, j)->tNs - plan.slotNs[j]);
      const double w = std::max(1e-3, std::min({ij->confidence, ji->confidence, at(i, i)->confidence,
                                                at(j, j)->confidence}));
      pairs.push_back({i, j, ((aij - aji) + (si - sj)) / 2.0, w});
    }

  // the largest set the pairs link, the earliest member's on a tie (the leader's when it is in one)
  std::vector<int> component(n, -1);
  int components = 0;
  for (std::size_t s = 0; s < n; ++s) {
    if (component[s] >= 0 || !self[s])
      continue;
    std::vector<std::size_t> stack{s};
    component[s] = components;
    while (!stack.empty()) {
      const auto u = stack.back();
      stack.pop_back();
      for (const auto& p : pairs) {
        const auto v = p.i == u ? p.j : p.j == u ? p.i : n;
        if (v < n && component[v] < 0) {
          component[v] = components;
          stack.push_back(v);
        }
      }
    }
    ++components;
  }
  int chosen = -1;
  std::size_t chosenSize = 0;
  for (int c = 0; c < components; ++c) {
    const auto size = static_cast<std::size_t>(std::count(component.begin(), component.end(), c));
    if (size > chosenSize) {
      chosen = c;
      chosenSize = size;
    }
  }
  if (chosenSize < 2) {
    for (std::size_t i = 0; i < n; ++i)
      if (self[i])
        result.members[i].code = isolated(i);
    result.code = ResultCode::Incomplete;
    return result;
  }
  std::vector<std::size_t> nodes;
  std::vector<int> index(n, -1);
  for (std::size_t i = 0; i < n; ++i) {
    if (component[i] == chosen) {
      index[i] = static_cast<int>(nodes.size());
      nodes.push_back(i);
    } else if (self[i]) {
      result.members[i].code = isolated(i);
    }
  }

  // weighted least squares of x_i - x_j = d_ij with sum x = 0: (L + 1 1^T) x = b, L the weighted laplacian
  const auto m = nodes.size();
  std::vector<std::vector<double>> a(m, std::vector<double>(m + 1, 1.0));
  for (auto& row : a)
    row[m] = 0;
  for (const auto& p : pairs) {
    if (index[p.i] < 0 || index[p.j] < 0)
      continue;
    const auto u = static_cast<std::size_t>(index[p.i]);
    const auto v = static_cast<std::size_t>(index[p.j]);
    a[u][u] += p.w;
    a[v][v] += p.w;
    a[u][v] -= p.w;
    a[v][u] -= p.w;
    a[u][m] += p.w * p.d;
    a[v][m] -= p.w * p.d;
  }
  for (std::size_t col = 0; col < m; ++col) {
    std::size_t pivot = col;
    for (std::size_t r = col + 1; r < m; ++r)
      if (std::abs(a[r][col]) > std::abs(a[pivot][col]))
        pivot = r;
    std::swap(a[col], a[pivot]);
    for (std::size_t r = 0; r < m; ++r) {
      if (r == col || a[col][col] == 0)
        continue;
      const double f = a[r][col] / a[col][col];
      for (std::size_t c = col; c <= m; ++c)
        a[r][c] -= f * a[col][c];
    }
  }
  std::vector<double> x(m);
  for (std::size_t r = 0; r < m; ++r)
    x[r] = a[r][r] != 0 ? a[r][m] / a[r][r] : 0;

  double sum = 0, weights = 0;
  std::vector<double> best(m, 0);
  for (const auto& p : pairs) {
    if (index[p.i] < 0 || index[p.j] < 0)
      continue;
    const auto u = static_cast<std::size_t>(index[p.i]);
    const auto v = static_cast<std::size_t>(index[p.j]);
    const double e = x[u] - x[v] - p.d;
    sum += p.w * e * e;
    weights += p.w;
    best[u] = std::max(best[u], p.w);
    best[v] = std::max(best[v], p.w);
    // the four arrivals the pair rests on: the largest spread and the fewest rounds among them
    for (const auto* h : {at(p.j, p.i), at(p.i, p.j), at(p.i, p.i), at(p.j, p.j)})
      for (const auto k : {p.i, p.j}) {
        auto& member = result.members[k];
        member.spreadNs = std::max(member.spreadNs, h->spreadNs);
        member.rounds = member.rounds == 0 ? h->rounds : std::min(member.rounds, h->rounds);
      }
  }
  result.residualNs = weights > 0 ? std::sqrt(sum / weights) : 0;
  for (std::size_t k = 0; k < m; ++k) {
    auto& member = result.members[nodes[k]];
    member.offsetNs = x[k];
    member.confidence = std::min(best[k], at(nodes[k], nodes[k])->confidence);
  }
  const bool all = std::all_of(result.members.begin(), result.members.end(),
                               [](const MemberOffset& o) { return o.code == MemberCode::Ok; });
  result.code = !all                                        ? ResultCode::Incomplete
                : result.residualNs > config.maxResidualNs ? ResultCode::Inconsistent
                                                            : ResultCode::Ok;
  return result;
}

}  // namespace cadence::sync::calibration
