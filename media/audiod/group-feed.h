#pragma once

#include "backend/pcm-input.h"
#include "backend/presentation-clock.h"
#include "sync/group-leader.h"

#include <atomic>
#include <cstdint>
#include <functional>
#include <optional>
#include <utility>

namespace cadence::audiod {

// a group leader's feed as the sources outside the media sessions' pushes see it, while this device leads: a MIXER
// external session's ring is read into it by a ring pump (RingPump) instead of the mixer, and the tap's presentation
// clock (airplay mirroring's pictures) carries the group's lead. every call is safe from any thread
class GroupFeed {
 public:
  virtual ~GroupFeed() = default;

  // the input the sessions push into while the group leads
  [[nodiscard]] virtual PcmInput& input() = 0;
  // CLOCK_MONOTONIC when the group hears the input's frame at pos (a PcmInput position); none before the running
  // epoch's T0
  [[nodiscard]] virtual std::optional<std::int64_t> heardNs(std::uint64_t pos) const = 0;
  // how long from now until the group hears the next frame the input gives (the latency while no epoch runs)
  [[nodiscard]] virtual std::int64_t leadNs() const = 0;
  // a ring pump started (on the loop thread, as the session's output opens) or stopped reading into the input
  virtual void pumping(bool on) = 0;
  // the pump's ring was flushed by its player (a seek), on the pump's thread: the group flushes before the pump clears
  // the input, so the audio after the flush starts the next epoch
  virtual void flushed() = 0;
  // on the pump's thread: the group first heard the ring's audio delayNs after the pump took it (an epoch's start)
  virtual void started(std::int64_t delayNs) = 0;
};

// the presentation clock's sample on a group's leader (airplay mirroring's pictures on a tv that leads): its sources'
// audio is heard the group's lead from now (leadNs) rather than after this device's own output latency, which the
// sample holds already, so the media time it names sounds that much later and the pictures wait as long
inline PresentationSample heardByGroup(PresentationSample sample, std::int64_t leadNs) {
  const auto extra = leadNs > sample.latencyNs ? leadNs - sample.latencyNs : 0;
  sample.monoNs += extra;
  sample.latencyNs += extra;
  return sample;
}

// the leader's GroupFeed: GroupHandlers' while it leads. pumping, flushed and started go to the hooks too (logs, on
// whatever thread called)
class LeaderFeed final : public GroupFeed {
 public:
  // how far past the latency the leader reads a ring's feed: its player's audio runs that far ahead of its picture's
  // group-heard time, and a pause skips what was sent past it
  static constexpr std::int64_t kRingAheadNs = 100'000'000;

  struct Hooks {
    std::function<void(bool on)> pumping;
    std::function<void()> flushed;
    std::function<void(std::int64_t delayNs)> started;
  };

  LeaderFeed(sync::GroupLeader& leader, PcmInput& input, Hooks hooks = {})
      : leader_(leader), input_(input), hooks_(std::move(hooks)) {}

  // the media position the session says: a ring's epoch starts there, and a flush of the player's own goes there
  void setSessionPosition(std::uint64_t ms) { positionMs_.store(ms, std::memory_order_relaxed); }
  // a seek audiod asked of the player: the flush it makes goes to this position, once
  void setSeek(std::uint64_t ms) {
    seekMs_.store(ms, std::memory_order_relaxed);
    seek_.store(true, std::memory_order_release);
  }

  [[nodiscard]] PcmInput& input() override { return input_; }
  [[nodiscard]] std::optional<std::int64_t> heardNs(std::uint64_t pos) const override {
    return leader_.feedHeardNs(pos);
  }
  [[nodiscard]] std::int64_t leadNs() const override { return leader_.feedLeadNs(); }
  void pumping(bool on) override {
    leader_.setReadAhead(on ? std::optional<std::int64_t>(kRingAheadNs) : std::nullopt);
    if (on)
      leader_.setMediaPosition(positionMs_.load(std::memory_order_relaxed));
    if (hooks_.pumping)
      hooks_.pumping(on);
  }
  void flushed() override {
    const auto position = seek_.exchange(false, std::memory_order_acquire) ? seekMs_.load(std::memory_order_relaxed)
                                                                           : positionMs_.load(std::memory_order_relaxed);
    leader_.flush(position);
    if (hooks_.flushed)
      hooks_.flushed();
  }
  void started(std::int64_t delayNs) override {
    if (hooks_.started)
      hooks_.started(delayNs);
  }

 private:
  sync::GroupLeader& leader_;
  PcmInput& input_;
  Hooks hooks_;
  std::atomic<std::uint64_t> positionMs_{0};
  std::atomic<std::uint64_t> seekMs_{0};
  std::atomic<bool> seek_{false};
};

}  // namespace cadence::audiod
