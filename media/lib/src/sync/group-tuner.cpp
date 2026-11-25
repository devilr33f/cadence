#include "sync/group-tuner.h"

#include <algorithm>
#include <array>
#include <cmath>
#include <utility>

namespace cadence::sync {

namespace {

constexpr std::uint32_t kDefaultKbps = 256;
constexpr std::uint32_t kDefaultWaitMs = 3000;
constexpr std::array<std::uint32_t, 4> kLadder{256, 192, 128, 96};

std::uint32_t roundUp50(std::uint32_t ms) { return (ms + 49) / 50 * 50; }

}  // namespace

GroupTuner::GroupTuner(GroupTunerConfig config)
    : config_(config),
      latencyMs_(std::clamp(config.latencyMs, kTuneLatencyMinMs, kTuneLatencyMaxMs)),
      pendingMs_(latencyMs_) {
  const auto ceiling = config_.bitrateKbps == 0 ? kDefaultKbps : config_.bitrateKbps;
  ladder_.push_back(ceiling);
  for (const auto rung : kLadder)
    if (rung < ceiling)
      ladder_.push_back(rung);
}

std::uint32_t GroupTuner::bitrateKbps() const {
  if (config_.pcm)
    return 0;
  return config_.autoBitrate ? ladder_[rung_] : ladder_.front();
}

std::uint32_t GroupTuner::waitMs() const {
  const auto ceiling = config_.waitMs == 0 ? kDefaultWaitMs : config_.waitMs;
  if (!config_.autoWait)
    return ceiling;
  std::optional<std::uint32_t> most;
  for (const auto& [id, delays] : ready_)
    for (const auto d : delays)
      most = std::max(most.value_or(0), d);
  if (!most)
    return ceiling;
  const auto wait = static_cast<std::uint32_t>(std::lround(static_cast<double>(*most) * 1.5)) + 300;
  return std::min(std::max(wait, kTuneWaitMinMs), ceiling);
}

std::optional<GroupTuned> GroupTuner::tick(std::int64_t now, bool playing) {
  const auto dt = lastTickNs_ ? std::max<std::int64_t>(now - *lastTickNs_, 0) : 0;
  lastTickNs_ = now;
  if (playing) {
    playedNs_ += dt;
    cleanNs_ += dt;
  }
  if (config_.autoLatency && pendingMs_ == latencyMs_ && playedNs_ >= kTuneLowerAfterNs) {
    if (minQueueMs_ && *minQueueMs_ >= latencyMs_ / 2) {
      const auto cur = static_cast<std::int64_t>(latencyMs_);
      const auto step = std::max<std::int64_t>(100, std::lround(static_cast<double>(cur) * 0.15));
      const auto floor = 2 * (cur - static_cast<std::int64_t>(*minQueueMs_)) + 150;
      const auto target = std::max({cur - step, std::int64_t{kTuneLatencyMinMs}, floor});
      if (target < cur) {
        pendingMs_ = static_cast<std::uint32_t>(target);
        pendingReason_ = "margin";
      }
    }
    lowerWindowRestart();
  }
  if (!config_.autoBitrate || config_.pcm)
    return std::nullopt;
  const bool wanted = std::exchange(stepDownWanted_, false);
  if (wanted && rung_ + 1 < ladder_.size() &&
      (!lastStepDownNs_ || now - *lastStepDownNs_ >= kTuneStepDownEveryNs)) {
    const auto from = ladder_[rung_++];
    lastStepDownNs_ = now;
    cleanNs_ = 0;
    return GroupTuned{"bitrate", from, ladder_[rung_], stepDownReason_};
  }
  if (rung_ > 0 && cleanNs_ >= kTuneStepUpAfterNs) {
    const auto from = ladder_[rung_--];
    cleanNs_ = 0;
    return GroupTuned{"bitrate", from, ladder_[rung_], "clean"};
  }
  return std::nullopt;
}

std::optional<GroupTuned> GroupTuner::epochStarted(std::int64_t now) {
  epochNs_ = now;
  if (pendingMs_ == latencyMs_)
    return std::nullopt;
  GroupTuned out{"latency", latencyMs_, pendingMs_, pendingReason_};
  latencyMs_ = pendingMs_;
  // the queues the old latency left say nothing of the new one's margin
  lowerWindowRestart();
  return out;
}

void GroupTuner::memberStatus(const std::string& id, std::uint32_t underruns, std::uint32_t queueMs, std::int64_t now,
                              bool playing) {
  const auto found = underruns_.find(id);
  const bool rose = found != underruns_.end() && underruns > found->second;
  underruns_[id] = underruns;
  if (rose && playing)
    raise(now, "underrun");
  if (playing && epochNs_ && now - *epochNs_ >= kTuneSettleNs)
    minQueueMs_ = std::min(minQueueMs_.value_or(queueMs), queueMs);
}

void GroupTuner::memberBacklog(const std::string& /*id*/, std::uint32_t unsentMs, std::int64_t now, bool playing) {
  if (!playing || !epochNs_ || now - *epochNs_ < kTuneSettleNs || unsentMs <= kTuneBacklogMs)
    return;
  stepDownWanted_ = true;
  stepDownReason_ = "backlog";
  cleanNs_ = 0;
}

void GroupTuner::memberRejoined(const std::string& id, std::int64_t now, bool playing) {
  if (playing)
    raise(now, "rejoin");
  auto& times = rejoins_[id];
  times.push_back(now);
  while (!times.empty() && now - times.front() > kTuneRejoinWindowNs)
    times.pop_front();
  if (times.size() >= 2) {
    times.clear();
    stepDownWanted_ = true;
    stepDownReason_ = "rejoin";
    cleanNs_ = 0;
  }
}

void GroupTuner::ownUnderruns(std::uint64_t count, std::int64_t now, bool playing) {
  const bool rose = ownUnderruns_ && count > *ownUnderruns_;
  ownUnderruns_ = count;
  if (rose && playing)
    raise(now, "underrun");
}

void GroupTuner::memberGone(const std::string& id) { underruns_.erase(id); }

void GroupTuner::memberReady(const std::string& id, std::uint32_t delayMs) {
  auto& delays = ready_[id];
  delays.push_back(delayMs);
  while (delays.size() > kTuneReadySamples)
    delays.pop_front();
}

void GroupTuner::memberTimedOut(const std::string& id) {
  memberReady(id, config_.waitMs == 0 ? kDefaultWaitMs : config_.waitMs);
}

void GroupTuner::raise(std::int64_t now, std::string_view reason) {
  if (!config_.autoLatency)
    return;
  lowerWindowRestart();
  if (lastRaiseNs_ && now - *lastRaiseNs_ < kTuneRaiseEveryNs)
    return;
  const auto base = pendingMs_;
  const auto grown = std::max(base + 250, static_cast<std::uint32_t>(std::ceil(static_cast<double>(base) * 1.5)));
  const auto target = std::min(kTuneLatencyMaxMs, roundUp50(grown));
  if (target <= base)
    return;
  pendingMs_ = target;
  pendingReason_ = reason;
  lastRaiseNs_ = now;
}

void GroupTuner::lowerWindowRestart() {
  playedNs_ = 0;
  minQueueMs_.reset();
}

}  // namespace cadence::sync
