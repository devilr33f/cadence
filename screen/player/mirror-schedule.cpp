#include "player/mirror-schedule.h"

#include <algorithm>

namespace cadence::screen::player {

namespace {

bool contains(const std::vector<std::string>& ids, const std::string& id) {
  return std::find(ids.begin(), ids.end(), id) != ids.end();
}

}  // namespace

MirrorSchedule::Input MirrorSchedule::frame(const std::string& sessionId, bool config, bool keyframe,
                                            std::span<const std::uint8_t> data) {
  if (sessionId.empty() || contains(ended_, sessionId)) return Input::Skip;
  if (sessionId != session_) {
    // a session the screen has not shown: its own config first
    session_ = sessionId;
    config_.clear();
    configured_ = false;
    needKeyframe_ = true;
  }
  if (config) {
    if (data.empty()) return Input::Skip;
    if (configured_ && std::equal(data.begin(), data.end(), config_.begin(), config_.end())) return Input::Skip;
    config_.assign(data.begin(), data.end());
    configured_ = true;
    needKeyframe_ = true;
    return Input::Configure;
  }
  if (!configured_) return Input::Skip;
  if (needKeyframe_) {
    if (!keyframe) return Input::Skip;
    needKeyframe_ = false;
  }
  return Input::Decode;
}

void MirrorSchedule::start(const std::string& sessionId) {
  ended_.erase(std::remove(ended_.begin(), ended_.end(), sessionId), ended_.end());
}

bool MirrorSchedule::stop(const std::string& sessionId) {
  const bool shown = !session_.empty() && (sessionId.empty() || sessionId == session_);
  const std::string& id = sessionId.empty() ? session_ : sessionId;
  if (!id.empty() && !contains(ended_, id)) {
    ended_.push_back(id);
    // a handful remembered: a sender's session ids are not reused
    if (ended_.size() > 8) ended_.erase(ended_.begin());
  }
  if (shown) {
    session_.clear();
    config_.clear();
    configured_ = false;
    needKeyframe_ = true;
  }
  return shown;
}

void MirrorSchedule::lost() { needKeyframe_ = true; }

void MirrorSchedule::clock(std::int64_t mediaNs, std::int64_t monoNs, std::int64_t latencyNs) {
  mediaNs_ = mediaNs;
  monoNs_ = monoNs;
  latencyNs_ = latencyNs;
  anchored_ = monoNs > 0;
}

std::int64_t MirrorSchedule::waitBeforeDecode(std::int64_t ptsNs, std::int64_t nowNs) const {
  if (!anchored_ || latencyNs_ <= kDecodeAheadNs) return 0;
  const std::int64_t ahead = monoNs_ + (ptsNs - mediaNs_) - nowNs;
  if (ahead <= kDecodeAheadNs || ahead > kMaxWaitNs) return 0;
  return ahead - kDecodeAheadNs;
}

MirrorSchedule::Release MirrorSchedule::output(std::int64_t ptsNs, std::int64_t nowNs) {
  if (!anchored_) return {Release::Kind::RenderNow, nowNs, false};
  const std::int64_t at = monoNs_ + (ptsNs - mediaNs_);
  if (at > nowNs + kMaxAheadNs) {
    // held, it would never come due before the decoder ran out of buffers
    lateRun_ = 0;
    return {Release::Kind::RenderNow, at, true};
  }
  if (at < nowNs - kLateNs) {
    if (++lateRun_ <= kMaxLateRun) return {Release::Kind::Drop, at, false};
    return {Release::Kind::RenderNow, at, true};
  }
  lateRun_ = 0;
  if (at > nowNs + kLeadNs) return {Release::Kind::Hold, at, false};
  return {Release::Kind::Render, std::max(at, nowNs), false};
}

MirrorCodec codecOfConfig(std::span<const std::uint8_t> annexB) {
  // the first nal after a 3- or 4-byte start code
  std::size_t i = 0;
  while (i + 2 < annexB.size() && !(annexB[i] == 0 && annexB[i + 1] == 0 && annexB[i + 2] == 1)) i++;
  if (i + 3 >= annexB.size()) return MirrorCodec::Unknown;
  const std::uint8_t header = annexB[i + 3];
  const int h265 = (header >> 1) & 0x3f;
  const int h264 = header & 0x1f;
  // h265's forbidden bit is the top one, as h264's: tell them by the types each stream starts its config with
  if (h265 == 32 || h265 == 33) return MirrorCodec::H265;
  if (h264 == 7) return MirrorCodec::H264;
  return MirrorCodec::Unknown;
}

}  // namespace cadence::screen::player
