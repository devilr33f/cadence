#include "player/video-follow.h"

#include <algorithm>
#include <cmath>

namespace cadence::screen::player {

void VideoFollower::reset() {
  const auto speed = speed_;
  const auto speedAsked = speedAsked_;
  *this = VideoFollower(config_);
  // the player keeps its speed across items: it is set back to 1 by the next step that steers
  speed_ = speed;
  speedAsked_ = speedAsked;
}

std::optional<double> VideoFollower::groupMs(std::int64_t nowMono) const {
  if (!clock_ || nowMono - clock_->monoNs > config_.staleNs) return std::nullopt;
  const auto since = clock_->playing ? std::max<std::int64_t>(nowMono - clock_->monoNs, 0) : 0;
  return static_cast<double>(clock_->mediaNs + since) / 1e6;
}

void VideoFollower::pause(FollowAction& action, bool paused) {
  if (pauseAsked_ && paused_ == paused) return;
  paused_ = paused;
  pauseAsked_ = true;
  action.pause = paused;
}

void VideoFollower::steer(FollowAction& action, double speed) {
  if (speedAsked_ && std::fabs(speed - speed_) < 0.001) return;
  speed_ = speed;
  speedAsked_ = true;
  action.speed = speed;
}

void VideoFollower::seek(FollowAction& action, double targetMs, std::int64_t nowMono) {
  action.seekMs = std::max(0.0, targetMs);
  seekAskedNs_ = nowMono;
  ++seeks_;
  placed_ = true;
  epoch_ = clock_->epoch;
  holding_ = true;
  smoothedMs_ = 0;
  pause(action, true);
  steer(action, 1);
}

FollowAction VideoFollower::step(std::int64_t nowMono, const FollowReading& reading) {
  FollowAction action;
  const auto group = groupMs(nowMono);
  if (!group) {
    // no group heard: the picture waits where it is
    pause(action, true);
    steer(action, 1);
    return action;
  }
  if (!reading.positionMs || reading.seeking) {
    // loading, or a seek under way: held until it lands
    pause(action, true);
    return action;
  }
  if (seekAskedNs_) {
    // a seek landed: how long it took is the next one's lead
    const auto took = static_cast<double>(nowMono - *seekAskedNs_) / 1e6;
    leadMs_ = std::clamp(leadMs_ == 0 ? took : 0.5 * leadMs_ + 0.5 * took, 0.0, config_.maxLeadMs);
    seekAskedNs_.reset();
  }
  const double error = *reading.positionMs - config_.displayDelayMs - *group;
  errorMs_ = error;
  if (!clock_->playing) {
    // the group stopped: the picture waits on its position
    if (std::fabs(error) > config_.seekErrorMs) {
      seek(action, *group + config_.displayDelayMs, nowMono);
      // a stop is no epoch to seek to again once it plays
      holding_ = false;
      return action;
    }
    pause(action, true);
    steer(action, 1);
    return action;
  }
  const bool newEpoch = !placed_ || clock_->epoch != epoch_;
  if ((newEpoch && std::fabs(error) > config_.epochErrorMs) || error < -config_.seekErrorMs) {
    seek(action, *group + config_.displayDelayMs + leadMs_, nowMono);
    return action;
  }
  placed_ = true;
  epoch_ = clock_->epoch;
  if (error > config_.seekErrorMs) holding_ = true;
  if (holding_) {
    // ahead: the picture waits for the group to reach it
    if (error > 0) {
      pause(action, true);
      steer(action, 1);
      return action;
    }
    holding_ = false;
    smoothedMs_ = error;
  }
  pause(action, false);
  smoothedMs_ += config_.smoothing * (error - smoothedMs_);
  double speed = 1;
  if (std::fabs(smoothedMs_) >= config_.deadbandMs) {
    speed = 1 - std::clamp(smoothedMs_ / 1000 * config_.gainPerS, -config_.maxSpeedDelta, config_.maxSpeedDelta);
  }
  steer(action, speed);
  return action;
}

}  // namespace cadence::screen::player
