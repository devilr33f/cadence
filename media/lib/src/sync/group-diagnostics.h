#pragma once

#include <array>
#include <cstddef>
#include <cstdint>
#include <optional>
#include <string>
#include <string_view>
#include <vector>

// why a member's audio or clock dropped, for its logs: each drop and each clock event as it happens (a cause at most
// once per few seconds, the rest counted), and a summary every 30 s while it plays. numbers and fixed tokens alone
// (rule 8)
namespace cadence::sync {

enum class GroupCause : std::uint8_t {
  // the playout's rejoins (rejoinToken)
  Placement,
  Underrun,
  ClockLost,
  ClockStep,
  Gap,
  // the leader's link dropped while it played, and came back: a rejoin from the backlog
  Link,
  // the clock's mapping: withdrawn (no reply for its lostAfter), stepped, its error past the budget, under it again
  Lost,
  Step,
  Unsynced,
  Synced,
};
inline constexpr std::size_t kGroupCauses = 10;

std::string_view causeToken(GroupCause cause);

// a group_rejoin line
struct GroupRejoinNote {
  GroupCause cause = GroupCause::Placement;
  // the link's own token for a link rejoin (closed, unreachable, protocol, ...), empty otherwise
  std::string link;
  std::int64_t errorUs = 0;
  std::int64_t delayFrames = 0;
  std::int64_t delayMinFrames = 0;
  std::int64_t delayMaxFrames = 0;
  std::uint32_t queueMs = 0;
  std::int64_t playedMs = 0;
  std::uint32_t clockErrorUs = 0;
  std::uint32_t rejoins = 0;
  std::uint32_t underruns = 0;
  // lines of this cause held back since the last one that went out
  std::uint64_t suppressed = 0;
};

// a group_clock line
struct GroupClockNote {
  GroupCause cause = GroupCause::Synced;
  // a step's jump (the new estimate minus the published line)
  std::int64_t stepUs = 0;
  std::uint32_t errorUs = 0;
  std::uint32_t budgetUs = 0;
  std::uint32_t rttMinUs = 0;
  std::uint32_t rttP50Us = 0;
  std::uint32_t samples = 0;
  std::int32_t ppm = 0;
  std::uint64_t suppressed = 0;
};

// what the clock and the output looked like at one moment, sampled a few times a second
struct GroupSample {
  bool mapped = false;
  std::uint32_t clockErrorUs = 0;
  // 0: none known
  std::uint32_t rttMinUs = 0;
  std::uint32_t rttP50Us = 0;
  bool playing = false;
  std::int64_t placementErrorUs = 0;
  // 0: none known
  std::int64_t delayFrames = 0;
};

// a group_summary line
struct GroupSummary {
  std::int64_t periodMs = 0;
  std::array<std::uint32_t, kGroupCauses> counts{};
  std::uint32_t clockErrorP50Us = 0;
  std::uint32_t clockErrorMaxUs = 0;
  std::uint32_t rttMinUs = 0;
  std::uint32_t rttP50Us = 0;
  std::int64_t placementErrorMaxUs = 0;
  std::int64_t delayMinFrames = 0;
  std::int64_t delayMaxFrames = 0;
};

struct GroupDiagnosticsConfig {
  // a cause logs at most once per this; the rest are counted
  std::int64_t perCauseNs = 5'000'000'000;
  std::int64_t summaryEveryNs = 30'000'000'000;
  std::int64_t sampleEveryNs = 100'000'000;
};

// one thread (the member's playout thread); now is mono ns
class GroupDiagnostics {
 public:
  explicit GroupDiagnostics(GroupDiagnosticsConfig config = {});

  // counts the cause; true when its line goes out now, with suppressed set to how many were held back before it
  bool admit(GroupCause cause, std::int64_t now, std::uint64_t& suppressed);
  // whether a sample is due (sampleEveryNs since the last)
  [[nodiscard]] bool sampleDue(std::int64_t now) const;
  void sample(std::int64_t now, const GroupSample& sample);
  // the summary once summaryEveryNs passed since the window began, which then begins again
  std::optional<GroupSummary> summary(std::int64_t now);
  // the window begins again with nothing in it (a member set aside: its idle time is no part of a summary)
  void restart(std::int64_t now);

 private:
  GroupDiagnosticsConfig config_;
  std::array<std::int64_t, kGroupCauses> lastLogged_{};
  std::array<bool, kGroupCauses> logged_{};
  std::array<std::uint64_t, kGroupCauses> held_{};
  std::array<std::uint32_t, kGroupCauses> counts_{};
  std::optional<std::int64_t> windowFrom_;
  std::optional<std::int64_t> sampledAt_;
  std::vector<std::uint32_t> clockErrors_;
  std::vector<std::uint32_t> rttP50s_;
  std::uint32_t rttMinUs_ = 0;
  std::int64_t placementMaxUs_ = 0;
  std::int64_t delayMin_ = 0;
  std::int64_t delayMax_ = 0;
};

}  // namespace cadence::sync
