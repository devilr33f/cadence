#pragma once

#include <cstdint>
#include <optional>

namespace cadence::screen::player {

// the group's media clock as audiod's tap gives it (GroupClock, multiroom P4): the media position heard at a
// CLOCK_MONOTONIC instant, the stream's epoch, and whether the group plays there
struct FollowClock {
  std::int64_t mediaNs = 0;
  std::int64_t monoNs = 0;
  std::uint32_t epoch = 0;
  bool playing = false;
};

// the player as a follower reads it at a step
struct FollowReading {
  // time-pos now (run on from its last reading), ms; none before the file is loaded
  std::optional<double> positionMs;
  // a seek is under way
  bool seeking = false;
};

// what a step asks of the player; each one only when it changes
struct FollowAction {
  std::optional<double> seekMs;
  std::optional<bool> pause;
  std::optional<double> speed;
};

// how the follower does, for its log line (numbers only, rule 8)
struct FollowStats {
  double errorMs = 0;
  double speed = 1;
  std::uint64_t seeks = 0;
};

// a follower screen's picture slaved to the group's media timeline (multiroom P4): its mpv plays no audio, and this
// keeps its time-pos (less the display's delay) on the position the group hears. pure, clock-injected: the player
// steps it on every loop turn with the monotonic time and its reading, and carries out what it answers.
//  - no clock, a clock gone stale, or a group that stopped: the player waits paused, at the group's position when it
//    is more than seekErrorMs off;
//  - a start, or a new epoch (a seek, a new track, a resume) more than epochErrorMs off, or the player more than
//    seekErrorMs behind: a seek ahead of the group by how long seeks have taken, then a hold;
//  - the player ahead (after such a seek, or past seekErrorMs): it holds paused until the group reaches it;
//  - else its speed is steered, proportional to the smoothed error and within maxSpeedDelta of 1.
class VideoFollower {
 public:
  struct Config {
    double maxSpeedDelta = 0.05;
    // the speed change per second of smoothed error
    double gainPerS = 0.5;
    // the newest error's weight in the smoothed one
    double smoothing = 0.3;
    // an error the speed does not steer away: a seek or a hold does
    double seekErrorMs = 300;
    // a new epoch further off than this is sought
    double epochErrorMs = 80;
    // a smoothed error under this leaves the speed at 1
    double deadbandMs = 4;
    // how long a picture takes from mpv to the glass
    double displayDelayMs = 0;
    // a clock older than this is lost
    std::int64_t staleNs = 1'500'000'000;
    // a seek's lead is learned from how long seeks took, up to this
    double maxLeadMs = 3000;
  };

  VideoFollower() : VideoFollower(Config{}) {}
  explicit VideoFollower(Config config) : config_(config) {}

  // the tap's newest clock
  void clock(const FollowClock& clock) { clock_ = clock; }
  // a new item: nothing of the last one's holds
  void reset();
  FollowAction step(std::int64_t nowMono, const FollowReading& reading);
  [[nodiscard]] FollowStats stats() const { return {errorMs_, speed_, seeks_}; }
  // the group's position at nowMono, none without a live clock
  [[nodiscard]] std::optional<double> groupMs(std::int64_t nowMono) const;

 private:
  void pause(FollowAction& action, bool paused);
  void steer(FollowAction& action, double speed);
  void seek(FollowAction& action, double targetMs, std::int64_t nowMono);

  Config config_;
  std::optional<FollowClock> clock_;
  // what the player was last asked: paused, its speed
  bool paused_ = false;
  bool pauseAsked_ = false;
  double speed_ = 1;
  bool speedAsked_ = false;
  // the epoch the player was last put on, and whether it ever was
  std::uint32_t epoch_ = 0;
  bool placed_ = false;
  // a seek asked and not landed yet: when, and how long seeks take (the next one's lead)
  std::optional<std::int64_t> seekAskedNs_;
  double leadMs_ = 0;
  // holding paused until the group reaches the player
  bool holding_ = false;
  double smoothedMs_ = 0;
  double errorMs_ = 0;
  std::uint64_t seeks_ = 0;
};

}  // namespace cadence::screen::player
