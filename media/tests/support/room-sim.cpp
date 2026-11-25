#include "support/room-sim.h"

#include <algorithm>
#include <cmath>
#include <numbers>
#include <random>

namespace cadence::sync::calibration::test {

namespace {

double distance(const Endpoint& a, const Endpoint& b) { return std::max(std::hypot(a.x - b.x, a.y - b.y), 0.05); }

float atSample(std::span<const float> pcm, double at) {
  const auto centre = static_cast<long>(std::floor(at));
  double out = 0;
  for (long k = centre - 7; k <= centre + 8; ++k) {
    if (k < 0 || k >= static_cast<long>(pcm.size()))
      continue;
    const double x = at - static_cast<double>(k);
    const double sinc = std::abs(x) < 1e-12 ? 1.0 : std::sin(std::numbers::pi * x) / (std::numbers::pi * x);
    const double u = x / 8.0;
    const double window = 0.42 + 0.5 * std::cos(std::numbers::pi * u) + 0.08 * std::cos(2 * std::numbers::pi * u);
    out += static_cast<double>(pcm[static_cast<std::size_t>(k)]) * sinc * window;
  }
  return static_cast<float>(out);
}

}  // namespace

RoomSim::RoomSim(Room room, Plan plan) : room_(std::move(room)), plan_(std::move(plan)) {}

double RoomSim::arrivalS(std::size_t slot, std::size_t source, std::size_t recorder, std::size_t channel) const {
  const auto& from = room_.endpoints[source];
  const auto& to = room_.endpoints[recorder];
  return static_cast<double>(plan_.slotNs[slot]) / 1e9 + from.outputS + distance(from, to) / 343.0 + to.inputS +
         static_cast<double>(channel) * room_.micStepS;
}

Recording RoomSim::record(std::size_t recorder) const {
  const auto& dev = room_.endpoints[recorder];
  const double ppm = 1.0 + dev.clockPpm * 1e-6;
  auto toLocal = [&](double leader) { return leader + dev.clockOffsetS + dev.clockPpm * 1e-6 * leader; };
  const double fromLeader = room_.recordFromS.value_or(static_cast<double>(plan_.windowFromNs()) / 1e9 - 0.013);
  const double toLeaderEnd = room_.recordToS.value_or(static_cast<double>(plan_.windowToNs()) / 1e9 + 0.020);
  const double startLocal = toLocal(fromLeader);
  const double actualRate = room_.rate * (1.0 + dev.micPpm * 1e-6);
  const auto frames = static_cast<std::size_t>(std::ceil((toLocal(toLeaderEnd) - startLocal) * actualRate));
  const auto channels = std::max<std::size_t>(room_.micChannels, 1);

  std::vector<std::vector<float>> probes;
  for (std::size_t i = 0; i < room_.endpoints.size(); ++i)
    probes.push_back(probeSamples(plan_.probe, plan_.probeCode(i), room_.rate));

  Recording out;
  out.channels = channels;
  out.rate = room_.rate;
  out.pcm.assign(frames * channels, 0);
  for (std::size_t ch = 0; ch < channels; ++ch) {
    std::vector<float> signal(frames);
    for (std::size_t slot = 0; slot < plan_.slotNs.size(); ++slot) {
      const auto source = slot % room_.endpoints.size();
      const auto& from = room_.endpoints[source];
      if (from.silent || from.level == 0)
        continue;
      const double direct = arrivalS(slot, source, recorder, ch);
      const double gain = from.level / std::max(distance(from, dev), 1.0);
      struct Path {
        double at;
        double gain;
      };
      std::vector<Path> paths{{direct, gain}};
      for (const auto& reflection : room_.reflections)
        paths.push_back({direct + reflection.delayS, gain * reflection.gain});
      for (const auto& path : paths) {
        const double position = (toLocal(path.at) - startLocal) * actualRate;
        const auto first = std::max<long>(0, static_cast<long>(std::floor(position)) - 8);
        const auto last = std::min<long>(static_cast<long>(frames),
                                         static_cast<long>(std::ceil(position + static_cast<double>(probes[source].size()))) + 9);
        for (auto n = first; n < last; ++n) {
          const double at = static_cast<double>(n) - position;
          if (at >= -8 && at < static_cast<double>(probes[source].size()) + 8)
            signal[static_cast<std::size_t>(n)] += static_cast<float>(path.gain * atSample(probes[source], at));
        }
      }
    }

    std::mt19937 rng(room_.seed + static_cast<std::uint32_t>(recorder * 101 + ch));
    std::normal_distribution<double> normal(0, 1);
    const double noiseRms = room_.snrDb >= 200 ? 0 : 0.1 * std::pow(10.0, -room_.snrDb / 20.0);
    double b0 = 0, b1 = 0, b2 = 0, pinkEnergy = 0;
    std::vector<double> pink(frames);
    for (std::size_t n = 0; n < frames; ++n) {
      const double white = normal(rng);
      b0 = 0.99765 * b0 + white * 0.0990460;
      b1 = 0.96300 * b1 + white * 0.2965164;
      b2 = 0.57000 * b2 + white * 1.0526913;
      pink[n] = b0 + b1 + b2 + white * 0.1848;
      pinkEnergy += pink[n] * pink[n];
    }
    if (room_.musicMasking) {
      // keep masking in the audible band: hard-clipped full-band white noise would manufacture ultrasonic energy
      double filtered = 0;
      const double alpha = 1.0 - std::exp(-2.0 * std::numbers::pi * 8000.0 / room_.rate);
      pinkEnergy = 0;
      for (auto& sample : pink) {
        filtered += alpha * (sample - filtered);
        sample = std::tanh(0.5 * filtered);
        pinkEnergy += sample * sample;
      }
    }
    const double pinkScale = !pink.empty() && pinkEnergy > 0
                                 ? noiseRms / std::sqrt((room_.musicMasking ? 1.0 : 2.0) * pinkEnergy /
                                                        static_cast<double>(pink.size()))
                                 : 0;
    for (std::size_t n = 0; n < frames; ++n) {
      const double noise = room_.musicMasking
                               ? pinkScale * pink[n]
                               : noiseRms / std::sqrt(2.0) * normal(rng) + pinkScale * pink[n];
      const double v = std::clamp(static_cast<double>(signal[n]) + noise, -1.0, 1.0);
      out.pcm[n * channels + ch] = static_cast<std::int16_t>(std::lround(v * 32767.0));
    }
  }

  const auto readFrames = std::max<std::size_t>(static_cast<std::size_t>(room_.rate / 100), 1);
  std::mt19937 jitter(room_.seed + 999 + static_cast<std::uint32_t>(recorder));
  std::uniform_real_distribution<double> late(0, std::max(room_.stampJitterS, 0.0));
  for (std::size_t end = readFrames; end <= frames; end += readFrames) {
    const double mono = startLocal + static_cast<double>(end) / actualRate + 0.0007 + late(jitter);
    out.stamps.push_back({end, static_cast<std::int64_t>(std::llround(mono * 1e9))});
  }
  out.clock = fitSampleClock(out.stamps, room_.rate);
  out.toLeader = [offset = dev.clockOffsetS, ppm](double localNs) {
    return (localNs - offset * 1e9) / ppm;
  };
  return out;
}

}  // namespace cadence::sync::calibration::test
