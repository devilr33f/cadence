#pragma once

#include "player/player.h"

#include <cstdint>

namespace cadence::screen::player {

// a change of the buffer alone is reported at most this often
inline constexpr std::int64_t kBufferReportMs = 1000;

// whether next is worth a MediaState after last: anything but the clock moved, or the position left where last's
// would have run to by more than a second (a seek, a stall), or waiting for the cache began or ended. at most once a
// second (kBufferReportMs), the cache's fill changed while it waits, or the buffer by a second or a fifth of what it
// was, whichever is more (a full 60 s cache reports a 12 s change). a steady play sends nothing, as cadenced runs the
// position on from position_at_ms and rate
bool worthReporting(const PlayerState& last, const PlayerState& next);

// the playback state mpv's properties add up to
struct MpvFlags {
  bool hasSession = false;
  bool loaded = false;
  bool paused = false;
  bool pausedForCache = false;
  bool seeking = false;
  bool ended = false;
};
cadence_v2_PlaybackState playbackStateFor(const MpvFlags& flags);

}  // namespace cadence::screen::player
