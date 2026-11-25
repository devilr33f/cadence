#include "player/state-report.h"

#include <algorithm>
#include <cmath>
#include <cstdlib>

namespace cadence::screen::player {

bool worthReporting(const PlayerState& a, const PlayerState& b) {
  if (a.sessionId != b.sessionId || a.purpose != b.purpose || a.state != b.state || a.rate != b.rate ||
      a.durationMs != b.durationMs || a.tracks != b.tracks || a.audioRoute != b.audioRoute ||
      a.audioControls != b.audioControls || a.controlLatencyMs != b.controlLatencyMs || a.errorCode != b.errorCode ||
      a.gated != b.gated || a.ended != b.ended || a.seekSeq != b.seekSeq || a.seekPending != b.seekPending) {
    return true;
  }
  const double expected = static_cast<double>(a.positionMs) +
                          static_cast<double>(b.positionAtMs - a.positionAtMs) * static_cast<double>(a.rate);
  if (std::fabs(static_cast<double>(b.positionMs) - expected) > 1000.0) return true;
  // waiting for the cache began or ended
  if (a.bufferingPercent.has_value() != b.bufferingPercent.has_value()) return true;
  // the buffer and the cache's fill: at most one a second
  if (b.positionAtMs - a.positionAtMs < kBufferReportMs) return false;
  if (a.bufferingPercent != b.bufferingPercent) return true;
  const auto buffered = static_cast<std::int64_t>(b.bufferedMs) - static_cast<std::int64_t>(a.bufferedMs);
  const auto step = std::max<std::int64_t>(kBufferReportMs, static_cast<std::int64_t>(a.bufferedMs / 5));
  return std::llabs(buffered) >= step;
}

cadence_v2_PlaybackState playbackStateFor(const MpvFlags& f) {
  if (!f.hasSession || f.ended) return cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE;
  if (f.paused) return cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED;
  if (!f.loaded || f.pausedForCache || f.seeking) return cadence_v2_PlaybackState_PLAYBACK_STATE_BUFFERING;
  return cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING;
}

}  // namespace cadence::screen::player
