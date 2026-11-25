#include "support/calibration-fixtures.h"

#include "support/room-sim.h"

#include <catch2/catch_approx.hpp>
#include <catch2/catch_test_macros.hpp>
#include <nlohmann/json.hpp>

#include <algorithm>
#include <array>
#include <cmath>
#include <cstdint>
#include <cstdlib>
#include <filesystem>
#include <fstream>
#include <string>
#include <vector>

namespace cadence::sync::calibration::test {

namespace {

using Json = nlohmann::json;
const std::filesystem::path kRoot = CADENCE_CALIBRATION_FIXTURES;

bool update() {
  const auto* value = std::getenv("CADENCE_UPDATE_FIXTURES");
  return value != nullptr && std::string(value) == "1";
}

void write(const std::string& name, const Json& document) {
  std::ofstream out(kRoot / name);
  out << document.dump() << '\n';
  REQUIRE(out.good());
}

Json read(const std::string& name) {
  std::ifstream in(kRoot / name);
  REQUIRE(in.good());
  return Json::parse(in);
}

std::string encode(std::span<const std::int16_t> pcm) {
  constexpr char alphabet[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
  std::string out;
  out.reserve((pcm.size() * 2 + 2) / 3 * 4);
  std::vector<std::uint8_t> bytes;
  bytes.reserve(pcm.size() * 2);
  for (const auto sample : pcm) {
    const auto u = static_cast<std::uint16_t>(sample);
    bytes.push_back(static_cast<std::uint8_t>(u & 0xff));
    bytes.push_back(static_cast<std::uint8_t>(u >> 8));
  }
  for (std::size_t k = 0; k < bytes.size(); k += 3) {
    const auto left = bytes.size() - k;
    const std::uint32_t value = static_cast<std::uint32_t>(bytes[k]) << 16 |
                                (left > 1 ? static_cast<std::uint32_t>(bytes[k + 1]) << 8 : 0) |
                                (left > 2 ? bytes[k + 2] : 0);
    out.push_back(alphabet[(value >> 18) & 63]);
    out.push_back(alphabet[(value >> 12) & 63]);
    out.push_back(left > 1 ? alphabet[(value >> 6) & 63] : '=');
    out.push_back(left > 2 ? alphabet[value & 63] : '=');
  }
  return out;
}

std::vector<std::int16_t> decode(const std::string& text) {
  std::array<int, 256> values{};
  values.fill(-1);
  const std::string alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
  for (std::size_t k = 0; k < alphabet.size(); ++k)
    values[static_cast<std::uint8_t>(alphabet[k])] = static_cast<int>(k);
  std::vector<std::uint8_t> bytes;
  std::uint32_t value = 0;
  int bits = 0;
  for (const char c : text) {
    if (c == '=')
      break;
    const int digit = values[static_cast<std::uint8_t>(c)];
    REQUIRE(digit >= 0);
    value = value << 6 | static_cast<std::uint32_t>(digit);
    bits += 6;
    if (bits >= 8) {
      bits -= 8;
      bytes.push_back(static_cast<std::uint8_t>((value >> bits) & 0xff));
    }
  }
  REQUIRE(bytes.size() % 2 == 0);
  std::vector<std::int16_t> out;
  out.reserve(bytes.size() / 2);
  for (std::size_t k = 0; k < bytes.size(); k += 2) {
    const auto u = static_cast<std::uint16_t>(bytes[k] | static_cast<std::uint16_t>(bytes[k + 1]) << 8);
    out.push_back(static_cast<std::int16_t>(u));
  }
  return out;
}

Json shapeJson() {
  const ProbeShape shape;
  return {{"kind", "gold"},
          {"bandLoHz", shape.bandLoHz},
          {"bandHiHz", shape.bandHiHz},
          {"fadeS", shape.fadeS},
          {"amplitude", shape.amplitude},
          {"rollOff", kGoldRollOff},
          {"pulseHalfChips", kGoldPulseChips}};
}

void checkShape(const Json& json) {
  CHECK(json.at("kind").get<std::string>() == "gold");
  CHECK(json.at("bandLoHz").get<double>() == Catch::Approx(1000));
  CHECK(json.at("bandHiHz").get<double>() == Catch::Approx(6000));
  CHECK(json.at("fadeS").get<double>() == Catch::Approx(0.01));
  CHECK(json.at("amplitude").get<double>() == Catch::Approx(0.1));
  CHECK(json.at("rollOff").get<double>() == Catch::Approx(0.25));
  CHECK(json.at("pulseHalfChips").get<std::size_t>() == kGoldPulseChips);
}

Json probes() {
  Json document{{"version", 1}, {"shape", shapeJson()}, {"probes", Json::array()}};
  for (const std::uint32_t code : {0u, 1u, 7u, 31u}) {
    for (const double rate : {16000.0, 48000.0}) {
      const auto pcm = probeSamples(ProbeShape{}, code, rate);
      double sum = 0, energy = 0;
      for (const auto sample : pcm) {
        sum += sample;
        energy += static_cast<double>(sample) * sample;
      }
      const auto first = std::min<std::size_t>(64, pcm.size());
      document["probes"].push_back({{"code", code},
                                    {"rate", rate},
                                    {"count", pcm.size()},
                                    {"first64", std::vector<float>(pcm.begin(), pcm.begin() + static_cast<long>(first))},
                                    {"sum", sum},
                                    {"energy", energy}});
    }
  }
  return document;
}

void checkProbes(const Json& document) {
  CHECK(document.at("version").get<int>() == 1);
  checkShape(document.at("shape"));
  std::size_t seen = 0;
  for (const auto& entry : document.at("probes")) {
    const auto code = entry.at("code").get<std::uint32_t>();
    const auto rate = entry.at("rate").get<double>();
    const auto pcm = probeSamples(ProbeShape{}, code, rate);
    REQUIRE(entry.at("count").get<std::size_t>() == pcm.size());
    double sum = 0, energy = 0;
    for (const auto sample : pcm) {
      sum += sample;
      energy += static_cast<double>(sample) * sample;
    }
    const auto& first = entry.at("first64");
    REQUIRE(first.size() == 64);
    for (std::size_t k = 0; k < first.size(); ++k)
      CHECK(first[k].get<double>() == Catch::Approx(pcm[k]).margin(1e-6));
    CHECK(entry.at("sum").get<double>() == Catch::Approx(sum).margin(1e-6));
    CHECK(entry.at("energy").get<double>() == Catch::Approx(energy).margin(1e-6));
    ++seen;
  }
  CHECK(seen == 8);
}

Room sceneRoom(bool reflection) {
  Room room;
  room.endpoints = reflection ? std::vector<Endpoint>{{.id = "a", .outputS = 0.020, .inputS = 0.003, .x = 0},
                                                      {.id = "b", .outputS = 0.090, .inputS = 0.008, .x = 2.0}}
                              : std::vector<Endpoint>{{.id = "a", .outputS = 0.012, .inputS = 0.003, .x = 0},
                                                      {.id = "b", .outputS = 0.047, .inputS = 0.008, .x = 2.0}};
  if (reflection)
    room.reflections = {{0.004, std::pow(10.0, 3.0 / 20.0)}};
  room.micChannels = 1;
  room.snrDb = 80;
  room.seed = reflection ? 202 : 101;
  room.recordFromS = 0;
  room.recordToS = 2.8;
  return room;
}

Json scene(bool reflection) {
  auto room = sceneRoom(reflection);
  const auto plan = makeProbePlan({"a", "b"}, 250'000'000, {}, {}, kSlotSpacingNs, 2);
  const RoomSim sim(room, plan);
  const auto recording = sim.record(0);
  Json document{{"version", 1},
                {"rate", recording.rate},
                {"channels", recording.channels},
                {"pcmLe16", encode(recording.pcm)},
                {"clock", {{"originNs", recording.clock.originNs}, {"nsPerFrame", recording.clock.nsPerFrame}}},
                {"toLeader", {{"offsetNs", 0}, {"ppm", 0}}},
                {"stamps", Json::array()},
                {"expected", Json::array()}};
  document["plan"] = {{"members", plan.members},
                      {"slotNs", plan.slotNs},
                      {"rounds", plan.rounds},
                      {"probeCodes", plan.probeCodes},
                      {"priorNs", plan.priorNs},
                      {"priorSpanNs", plan.priorSpanNs},
                      {"probe", shapeJson()}};
  for (const auto& stamp : recording.stamps)
    document["stamps"].push_back({{"endFrame", stamp.endFrame}, {"monoNs", stamp.monoNs}});
  for (std::size_t i = 0; i < plan.members.size(); ++i)
    // the room's read stamps carry a fixed 700 us return delay, which the fitted sample clock retains
    document["expected"].push_back({{"source", plan.members[i]},
                                    {"arrivalNs", sim.arrivalS(i, i, 0) * 1e9 - static_cast<double>(plan.slot(0, i)) +
                                                      700'000},
                                    {"toleranceNs", 50'000}});
  return document;
}

void checkScene(const Json& document) {
  Plan plan;
  const auto& p = document.at("plan");
  plan.members = p.at("members").get<std::vector<std::string>>();
  plan.slotNs = p.at("slotNs").get<std::vector<std::int64_t>>();
  plan.rounds = p.at("rounds").get<std::uint32_t>();
  plan.probeCodes = p.at("probeCodes").get<std::vector<std::uint32_t>>();
  plan.priorNs = p.at("priorNs").get<std::vector<std::int64_t>>();
  plan.priorSpanNs = p.at("priorSpanNs").get<std::vector<std::int64_t>>();
  plan.probe = ProbeShape{};
  checkShape(p.at("probe"));
  const auto rate = document.at("rate").get<double>();
  const auto channels = document.at("channels").get<std::size_t>();
  const auto pcm = decode(document.at("pcmLe16").get<std::string>());
  REQUIRE(pcm.size() % channels == 0);
  std::vector<ReadStamp> stamps;
  for (const auto& stamp : document.at("stamps"))
    stamps.push_back({stamp.at("endFrame").get<std::uint64_t>(), stamp.at("monoNs").get<std::int64_t>()});
  const auto fitted = fitSampleClock(stamps, rate);
  SampleClock clock{document.at("clock").at("originNs").get<double>(),
                    document.at("clock").at("nsPerFrame").get<double>()};
  CHECK(fitted.originNs == Catch::Approx(clock.originNs).margin(1));
  CHECK(fitted.nsPerFrame == Catch::Approx(clock.nsPerFrame).margin(1e-6));
  const double offsetNs = document.at("toLeader").at("offsetNs").get<double>();
  const double ppm = document.at("toLeader").at("ppm").get<double>();
  const auto toLeader = [=](double localNs) { return (localNs - offsetNs) / (1.0 + ppm * 1e-6); };
  const auto heard = analyse(plan, pcm, channels, rate, 0, clock, toLeader);
  REQUIRE(heard.size() == plan.members.size());
  for (const auto& expected : document.at("expected")) {
    const auto id = expected.at("source").get<std::string>();
    const auto found = std::find(plan.members.begin(), plan.members.end(), id);
    REQUIRE(found != plan.members.end());
    const auto i = static_cast<std::size_t>(found - plan.members.begin());
    REQUIRE(heard[i].code == ArrivalCodeV2::Ok);
    const double arrival = static_cast<double>(heard[i].tNs - plan.slot(0, i));
    CHECK(arrival == Catch::Approx(expected.at("arrivalNs").get<double>())
                         .margin(expected.at("toleranceNs").get<double>()));
  }
}

}  // namespace

void checkCalibrationFixtures() {
  if (update()) {
    std::filesystem::create_directories(kRoot);
    write("probes.json", probes());
    write("scene-stereo.json", scene(false));
    write("scene-reflection.json", scene(true));
    return;
  }
  checkProbes(read("probes.json"));
  checkScene(read("scene-stereo.json"));
  checkScene(read("scene-reflection.json"));
  std::uintmax_t bytes = 0;
  for (const auto& entry : std::filesystem::directory_iterator(kRoot))
    bytes += entry.file_size();
  CHECK(bytes < 300'000);
}

}  // namespace cadence::sync::calibration::test
