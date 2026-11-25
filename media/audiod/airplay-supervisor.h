#pragma once

#include "audiod/airplay-metadata.h"
#include "audiod/airplay-pipe.h"
#include "audiod/airplay-session.h"
#include "audiod/airplay-stack.h"
#include "audiod/log.h"
#include "audiod/media-source.h"
#include "backend/pcm-input.h"
#include "dbus/client.h"
#include "dbus/message.h"

#include <chrono>
#include <cstdint>
#include <functional>
#include <memory>
#include <optional>
#include <string>
#include <string_view>

namespace cadence::audiod {

enum class AirplayState : std::uint8_t { Off, Starting, Ready, Failed };

// what AirplayStatus carries
struct AirplayStatus {
  AirplayState state = AirplayState::Off;
  // a fixed token with Failed (airplay::k…, or session_lost)
  std::string code;

  bool operator==(const AirplayStatus&) const = default;
};

inline constexpr std::string_view kAirplaySessionLost = airplay::kSessionLost;

// what the supervisor reports, on its thread: an implementation hands it to the loop (Loop::post) and never blocks
struct AirplayHooks {
  std::function<void(const AirplayStatus& status)> status;
};

// what the receiver's media source plugs in, called on the supervisor's thread: the connection to the private bus once
// shairport owns its name there, every signal it gets while attached, what shairport's metadata pipe says, and the end
// of it. while attached, its deadline wakes the supervisor for its tick, and a failure it reports ends the receiver
// like a daemon that exited, Failed with that code
class AirplayReceiver {
 public:
  using Clock = std::chrono::steady_clock;

  virtual ~AirplayReceiver() = default;
  // client stays valid until detached; owner is shairport's unique name, whose properties alone count
  virtual void attached(dbus::Client& client, const std::string& owner) = 0;
  virtual void signal(const dbus::Message& message) = 0;
  // a daemon exited, the bus closed, shairport's name changed owner, the receiver failed, the config changed or
  // disabled it, or a stop
  virtual void detached() = 0;
  // the whole metadata each time the pipe changed it; memory only
  virtual void metadata(const MediaMetadata& /*metadata*/) {}
  // where the track stands, each time the pipe says
  virtual void progress(const airplay::Progress& /*progress*/) {}
  // while attached: when tick has something to do, and tick at or after it
  [[nodiscard]] virtual std::optional<Clock::time_point> deadline() const { return std::nullopt; }
  virtual void tick(Clock::time_point /*now*/) {}
  // while attached: a fixed token once the receiver is to be restarted, else empty
  [[nodiscard]] virtual std::string_view failure() const { return {}; }
};

// the airplay receiver under audiod (M4d-D2, reference §2): one long-lived thread runs its daemons (AirplayStack),
// polling a command queue, a stop fd and the private bus, with the backoff's timer. everything that blocks (an
// attempt's waits, a stop and its reaps) happens on that thread, never on the loop's, which only queues commands; an
// attempt's waits watch the queue, and a stop, a disable, a new config or a shut gate ends the attempt at once.
//
// the receiver starts once the config enables it and the playback gate is open (S0 §7.5: no media child before a volume
// held); it keeps running when the gate shuts, while its audio stops (see AirplayOutput). a failed attempt (Failed with
// its code), and a receiver that went away once up (Failed session_lost: a daemon exited, the bus closed, shairport's
// name changed owner; or Failed with the code the attached receiver failed with), stop every daemon, reaped before the
// next attempt, which comes after a wait of firstBackoff doubling to lastBackoff, from the first again after a steady
// run. a new config restarts it, a disabled one stops it. shairport's fifos are drained for as long as it runs,
// whatever the output does.
//
// logs carry event names, daemon names, codes, pids and counts: never the receiver's name, a client or metadata
class AirplaySupervisor {
 public:
  AirplaySupervisor(const Log& log, AirplayHooks hooks, AirplayOptions options = {});
  // stops it
  ~AirplaySupervisor();

  AirplaySupervisor(const AirplaySupervisor&) = delete;
  AirplaySupervisor& operator=(const AirplaySupervisor&) = delete;

  // before start
  void receiver(std::shared_ptr<AirplayReceiver> receiver);
  // starts the thread
  void start();
  // bounded: every daemon killed and reaped (or left to its thread after the reap wait) and the thread joined; nothing
  // is reported after it returns
  void stop();

  // any thread; queued for the supervisor's thread, in order. an identical config changes nothing
  void configure(AirplaySettings settings);
  // any thread: the audio follows the gate at once, the start logic once the thread takes it
  void gate(bool open);
  // any thread: runs task on the supervisor's thread, in order with the rest, where the receiver lives; dropped when
  // the supervisor stops first
  void post(std::function<void()> task);

  // any thread: shairport's audio into music (the session's target: the music input or a group leader's feed) while
  // airplay owns the session and the receiver does not hold it, or drained unheard
  void openOutput(PcmInput& music);
  void closeOutput();
  void holdOutput(bool held);
  // see AirplayOutput::pause, release and pauses
  std::uint64_t pauseOutput();
  void releaseOutput(std::uint64_t pausesTaken);
  [[nodiscard]] std::uint64_t outputPauses() const;

 private:
  class Worker;
  std::unique_ptr<Worker> worker_;
};

}  // namespace cadence::audiod
