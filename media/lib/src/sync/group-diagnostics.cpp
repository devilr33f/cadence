#include "sync/group-diagnostics.h"

#include <algorithm>
#include <cstdlib>

namespace cadence::sync {

namespace {

// samples a window keeps at most (30 s at 10 Hz is 300): no growth on a thread that missed its summary
constexpr std::size_t kMaxSamples = 1024;

std::uint32_t median(std::vector<std::uint32_t>& values) {
  if (values.empty())
    return 0;
  const auto mid = values.begin() + static_cast<std::ptrdiff_t>(values.size() / 2);
  std::nth_element(values.begin(), mid, values.end());
  return *mid;
}

}  // namespace

std::string_view causeToken(GroupCause cause) {
  switch (cause) {
    case GroupCause::Placement:
      return "placement";
    case GroupCause::Underrun:
      return "underrun";
    case GroupCause::ClockLost:
      return "clock_lost";
    case GroupCause::ClockStep:
      return "clock_step";
    case GroupCause::Gap:
      return "gap";
    case GroupCause::Link:
      return "link";
    case GroupCause::Lost:
      return "lost";
    case GroupCause::Step:
      return "step";
    case GroupCause::Unsynced:
      return "unsynced";
    case GroupCause::Synced:
      return "synced";
  }
  return "unknown";
}

GroupDiagnostics::GroupDiagnostics(GroupDiagnosticsConfig config) : config_(config) {
  clockErrors_.reserve(kMaxSamples);
  rttP50s_.reserve(kMaxSamples);
}

bool GroupDiagnostics::admit(GroupCause cause, std::int64_t now, std::uint64_t& suppressed) {
  const auto i = static_cast<std::size_t>(cause);
  ++counts_[i];
  if (logged_[i] && now - lastLogged_[i] < config_.perCauseNs) {
    ++held_[i];
    return false;
  }
  logged_[i] = true;
  lastLogged_[i] = now;
  suppressed = held_[i];
  held_[i] = 0;
  return true;
}

bool GroupDiagnostics::sampleDue(std::int64_t now) const {
  return !sampledAt_ || now - *sampledAt_ >= config_.sampleEveryNs;
}

void GroupDiagnostics::sample(std::int64_t now, const GroupSample& s) {
  sampledAt_ = now;
  if (!windowFrom_)
    windowFrom_ = now;
  if (s.mapped && clockErrors_.size() < kMaxSamples)
    clockErrors_.push_back(s.clockErrorUs);
  if (s.rttMinUs != 0)
    rttMinUs_ = rttMinUs_ == 0 ? s.rttMinUs : std::min(rttMinUs_, s.rttMinUs);
  if (s.rttP50Us != 0 && rttP50s_.size() < kMaxSamples)
    rttP50s_.push_back(s.rttP50Us);
  if (s.playing && std::llabs(s.placementErrorUs) > std::llabs(placementMaxUs_))
    placementMaxUs_ = s.placementErrorUs;
  if (s.delayFrames != 0) {
    delayMin_ = delayMin_ == 0 ? s.delayFrames : std::min(delayMin_, s.delayFrames);
    delayMax_ = std::max(delayMax_, s.delayFrames);
  }
}

std::optional<GroupSummary> GroupDiagnostics::summary(std::int64_t now) {
  if (!windowFrom_) {
    windowFrom_ = now;
    return std::nullopt;
  }
  if (now - *windowFrom_ < config_.summaryEveryNs)
    return std::nullopt;
  GroupSummary s;
  s.periodMs = (now - *windowFrom_) / 1'000'000;
  s.counts = counts_;
  s.clockErrorMaxUs = clockErrors_.empty() ? 0 : *std::max_element(clockErrors_.begin(), clockErrors_.end());
  s.clockErrorP50Us = median(clockErrors_);
  s.rttMinUs = rttMinUs_;
  s.rttP50Us = median(rttP50s_);
  s.placementErrorMaxUs = placementMaxUs_;
  s.delayMinFrames = delayMin_;
  s.delayMaxFrames = delayMax_;
  restart(now);
  return s;
}

void GroupDiagnostics::restart(std::int64_t now) {
  windowFrom_ = now;
  counts_ = {};
  clockErrors_.clear();
  rttP50s_.clear();
  rttMinUs_ = 0;
  placementMaxUs_ = 0;
  delayMin_ = 0;
  delayMax_ = 0;
}

}  // namespace cadence::sync
