#pragma once

#include <chrono>
#include <cstdint>
#include <deque>
#include <optional>
#include <vector>

namespace cadence::audiod {

// rule 7 for a media sender (M4c-D9, reference §5; v1's volume.rs, echo.rs and spotify/session.rs, ported exactly): a
// sender's volume moves the speaker only as a deliberate slider move, never as the speaker's own level coming back,
// nor as the level the sender's app stored and reports when a session comes up

using SenderClock = std::chrono::steady_clock;

inline constexpr std::uint8_t kMaxSenderLevel = 100;
// --initial-volume when neither the speaker's level nor cadenced's is known (reference §1.4)
inline constexpr std::uint8_t kUnknownLevel = 50;
// how long a level sent to a sender may still come back: past bluez's 2 s call limit, network turns and the signal's
// lag. a value the sender reports later is its own
inline constexpr std::chrono::seconds kEchoWindow{10};
// a backstop only: the window ends them first
inline constexpr std::size_t kEchoBound = 32;
// soloist may report our level rounded one step down or up: taken as the same echo, or each round trip would move the
// volume by one. only a request is lost to it, never one made
inline constexpr std::uint16_t kSoloistEchoTolerance = 1;
// one sender's requests go out at most this often, the latest one; the last always goes
inline constexpr std::chrono::milliseconds kSenderCoalesce{200};

enum class Incoming : std::uint8_t { Echo, Dropped, Request };

// one per sender, across all its connections: tells the speaker's own level coming back, and a sender's stored level
// as it connects, from a real request
class SenderGuard {
 public:
  // the next value that differs from the speaker's level is dropped
  void connected() { fresh_ = true; }
  void setCurrent(std::uint8_t level);
  [[nodiscard]] std::optional<std::uint8_t> current() const { return current_; }
  // an unknown level drops everything, and the first value after it is learnt is dropped too
  Incoming incoming(std::uint8_t level);

 private:
  std::optional<std::uint8_t> current_;
  bool fresh_ = false;
};

// the levels sent to one sender and not seen back yet, oldest first: what comes back of them within the window is
// never the sender's. the times are where each happened, so a late reader expires nothing early
class Echoes {
 public:
  void sent(std::uint16_t raw, SenderClock::time_point at);
  // one of ours coming back within the window settles it and every older one
  bool echo(std::uint16_t raw, SenderClock::time_point at) { return echoNear(raw, 0, at); }
  // for a sender that rounds what it is sent: a value within tolerance of one of ours counts as that one, an exact
  // match first, else the oldest near one. it can only take a value for an echo, never make one a request
  bool echoNear(std::uint16_t raw, std::uint16_t tolerance, SenderClock::time_point at);
  [[nodiscard]] bool empty() const { return unseen_.empty(); }

 private:
  struct Sent {
    std::uint16_t raw = 0;
    SenderClock::time_point at;
  };

  void expire(SenderClock::time_point now);

  std::deque<Sent> unseen_;
};

// one request per interval: the first at once, the latest of those within the interval when it ends
class Coalescer {
 public:
  explicit Coalescer(std::chrono::milliseconds interval = kSenderCoalesce) : interval_(interval) {}

  std::optional<std::uint8_t> push(std::uint8_t level, SenderClock::time_point now);
  std::optional<std::uint8_t> poll(SenderClock::time_point now);
  void clearPending() { pending_.reset(); }
  // when poll has one to give
  [[nodiscard]] std::optional<SenderClock::time_point> deadline() const;

 private:
  std::chrono::milliseconds interval_;
  std::optional<SenderClock::time_point> lastSent_;
  std::optional<std::uint8_t> pending_;
};

// what an event of a soloist session asks for
struct SessionReaction {
  // set_volume levels to send, in order, each recorded as an echo already
  std::vector<std::uint8_t> send{};
  // a deliberate slider move
  std::optional<std::uint8_t> request{};
  // its activation was lost
  bool gone = false;
  // its login was lost
  bool loggedOut = false;

  bool operator==(const SessionReaction&) const = default;
};

// rule 7 on one soloist websocket connection, which soloist starts its state over on: its volume is the speaker's own
// 0-100 level, so all there is to tell apart is the app's stored level on a session that comes up, and our own
// set_volume coming back, from a real slider move
class SoloistSession {
 public:
  // arms the guard at once: soloist may send the app's stored level before any event that says a session is up
  explicit SoloistSession(SenderGuard& guard);

  SessionReaction auth(bool loggedIn, bool active, SenderClock::time_point now);
  SessionReaction device(bool active, SenderClock::time_point now);
  // a volume_changed
  SessionReaction volume(std::uint8_t level, SenderClock::time_point now);
  // the speaker's level: kept while logged out for the announce and the initial volume, sent once logged in
  std::optional<std::uint8_t> speakerLevel(std::uint8_t level, SenderClock::time_point now);

  [[nodiscard]] bool loggedIn() const { return loggedIn_; }
  [[nodiscard]] bool active() const { return active_; }

 private:
  SessionReaction transition(bool loggedIn, bool active, SenderClock::time_point now);
  std::optional<std::uint8_t> announce(SenderClock::time_point now);

  SenderGuard& guard_;
  Echoes echoes_;
  bool loggedIn_ = false;
  bool active_ = false;
};

// --initial-volume for a start: the speaker's level, else fallback (cadenced's own), else 50
std::uint8_t initialVolume(const SenderGuard& guard, unsigned fallback);

}  // namespace cadence::audiod
