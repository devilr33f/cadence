#pragma once

#include "audiod/group-feed.h"
#include "ring/ring-input.h"

#include <atomic>
#include <cstdint>
#include <thread>

namespace cadence::audiod {

// a MIXER external session's ring read into a group leader's feed instead of the mixer (P2: the screen's player
// feeds the group), on a thread of its own. it reads up to 20 ms at a time while the feed holds less than
// kFeedBoundFrames (its backpressure: the leader's timeline decides how far ahead the feed is read), and pushes the
// frames as they are: the session's own gain stays off them, as the device's mixer ducks and mutes what the group
// plays.
//
// after each pass it publishes on the ring when the group hears the frame at its read position (the feed's heard time,
// GroupFeed::heardNs), so the player's a/v sync holds its picture back by the group's latency. while no epoch runs
// (before T0, a pause, a stall) it publishes the frame the group plays next as frozen, kFrozenAheadNs from now, as the
// mixer's refill hold does, so the player holds its picture there. a flush of the player's (a seek) flushes the group
// and clears the feed. made and destroyed on the loop thread: it tells the feed pumping(true) before its thread starts
// and pumping(false) after it was joined
class RingPump {
 public:
  static constexpr std::size_t kReadFrames = 960;
  static constexpr std::uint64_t kFeedBoundFrames = 4800;
  static constexpr std::int64_t kFrozenAheadNs = 20'000'000;

  RingPump(RingInput& ring, GroupFeed& feed);
  ~RingPump();
  RingPump(const RingPump&) = delete;
  RingPump& operator=(const RingPump&) = delete;

 private:
  void run();
  void pass(std::span<float> buffer);
  void publish();

  RingInput& ring_;
  GroupFeed& feed_;
  std::atomic<bool> stop_{false};
  std::thread thread_;

  // the thread's own: the last frames pushed end at ring position anchorRing_, and the ring's next frame lands at the
  // feed's anchorFeed_ (its write position past what its resampler still holds)
  bool anchored_ = false;
  std::uint64_t anchorRing_ = 0;
  std::uint64_t anchorFeed_ = 0;
  // the ring's position after its last flush: nothing before it plays again
  std::uint64_t floorRing_ = 0;
  // no epoch hears the ring's audio yet; the first frame pumped since, when and where, for the start's delay
  bool holding_ = true;
  std::int64_t firstNs_ = 0;
  std::uint64_t firstPos_ = 0;
};

}  // namespace cadence::audiod
