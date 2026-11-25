#pragma once

#include "sync/group-playout.h"

#include <cstdint>
#include <optional>

namespace cadence::audiod {

// a group's media timeline as this device hears it (the tap's GroupClock, multiroom P4): the source's position heard
// at a CLOCK_MONOTONIC instant, the epoch it is of, and whether the group plays there or stopped
struct GroupClockSample {
  std::int64_t mediaNs = 0;
  std::int64_t monoNs = 0;
  std::uint32_t epoch = 0;
  bool playing = false;
};

// a frame heard this long ago, and none after it, is where the group stopped: a frame and a few lost ones past it
inline constexpr std::int64_t kGroupClockRunOnNs = 100'000'000;

// the clock at nowMono from the playout's frame heard last (GroupPlayout::heardAt): its media position runs on from
// its due time while it is recent, and stands at its end once it is not; none before any frame was heard
inline std::optional<GroupClockSample> groupClockAt(const std::optional<sync::PlacedFrame>& heard,
                                                    std::int64_t nowMono) {
  if (!heard || heard->dueMonoNs > nowMono)
    return std::nullopt;
  const auto mediaNs = static_cast<std::int64_t>(heard->mediaMs) * 1'000'000;
  const auto since = nowMono - heard->dueMonoNs;
  if (since <= kGroupClockRunOnNs)
    return GroupClockSample{mediaNs + since, nowMono, heard->epoch, true};
  return GroupClockSample{mediaNs + kGroupFrameNs, nowMono, heard->epoch, false};
}

}  // namespace cadence::audiod
