#include "audiod/ring-pump.h"

#include "dsp/format.h"
#include "sync/clock-socket.h"

#include <algorithm>
#include <chrono>
#include <cmath>
#include <vector>

namespace cadence::audiod {

namespace {

constexpr auto kPassEvery = std::chrono::milliseconds(5);

std::int64_t framesNs(std::uint64_t frames) {
  return static_cast<std::int64_t>(frames * 1'000'000'000ULL / dsp::kRate);
}

}  // namespace

RingPump::RingPump(RingInput& ring, GroupFeed& feed) : ring_(ring), feed_(feed) {
  floorRing_ = ring_.readPosition();
  feed_.pumping(true);
  thread_ = std::thread([this] { run(); });
}

RingPump::~RingPump() {
  stop_ = true;
  if (thread_.joinable())
    thread_.join();
  feed_.pumping(false);
}

void RingPump::run() {
  std::vector<float> buffer(kReadFrames * dsp::kStereo);
  while (!stop_) {
    pass(buffer);
    publish();
    std::this_thread::sleep_for(kPassEvery);
  }
}

void RingPump::pass(std::span<float> buffer) {
  auto& input = feed_.input();
  while (!stop_ && input.status().queued + kReadFrames <= kFeedBoundFrames) {
    bool flushed = false;
    const auto n = ring_.drain(buffer, flushed);
    if (flushed) {
      // a seek of the player's: the group flushes first, so the clear starts the next epoch with what comes after it
      feed_.flushed();
      input.clear();
      anchored_ = false;
      floorRing_ = ring_.readPosition() - n;
      holding_ = true;
      firstNs_ = 0;
    }
    if (n == 0)
      return;
    if (holding_ && firstNs_ == 0) {
      firstNs_ = sync::monoNs();
      firstPos_ = ring_.readPosition() - n;
    }
    input.push(buffer.first(n * dsp::kStereo), static_cast<std::uint32_t>(dsp::kRate), dsp::kStereo);
    anchorRing_ = ring_.readPosition();
    anchorFeed_ = input.status().writePos + static_cast<std::uint64_t>(std::llround(input.pendingFrames()));
    anchored_ = true;
  }
}

void RingPump::publish() {
  const auto now = sync::monoNs();
  if (!anchored_) {
    ring_.publishHeard(ring_.readPosition(), now + kFrozenAheadNs);
    return;
  }
  if (const auto heard = feed_.heardNs(anchorFeed_)) {
    if (holding_ && firstNs_ != 0)
      feed_.started(*heard - framesNs(anchorRing_ - firstPos_) - firstNs_);
    holding_ = false;
    firstNs_ = 0;
    ring_.publishHeard(anchorRing_, *heard);
    return;
  }
  if (!holding_) {
    holding_ = true;
    firstNs_ = 0;
  }
  // frozen at the frame the group plays next: the feed's first unread, as the ring counts it
  const auto unread = static_cast<std::int64_t>(anchorFeed_) - static_cast<std::int64_t>(feed_.input().readPosition());
  const auto behind = static_cast<std::uint64_t>(std::clamp<std::int64_t>(
      unread, 0, static_cast<std::int64_t>(anchorRing_ - std::min(anchorRing_, floorRing_))));
  ring_.publishHeard(anchorRing_ - behind, now + kFrozenAheadNs);
}

}  // namespace cadence::audiod
