// coded probes, their exact waveform and band, and the float transform behind the v2 acoustic calibration
#include "sync/calibration.h"
#include "support/room-sim.h"
#include "support/calibration-fixtures.h"

#include <catch2/catch_approx.hpp>
#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <array>
#include <chrono>
#include <cmath>
#include <complex>
#include <numbers>
#include <random>
#include <set>
#include <vector>

using namespace cadence::sync::calibration;

namespace {

// an independent fibonacci lfsr, to pin the preferred pair rather than checking the generator against itself
std::array<int, kGoldChips> referenceMSequence(bool second) {
  std::array<int, kGoldChips> out{};
  std::array<int, 10> state{};
  state[0] = 1;
  for (std::size_t n = 0; n < out.size(); ++n) {
    out[n] = state[0];
    const int next = second ? state[8] ^ state[3] ^ state[2] ^ state[0] : state[3] ^ state[0];
    for (std::size_t k = 0; k + 1 < state.size(); ++k)
      state[k] = state[k + 1];
    state.back() = next;
  }
  return out;
}

std::vector<std::complex<double>> naiveDft(const std::vector<std::complex<float>>& x) {
  std::vector<std::complex<double>> out(x.size());
  for (std::size_t k = 0; k < x.size(); ++k)
    for (std::size_t n = 0; n < x.size(); ++n)
      out[k] += std::complex<double>(x[n].real(), x[n].imag()) *
                std::polar(1.0, -2.0 * std::numbers::pi * static_cast<double>(k * n) / static_cast<double>(x.size()));
  return out;
}

}  // namespace

TEST_CASE("v2 probes: the degree-10 preferred pair has three-valued periodic cross-correlation") {
  const auto a = referenceMSequence(false);
  const auto b = referenceMSequence(true);
  std::set<int> values;
  for (std::size_t shift = 0; shift < kGoldChips; ++shift) {
    int sum = 0;
    for (std::size_t n = 0; n < kGoldChips; ++n)
      sum += (1 - 2 * a[n]) * (1 - 2 * b[(n + shift) % kGoldChips]);
    values.insert(sum);
  }
  CHECK(values.size() == 3);
  const auto code = goldCode(7);
  for (std::size_t n = 0; n < code.size(); ++n)
    CHECK(code[n] == (1 - 2 * (a[n] ^ b[(n + 7) % kGoldChips])));
}

TEST_CASE("v2 probes: the closed form and sampled gold waveform agree, and the legacy chirp is unchanged") {
  ProbeShape shape;
  CHECK(shape.kind == ProbeKind::Gold);
  CHECK(shape.bandLoHz == Catch::Approx(1000));
  CHECK(shape.bandHiHz == Catch::Approx(6000));
  CHECK(probeDuration(shape) == Catch::Approx((kGoldChips + 16.0) / 5000.0));
  for (double rate : {16000.0, 48000.0}) {
    const auto pcm = probeSamples(shape, 31, rate);
    REQUIRE(pcm.size() == static_cast<std::size_t>(std::ceil(probeDuration(shape) * rate)));
    for (std::size_t n = 0; n < pcm.size(); n += 97)
      CHECK(pcm[n] == Catch::Approx(probeAt(shape, 31, static_cast<double>(n) / rate)).margin(1e-7));
    CHECK(probeAt(shape, 31, -0.001) == 0);
    CHECK(probeAt(shape, 31, probeDuration(shape)) == 0);
  }

  const ChirpShape chirp;
  const auto legacy = chirpProbe(chirp);
  CHECK(legacy.kind == ProbeKind::Chirp);
  for (double t : {0.0, 0.001, 0.0137, 0.2, 0.249})
    CHECK(probeAt(legacy, 0, t) == Catch::Approx(chirpAt(chirp, t)));
}

TEST_CASE("v2 probes: the modulated waveform keeps its energy in the microphone band") {
  const ProbeShape shape;
  const auto pcm = probeSamples(shape, 3, 16000);
  std::vector<std::complex<double>> spectrum(32768);
  for (std::size_t n = 0; n < pcm.size(); ++n)
    spectrum[n] = pcm[n];
  fft(spectrum, false);
  double inside = 0, outside = 0;
  for (std::size_t k = 1; k < spectrum.size() / 2; ++k) {
    const double hz = static_cast<double>(k) * 16000.0 / static_cast<double>(spectrum.size());
    const double energy = std::norm(spectrum[k]);
    (hz >= shape.bandLoHz - 500 && hz <= shape.bandHiHz + 500 ? inside : outside) += energy;
  }
  CHECK(outside < inside * 0.001);
}

TEST_CASE("v2 fft: cached float twiddles match a plain dft and invert") {
  std::mt19937 rng(73);
  std::uniform_real_distribution<float> uniform(-1, 1);
  std::vector<std::complex<float>> input(64);
  for (auto& v : input)
    v = {uniform(rng), uniform(rng)};
  const auto want = naiveDft(input);
  Fft fft;
  auto actual = input;
  fft(actual, false);
  for (std::size_t k = 0; k < actual.size(); ++k)
    CHECK(std::abs(actual[k] - std::complex<float>(want[k])) < 2e-4f);
  fft(actual, true);
  for (std::size_t k = 0; k < actual.size(); ++k)
    CHECK(std::abs(actual[k] / 64.0f - input[k]) < 2e-6f);

  auto again = input;
  fft(again, false);
  for (std::size_t k = 0; k < actual.size(); ++k)
    CHECK(std::abs(again[k] - std::complex<float>(want[k])) < 2e-4f);
}

namespace {

using cadence::sync::calibration::test::Recording;
using cadence::sync::calibration::test::Room;
using cadence::sync::calibration::test::RoomSim;

float highestReal(std::span<const std::complex<float>> corr) {
  float out = 0;
  for (const auto& v : corr)
    out = std::max(out, std::abs(v.real()));
  return out;
}

std::vector<HeardV2> analyseRecording(const Plan& plan, const Recording& recording, std::size_t self) {
  return analyse(plan, recording.pcm, recording.channels, recording.rate, self, recording.clock, recording.toLeader);
}

std::vector<double> expectedOffsets(const Room& room) {
  double mean = 0;
  for (const auto& endpoint : room.endpoints)
    mean += endpoint.outputS;
  mean /= static_cast<double>(room.endpoints.size());
  std::vector<double> out;
  for (const auto& endpoint : room.endpoints)
    out.push_back((endpoint.outputS - mean) * 1e9);
  return out;
}

}  // namespace

TEST_CASE("v2 probes: every modulated code pair stays below -20 db at 16 khz") {
  constexpr std::size_t count = 32;
  constexpr std::size_t guard = 64;
  const ProbeShape shape;
  std::vector<std::vector<float>> signals;
  for (std::size_t code = 0; code < count; ++code) {
    const auto probe = probeSamples(shape, static_cast<std::uint32_t>(code), 16000);
    std::vector<float> signal(probe.size() + 2 * guard);
    std::copy(probe.begin(), probe.end(), signal.begin() + static_cast<long>(guard));
    signals.push_back(std::move(signal));
  }
  std::vector<float> automatic(count);
  for (std::size_t code = 0; code < count; ++code) {
    Correlator correlator(probeSamples(shape, static_cast<std::uint32_t>(code), 16000));
    automatic[code] = highestReal(correlator(signals[code]));
  }
  for (std::size_t other = 0; other < count; ++other) {
    Correlator correlator(probeSamples(shape, static_cast<std::uint32_t>(other), 16000));
    for (std::size_t source = 0; source < count; ++source) {
      if (source == other)
        continue;
      const float peak = highestReal(correlator(signals[source]));
      INFO("codes " << source << " and " << other);
      CHECK(peak < 0.1f * automatic[source]);
    }
  }
}

TEST_CASE("v2 plan: latency priors widen the search and move the slots apart") {
  const auto plan = makeProbePlan({"speaker", "tv"}, 1e9, {0, 250'000'000}, {150'000'000, 200'000'000});
  REQUIRE(plan.slotNs.size() == 8);
  CHECK(plan.probe.kind == ProbeKind::Gold);
  CHECK(plan.probeCode(0) == 0);
  CHECK(plan.probeCode(1) == 1);
  CHECK(plan.probeCode(2) == 0);
  CHECK(plan.sourceWindowFromNs(0, 1) == plan.slot(0, 1) + 30'000'000);
  CHECK(plan.sourceWindowToNs(0, 1) == plan.slot(0, 1) + 470'000'000);
  CHECK(plan.slotNs[1] - plan.slotNs[0] >= 746'000'000);
  const auto legacy = makePlan({"a", "b"}, 0);
  CHECK(legacy.probe.kind == ProbeKind::Chirp);
  CHECK(legacy.slotNs[1] == kSlotSpacingNs);
}

TEST_CASE("v2 plan: a four-member bluetooth-heavy run fits the slot and recording bounds") {
  const auto plan = makeProbePlan({"a", "b", "c", "d"}, 1'000'000'000,
                                  {200'000'000, 200'000'000, 200'000'000, 200'000'000},
                                  {250'000'000, 250'000'000, 250'000'000, 250'000'000});
  REQUIRE(plan.slotNs.size() == 16);
  CHECK(plan.slotNs.size() <= kMaxSlots);
  // kMaxRecordingNs in calibration-run.h (not included here: its wire types need another include path)
  CHECK(plan.windowToNs() - plan.windowFromNs() <= 60'000'000'000);
  const auto spacing = plan.slotNs[1] - plan.slotNs[0];
  CHECK(spacing > kSlotSpacingNs);
  for (std::size_t k = 1; k < plan.slotNs.size(); ++k)
    CHECK(plan.slotNs[k] - plan.slotNs[k - 1] == spacing);
  // listener outcomes subtract plan.slotNs.front() from these same slots, so their browser offsets grow by spacing too
}

TEST_CASE("v2 regression: a silent member is not given its 30 db louder neighbour's arrival") {
  Room room;
  room.endpoints = {
      {.id = "loud", .outputS = 0.620, .inputS = 0.002, .x = 0, .level = 31.6227766},
      {.id = "silent", .outputS = 0.020, .inputS = 0.004, .x = 2, .level = 1, .silent = true},
  };
  room.micChannels = 2;
  room.snrDb = 55;
  const std::vector<std::string> ids{"loud", "silent"};
  auto oldPlan = makePlan(ids, 1'000'000'000);
  oldPlan.chirp.amplitude = 0.003;
  oldPlan.probe = chirpProbe(oldPlan.chirp);
  const auto oldRecording = RoomSim(room, oldPlan).record(1);
  const auto old = analyse(oldPlan, oldRecording.pcm, oldRecording.channels, oldRecording.rate, oldRecording.clock,
                           oldRecording.toLeader);
  // the old fixed slot takes the loud chirp's tail: this is the misattribution being regressed
  REQUIRE(old[1].code == ArrivalCode::Ok);

  auto plan = makeProbePlan(ids, 1'000'000'000);
  plan.probe.amplitude = 0.003;
  for (std::size_t recorder = 0; recorder < room.endpoints.size(); ++recorder) {
    const auto heard = analyseRecording(plan, RoomSim(room, plan).record(recorder), recorder);
    INFO("recorder " << recorder);
    CHECK((heard[1].code == ArrivalCodeV2::Missed || heard[1].code == ArrivalCodeV2::Weak));
    CHECK(heard[1].code != ArrivalCodeV2::Ok);
  }
}

TEST_CASE("v2 regression: a 380 ms output is found with its bluetooth prior") {
  Room room;
  room.endpoints = {
      {.id = "speaker", .outputS = 0.010, .inputS = 0.003, .x = 0},
      {.id = "bt", .outputS = 0.380, .inputS = 0.008, .x = 2},
  };
  room.micChannels = 2;
  room.stampJitterS = 0.001;
  const std::vector<std::string> ids{"speaker", "bt"};
  const auto oldPlan = makePlan(ids, 1'000'000'000);
  const auto oldRecording = RoomSim(room, oldPlan).record(0);
  const auto old = analyse(oldPlan, oldRecording.pcm, oldRecording.channels, oldRecording.rate, oldRecording.clock,
                           oldRecording.toLeader);
  REQUIRE(old[1].code != ArrivalCode::Ok);

  const auto plan = makeProbePlan(ids, 1'000'000'000, {0, 250'000'000}, {150'000'000, 200'000'000});
  std::vector<std::vector<HeardV2>> heard;
  for (std::size_t recorder = 0; recorder < room.endpoints.size(); ++recorder) {
    heard.push_back(analyseRecording(plan, RoomSim(room, plan).record(recorder), recorder));
    REQUIRE(heard.back()[1].code == ArrivalCodeV2::Ok);
  }
  const auto solution = solveV2(plan, heard);
  REQUIRE(solution.code == ResultCode::Ok);
  const auto want = expectedOffsets(room);
  for (std::size_t i = 0; i < want.size(); ++i)
    CHECK(std::abs(solution.members[i].offsetNs - want[i]) < 50'000);
}

TEST_CASE("v2 regression: a louder reflection and late read jitter do not move the direct path") {
  for (std::size_t count : {2u, 3u}) {
    DYNAMIC_SECTION("members " << count) {
      Room room;
      room.endpoints = {
          {.id = "a", .outputS = 0.010, .inputS = 0.003, .x = 0, .y = 0},
          {.id = "b", .outputS = 0.043, .inputS = 0.011, .x = 2.4, .y = 0.3, .clockOffsetS = 17.2, .clockPpm = 18},
          {.id = "c", .outputS = 0.082, .inputS = 0.006, .x = 0.7, .y = 3.1, .clockOffsetS = -4.1, .clockPpm = -23},
      };
      room.endpoints.resize(count);
      room.reflections = {{0.004, std::pow(10.0, 3.0 / 20.0)}};
      room.micChannels = 3;
      room.stampJitterS = 0.003;
      room.snrDb = 55;
      std::vector<std::string> ids;
      for (const auto& endpoint : room.endpoints)
        ids.push_back(endpoint.id);
      const auto plan = makeProbePlan(ids, 1'000'000'000);
      const auto want = expectedOffsets(room);
      std::vector<std::vector<HeardV2>> heard;
      for (std::size_t recorder = 0; recorder < count; ++recorder)
        heard.push_back(analyseRecording(plan, RoomSim(room, plan).record(recorder), recorder));
      const auto solution = solveV2(plan, heard);
      INFO("residual " << solution.residualNs);
      REQUIRE(solution.code == ResultCode::Ok);
      for (std::size_t i = 0; i < count; ++i) {
        INFO("member " << ids[i]);
        CHECK(std::abs(solution.members[i].offsetNs - want[i]) < 50'000);
        CHECK(solution.members[i].ci95Ns < 250'000);
      }
    }
  }
}

TEST_CASE("v2 decimation: 48 khz and 16 khz renderings report the same arrivals") {
  Room room;
  room.endpoints = {
      {.id = "a", .outputS = 0.013, .inputS = 0.004, .x = 0},
      {.id = "b", .outputS = 0.071, .inputS = 0.009, .x = 2.1, .y = 0.4},
  };
  room.reflections = {{0.003, 0.8}};
  room.micChannels = 2;
  room.snrDb = 60;
  const auto plan = makeProbePlan({"a", "b"}, 1'000'000'000);
  const auto fine = analyseRecording(plan, RoomSim(room, plan).record(0), 0);
  room.rate = 48000;
  const auto high = analyseRecording(plan, RoomSim(room, plan).record(0), 0);
  REQUIRE(fine.size() == high.size());
  for (std::size_t i = 0; i < fine.size(); ++i) {
    REQUIRE(fine[i].code == ArrivalCodeV2::Ok);
    REQUIRE(high[i].code == ArrivalCodeV2::Ok);
    CHECK(std::abs(high[i].tNs - fine[i].tNs) < 20'000);
  }
}

TEST_CASE("v2 performance: decimated coded window analysis beats the legacy two-pass analysis") {
  Room room;
  room.endpoints = {
      {.id = "a", .outputS = 0.010, .inputS = 0.003, .x = 0},
      {.id = "b", .outputS = 0.047, .inputS = 0.008, .x = 2.2},
      {.id = "c", .outputS = 0.095, .inputS = 0.005, .x = 0.8, .y = 3.0},
  };
  room.rate = 48000;
  room.micChannels = 8;
  room.snrDb = 60;
  room.stampJitterS = 0.001;
  const std::vector<std::string> ids{"a", "b", "c"};
  const auto oldPlan = makePlan(ids, 1'000'000'000);
  const auto oldRecording = RoomSim(room, oldPlan).record(0);
  const auto startedOld = std::chrono::steady_clock::now();
  const auto old = analyse(oldPlan, oldRecording.pcm, oldRecording.channels, oldRecording.rate, oldRecording.clock,
                           oldRecording.toLeader);
  const auto oldUs = std::chrono::duration_cast<std::chrono::microseconds>(std::chrono::steady_clock::now() - startedOld);

  const auto plan = makeProbePlan(ids, 1'000'000'000);
  const auto recording = RoomSim(room, plan).record(0);
  const auto startedNew = std::chrono::steady_clock::now();
  const auto current = analyseRecording(plan, recording, 0);
  const auto newUs = std::chrono::duration_cast<std::chrono::microseconds>(std::chrono::steady_clock::now() - startedNew);
  WARN("calibration analysis old_us=" << oldUs.count() << " new_us=" << newUs.count());
  REQUIRE(old.size() == current.size());
  for (const auto& heard : current)
    REQUIRE(heard.code == ArrivalCodeV2::Ok);
  CHECK(newUs < oldUs / 2);
}

TEST_CASE("v2 fixtures: the typescript port has exact probes and small coded scenes") {
  cadence::sync::calibration::test::checkCalibrationFixtures();
}
