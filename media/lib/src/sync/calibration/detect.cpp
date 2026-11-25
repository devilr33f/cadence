#include "sync/calibration/detect.h"

#include <algorithm>
#include <cmath>
#include <numbers>

namespace cadence::sync::calibration {

namespace {

constexpr int kSincHalf = 16;
constexpr int kFineSteps = 32;
constexpr double kRayleighMedian = 1.1774100225154747;

std::size_t nextPow2(std::size_t n) {
  std::size_t out = 1;
  while (out < n)
    out <<= 1;
  return out;
}

double psrDb(double peak, double sigma) {
  if (sigma <= 0)
    return peak > 0 ? 200 : 0;
  return std::min(200.0, 20.0 * std::log10(peak / sigma));
}

float envelopeAt(std::span<const std::complex<float>> corr, double lag) {
  return std::abs(interpolate(corr, lag));
}

}  // namespace

const char* token(ArrivalCodeV2 code) {
  switch (code) {
    case ArrivalCodeV2::Ok:
      return "ok";
    case ArrivalCodeV2::NotCovered:
      return "not_covered";
    case ArrivalCodeV2::Weak:
      return "weak";
    case ArrivalCodeV2::Unstable:
      return "unstable";
    case ArrivalCodeV2::Missed:
      return "missed";
    case ArrivalCodeV2::Unsupported:
      return "unsupported";
  }
  return "missed";
}

Decimated decimate(std::span<const float> pcm, double rate) {
  const bool integer = std::abs(rate - 32000) < 0.01 || std::abs(rate - 48000) < 0.01;
  if (rate <= 16000 || !integer)
    return {std::vector<float>(pcm.begin(), pcm.end()), rate, 1};
  const auto factor = static_cast<std::size_t>(std::lround(rate / 16000));
  const auto half = static_cast<long>(4 * factor);
  std::vector<float> taps(static_cast<std::size_t>(2 * half + 1));
  const double cutoff = 0.45 / static_cast<double>(factor);
  double sum = 0;
  for (long k = -half; k <= half; ++k) {
    const double x = cutoff * static_cast<double>(k);
    const double sinc = std::abs(x) < 1e-12 ? 1.0 : std::sin(std::numbers::pi * x) / (std::numbers::pi * x);
    const double window = 0.54 + 0.46 * std::cos(std::numbers::pi * static_cast<double>(k) / static_cast<double>(half));
    const double h = cutoff * sinc * window;
    taps[static_cast<std::size_t>(k + half)] = static_cast<float>(h);
    sum += h;
  }
  for (auto& h : taps)
    h = static_cast<float>(h / sum);
  Decimated out;
  out.rate = 16000;
  out.stride = static_cast<double>(factor);
  out.pcm.resize(pcm.size() / factor);
  for (std::size_t n = 0; n < out.pcm.size(); ++n) {
    const auto centre = static_cast<long>(n * factor);
    float sample = 0;
    for (long k = -half; k <= half; ++k) {
      const auto at = centre + k;
      if (at >= 0 && at < static_cast<long>(pcm.size()))
        sample += taps[static_cast<std::size_t>(k + half)] * pcm[static_cast<std::size_t>(at)];
    }
    out.pcm[n] = sample;
  }
  return out;
}

Correlator::Correlator(std::vector<float> ref) : ref_(std::move(ref)) {}

std::vector<std::complex<float>> Correlator::operator()(std::span<const float> signal) {
  if (ref_.empty() || ref_.size() > signal.size())
    return {};
  const auto n = nextPow2(signal.size() + ref_.size());
  auto& r = spectra_[n];
  if (r.empty()) {
    r.assign(n, {});
    for (std::size_t k = 0; k < ref_.size(); ++k)
      r[k] = ref_[k];
    fft_(r, false);
  }
  std::vector<std::complex<float>> s(n);
  for (std::size_t k = 0; k < signal.size(); ++k)
    s[k] = signal[k];
  fft_(s, false);
  for (std::size_t k = 0; k < n; ++k) {
    const auto x = s[k] * std::conj(r[k]);
    if (k == 0 || k == n / 2)
      s[k] = x;
    else if (k < n / 2)
      s[k] = 2.0f * x;
    else
      s[k] = 0;
  }
  fft_(s, true);
  s.resize(signal.size() - ref_.size() + 1);
  const float scale = 1.0f / static_cast<float>(n);
  for (auto& v : s)
    v *= scale;
  return s;
}

Baseband downconvert(std::span<const float> pcm, double rate, double carrierHz) {
  if (std::abs(rate - 48000) > 0.01)
    return {};
  constexpr std::size_t factor = 6;
  constexpr long half = 48;
  // 900 hz effective cutoff keeps the code's central correlation lobe and rejects music mixed down from 16 khz
  const double cutoff = 1800.0 / rate;
  std::vector<double> taps(2 * half + 1);
  double sum = 0;
  for (long k = -half; k <= half; ++k) {
    const double x = cutoff * static_cast<double>(k);
    const double sinc = std::abs(x) < 1e-12 ? 1.0 : std::sin(std::numbers::pi * x) / (std::numbers::pi * x);
    const double window = 0.42 + 0.5 * std::cos(std::numbers::pi * static_cast<double>(k) / half) +
                          0.08 * std::cos(2 * std::numbers::pi * static_cast<double>(k) / half);
    const double h = cutoff * sinc * window;
    taps[static_cast<std::size_t>(k + half)] = h;
    sum += h;
  }
  for (auto& h : taps)
    h /= sum;
  Baseband out;
  out.rate = 8000;
  out.stride = factor;
  out.pcm.resize(pcm.size() / factor);
  const double phase = 2.0 * std::numbers::pi * carrierHz / rate;
  for (std::size_t n = 0; n < out.pcm.size(); ++n) {
    const auto centre = static_cast<long>(n * factor);
    std::complex<double> sample;
    for (long k = -half; k <= half; ++k) {
      const auto at = centre + k;
      if (at < 0 || at >= static_cast<long>(pcm.size()))
        continue;
      const auto oscillator = std::polar(1.0, -phase * static_cast<double>(at));
      sample += taps[static_cast<std::size_t>(k + half)] * static_cast<double>(pcm[static_cast<std::size_t>(at)]) *
                oscillator;
    }
    out.pcm[n] = static_cast<std::complex<float>>(sample);
  }
  return out;
}

ComplexCorrelator::ComplexCorrelator(std::vector<std::complex<float>> ref) : ref_(std::move(ref)) {}

std::vector<std::complex<float>> ComplexCorrelator::operator()(std::span<const std::complex<float>> signal) {
  if (ref_.empty() || ref_.size() > signal.size())
    return {};
  const auto n = nextPow2(signal.size() + ref_.size());
  auto& r = spectra_[n];
  if (r.empty()) {
    r.assign(n, {});
    std::copy(ref_.begin(), ref_.end(), r.begin());
    fft_(r, false);
  }
  std::vector<std::complex<float>> s(n);
  std::copy(signal.begin(), signal.end(), s.begin());
  fft_(s, false);
  for (std::size_t k = 0; k < n; ++k)
    s[k] *= std::conj(r[k]);
  fft_(s, true);
  s.resize(signal.size() - ref_.size() + 1);
  const float scale = 1.0f / static_cast<float>(n);
  for (auto& v : s)
    v *= scale;
  return s;
}

std::vector<std::complex<float>> basebandReference(const ProbeShape& shape, std::uint32_t code) {
  const auto passband = probeSamples(shape, code, 48000);
  return downconvert(passband, 48000, (shape.bandLoHz + shape.bandHiHz) / 2.0).pcm;
}

std::complex<float> interpolate(std::span<const std::complex<float>> corr, double lag) {  const auto base = static_cast<long>(std::floor(lag));
  std::complex<float> sum(0, 0);
  for (long k = base - kSincHalf + 1; k <= base + kSincHalf; ++k) {
    if (k < 0 || k >= static_cast<long>(corr.size()))
      continue;
    const double x = lag - static_cast<double>(k);
    const double sinc = std::abs(x) < 1e-12 ? 1.0 : std::sin(std::numbers::pi * x) / (std::numbers::pi * x);
    const double u = x / static_cast<double>(kSincHalf);
    if (std::abs(u) >= 1)
      continue;
    const double window = 0.42 + 0.5 * std::cos(std::numbers::pi * u) + 0.08 * std::cos(2 * std::numbers::pi * u);
    sum += corr[static_cast<std::size_t>(k)] * static_cast<float>(sinc * window);
  }
  return sum;
}

std::vector<Peak> findCandidates(std::span<const std::complex<float>> corr, double from, double to, double sigma,
                                 const DetectorConfig& config) {
  std::vector<Peak> out;
  if (from < 0 || to > static_cast<double>(corr.size()) || to - from < 3)
    return out;
  const auto first = static_cast<std::size_t>(std::ceil(from));
  const auto last = static_cast<std::size_t>(std::floor(to)) - 1;
  std::vector<float> envelope;
  envelope.reserve(last - first + 1);
  for (std::size_t k = first; k <= last; ++k)
    envelope.push_back(std::abs(corr[k]));
  if (sigma <= 0) {
    auto sorted = envelope;
    std::nth_element(sorted.begin(), sorted.begin() + static_cast<long>(sorted.size() / 2), sorted.end());
    sigma = sorted[sorted.size() / 2] / kRayleighMedian;
  }
  for (std::size_t i = 0; i < envelope.size(); ++i) {
    const bool peak = (i == 0 || envelope[i] >= envelope[i - 1]) &&
                      (i + 1 == envelope.size() || envelope[i] >= envelope[i + 1]);
    const double psr = psrDb(envelope[i], sigma);
    if (!peak || psr < config.minPsrDb)
      continue;
    const double centre = static_cast<double>(first + i);
    const double lo = std::max(centre - 1.0, 0.0);
    const double hi = std::min(centre + 1.0, static_cast<double>(corr.size() - 1));
    constexpr double step = 1.0 / kFineSteps;
    double bestLag = centre;
    float best = -1;
    for (double x = lo; x <= hi + 1e-9; x += step) {
      const float e = envelopeAt(corr, x);
      if (e > best) {
        best = e;
        bestLag = x;
      }
    }
    const float left = envelopeAt(corr, bestLag - step);
    const float right = envelopeAt(corr, bestLag + step);
    const double denominator = static_cast<double>(left) - 2.0 * best + right;
    if (denominator < 0)
      bestLag += step * 0.5 * (left - right) / denominator;
    out.push_back({bestLag, best, psr});
  }
  return out;
}

bool passesIdentity(std::span<const std::vector<std::complex<float>>> correlations, std::size_t expected,
                    const Peak& candidate, double radiusLags, double marginDb, double coverLags) {
  if (expected >= correlations.size())
    return false;
  const double threshold = candidate.envelope * std::pow(10.0, -marginDb / 20.0);
  for (std::size_t other = 0; other < correlations.size(); ++other) {
    if (other == expected)
      continue;
    const auto& corr = correlations[other];
    const auto from = std::max<long>(0, static_cast<long>(std::ceil(candidate.lag - radiusLags)));
    const auto to = std::min<long>(static_cast<long>(corr.size()),
                                   static_cast<long>(std::floor(candidate.lag + radiusLags)) + 1);
    for (auto lag = from; lag < to; ++lag)
      if (std::abs(corr[static_cast<std::size_t>(lag)]) > threshold)
        return false;
    if (coverLags > radiusLags) {
      // a loud code's cross-correlation sidelobe need not sit at its main peak; a stronger other-code probe covering
      // this candidate still owns it
      const auto coverFrom = std::max<long>(0, static_cast<long>(std::ceil(candidate.lag - coverLags)));
      const auto coverTo = std::min<long>(static_cast<long>(corr.size()),
                                          static_cast<long>(std::floor(candidate.lag + coverLags)) + 1);
      for (auto lag = coverFrom; lag < coverTo; ++lag)
        if (std::abs(corr[static_cast<std::size_t>(lag)]) > threshold)
          return false;
    }
  }
  return true;
}

}  // namespace cadence::sync::calibration
