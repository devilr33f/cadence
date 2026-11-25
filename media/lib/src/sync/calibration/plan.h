#pragma once

#include "sync/calibration/probe.h"

#include <algorithm>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <string>
#include <vector>

namespace cadence::sync::calibration {

// slots are round-major: slot r * members + k belongs to member k, on the leader's timeline
struct Plan {
  std::vector<std::string> members;
  std::uint32_t rounds = 1;
  std::vector<std::int64_t> slotNs;
  // each slot's boost over the probe's amplitude, db (empty: none)
  std::vector<std::uint32_t> gainDb;
  ChirpShape chirp;
  // coded plans use probe; chirp stays for the legacy wire and api
  ProbeShape probe;
  std::vector<std::uint32_t> probeCodes;
  // per member, an output latency estimate and how far either side of it an arrival may fall
  std::vector<std::int64_t> priorNs;
  std::vector<std::int64_t> priorSpanNs;
  // coded arrivals whose 95% interval is wider than this are unstable
  std::int64_t toleranceNs = 2'000'000;
  // a version-3 manual run's extra ultrasonic round, one slot index per member; zero marks an unsupported output
  std::optional<ProbeShape> ultrasonic;
  std::vector<std::int64_t> ultrasonicSlotNs;
  std::vector<std::uint32_t> ultrasonicCodes;
  // sources without a slot in a one-round plan (an output that cannot carry ultrasound)
  std::vector<bool> sourceSkipped;
  // the legacy fixed search window
  std::int64_t searchBeforeNs = 100'000'000;
  std::int64_t searchAfterNs = 300'000'000;

  [[nodiscard]] std::int64_t slot(std::size_t round, std::size_t member) const {
    return slotNs[round * members.size() + member];
  }
  [[nodiscard]] std::uint32_t gain(std::size_t index) const { return index < gainDb.size() ? gainDb[index] : 0; }
  [[nodiscard]] std::uint32_t probeCode(std::size_t index) const {
    return index < probeCodes.size() ? probeCodes[index] : static_cast<std::uint32_t>(index % std::max<std::size_t>(members.size(), 1));
  }
  [[nodiscard]] std::int64_t prior(std::size_t member) const { return member < priorNs.size() ? priorNs[member] : 0; }
  [[nodiscard]] std::int64_t priorSpan(std::size_t member) const {
    return member < priorSpanNs.size() ? priorSpanNs[member] : 150'000'000;
  }
  [[nodiscard]] bool skipped(std::size_t member) const {
    return member < sourceSkipped.size() && sourceSkipped[member];
  }
  [[nodiscard]] bool hasUltrasonic() const { return ultrasonic.has_value() && ultrasonicSlotNs.size() == members.size(); }
  // the extra round as its own one-round plan, with this run's members, codes and output priors
  [[nodiscard]] Plan ultrasonicPlan() const;
  [[nodiscard]] std::int64_t sourceWindowFromNs(std::size_t round, std::size_t member) const {
    return slot(round, member) + prior(member) - priorSpan(member) - 20'000'000;
  }
  [[nodiscard]] std::int64_t sourceWindowToNs(std::size_t round, std::size_t member) const {
    return slot(round, member) + prior(member) + priorSpan(member) + 20'000'000;
  }
  [[nodiscard]] std::int64_t windowFromNs() const;
  [[nodiscard]] std::int64_t windowToNs() const;
};

inline constexpr std::int64_t kSlotSpacingNs = 700'000'000;
inline constexpr std::uint32_t kCalibrationRounds = 4;
inline constexpr std::size_t kMaxMembers = 8;
inline constexpr std::size_t kMaxSlots = 32;

// the legacy plan: identical chirps, fixed windows and the caller's spacing
[[nodiscard]] Plan makePlan(std::vector<std::string> members, std::int64_t startNs,
                            std::int64_t spacingNs = kSlotSpacingNs, std::uint32_t rounds = kCalibrationRounds);
// gold codes default to the member index; spacing grows to hold the widest latency window, never below 700 ms
[[nodiscard]] Plan makeProbePlan(std::vector<std::string> members, std::int64_t startNs,
                                 std::vector<std::int64_t> priorNs = {}, std::vector<std::int64_t> priorSpanNs = {},
                                 std::int64_t spacingNs = kSlotSpacingNs, std::uint32_t rounds = kCalibrationRounds);
// append the version-3 capability round after the audible slots; skipped members (bluetooth) get no slot
void addUltrasonicRound(Plan& plan, std::int64_t startNs, std::int64_t spacingNs = kSlotSpacingNs,
                        std::vector<bool> skipped = {});

}  // namespace cadence::sync::calibration
