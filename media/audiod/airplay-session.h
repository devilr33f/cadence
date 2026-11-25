#pragma once

#include "audiod/airplay-volume.h"
#include "audiod/log.h"
#include "audiod/media-source.h"
#include "audiod/sender-volume.h"

#include <chrono>
#include <cstdint>
#include <deque>
#include <optional>
#include <string>
#include <string_view>

namespace cadence::audiod {

namespace airplay {

// shairport's RemoteControl PlayerState (reference §3.1)
enum class PlayerState : std::uint8_t { Playing, Paused, Stopped, NotAvailable };
// nullopt for any other text, which ends the session as invalid
std::optional<PlayerState> playerState(std::string_view text);
// a fixed token for logs
std::string_view playerStateToken(PlayerState state);

// what one PropertiesChanged of the remote control said about the properties read, each set when it came
struct Change {
  // the sender's identity, empty for none: an opaque change detector, never logged or exposed
  std::optional<std::string> client;
  std::optional<PlayerState> state;
  std::optional<double> volumeDb;
};

// the properties as the connect read them
struct Snapshot {
  PlayerState state = PlayerState::NotAvailable;
  double volumeDb = 0;
  std::string client;
};

// the receiver's methods the session calls
enum class Request : std::uint8_t { Play, Pause, Next, Previous, DropSession, SetVolume };
std::string_view requestToken(Request request);

// a control the receiver could not take (v1's ControlUnsupported), as soloist answers it too
inline constexpr std::string_view kControlUnsupported = "control_unsupported";
// why a receiver ended, as AirplayStatus carries it: it went, or its session could not be dropped
inline constexpr std::string_view kSessionLost = "session_lost";
// it said something it cannot have: a PlayerState unknown, a property of the wrong type
inline constexpr std::string_view kInvalidState = "invalid_state";

}  // namespace airplay

// v1's schedule (reference §5, §6); tests shorten it
struct AirplaySessionTimings {
  // how long the receiver has to reflect a pause before its session is dropped (v1's PAUSE_REFLECTION)
  std::chrono::milliseconds pauseReflection{2000};
  // the sender's requests go out at most this often, the latest; the last always
  std::chrono::milliseconds coalesce = kSenderCoalesce;
};

// the receiver's state and its controls (M4d-D5, reference §3.4 and §6; v1's source.rs ported exactly), on one thread
// and driven with the time of each input, so it is pure: what it decides reaches the port.
//
// a session is the receiver's while its PlayerState is not Not Available. the output stays held unless the receiver
// genuinely plays: a Playing that follows anything else, the speaker's level known (a cast before the first level stays
// held and does not play) and not paused here, which playing() reports once. Paused and Stopped hold it and report
// paused; Not Available ends the session: held, its transients and generation gone, ended() reported once. a new
// client in a session is a new sender: its transients go and its Playing counts afresh.
//
// a pause asked on another thread holds the output there at once and is counted: until the session took that pause,
// nothing it decides releases the output again (a Play's ok or a Playing racing it included).
// a pause holds the output before anything else, reports paused, and asks the receiver to Pause; if the receiver still
// plays when the pause watchdog fires, its session is dropped (DropSession: ok ends it, a failure ends the receiver).
// a pause the receiver reflects, a resume or a new genuine Playing calls the watchdog off. a resume asks for Play and
// releases the output only once Play answered ok with the receiver playing. next and previous are only delivered.
// controls with no session answer control_unsupported. calls go out one at a time, in order, a volume write only when
// no other waits; a reply of an older session or command generation changes nothing.
//
// the sender's volume (M4d-D6, rule 7, reference §5) moves the speaker only as a deliberate slider move: with no
// session it is an observation only; one that comes while a write is in flight, or that a write may still bring back
// (the echo table), is ours; the first that differs from the speaker's level after the session, the sender or a
// genuine Playing came up is the level the sender's app stored, dropped and answered with the speaker's level; the
// rest are requests, one per coalesce interval, the latest. the speaker's level, which outlives receivers and their
// sessions, goes to the receiver on a genuine Playing, a new sender, a new session, a dropped first value and each new
// level from cadenced
class AirplaySession {
 public:
  using Clock = std::chrono::steady_clock;

  struct Call {
    std::uint64_t id = 0;
    airplay::Request request = airplay::Request::Play;
    // a SetVolume's
    double db = 0;
  };

  // what the session does: each on its own thread, never back into the session
  class Port {
   public:
    virtual ~Port() = default;
    // sends it; its reply comes back through reply(), the same id. false when it could not go out
    virtual bool call(const Call& call) = 0;
    // the output held, at once
    virtual void hold() = 0;
    // the output flowing as far as the receiver goes, unless a pause asked for since (pausesAsked) is one the session
    // has not taken yet: checked with the pause where it is counted, so the two never cross
    virtual void release(std::uint64_t pausesTaken) = 0;
    // the pauses asked for so far on other threads (AirplaySource), each held at once, which the session takes as
    // control(Pause) comes with its number
    [[nodiscard]] virtual std::uint64_t pausesAsked() const = 0;
    virtual void playing() = 0;
    virtual void paused() = 0;
    virtual void ended() = 0;
    // a deliberate slider move, for cadenced (SenderVolume)
    virtual void request(std::uint8_t level) = 0;
  };

  using Timings = AirplaySessionTimings;

  AirplaySession(const Log& log, Port& port, Timings timings = {});

  AirplaySession(const AirplaySession&) = delete;
  AirplaySession& operator=(const AirplaySession&) = delete;

  // a receiver came up and the connect read this
  void attach(const airplay::Snapshot& snapshot, Clock::time_point now);
  // the receiver went: its session ends, the speaker's level stays
  void detach();
  void change(const airplay::Change& change, Clock::time_point now);
  // the receiver said something it cannot have: the output is held and the receiver is to be restarted
  void invalid();
  // the reply to the call of that id
  void reply(std::uint64_t id, bool ok, Clock::time_point now);
  // pause, resume, next or previous; done, when set, gets the code once (any other action: unsupported). a pause
  // carries the number pausesAsked gave it, and is taken with or without a session
  void control(MediaAction action, CommandDone done, Clock::time_point now, std::uint64_t pauseNumber = 0);
  // the speaker's level (SenderVolumeSet)
  void speakerLevel(std::uint8_t level, Clock::time_point now);
  // at or after deadline()
  void tick(Clock::time_point now);

  [[nodiscard]] std::optional<Clock::time_point> deadline() const;
  // a session is up: PlayerState is not Not Available
  [[nodiscard]] bool live() const { return raw_ != airplay::PlayerState::NotAvailable; }
  // a fixed token once the receiver is to be restarted, else empty
  [[nodiscard]] std::string_view failure() const { return failure_; }

 private:
  enum class Effective : std::uint8_t { Idle, Playing, Paused };

  struct Pending {
    std::uint64_t id = 0;
    std::uint64_t generation = 0;
    std::uint64_t command = 0;
    airplay::Request request = airplay::Request::Play;
    double db = 0;
    CommandDone done;
  };

  struct Watchdog {
    Clock::time_point at;
    std::uint64_t session = 0;
    std::uint64_t command = 0;
  };

  void apply(const airplay::Change& change, bool initial, Clock::time_point now);
  void clearTransient();
  void sessionGone();
  void identityChanged();
  void onState(airplay::PlayerState state);
  void onVolume(double db, Clock::time_point now);
  void maybePlaying();
  // a fresh guard on the speaker's level, which drops the next value that differs
  void resetGuard();
  // the speaker's level goes to the receiver once nothing else waits, while a session is up
  void announceLevel();
  void request(std::uint8_t level);
  void pause(Clock::time_point now);
  void cancelWatchdog();
  void queue(airplay::Request request, CommandDone done = {});
  void pump(Clock::time_point now);
  // answers a call that will not be made or whose reply no longer counts
  static void drop(Pending& pending);

  const Log& log_;
  Port& port_;
  const Timings timings_;

  // lives as long as the session object: survives receivers and their sessions
  std::optional<std::uint8_t> level_;
  airplay::PlayerState raw_ = airplay::PlayerState::NotAvailable;
  std::string client_;
  Effective effective_ = Effective::Idle;
  bool localPaused_ = false;
  bool resumePending_ = false;
  bool goneReported_ = true;
  std::uint64_t generation_ = 0;
  std::uint64_t commandGeneration_ = 0;
  std::optional<Watchdog> watchdog_;
  std::uint64_t nextCall_ = 1;
  // the pauses asked for elsewhere that control(Pause) brought here: until it caught up, nothing is released
  std::uint64_t pausesTaken_ = 0;
  std::optional<Pending> inFlight_;
  std::deque<Pending> calls_;
  std::string_view failure_;
  SenderGuard guard_;
  airplay::EchoTable echoes_;
  // what the receiver last reported, where a write starts from
  std::optional<double> observedDb_;
  // the speaker's level, to be written once no other call waits
  std::optional<std::uint8_t> pendingVolume_;
  Coalescer coalescer_;
};

}  // namespace cadence::audiod
