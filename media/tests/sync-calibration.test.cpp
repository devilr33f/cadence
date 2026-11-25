// the acoustic calibration's pure parts: the sweep, the fft and the analytic correlation, the arrival detector (the
// direct path before louder reflections, a sub-sample peak, a missed or weak chirp refused), the capture clock fit and
// the solver; then whole rooms of synthetic devices with known output and input latencies, distances, clocks, noise
// and echoes, whose recordings are analysed and solved back to the output latencies they were made with
#include "sync/calibration.h"

#include <catch2/catch_approx.hpp>
#include <catch2/catch_test_macros.hpp>

#include <cmath>
#include <numbers>
#include <random>

using namespace cadence::sync::calibration;

namespace {

constexpr double kRate = 16000;
constexpr double kSound = 343.0;

std::vector<std::complex<double>> naiveDft(const std::vector<std::complex<double>>& x) {
  const auto n = x.size();
  std::vector<std::complex<double>> out(n);
  for (std::size_t k = 0; k < n; ++k)
    for (std::size_t t = 0; t < n; ++t)
      out[k] += x[t] * std::polar(1.0, -2.0 * std::numbers::pi * static_cast<double>(k * t) / static_cast<double>(n));
  return out;
}

// a recording of chirps at known start times (seconds into it), each with a gain, plus white noise
std::vector<float> record(std::size_t samples, const std::vector<std::pair<double, double>>& chirps, double noiseRms,
                          std::uint32_t seed = 7, const ChirpShape& shape = {}) {
  std::vector<float> out(samples);
  std::mt19937 rng(seed);
  std::normal_distribution<double> noise(0, noiseRms);
  for (std::size_t n = 0; n < samples; ++n) {
    double v = noiseRms > 0 ? noise(rng) : 0;
    const double t = static_cast<double>(n) / kRate;
    for (const auto& [at, gain] : chirps)
      v += gain * chirpAt(shape, t - at);
    out[n] = static_cast<float>(v);
  }
  return out;
}

double lagSeconds(const Arrival& a) { return a.lag / kRate; }

struct Position {
  double x = 0, y = 0;
};

struct Reflection {
  double extraS = 0;
  double gain = 0;
};

// a synthetic device: its unknown output latency past the playhead, its input latency, where it stands, its clock
// against the leader's (local = leader + offset + ppm * leader), its microphone's rate error and how loud it plays
struct SimDevice {
  std::string id;
  double outS = 0;
  double inS = 0;
  Position at;
  double clockOffsetS = 0;
  double clockPpm = 0;
  double micPpm = 0;
  double gain = 1;
};

struct Room {
  std::vector<SimDevice> devices;
  std::vector<Reflection> reflections;
  double noiseRms = 0.0005;
  // a direct path's level at 1 m (spherical spreading past it)
  double level = 1.0;
  // the stamps' late jitter, seconds
  double stampJitterS = 0.0005;
};

struct Recorded {
  std::vector<float> pcm;
  std::vector<ReadStamp> stamps;
};

double distance(const Position& a, const Position& b) {
  const double d = std::hypot(a.x - b.x, a.y - b.y);
  return std::max(d, 0.05);
}

// what device j's microphone took over the plan's window: every device's chirp at its slot, through its output
// latency, the air and j's input latency, with the room's reflections and noise. time 0 of the leader is 1000 s
Recorded recordAt(const Room& room, const Plan& plan, std::size_t j, std::uint32_t seed) {
  const auto& dev = room.devices[j];
  auto toLocal = [&](double leaderS) { return leaderS + dev.clockOffsetS + dev.clockPpm * 1e-6 * leaderS; };
  auto toLeader = [&](double localS) { return (localS - dev.clockOffsetS) / (1.0 + dev.clockPpm * 1e-6); };
  const double fromLeader = static_cast<double>(plan.windowFromNs()) / 1e9;
  const double toLeaderEnd = static_cast<double>(plan.windowToNs()) / 1e9;
  // the recording starts a little before the window and ends a little after, on the local clock
  const double startLocal = toLocal(fromLeader) - 0.013;
  const double micPeriod = 1.0 / (kRate * (1.0 + dev.micPpm * 1e-6));
  const auto samples = static_cast<std::size_t>((toLocal(toLeaderEnd) + 0.02 - startLocal) / micPeriod);
  Recorded out;
  out.pcm.resize(samples);
  std::mt19937 rng(seed);
  std::normal_distribution<double> noise(0, room.noiseRms);
  struct Path {
    double atLeaderS;
    double gain;
  };
  std::vector<Path> paths;
  // every slot of every round: slot k is member k % members' chirp
  for (std::size_t k = 0; k < plan.slotNs.size(); ++k) {
    const auto i = k % room.devices.size();
    const auto& src = room.devices[i];
    const double d = distance(src.at, dev.at);
    const double direct = static_cast<double>(plan.slotNs[k]) / 1e9 + src.outS + d / kSound + dev.inS;
    const double g = src.gain * room.level / std::max(d, 1.0);
    paths.push_back({direct, g});
    for (const auto& r : room.reflections)
      paths.push_back({direct + r.extraS, g * r.gain});
  }
  for (std::size_t n = 0; n < samples; ++n) {
    const double leaderS = toLeader(startLocal + static_cast<double>(n) * micPeriod);
    double v = noise(rng);
    for (const auto& p : paths)
      if (leaderS >= p.atLeaderS && leaderS < p.atLeaderS + plan.chirp.durationS)
        v += p.gain * chirpAt(plan.chirp, leaderS - p.atLeaderS);
    out.pcm[n] = static_cast<float>(v);
  }
  // a read every 10 ms returns a little late, each by its own jitter (one of them by 40 ms: a busy scheduler)
  std::exponential_distribution<double> late(1.0 / std::max(room.stampJitterS, 1e-9));
  const std::size_t period = 160;
  for (std::size_t end = period, k = 0; end <= samples; end += period, ++k) {
    const double localS = startLocal + static_cast<double>(end) * micPeriod + 0.0007 + late(rng) +
                          (k == 37 ? 0.040 : 0.0);
    out.stamps.push_back({end, static_cast<std::int64_t>(std::llround(localS * 1e9))});
  }
  return out;
}

struct RoomResult {
  Solution solution;
  std::vector<std::vector<Heard>> heard;
};

RoomResult calibrate(const Room& room, std::uint32_t seed = 11) {
  std::vector<std::string> ids;
  for (const auto& d : room.devices)
    ids.push_back(d.id);
  const auto plan = makePlan(ids, 1'000'000'000'000);
  RoomResult out;
  for (std::size_t j = 0; j < room.devices.size(); ++j) {
    const auto& dev = room.devices[j];
    const auto rec = recordAt(room, plan, j, seed + static_cast<std::uint32_t>(j));
    const auto clock = fitSampleClock(rec.stamps, kRate);
    auto toLeader = [&](double localNs) {
      return (localNs - dev.clockOffsetS * 1e9) / (1.0 + dev.clockPpm * 1e-6);
    };
    out.heard.push_back(analyse(plan, rec.pcm, kRate, clock, toLeader));
  }
  out.solution = solve(plan, out.heard);
  return out;
}

// what the solution should say: each output latency less their mean, ns
std::vector<double> expected(const Room& room) {
  double mean = 0;
  for (const auto& d : room.devices)
    mean += d.outS;
  mean /= static_cast<double>(room.devices.size());
  std::vector<double> out;
  for (const auto& d : room.devices)
    out.push_back((d.outS - mean) * 1e9);
  return out;
}

Heard ok(std::int64_t tNs, double confidence = 1) { return {ArrivalCode::Ok, tNs, confidence}; }

}  // namespace

TEST_CASE("the sweep starts and ends silent, stays within its amplitude, and keeps its energy in its band") {
  const ChirpShape shape;
  const auto pcm = chirpSamples(shape, 48000);
  REQUIRE(pcm.size() == 12000);
  CHECK(std::abs(pcm.front()) < 1e-6);
  CHECK(std::abs(chirpAt(shape, shape.durationS - 1e-9)) < 1e-3);
  CHECK(chirpAt(shape, -0.001) == 0);
  CHECK(chirpAt(shape, shape.durationS) == 0);
  float peak = 0;
  for (auto v : pcm)
    peak = std::max(peak, std::abs(v));
  CHECK(peak <= shape.amplitude + 1e-6);
  CHECK(peak > shape.amplitude * 0.95);

  std::vector<std::complex<double>> spectrum(16384);
  for (std::size_t i = 0; i < pcm.size(); ++i)
    spectrum[i] = pcm[i];
  fft(spectrum, false);
  double in = 0, out = 0;
  for (std::size_t k = 1; k < spectrum.size() / 2; ++k) {
    const double f = static_cast<double>(k) * 48000.0 / static_cast<double>(spectrum.size());
    const double e = std::norm(spectrum[k]);
    (f >= 800 && f <= 7000 ? in : out) += e;
  }
  CHECK(out < 0.01 * in);
}

TEST_CASE("the fft matches a plain dft forward, and the inverse brings the input back") {
  std::mt19937 rng(3);
  std::uniform_real_distribution<double> u(-1, 1);
  std::vector<std::complex<double>> x(64);
  for (auto& v : x)
    v = {u(rng), u(rng)};
  auto y = x;
  fft(y, false);
  const auto want = naiveDft(x);
  for (std::size_t k = 0; k < x.size(); ++k)
    CHECK(std::abs(y[k] - want[k]) < 1e-9);
  fft(y, true);
  for (std::size_t k = 0; k < x.size(); ++k)
    CHECK(std::abs(y[k] / 64.0 - x[k]) < 1e-12);
}

TEST_CASE("the analytic correlation's real part is the plain cross-correlation") {
  std::mt19937 rng(5);
  std::uniform_real_distribution<float> u(-1, 1);
  std::vector<float> signal(300), ref(40);
  for (auto& v : signal)
    v = u(rng);
  for (auto& v : ref)
    v = u(rng);
  const auto corr = correlate(signal, ref);
  REQUIRE(corr.size() == 261);
  for (std::size_t k = 0; k < corr.size(); k += 13) {
    double want = 0;
    for (std::size_t n = 0; n < ref.size(); ++n)
      want += static_cast<double>(signal[n + k]) * static_cast<double>(ref[n]);
    CHECK(corr[k].real() == Catch::Approx(want).margin(1e-6));
  }
  CHECK(correlate(ref, signal).empty());
}

TEST_CASE("a chirp's start is found to a few microseconds at any fractional delay") {
  const auto ref = chirpSamples({}, kRate);
  for (double at : {0.2, 0.2 + 0.25 / kRate, 0.2 + 0.5 / kRate, 0.2 + 0.77 / kRate, 0.3137}) {
    const auto rec = record(16000, {{at, 0.5}}, 0);
    const auto corr = correlate(rec, ref);
    const auto a = findArrival(corr, 0.1 * kRate, 0.45 * kRate);
    REQUIRE(a.code == ArrivalCode::Ok);
    CHECK(std::abs(lagSeconds(a) - at) < 2e-6);
    CHECK(a.confidence == Catch::Approx(1.0));
  }
}

TEST_CASE("in noise the start is still found within 20 us, its confidence telling the margin") {
  const auto ref = chirpSamples({}, kRate);
  // the chirp's peak at -50 dBFS against noise at -50 dBFS rms: under the noise in every sample, 30 dB over it once
  // correlated
  for (std::uint32_t seed : {21u, 22u, 23u, 24u, 25u}) {
    INFO("seed " << seed);
    const auto rec = record(16000, {{0.25 + 0.3 / kRate, 0.03}}, 0.003, seed);
    const auto corr = correlate(rec, ref);
    const auto a = findArrival(corr, 0.1 * kRate, 0.45 * kRate);
    REQUIRE(a.code == ArrivalCode::Ok);
    CHECK(std::abs(lagSeconds(a) - (0.25 + 0.3 / kRate)) < 20e-6);
    CHECK(a.confidence > 0.2);
    CHECK(a.confidence < 1.0);
  }
}

TEST_CASE("the direct path is taken before a louder reflection, and a weaker one does not move it") {
  const auto ref = chirpSamples({}, kRate);
  SECTION("louder reflection 4 ms later") {
    const auto rec = record(16000, {{0.2, 0.3}, {0.204, 0.5}}, 0.0005);
    const auto a = findArrival(correlate(rec, ref), 0.1 * kRate, 0.45 * kRate);
    REQUIRE(a.code == ArrivalCode::Ok);
    CHECK(std::abs(lagSeconds(a) - 0.2) < 10e-6);
  }
  SECTION("a floor bounce 1.5 ms later, and a wall at 7 ms") {
    const auto rec = record(16000, {{0.2, 0.5}, {0.2015, 0.25}, {0.207, 0.2}}, 0.0005);
    const auto a = findArrival(correlate(rec, ref), 0.1 * kRate, 0.45 * kRate);
    REQUIRE(a.code == ArrivalCode::Ok);
    CHECK(std::abs(lagSeconds(a) - 0.2) < 20e-6);
  }
  SECTION("a reflection under half the direct path before it is never taken") {
    const auto rec = record(16000, {{0.1995, 0.2}, {0.2, 0.5}}, 0.0005);
    const auto a = findArrival(correlate(rec, ref), 0.1 * kRate, 0.45 * kRate);
    REQUIRE(a.code == ArrivalCode::Ok);
    CHECK(std::abs(lagSeconds(a) - 0.2) < 20e-6);
  }
}

TEST_CASE("a missed or buried chirp is weak, and a window outside the recording is not covered") {
  const auto ref = chirpSamples({}, kRate);
  SECTION("noise alone") {
    const auto rec = record(16000, {}, 0.01, 9);
    const auto a = findArrival(correlate(rec, ref), 0.1 * kRate, 0.45 * kRate);
    CHECK(a.code == ArrivalCode::Weak);
    CHECK(a.confidence < 0.2);
  }
  SECTION("a chirp far under the noise") {
    const auto rec = record(16000, {{0.2, 0.0002}}, 0.01, 9);
    CHECK(findArrival(correlate(rec, ref), 0.1 * kRate, 0.45 * kRate).code == ArrivalCode::Weak);
  }
  SECTION("silence") {
    const std::vector<float> rec(16000, 0.0f);
    CHECK(findArrival(correlate(rec, ref), 0.1 * kRate, 0.45 * kRate).code == ArrivalCode::Weak);
  }
  SECTION("the chirp is outside the window searched") {
    const auto rec = record(16000, {{0.6, 0.5}}, 0.001, 9);
    CHECK(findArrival(correlate(rec, ref), 0.1 * kRate, 0.45 * kRate).code == ArrivalCode::Weak);
  }
  SECTION("a window past either end") {
    const auto rec = record(16000, {{0.2, 0.5}}, 0.001, 9);
    const auto corr = correlate(rec, ref);
    CHECK(findArrival(corr, -10, 0.45 * kRate).code == ArrivalCode::NotCovered);
    CHECK(findArrival(corr, 0.5 * kRate, 1.2 * kRate).code == ArrivalCode::NotCovered);
    CHECK(findArrival({}, 0, 10).code == ArrivalCode::NotCovered);
  }
}

TEST_CASE("the capture clock comes from the reads' lower envelope, late reads and a stall aside") {
  std::mt19937 rng(17);
  std::exponential_distribution<double> late(1.0 / 2e6);
  const double ns = 1e9 / (16000 * (1 + 80e-6));
  std::vector<ReadStamp> stamps;
  for (std::uint64_t k = 1; k <= 400; ++k) {
    const auto end = k * 160;
    double t = 5e12 + static_cast<double>(end) * ns + 300'000 + late(rng);
    if (k == 100)
      t += 60e6;
    stamps.push_back({end, static_cast<std::int64_t>(t)});
  }
  const auto clock = fitSampleClock(stamps, 16000);
  CHECK(std::abs(clock.nsPerFrame / ns - 1) < 20e-6);
  // the origin sits on the earliest reads: within the jitter's lucky draws of the true one
  const double want = 5e12 + 300'000;
  CHECK(std::abs(clock.originNs - want) < 50'000);
  CHECK(clock.originNs <= want + 20'000);

  SECTION("a slope past the bound keeps the nominal rate") {
    std::vector<ReadStamp> fast;
    for (std::uint64_t k = 1; k <= 50; ++k)
      fast.push_back({k * 160, static_cast<std::int64_t>(1e9 + static_cast<double>(k * 160) * 1e9 / 16000 * 1.01)});
    CHECK(fitSampleClock(fast, 16000).nsPerFrame == Catch::Approx(62500.0));
  }
  SECTION("one stamp, or none") {
    const ReadStamp one{1600, 2'000'000'000};
    const auto c = fitSampleClock(std::span(&one, 1), 16000);
    CHECK(c.nsPerFrame == Catch::Approx(62500.0));
    CHECK(c.monoAt(1600) == Catch::Approx(2e9));
    CHECK(fitSampleClock({}, 16000).nsPerFrame == Catch::Approx(62500.0));
  }
}

TEST_CASE("a plan gives each member a slot every round, round-major, the leader first, and a window around them all") {
  const auto plan = makePlan({"leader", "a", "b"}, 10'000'000'000);
  REQUIRE(plan.rounds == kCalibrationRounds);
  REQUIRE(plan.rounds == 4);
  REQUIRE(plan.slotNs.size() == 12);
  CHECK(kSlotSpacingNs == 700'000'000);
  CHECK(plan.slotNs[0] == 10'000'000'000);
  for (std::size_t k = 1; k < plan.slotNs.size(); ++k)
    CHECK(plan.slotNs[k] - plan.slotNs[k - 1] == kSlotSpacingNs);
  // round r's member k is slot r * 3 + k, the same order every round
  CHECK(plan.slot(0, 2) == 10'000'000'000 + 2 * kSlotSpacingNs);
  CHECK(plan.slot(1, 0) == 10'000'000'000 + 3 * kSlotSpacingNs);
  CHECK(plan.slot(3, 2) == plan.slotNs[11]);
  CHECK(plan.windowFromNs() < plan.slotNs[0] - plan.searchBeforeNs);
  CHECK(plan.windowToNs() > plan.slotNs[11] + plan.searchAfterNs + 250'000'000);
  // a slot's search window, with a chirp found at its very end, ends before the next slot's chirp can begin to arrive
  // (its own window's start)
  const auto chirpNs = static_cast<std::int64_t>(plan.chirp.durationS * 1e9);
  CHECK(plan.searchBeforeNs + plan.searchAfterNs + chirpNs < kSlotSpacingNs);
  // eight members fit the 32 slots a start carries, four rounds each
  std::vector<std::string> eight{"l", "a", "b", "c", "d", "e", "f", "g"};
  const auto big = makePlan(eight, 0);
  CHECK(big.rounds == 4);
  CHECK(big.slotNs.size() == kMaxSlots);
  CHECK(makePlan({"l", "a"}, 0, kSlotSpacingNs, 1).slotNs.size() == 2);
}

TEST_CASE("the default sweep and the one before rounds; a boost raises the amplitude, never past -6 dBFS") {
  const ChirpShape shape;
  CHECK(shape.f0Hz == 800);
  CHECK(shape.f1Hz == 7000);
  CHECK(shape.durationS == Catch::Approx(0.25));
  CHECK(shape.fadeS == Catch::Approx(0.01));
  CHECK(shape.amplitude == Catch::Approx(0.1));
  const auto legacy = legacyChirp();
  CHECK(legacy.f0Hz == 1000);
  CHECK(legacy.f1Hz == 6000);
  CHECK(legacy.durationS == Catch::Approx(0.1));
  CHECK(legacy.fadeS == Catch::Approx(0.005));
  CHECK(withGain(shape, 0).amplitude == Catch::Approx(0.1));
  CHECK(withGain(shape, 6).amplitude == Catch::Approx(0.1 * std::pow(10.0, 0.3)));
  CHECK(withGain(shape, 14).amplitude == Catch::Approx(0.1 * std::pow(10.0, 0.7)));
  // past 14 dB it is held there: about 0.5 of full scale
  CHECK(withGain(shape, 40).amplitude == Catch::Approx(withGain(shape, 14).amplitude));
  CHECK(withGain(shape, 40).amplitude < 0.51);
  float peak = 0;
  for (auto v : chirpSamples(withGain(shape, 99), 16000))
    peak = std::max(peak, std::abs(v));
  CHECK(peak <= 0.51f);
}

namespace {

// one source's rounds in a recording that starts at leader time 0 on an exact 16 kHz clock: each round's paths as
// (delay past the slot, gain)
struct RoundPaths {
  std::vector<std::pair<double, double>> paths;
};

std::vector<Heard> analyseRounds(const std::vector<RoundPaths>& rounds, double noise = 0.0005,
                                 std::uint32_t seed = 5) {
  auto plan = makePlan({"a"}, 1'000'000'000, kSlotSpacingNs, static_cast<std::uint32_t>(rounds.size()));
  std::vector<std::pair<double, double>> chirps;
  for (std::size_t r = 0; r < rounds.size(); ++r)
    for (const auto& [delay, gain] : rounds[r].paths)
      chirps.emplace_back(static_cast<double>(plan.slotNs[r]) / 1e9 + delay, gain);
  const auto samples = static_cast<std::size_t>((static_cast<double>(plan.windowToNs()) / 1e9 + 0.1) * kRate);
  const auto pcm = record(samples, chirps, noise, seed, plan.chirp);
  const SampleClock clock{0, 1e9 / kRate};
  return analyse(plan, pcm, kRate, clock, [](double ns) { return ns; });
}

}  // namespace

TEST_CASE("rounds: a round whose reflection outshone its direct path keeps the direct path, by the narrow pass") {
  const double direct = 0.0123;
  std::vector<RoundPaths> rounds(4, RoundPaths{{{direct, 0.4}, {direct + 0.006, 0.2}}});
  // round 2: the direct path faint and a reflection 4 ms later far louder (more than twice it): the wide window
  // alone takes the reflection
  rounds[2].paths = {{direct, 0.1}, {direct + 0.004, 0.5}};
  const auto heard = analyseRounds(rounds);
  REQUIRE(heard.size() == 1);
  CHECK(heard[0].code == ArrivalCode::Ok);
  CHECK(heard[0].rounds == 4);
  CHECK(std::abs(static_cast<double>(heard[0].tNs) - (1e9 + direct * 1e9)) < 20'000);
  CHECK(heard[0].spreadNs < 20'000);
  CHECK(heard[0].confidence > 0.5);
}

TEST_CASE("rounds: an outlier round is dropped, three agreeing are enough") {
  const double direct = 0.02;
  std::vector<RoundPaths> rounds(4, RoundPaths{{{direct, 0.4}}});
  // round 1 arrived 1.5 ms late on its own: inside the narrow window, outside the gate
  rounds[1].paths = {{direct + 0.0015, 0.4}};
  const auto heard = analyseRounds(rounds);
  CHECK(heard[0].code == ArrivalCode::Ok);
  CHECK(heard[0].rounds == 3);
  CHECK(std::abs(static_cast<double>(heard[0].tNs) - (1e9 + direct * 1e9)) < 20'000);
  CHECK(heard[0].spreadNs < 20'000);
}

TEST_CASE("rounds: rounds that spread too far, or too few heard, are unstable") {
  const double direct = 0.02;
  SECTION("two and two, 0.9 ms apart") {
    std::vector<RoundPaths> rounds(4, RoundPaths{{{direct, 0.4}}});
    rounds[1].paths = {{direct + 0.0009, 0.4}};
    rounds[3].paths = {{direct + 0.0009, 0.4}};
    const auto heard = analyseRounds(rounds);
    CHECK(heard[0].code == ArrivalCode::Unstable);
    CHECK(heard[0].rounds == 4);
    CHECK(heard[0].spreadNs > 400'000);
  }
  SECTION("two of four heard") {
    std::vector<RoundPaths> rounds(4, RoundPaths{{{direct, 0.4}}});
    rounds[0].paths = {};
    rounds[2].paths = {};
    const auto heard = analyseRounds(rounds);
    CHECK(heard[0].code == ArrivalCode::Unstable);
    CHECK(heard[0].rounds == 2);
  }
  SECTION("none heard is weak, as before rounds") {
    const auto heard = analyseRounds(std::vector<RoundPaths>(4));
    CHECK(heard[0].code == ArrivalCode::Weak);
    CHECK(heard[0].rounds == 0);
  }
  SECTION("one round alone, the plan's only one, is ok") {
    const auto heard = analyseRounds(std::vector<RoundPaths>(1, RoundPaths{{{direct, 0.4}}}));
    CHECK(heard[0].code == ArrivalCode::Ok);
    CHECK(heard[0].rounds == 1);
    CHECK(heard[0].spreadNs == 0);
  }
}

TEST_CASE("rounds: each microphone is detected apart, a round's arrival their median") {
  const auto plan = makePlan({"a"}, 1'000'000'000, kSlotSpacingNs, 2);
  const auto samples = static_cast<std::size_t>((static_cast<double>(plan.windowToNs()) / 1e9 + 0.1) * kRate);
  // three microphones a little apart (0, 40 and 400 us later) and a fourth that hears nothing
  const std::vector<double> lags{0.0, 0.00004, 0.0004};
  std::vector<std::vector<float>> channels;
  for (std::size_t c = 0; c < 4; ++c) {
    std::vector<std::pair<double, double>> chirps;
    if (c < lags.size())
      for (std::size_t r = 0; r < 2; ++r)
        chirps.emplace_back(static_cast<double>(plan.slotNs[r]) / 1e9 + 0.01 + lags[c], 0.4);
    channels.push_back(record(samples, chirps, 0.0005, 31 + static_cast<std::uint32_t>(c), plan.chirp));
  }
  std::vector<std::int16_t> interleaved;
  for (std::size_t n = 0; n < samples; ++n)
    for (const auto& ch : channels)
      interleaved.push_back(static_cast<std::int16_t>(std::lround(std::clamp(ch[n], -1.0f, 1.0f) * 32767.0f)));
  const SampleClock clock{0, 1e9 / kRate};
  const auto heard = analyse(plan, interleaved, 4, kRate, clock, [](double ns) { return ns; });
  REQUIRE(heard.size() == 1);
  CHECK(heard[0].code == ArrivalCode::Ok);
  CHECK(heard[0].rounds == 2);
  CHECK(std::abs(static_cast<double>(heard[0].tNs) - (1e9 + 0.01e9 + 40'000)) < 10'000);
}

TEST_CASE("the solver: a member whose own rounds disagree, or whose every link does, is unstable") {
  const auto plan = makePlan({"l", "a", "b"}, 0, 1'000'000'000, 1);
  std::vector<std::vector<Heard>> h(3, std::vector<Heard>(3));
  for (std::size_t j = 0; j < 3; ++j)
    for (std::size_t i = 0; i < 3; ++i)
      h[j][i] = ok(plan.slotNs[i] + (i == j ? 0 : 2'000'000));
  for (std::size_t j = 0; j < 3; ++j)
    for (std::size_t i = 0; i < 3; ++i) {
      h[j][i].spreadNs = 10'000.0 * static_cast<double>(1 + i + j);
      h[j][i].rounds = 4;
    }
  SECTION("its own") {
    h[2][2].code = ArrivalCode::Unstable;
    const auto r = solve(plan, h);
    CHECK(r.code == ResultCode::Incomplete);
    CHECK(r.members[2].code == MemberCode::Unstable);
    CHECK(r.members[0].code == MemberCode::Ok);
    CHECK(r.members[1].code == MemberCode::Ok);
  }
  SECTION("every link") {
    for (std::size_t j = 0; j < 2; ++j) {
      h[j][2].code = ArrivalCode::Unstable;
      h[2][j].code = ArrivalCode::Unstable;
    }
    const auto r = solve(plan, h);
    CHECK(r.members[2].code == MemberCode::Unstable);
  }
  SECTION("measured: the largest spread and the fewest rounds its pairs rest on") {
    h[1][0].rounds = 3;
    const auto r = solve(plan, h);
    REQUIRE(r.code == ResultCode::Ok);
    // l's pairs (l-a, l-b) rest on arrivals up to b heard by b, i + j = 4
    CHECK(r.members[0].spreadNs == Catch::Approx(50'000));
    CHECK(r.members[0].rounds == 3);
    CHECK(r.members[2].rounds == 4);
  }
}

TEST_CASE("the solver turns round trips into output offsets: distance and input latency cancel") {
  // out 10 ms and 40 ms, in 2 ms and 7 ms, 3 ms of air between them, a self path of 0.1 ms
  const auto plan = makePlan({"l", "a"}, 0, 1'000'000'000);
  const double out[2] = {10e6, 40e6}, in[2] = {2e6, 7e6};
  std::vector<std::vector<Heard>> heard(2, std::vector<Heard>(2));
  for (std::size_t j = 0; j < 2; ++j)
    for (std::size_t i = 0; i < 2; ++i)
      heard[j][i] = ok(plan.slotNs[i] + std::llround(out[i] + (i == j ? 0.1e6 : 3e6) + in[j]));
  const auto s = solve(plan, heard);
  REQUIRE(s.code == ResultCode::Ok);
  CHECK(s.members[0].offsetNs == Catch::Approx(-15e6).margin(1));
  CHECK(s.members[1].offsetNs == Catch::Approx(15e6).margin(1));
  CHECK(s.members[0].id == "l");
  CHECK(s.residualNs == Catch::Approx(0).margin(1));

  SECTION("three members by least squares, one pair's error spread over the others") {
    const auto p3 = makePlan({"l", "a", "b"}, 0, 1'000'000'000);
    const double o[3] = {5e6, 25e6, 0};
    std::vector<std::vector<Heard>> h(3, std::vector<Heard>(3));
    for (std::size_t j = 0; j < 3; ++j)
      for (std::size_t i = 0; i < 3; ++i)
        h[j][i] = ok(p3.slotNs[i] + std::llround(o[i] + (i == j ? 0.0 : 4e6)));
    // b's arrival of a's chirp 60 us late
    h[2][1].tNs += 60'000;
    const auto r = solve(p3, h);
    REQUIRE(r.code == ResultCode::Ok);
    CHECK(r.members[0].offsetNs == Catch::Approx(5e6 - 10e6).margin(30'000));
    CHECK(r.members[1].offsetNs == Catch::Approx(25e6 - 10e6).margin(30'000));
    CHECK(r.members[2].offsetNs == Catch::Approx(-10e6).margin(30'000));
    CHECK(r.residualNs > 5'000);
    CHECK(r.residualNs < 30'000);
  }
  SECTION("a member that did not hear itself cannot be measured") {
    heard[1][1].code = ArrivalCode::Weak;
    const auto r = solve(plan, heard);
    CHECK(r.code == ResultCode::Incomplete);
    CHECK(r.members[1].code == MemberCode::NoSelf);
    CHECK(r.members[0].code == MemberCode::Isolated);
  }
  SECTION("a member nobody heard is isolated, the rest still solved") {
    const auto p3 = makePlan({"l", "a", "b"}, 0, 1'000'000'000);
    std::vector<std::vector<Heard>> h(3, std::vector<Heard>(3));
    for (std::size_t j = 0; j < 3; ++j)
      for (std::size_t i = 0; i < 3; ++i)
        h[j][i] = ok(p3.slotNs[i] + (i == 1 ? 8'000'000 : 0) + (i == j ? 0 : 2'000'000));
    h[0][2].code = ArrivalCode::Weak;
    h[1][2].code = ArrivalCode::NotCovered;
    const auto r = solve(p3, h);
    CHECK(r.code == ResultCode::Incomplete);
    CHECK(r.members[2].code == MemberCode::Isolated);
    CHECK(r.members[0].code == MemberCode::Ok);
    CHECK(r.members[1].offsetNs - r.members[0].offsetNs == Catch::Approx(8e6).margin(1));
  }
  SECTION("pairs that disagree by milliseconds are inconsistent") {
    const auto p3 = makePlan({"l", "a", "b"}, 0, 1'000'000'000);
    std::vector<std::vector<Heard>> h(3, std::vector<Heard>(3));
    for (std::size_t j = 0; j < 3; ++j)
      for (std::size_t i = 0; i < 3; ++i)
        h[j][i] = ok(p3.slotNs[i] + (i == j ? 0 : 2'000'000));
    // an echo 9 ms late taken for b's arrival at a
    h[1][2].tNs += 9'000'000;
    CHECK(solve(p3, h).code == ResultCode::Inconsistent);
  }
  SECTION("one member is too few") {
    const auto p1 = makePlan({"l"}, 0);
    CHECK(solve(p1, {{ok(0)}}).code == ResultCode::TooFew);
  }
}

TEST_CASE("a synthetic stereo pair: different output latencies are measured within 20 us") {
  Room room;
  room.devices = {
      {.id = "left", .outS = 0.012, .inS = 0.003, .at = {0, 0}, .clockOffsetS = 0, .clockPpm = 0, .micPpm = 20},
      {.id = "right", .outS = 0.0473, .inS = 0.0091, .at = {2.4, 0.3}, .clockOffsetS = 37.25, .clockPpm = 12,
       .micPpm = -35},
  };
  room.reflections = {{0.0021, 0.4}, {0.0063, 0.3}, {0.0137, 0.2}};
  const auto want = expected(room);
  // several draws of noise and read jitter, so a lucky one proves nothing
  for (std::uint32_t seed : {11u, 23u, 37u, 51u, 73u}) {
    INFO("seed " << seed);
    const auto r = calibrate(room, seed);
    INFO("residual " << r.solution.residualNs);
    REQUIRE(r.solution.code == ResultCode::Ok);
    for (std::size_t i = 0; i < want.size(); ++i) {
      INFO(room.devices[i].id);
      CHECK(std::abs(r.solution.members[i].offsetNs - want[i]) < 20'000);
      CHECK(r.solution.members[i].confidence > 0.5);
    }
  }
}

TEST_CASE("a synthetic room of four, a tv among them: every offset within 40 us, by least squares") {
  Room room;
  room.devices = {
      {.id = "kitchen", .outS = 0.020, .inS = 0.004, .at = {0, 0}},
      {.id = "tv", .outS = 0.135, .inS = 0.011, .at = {4.0, 1.0}, .clockOffsetS = -12.5, .clockPpm = -40,
       .micPpm = 60},
      {.id = "shelf", .outS = 0.0215, .inS = 0.0045, .at = {1.5, 3.0}, .clockOffsetS = 3.1, .clockPpm = 7},
      {.id = "desk", .outS = 0.044, .inS = 0.002, .at = {3.2, 3.8}, .clockOffsetS = 100.0, .clockPpm = 25,
       .micPpm = -15, .gain = 0.6},
  };
  room.reflections = {{0.0018, 0.45}, {0.0081, 0.35}, {0.017, 0.2}, {0.031, 0.1}};
  room.noiseRms = 0.002;
  const auto r = calibrate(room);
  INFO("residual " << r.solution.residualNs);
  REQUIRE(r.solution.code == ResultCode::Ok);
  const auto want = expected(room);
  for (std::size_t i = 0; i < want.size(); ++i) {
    INFO(room.devices[i].id);
    CHECK(std::abs(r.solution.members[i].offsetNs - want[i]) < 40'000);
  }
  CHECK(r.solution.residualNs < 50'000);
}

TEST_CASE("a member whose speaker is muted is heard by nobody: the calibration is refused as incomplete") {
  Room room;
  room.devices = {
      {.id = "l", .outS = 0.01, .inS = 0.003, .at = {0, 0}},
      {.id = "a", .outS = 0.02, .inS = 0.003, .at = {2, 0}, .clockOffsetS = 5},
      {.id = "b", .outS = 0.03, .inS = 0.003, .at = {0, 2}, .clockOffsetS = -5, .gain = 0},
  };
  room.noiseRms = 0.002;
  const auto r = calibrate(room);
  CHECK(r.solution.code == ResultCode::Incomplete);
  CHECK(r.solution.members[2].code == MemberCode::NoSelf);
  for (std::size_t j = 0; j < 3; ++j)
    CHECK(r.heard[j][2].code == ArrivalCode::Weak);
  CHECK(r.solution.members[0].code == MemberCode::Ok);
  CHECK(r.solution.members[1].offsetNs - r.solution.members[0].offsetNs == Catch::Approx(10e6).margin(20'000));
}
