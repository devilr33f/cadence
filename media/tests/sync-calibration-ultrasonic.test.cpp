// ultrasonic probes: no audible leak, and arrivals under music-like masking without decimating their band away
#include "support/room-sim.h"
#include "sync/calibration.h"

#include <catch2/catch_approx.hpp>
#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <chrono>
#include <cmath>
#include <complex>
#include <vector>

using namespace cadence::sync::calibration;
using namespace cadence::sync::calibration::test;

namespace {

std::vector<HeardV2> analyseRecording(const Plan& plan, const Recording& recording, std::size_t self) {
  return analyse(plan, recording.pcm, recording.channels, recording.rate, self, recording.clock, recording.toLeader);
}

Room maskedRoom() {
  Room room;
  room.rate = 48000;
  room.endpoints = {
      {.id = "a", .outputS = 0.013, .inputS = 0.004, .x = 0},
      {.id = "b", .outputS = 0.071, .inputS = 0.009, .x = 2.1, .y = 0.4},
  };
  room.micChannels = 2;
  // 0.5 rms of pink and white masking against a -30 dbfs probe: 30 db over its mean level, mostly below 16 khz
  room.snrDb = -20.0 * std::log10(0.5 / 0.1);
  room.musicMasking = true;
  room.stampJitterS = 0.001;
  return room;
}

Plan ultrasonicPlan(Room& room, std::uint32_t rounds = 2) {
  std::vector<std::string> ids;
  for (const auto& endpoint : room.endpoints)
    ids.push_back(endpoint.id);
  auto plan = makeProbePlan(ids, 1'000'000'000, {}, {}, kSlotSpacingNs, rounds);
  plan.probe = ultrasonicProbe();
  return plan;
}

}  // namespace

TEST_CASE("ultrasonic probe: its spectrum stays ultrasonic and leaks nothing audible above -60 db") {
  const auto shape = ultrasonicProbe();
  CHECK(shape.bandLoHz == Catch::Approx(18000));
  CHECK(shape.bandHiHz == Catch::Approx(20500));
  CHECK(probeDuration(shape) == Catch::Approx((kGoldChips + 16.0) / 2500.0));
  CHECK(shape.amplitude == Catch::Approx(std::pow(10.0, -30.0 / 20.0)));
  const auto pcm = probeSamples(shape, 7, 48000);
  std::vector<std::complex<float>> transformed(65536);
  for (std::size_t n = 0; n < pcm.size(); ++n)
    transformed[n] = {pcm[n], 0};
  Fft fft;
  fft(transformed, false);
  double peak = 0, audible = 0, lowGuard = 0, highGuard = 0;
  for (std::size_t k = 1; k < transformed.size() / 2; ++k) {
    const double hz = static_cast<double>(k) * 48000.0 / static_cast<double>(transformed.size());
    const double energy = std::norm(transformed[k]);
    peak = std::max(peak, energy);
    if (hz < 16000)
      audible = std::max(audible, energy);
    if (hz < 17500)
      lowGuard += energy;
    if (hz > 21000)
      highGuard += energy;
  }
  REQUIRE(peak > 0);
  CHECK(10.0 * std::log10(audible / peak) < -60);
  // the guard energies may be small, but the passband itself must dominate them decisively
  double inside = 0;
  for (std::size_t k = 1; k < transformed.size() / 2; ++k) {
    const double hz = static_cast<double>(k) * 48000.0 / static_cast<double>(transformed.size());
    if (hz >= 17500 && hz <= 21000)
      inside += std::norm(transformed[k]);
  }
  CHECK(lowGuard < inside * 0.01);
  CHECK(highGuard < inside * 0.01);
}

TEST_CASE("ultrasonic detector: baseband conversion adds no group delay to a clean arrival") {
  auto room = maskedRoom();
  room.musicMasking = false;
  room.snrDb = 60;
  room.stampJitterS = 0;
  const auto plan = ultrasonicPlan(room);
  const auto heard = analyseRecording(plan, RoomSim(room, plan).record(0), 0);
  REQUIRE(heard[0].code == ArrivalCodeV2::Ok);
  // room-sim stamps a read 700 us after its frames; the pairwise solve cancels that recorder delay
  const double expected = RoomSim(room, plan).arrivalS(0, 0, 0) * 1e9 + 700'000;
  CHECK(std::abs(static_cast<double>(heard[0].tNs) - expected) < 50'000);
}

TEST_CASE("ultrasonic detector: masked 48 khz arrivals land within 50 us and identity survives another code") {
  auto room = maskedRoom();
  const auto plan = ultrasonicPlan(room);
  std::vector<std::vector<HeardV2>> heard;
  for (std::size_t recorder = 0; recorder < room.endpoints.size(); ++recorder) {
    heard.push_back(analyseRecording(plan, RoomSim(room, plan).record(recorder), recorder));
    REQUIRE(heard.back().size() == room.endpoints.size());
    for (std::size_t source = 0; source < room.endpoints.size(); ++source) {
      INFO("recorder " << recorder << " source " << source << " code " << token(heard.back()[source].code));
      REQUIRE(heard.back()[source].code == ArrivalCodeV2::Ok);
      CHECK(heard.back()[source].hasRelative == (source != recorder));
      const double expected = RoomSim(room, plan).arrivalS(source, source, recorder) * 1e9 + 700'000;
      CHECK(std::abs(static_cast<double>(heard.back()[source].tNs) - expected) < 50'000);
    }
  }
  const auto solution = solveV2(plan, heard);
  REQUIRE(solution.code == ResultCode::Ok);
  const double mean = (room.endpoints[0].outputS + room.endpoints[1].outputS) / 2;
  for (std::size_t i = 0; i < solution.members.size(); ++i)
    CHECK(std::abs(solution.members[i].offsetNs - (room.endpoints[i].outputS - mean) * 1e9) < 50'000);
}

TEST_CASE("ultrasonic detector: a silent source is missed, never credited with its neighbour") {
  auto room = maskedRoom();
  room.endpoints[1].silent = true;
  room.snrDb = 60;
  const auto plan = ultrasonicPlan(room);
  for (std::size_t recorder = 0; recorder < room.endpoints.size(); ++recorder) {
    const auto heard = analyseRecording(plan, RoomSim(room, plan).record(recorder), recorder);
    REQUIRE(heard[0].code == ArrivalCodeV2::Ok);
    CHECK(heard[1].code != ArrivalCodeV2::Ok);
    CHECK((heard[1].code == ArrivalCodeV2::Missed || heard[1].code == ArrivalCodeV2::Weak));
  }
}

TEST_CASE("ultrasonic detector: a 44.1 khz recorder is unsupported") {
  auto room = maskedRoom();
  room.rate = 44100;
  room.snrDb = 60;
  const auto plan = ultrasonicPlan(room);
  const auto heard = analyseRecording(plan, RoomSim(room, plan).record(0), 0);
  REQUIRE(heard.size() == room.endpoints.size());
  for (const auto& source : heard)
    CHECK(source.code == ArrivalCodeV2::Unsupported);
}

TEST_CASE("a probe table plays what probeAt says, far cheaper per sample") {
  using namespace cadence::sync::calibration;
  for (const auto& shape : {ultrasonicProbe(), ProbeShape{}}) {
    const ProbeTable table(shape, 7);
    const auto samples = static_cast<std::size_t>(std::ceil(probeDuration(shape) * 48000.0));
    double peak = 0, error = 0;
    for (std::size_t n = 0; n < samples; ++n) {
      // an offset off the table's grid, as a slot on the group's timeline is
      const double t = (static_cast<double>(n) + 0.37) / 48000.0;
      const double exact = probeAt(shape, 7, t);
      peak = std::max(peak, std::abs(exact));
      error = std::max(error, std::abs(table.at(t) - exact));
    }
    INFO("band " << shape.bandLoHz << " peak " << peak << " error " << error);
    REQUIRE(peak > 0);
    // under -40 db of the probe: a receiver correlates against the exact waveform, so this costs it nothing
    CHECK(error < peak * 0.01);
  }
  // the pump's budget: a second of 48 khz output in well under a tenth of a second on the host
  const ProbeTable table(ultrasonicProbe(), 3);
  const auto started = std::chrono::steady_clock::now();
  double sum = 0;
  for (std::size_t n = 0; n < 48000; ++n)
    sum += table.at(static_cast<double>(n % 19000) / 48000.0);
  const auto ms = std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - started).count();
  INFO("48000 samples in " << ms << " ms, sum " << sum);
  CHECK(ms < 50);
}
