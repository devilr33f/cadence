#include "sync/calibration/estimate.h"

#include <algorithm>
#include <cmath>
#include <numeric>

namespace cadence::sync::calibration {

namespace {

struct Observation {
  double xNs = 0;
  double envelope = 0;
  double psrDb = 0;
  std::size_t round = 0;
  std::size_t channel = 0;
};

struct Cluster {
  std::vector<std::size_t> observations;
  double meanNs = 0;
  double meanEnvelope = 0;
  std::size_t rounds = 0;
};

struct Estimate {
  HeardV2 heard;
  std::vector<double> roundNs;
  std::vector<bool> roundKeep;
};

double median(std::vector<double> values) {
  if (values.empty())
    return 0;
  std::sort(values.begin(), values.end());
  const auto mid = values.size() / 2;
  return values.size() % 2 == 1 ? values[mid] : 0.5 * (values[mid - 1] + values[mid]);
}

double madSigma(std::span<const double> values, double centre) {
  std::vector<double> deviations;
  deviations.reserve(values.size());
  for (const auto value : values)
    deviations.push_back(std::abs(value - centre));
  return 1.4826 * median(std::move(deviations));
}

double confidence(double psr, const DetectorConfig& config) {
  return std::clamp((psr - config.zeroConfidenceDb) / (config.fullConfidenceDb - config.zeroConfidenceDb), 0.0, 1.0);
}

std::vector<Cluster> clusters(std::vector<Observation>& observations, double radiusNs, std::size_t rounds) {
  std::sort(observations.begin(), observations.end(), [](const auto& a, const auto& b) { return a.xNs < b.xNs; });
  std::vector<Cluster> out;
  for (std::size_t k = 0; k < observations.size(); ++k) {
    const auto& observation = observations[k];
    if (out.empty() || std::abs(observation.xNs - out.back().meanNs) > radiusNs) {
      out.push_back({});
      out.back().meanNs = observation.xNs;
      out.back().meanEnvelope = observation.envelope;
    } else {
      auto& cluster = out.back();
      const auto count = static_cast<double>(cluster.observations.size());
      cluster.meanNs = (cluster.meanNs * count + observation.xNs) / (count + 1);
      cluster.meanEnvelope = (cluster.meanEnvelope * count + observation.envelope) / (count + 1);
    }
    out.back().observations.push_back(k);
  }
  for (auto& cluster : out) {
    std::vector<bool> seen(rounds);
    for (const auto index : cluster.observations) {
      const auto round = observations[index].round;
      if (round < seen.size() && !seen[round]) {
        seen[round] = true;
        ++cluster.rounds;
      }
    }
  }
  return out;
}

Estimate combineSource(const Plan& plan, std::size_t source, const std::vector<Observation>& candidates,
                       bool covered, const AnalysisConfig& config) {
  Estimate out;
  out.roundNs.resize(plan.rounds);
  out.roundKeep.resize(plan.rounds);
  const auto minDirect = std::max<std::size_t>((static_cast<std::size_t>(plan.rounds) + 1) / 2, 1);
  const auto minKept = plan.rounds == 1 ? 1 : std::max<std::size_t>(minDirect, 2);
  if (candidates.empty()) {
    out.heard.code = covered ? ArrivalCodeV2::Missed : ArrivalCodeV2::NotCovered;
    return out;
  }

  auto sorted = candidates;
  auto groups = clusters(sorted, config.clusterNs, plan.rounds);
  double strongest = 0;
  for (const auto& group : groups)
    strongest = std::max(strongest, group.meanEnvelope);
  const Cluster* direct = nullptr;
  for (const auto& group : groups) {
    if (group.rounds >= minDirect && group.meanEnvelope >= config.minEnvelopeFraction * strongest) {
      direct = &group;
      break;
    }
  }
  if (direct == nullptr) {
    out.heard.code = ArrivalCodeV2::Unstable;
    return out;
  }

  std::vector<double> confidences(plan.rounds);
  std::vector<bool> present(plan.rounds);
  for (std::size_t r = 0; r < plan.rounds; ++r) {
    std::vector<double> xs, psrs, envelopes;
    std::vector<std::size_t> channels;
    for (const auto index : direct->observations) {
      const auto& observation = sorted[index];
      if (observation.round != r)
        continue;
      auto found = std::find(channels.begin(), channels.end(), observation.channel);
      if (found == channels.end()) {
        channels.push_back(observation.channel);
        xs.push_back(observation.xNs);
        psrs.push_back(observation.psrDb);
        envelopes.push_back(observation.envelope);
      } else {
        const auto at = static_cast<std::size_t>(found - channels.begin());
        // one voice per microphone: a near-in sidelobe must not outweigh the other microphones' main peaks
        if (observation.envelope > envelopes[at]) {
          xs[at] = observation.xNs;
          psrs[at] = observation.psrDb;
          envelopes[at] = observation.envelope;
        }
      }
    }
    if (!xs.empty()) {
      present[r] = true;
      out.roundNs[r] = median(std::move(xs));
      confidences[r] = confidence(median(std::move(psrs)), config.detector);
    }
  }
  std::vector<double> all;
  for (std::size_t r = 0; r < plan.rounds; ++r)
    if (present[r])
      all.push_back(out.roundNs[r]);
  const double centre = median(all);
  const double gate = std::max(3.0 * madSigma(all, centre), 300'000.0);
  std::vector<double> kept, keptConfidence;
  for (std::size_t r = 0; r < plan.rounds; ++r) {
    if (present[r] && std::abs(out.roundNs[r] - centre) <= gate) {
      out.roundKeep[r] = true;
      kept.push_back(out.roundNs[r]);
      keptConfidence.push_back(confidences[r]);
    }
  }
  const double absolute = median(kept);
  const double sigma = madSigma(kept, absolute);
  out.heard.tNs = std::llround(static_cast<double>(plan.slot(0, source)) + absolute);
  out.heard.confidence = median(std::move(keptConfidence));
  out.heard.rounds = static_cast<std::uint32_t>(kept.size());
  out.heard.spreadNs = sigma;
  out.heard.sigmaNs = sigma;
  out.heard.ci95Ns = kept.empty() ? 0 : 1.96 * std::max(sigma, 10'000.0) / std::sqrt(static_cast<double>(kept.size()));
  out.heard.code = kept.size() >= minKept && out.heard.ci95Ns <= config.toleranceNs ? ArrivalCodeV2::Ok
                                                                                     : ArrivalCodeV2::Unstable;
  return out;
}

using Reader = std::function<void(std::size_t, std::vector<float>&)>;

std::vector<HeardV2> analyseWith(const Plan& plan, std::size_t channels, const Reader& read, double rate,
                                 std::size_t selfIndex, const SampleClock& clock,
                                 const std::function<double(double)>& toLeader, const AnalysisConfig& config) {
  const auto sources = plan.members.size();
  std::vector<HeardV2> result(sources);
  if (sources == 0 || channels == 0 || plan.slotNs.size() < static_cast<std::size_t>(plan.rounds) * sources)
    return result;
  auto toLocal = [&](double leaderNs) {
    double local = leaderNs;
    for (int k = 0; k < 4; ++k)
      local += leaderNs - toLeader(local);
    return local;
  };

  const bool ultrasonic = calibration::ultrasonic(plan.probe);
  if (ultrasonic && std::abs(rate - 48000) > 0.01) {
    std::vector<HeardV2> unsupported(sources);
    for (auto& heard : unsupported)
      heard.code = ArrivalCodeV2::Unsupported;
    return unsupported;
  }

  std::vector<Correlator> correlators;
  std::vector<ComplexCorrelator> complexCorrelators;
  if (ultrasonic) {
    complexCorrelators.reserve(sources);
    for (std::size_t i = 0; i < sources; ++i)
      complexCorrelators.emplace_back(basebandReference(plan.probe, plan.probeCode(i)));
  } else {
    correlators.reserve(sources);
    for (std::size_t i = 0; i < sources; ++i)
      correlators.emplace_back(probeSamples(plan.probe, plan.probeCode(i), 16000));
  }

  std::vector<double> quality(channels);
  std::vector<std::vector<std::vector<Observation>>> observations(
      channels, std::vector<std::vector<Observation>>(sources));
  std::vector<std::vector<bool>> covered(sources, std::vector<bool>(plan.rounds));
  std::vector<float> channel;
  std::size_t currentChannel = 0;
  auto detect = [&](auto& detectors, const auto& decimated) {
    for (std::size_t i = 0; i < sources; ++i) {
      if (plan.skipped(i))
        continue;
      for (std::size_t r = 0; r < plan.rounds; ++r) {
        const double from = clock.frameAt(toLocal(static_cast<double>(plan.sourceWindowFromNs(r, i)))) / decimated.stride;
        const double to = clock.frameAt(toLocal(static_cast<double>(plan.sourceWindowToNs(r, i)))) / decimated.stride;
        constexpr double kMargin = 20;
        const double first = std::floor(from) - kMargin;
        const double last = std::ceil(to) + kMargin + static_cast<double>(detectors[i].refSize());
        if (first < 0 || last > static_cast<double>(decimated.pcm.size()) || to - from < 3)
          continue;
        covered[i][r] = true;
        const auto begin = static_cast<std::size_t>(first);
        const auto end = static_cast<std::size_t>(last);
        const std::span segment(decimated.pcm.data() + begin, end - begin);
        std::vector<std::vector<std::complex<float>>> correlations(sources);
        correlations[i] = detectors[i](segment);
        const auto candidates = findCandidates(correlations[i], from - first, to - first, 0, config.detector);
        for (const auto& candidate : candidates) {
          const double frame = (first + candidate.lag) * decimated.stride;
          const double arrivalNs = toLeader(clock.monoAt(frame));
          if (plan.probe.kind == ProbeKind::Gold) {
            for (std::size_t other = 0; other < sources; ++other)
              if (other != i && correlations[other].empty())
                correlations[other] = detectors[other](segment);
            if (!passesIdentity(correlations, i, candidate, 0.001 * decimated.rate, config.identityMarginDb,
                                probeDuration(plan.probe) * decimated.rate))
              continue;
          }
          const double xNs = arrivalNs - static_cast<double>(plan.slot(r, i));
          observations[currentChannel][i].push_back({xNs, candidate.envelope, candidate.psrDb, r, currentChannel});
          quality[currentChannel] = std::max(quality[currentChannel], candidate.psrDb);
        }
      }
    }
  };
  for (std::size_t ch = 0; ch < channels; ++ch) {
    read(ch, channel);
    currentChannel = ch;
    if (ultrasonic)
      detect(complexCorrelators,
             downconvert(channel, rate, (plan.probe.bandLoHz + plan.probe.bandHiHz) / 2.0));
    else
      detect(correlators, decimate(channel, rate));
  }

  std::vector<std::size_t> selected(channels);
  std::iota(selected.begin(), selected.end(), 0);
  std::stable_sort(selected.begin(), selected.end(), [&](auto a, auto b) { return quality[a] > quality[b]; });
  selected.resize(std::min({selected.size(), std::max<std::size_t>(config.maxMicChannels, 1), std::size_t{3}}));
  std::vector<Estimate> estimates;
  estimates.reserve(sources);
  for (std::size_t i = 0; i < sources; ++i) {
    std::vector<Observation> candidates;
    for (const auto ch : selected)
      candidates.insert(candidates.end(), observations[ch][i].begin(), observations[ch][i].end());
    const bool anyCoverage = std::any_of(covered[i].begin(), covered[i].end(), [](bool value) { return value; });
    estimates.push_back(combineSource(plan, i, candidates, anyCoverage, config));
  }

  const auto minRounds = plan.rounds == 1 ? 1 : std::max<std::size_t>((static_cast<std::size_t>(plan.rounds) + 1) / 2, 2);
  if (selfIndex < sources) {
    const auto& self = estimates[selfIndex];
    for (std::size_t i = 0; i < sources; ++i) {
      if (i == selfIndex)
        continue;
      std::vector<double> relative;
      for (std::size_t r = 0; r < plan.rounds; ++r)
        if (estimates[i].roundKeep[r] && self.roundKeep[r])
          relative.push_back(estimates[i].roundNs[r] - self.roundNs[r]);
      if (relative.size() < minRounds)
        continue;
      const double centre = median(relative);
      estimates[i].heard.relativeNs = centre;
      estimates[i].heard.relativeSpreadNs = madSigma(relative, centre);
      estimates[i].heard.hasRelative = true;
    }
  }
  for (std::size_t i = 0; i < estimates.size(); ++i) {
    result[i] = estimates[i].heard;
    if (plan.skipped(i))
      result[i].code = ArrivalCodeV2::Unsupported;
  }
  return result;
}

}  // namespace

std::vector<HeardV2> analyse(const Plan& plan, std::span<const std::int16_t> interleaved, std::size_t channels,
                             double rate, std::size_t selfIndex, const SampleClock& clock,
                             const std::function<double(double)>& toLeader, const AnalysisConfig& config) {
  if (channels == 0)
    return std::vector<HeardV2>(plan.members.size());
  const auto frames = interleaved.size() / channels;
  const Reader read = [&](std::size_t ch, std::vector<float>& out) {
    out.resize(frames);
    for (std::size_t n = 0; n < frames; ++n)
      out[n] = static_cast<float>(interleaved[n * channels + ch]) / 32768.0f;
  };
  return analyseWith(plan, channels, read, rate, selfIndex, clock, toLeader, config);
}

std::vector<HeardV2> analyse(const Plan& plan, std::span<const float> pcm, double rate, std::size_t selfIndex,
                             const SampleClock& clock, const std::function<double(double)>& toLeader,
                             const AnalysisConfig& config) {
  const Reader read = [&](std::size_t, std::vector<float>& out) { out.assign(pcm.begin(), pcm.end()); };
  return analyseWith(plan, 1, read, rate, selfIndex, clock, toLeader, config);
}

}  // namespace cadence::sync::calibration
