#include "audiod/airplay-session.h"
#include "audiod/airplay-volume.h"
#include "audiod/log.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <chrono>
#include <cstdint>
#include <initializer_list>
#include <limits>
#include <optional>
#include <string>
#include <string_view>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::audiod::airplay;
using namespace std::chrono_literals;
using Journal = std::vector<std::string>;

namespace {

// what the session did, in order: "call <request>" (a volume with its level), "hold 1|0", "playing", "paused",
// "ended"; and the requests it made of cadenced
class FakePort final : public AirplaySession::Port {
 public:
  bool call(const AirplaySession::Call& call) override {
    calls_.push_back(call);
    auto entry = "call " + std::string(requestToken(call.request));
    if (call.request == Request::SetVolume)
      entry += " " + std::to_string(toPercent(call.db).value_or(255));
    journal_.push_back(std::move(entry));
    return accepts_;
  }
  void hold() override { journal_.emplace_back("hold 1"); }
  // as the output does: refused while a pause asked for is one the session has not taken
  void release(std::uint64_t pausesTaken) override {
    journal_.emplace_back(pausesTaken < asked ? "hold refused" : "hold 0");
  }
  [[nodiscard]] std::uint64_t pausesAsked() const override { return asked; }
  void playing() override { journal_.emplace_back("playing"); }
  void paused() override { journal_.emplace_back("paused"); }
  void ended() override { journal_.emplace_back("ended"); }
  void request(std::uint8_t level) override { requests_.push_back(level); }

  // what happened since the last look
  Journal take() { return std::exchange(journal_, {}); }
  [[nodiscard]] const AirplaySession::Call& last() const {
    REQUIRE_FALSE(calls_.empty());
    return calls_.back();
  }
  void refuseCalls() { accepts_ = false; }
  [[nodiscard]] const std::vector<int>& requests() const { return requests_; }

  // the pauses the source asked for, each held at once on the loop thread
  std::uint64_t asked = 0;

 private:
  Journal journal_;
  std::vector<AirplaySession::Call> calls_;
  std::vector<int> requests_;
  bool accepts_ = true;
};

// codes a control was answered with
class Answers {
 public:
  CommandDone done() {
    return [this](std::string_view code) { codes_.emplace_back(code.empty() ? "ok" : code); };
  }
  std::vector<std::string> take() { return std::exchange(codes_, {}); }

 private:
  std::vector<std::string> codes_;
};

const auto t0 = std::chrono::steady_clock::time_point{} + 1000s;

Snapshot playingSnapshot(std::string client = "phone-a") {
  return {.state = PlayerState::Playing, .volumeDb = -21, .client = std::move(client)};
}

Change state(PlayerState state) { return {.client = std::nullopt, .state = state, .volumeDb = std::nullopt}; }

Change client(std::string id) { return {.client = std::move(id), .state = std::nullopt, .volumeDb = std::nullopt}; }

Change volume(double db) { return {.client = std::nullopt, .state = std::nullopt, .volumeDb = db}; }

Change volumeAt(int percent) { return volume(toDb(static_cast<std::uint8_t>(percent))); }

class Rig {
 public:
  using Clock = AirplaySession::Clock;

  Rig() = default;
  FakePort port;
  Answers answers;
  Log log{[](std::string_view) {}};
  AirplaySession session{log, port};

  // a receiver playing with the speaker's level known, told that level, which it took
  void playing(std::uint8_t level = 30, double storedDb = -21) {
    session.speakerLevel(level, t0);
    session.attach({.state = PlayerState::Playing, .volumeDb = storedDb, .client = "phone-a"}, t0);
    REQUIRE(port.take() == Journal{"hold 0", "playing", "call set_volume " + std::to_string(level)});
    session.reply(port.last().id, true, t0);
  }
  // the first value that differs after a connect, which is the app's stored level and dropped: the speaker's level
  // goes back, and the next value that differs is a slider move
  void consumeFirst(std::uint8_t level, Clock::time_point at, int stored = 20) {
    session.change(volumeAt(stored), at);
    REQUIRE(port.take() == Journal{"call set_volume " + std::to_string(level)});
    session.reply(port.last().id, true, at);
    REQUIRE(port.requests().empty());
  }
  // the receiver reports what it was set to
  void echoes(std::initializer_list<int> levels, Clock::time_point at) {
    for (const int level : levels)
      session.change(volumeAt(level), at);
  }
};

}  // namespace

TEST_CASE("a cast before the first speaker level stays held and does not play") {
  Rig rig;
  rig.session.attach(playingSnapshot(), t0);
  CHECK(rig.session.live());
  CHECK(rig.port.take().empty());
  rig.session.change(state(PlayerState::Playing), t0);
  CHECK(rig.port.take().empty());

  rig.session.speakerLevel(30, t0);
  CHECK(rig.port.take() == Journal{"hold 0", "playing", "call set_volume 30"});
  // a level waits for the write before it
  rig.session.speakerLevel(40, t0);
  CHECK(rig.port.take().empty());
  rig.session.reply(rig.port.last().id, true, t0);
  CHECK(rig.port.take() == Journal{"call set_volume 40"});
  CHECK(rig.port.requests().empty());
}

TEST_CASE("a repeated Playing never plays again, and a genuine one after a pause does") {
  Rig rig;
  rig.playing();
  rig.session.change(state(PlayerState::Playing), t0);
  CHECK(rig.port.take().empty());

  rig.session.change(state(PlayerState::Paused), t0);
  CHECK(rig.port.take() == Journal{"hold 1", "paused"});
  rig.session.change(state(PlayerState::Stopped), t0);
  CHECK(rig.port.take() == Journal{"hold 1"});
  rig.session.change(state(PlayerState::Playing), t0);
  CHECK(rig.port.take() == Journal{"hold 0", "playing", "call set_volume 30"});
}

TEST_CASE("a new sender in a session starts afresh: what was queued for the old one goes") {
  Rig rig;
  rig.playing();
  // one call in flight, one queued behind it
  rig.session.control(MediaAction::Next, rig.answers.done(), t0);
  rig.session.control(MediaAction::Previous, rig.answers.done(), t0);
  CHECK(rig.port.take() == Journal{"call next"});
  const auto next = rig.port.last().id;

  rig.session.change(client("phone-b"), t0);
  CHECK(rig.answers.take() == std::vector<std::string>{"control_unsupported"});
  CHECK(rig.port.take() == Journal{"hold 0", "playing"});
  // the old sender's reply is of an older session; the new one is told the speaker's level after it
  rig.session.reply(next, true, t0);
  CHECK(rig.answers.take() == std::vector<std::string>{"control_unsupported"});
  CHECK(rig.port.take() == Journal{"call set_volume 30"});

  // the same client again is no new sender
  rig.session.reply(rig.port.last().id, true, t0);
  rig.session.change(client("phone-b"), t0);
  CHECK(rig.port.take().empty());
}

TEST_CASE("Not Available ends the session once, held, and the speaker's level outlives it") {
  Rig rig;
  rig.playing();
  rig.session.change(state(PlayerState::NotAvailable), t0);
  CHECK_FALSE(rig.session.live());
  CHECK(rig.port.take() == Journal{"hold 1", "ended"});
  rig.session.change(state(PlayerState::NotAvailable), t0);
  rig.session.detach();
  CHECK(rig.port.take() == Journal{"hold 1", "hold 1"});

  // a receiver restart plays again with the level it had and tells it, without another level from cadenced
  rig.session.attach(playingSnapshot("phone-b"), t0);
  CHECK(rig.port.take() == Journal{"hold 0", "playing", "call set_volume 30"});
  rig.session.detach();
  CHECK(rig.port.take() == Journal{"hold 1", "ended"});
  CHECK(rig.port.requests().empty());
}

TEST_CASE("a snapshot of no session reports nothing until one comes") {
  Rig rig;
  rig.session.speakerLevel(30, t0);
  rig.session.attach({.state = PlayerState::NotAvailable, .volumeDb = 0, .client = {}}, t0);
  CHECK_FALSE(rig.session.live());
  rig.session.detach();
  CHECK(rig.port.take() == Journal{"hold 1"});
}

TEST_CASE("a pause holds the output before the receiver is asked, and an ignored one drops the session") {
  Rig rig;
  rig.playing();
  rig.session.control(MediaAction::Pause, rig.answers.done(), t0);
  CHECK(rig.port.take() == Journal{"hold 1", "paused", "call pause"});
  CHECK(rig.answers.take() == std::vector<std::string>{"ok"});
  REQUIRE(rig.session.deadline() == t0 + 2s);

  // the receiver answers but plays on, also saying Playing again: nothing reopens
  rig.session.reply(rig.port.last().id, true, t0 + 100ms);
  rig.session.change(state(PlayerState::Playing), t0 + 200ms);
  CHECK(rig.port.take().empty());
  rig.session.tick(t0 + 1999ms);
  CHECK(rig.port.take().empty());
  rig.session.tick(t0 + 2s);
  CHECK(rig.port.take() == Journal{"call drop_session"});
  rig.session.reply(rig.port.last().id, true, t0 + 2100ms);
  CHECK(rig.port.take() == Journal{"hold 1", "ended"});
  CHECK_FALSE(rig.session.live());
  CHECK(rig.session.failure().empty());
}

TEST_CASE("a failed drop ends the receiver") {
  Rig rig;
  rig.playing();
  rig.session.control(MediaAction::Pause, {}, t0);
  rig.session.reply(rig.port.last().id, false, t0);
  rig.session.tick(t0 + 2s);
  REQUIRE(rig.port.last().request == Request::DropSession);
  rig.session.reply(rig.port.last().id, false, t0 + 2s);
  CHECK(rig.session.failure() == "session_lost");
}

TEST_CASE("a pause the receiver reflects calls the drop off") {
  Rig rig;
  rig.playing();
  rig.session.control(MediaAction::Pause, {}, t0);
  rig.session.reply(rig.port.last().id, true, t0);
  rig.session.change(state(PlayerState::Paused), t0 + 500ms);
  rig.port.take();
  CHECK_FALSE(rig.session.deadline());
  rig.session.tick(t0 + 3s);
  CHECK(rig.port.take().empty());
}

TEST_CASE("a resume asks for Play, calls the drop off and opens only once Play answered") {
  Rig rig;
  rig.playing();
  rig.session.control(MediaAction::Pause, {}, t0);
  rig.session.control(MediaAction::Resume, rig.answers.done(), t0 + 100ms);
  // one call at a time: Play waits for the pause's reply
  CHECK(rig.port.take() == Journal{"hold 1", "paused", "call pause"});
  rig.session.reply(rig.port.last().id, true, t0 + 200ms);
  CHECK(rig.port.take() == Journal{"call play"});
  rig.session.tick(t0 + 3s);
  CHECK(rig.port.take().empty());
  rig.session.reply(rig.port.last().id, true, t0 + 3s);
  CHECK(rig.port.take() == Journal{"hold 0", "playing", "call set_volume 30"});
  CHECK(rig.answers.take() == std::vector<std::string>{"ok"});
  rig.session.reply(rig.port.last().id, true, t0 + 3s);

  // a Play the receiver refuses opens nothing and is answered so
  rig.session.control(MediaAction::Pause, {}, t0 + 4s);
  rig.session.reply(rig.port.last().id, true, t0 + 4s);
  rig.session.control(MediaAction::Resume, rig.answers.done(), t0 + 4s);
  CHECK(rig.port.take() == Journal{"hold 1", "paused", "call pause", "call play"});
  rig.session.reply(rig.port.last().id, false, t0 + 4s);
  CHECK(rig.port.take().empty());
  CHECK(rig.answers.take() == std::vector<std::string>{"control_unsupported"});
}

TEST_CASE("a resume after the watchdog fired makes the old drop's answer count for nothing") {
  Rig rig;
  rig.playing();
  rig.session.control(MediaAction::Pause, {}, t0);
  rig.session.reply(rig.port.last().id, true, t0);
  rig.session.tick(t0 + 2s);
  REQUIRE(rig.port.last().request == Request::DropSession);
  const auto drop = rig.port.last().id;
  rig.session.control(MediaAction::Resume, rig.answers.done(), t0 + 2100ms);
  rig.port.take();

  // the drop fails late: nothing ends, and Play goes out
  rig.session.reply(drop, false, t0 + 4s);
  CHECK(rig.session.failure().empty());
  CHECK(rig.port.take() == Journal{"call play"});
  rig.session.reply(rig.port.last().id, true, t0 + 4s);
  CHECK(rig.port.take() == Journal{"hold 0", "playing", "call set_volume 30"});
  CHECK(rig.answers.take() == std::vector<std::string>{"ok"});
}

TEST_CASE("next and previous are only delivered, and answered control_unsupported when the call fails") {
  Rig rig;
  rig.playing();
  rig.session.control(MediaAction::Next, rig.answers.done(), t0);
  rig.session.control(MediaAction::Previous, rig.answers.done(), t0);
  CHECK(rig.port.take() == Journal{"call next"});
  rig.session.reply(rig.port.last().id, false, t0);
  CHECK(rig.port.take() == Journal{"call previous"});
  rig.session.reply(rig.port.last().id, true, t0);
  CHECK(rig.answers.take() == std::vector<std::string>{"control_unsupported", "ok"});
  CHECK(rig.port.take().empty());

  rig.session.control(MediaAction::Seek, rig.answers.done(), t0);
  CHECK(rig.answers.take() == std::vector<std::string>{"unsupported"});
}

TEST_CASE("controls with no session answer control_unsupported and call nothing") {
  Rig rig;
  for (const auto action : {MediaAction::Pause, MediaAction::Resume, MediaAction::Next, MediaAction::Previous})
    rig.session.control(action, rig.answers.done(), t0);
  CHECK(rig.answers.take() == std::vector<std::string>(4, "control_unsupported"));
  CHECK(rig.port.take().empty());
}

TEST_CASE("an invalid state holds the output and ends the receiver, which ends its session") {
  Rig rig;
  rig.playing();
  rig.session.invalid();
  CHECK(rig.port.take() == Journal{"hold 1"});
  CHECK(rig.session.failure() == "invalid_state");
  rig.session.detach();
  CHECK(rig.port.take() == Journal{"hold 1", "ended"});
  // the next receiver starts clean
  rig.session.attach(playingSnapshot(), t0);
  CHECK(rig.session.failure().empty());
}

TEST_CASE("a call that cannot go out ends the receiver") {
  Rig rig;
  rig.playing();
  rig.port.refuseCalls();
  rig.session.control(MediaAction::Next, rig.answers.done(), t0);
  CHECK(rig.session.failure() == "session_lost");
  CHECK(rig.answers.take() == std::vector<std::string>{"control_unsupported"});
}

TEST_CASE("player states read as shairport spells them, and nothing else") {
  CHECK(playerState("Playing") == PlayerState::Playing);
  CHECK(playerState("Paused") == PlayerState::Paused);
  CHECK(playerState("Stopped") == PlayerState::Stopped);
  CHECK(playerState("Not Available") == PlayerState::NotAvailable);
  for (const auto* other : {"playing", "", "Not available", "Buffering"})
    CHECK_FALSE(playerState(other));
}

TEST_CASE("a session is told the speaker's level as it comes up, and the app's stored level is dropped") {
  Rig rig;
  rig.session.speakerLevel(30, t0);
  // the receiver comes up with the level the app stored: the first value that differs
  rig.session.attach({.state = PlayerState::Playing, .volumeDb = toDb(80), .client = "phone-a"}, t0);
  CHECK(rig.port.take() == Journal{"hold 0", "playing", "call set_volume 30"});
  rig.session.reply(rig.port.last().id, true, t0);
  // our level coming back, rounded or stepped, is no request
  rig.echoes({30, 29, 31, 35}, t0 + 100ms);
  CHECK(rig.port.requests().empty());
  CHECK(rig.port.take().empty());

  // a deliberate slider move is
  rig.session.change(volumeAt(55), t0 + 200ms);
  CHECK(rig.port.requests() == std::vector<int>{55});
}

TEST_CASE("echoes do not use up the drop of the first value that differs") {
  Rig rig;
  rig.playing(30, toDb(30));
  rig.echoes({30, 29, 31}, t0 + 30ms);
  CHECK(rig.port.take().empty());

  // past the window, still the first that differs: dropped and answered with the speaker's level
  rig.session.change(volumeAt(45), t0 + 11s);
  CHECK(rig.port.take() == Journal{"call set_volume 30"});
  rig.session.reply(rig.port.last().id, true, t0 + 11s);
  CHECK(rig.port.requests().empty());
  rig.session.change(volumeAt(50), t0 + 11200ms);
  CHECK(rig.port.requests() == std::vector<int>{50});
}

TEST_CASE("a report while a write is in flight is marked an echo, never a request") {
  Rig rig;
  rig.session.speakerLevel(30, t0);
  rig.session.attach({.state = PlayerState::Playing, .volumeDb = toDb(30), .client = "phone-a"}, t0);
  rig.port.take();
  // the write is in flight
  rig.session.change(volumeAt(60), t0 + 10ms);
  rig.session.reply(rig.port.last().id, true, t0 + 20ms);
  rig.session.change(volumeAt(61), t0 + 1s);
  CHECK(rig.port.take().empty());
  CHECK(rig.port.requests().empty());

  // it used up nothing: the first that differs is still dropped
  rig.session.change(volumeAt(70), t0 + 2s);
  CHECK(rig.port.take() == Journal{"call set_volume 30"});
  rig.session.reply(rig.port.last().id, true, t0 + 2s);
  rig.session.change(volumeAt(75), t0 + 3s);
  CHECK(rig.port.requests() == std::vector<int>{75});
}

TEST_CASE("from 30 to 90, the stepped echoes, delayed and reordered, never ratchet the speaker") {
  Rig rig;
  rig.playing(30, toDb(30));
  rig.consumeFirst(30, t0 + 100ms);
  rig.echoes({30}, t0 + 200ms);

  const auto t1 = t0 + 1s;
  rig.session.speakerLevel(90, t1);
  CHECK(rig.port.take() == Journal{"call set_volume 90"});
  // the receiver steps while the write is in flight, and answers late
  for (int level = 9; level <= 90; level += 9)
    rig.session.change(volumeAt(level), t1 + 100ms);
  rig.session.reply(rig.port.last().id, true, t1 + 3s);
  // and more of them come after the answer, out of order, the final one's neighbours too
  rig.echoes({90, 45, 89, 18, 91, 72, 81}, t1 + 3500ms);
  CHECK(rig.port.requests().empty());
  CHECK(rig.port.take().empty());

  rig.session.change(volumeAt(45), t1 + 11s);
  CHECK(rig.port.requests() == std::vector<int>{45});
}

TEST_CASE("from 90 to mute, the whole stepped way down is echo") {
  Rig rig;
  rig.playing(90, toDb(90));
  rig.consumeFirst(90, t0 + 100ms, 95);
  rig.echoes({90}, t0 + 200ms);

  const auto t1 = t0 + 1s;
  rig.session.speakerLevel(0, t1);
  CHECK(rig.port.take() == Journal{"call set_volume 0"});
  rig.session.reply(rig.port.last().id, true, t1 + 100ms);
  rig.echoes({81, 9, 72, 0, 63, 1, 54, 45, 36, 27, 18, 90}, t1 + 300ms);
  CHECK(rig.port.requests().empty());
  CHECK(rig.port.take().empty());

  rig.session.change(volumeAt(45), t1 + 11s);
  CHECK(rig.port.requests() == std::vector<int>{45});
}

TEST_CASE("a write that failed or timed out keeps its echoes: it may have reached the sender") {
  Rig rig;
  rig.playing(30, toDb(30));
  rig.consumeFirst(30, t0 + 100ms);
  rig.echoes({30}, t0 + 200ms);

  const auto t1 = t0 + 1s;
  rig.session.speakerLevel(90, t1);
  rig.session.reply(rig.port.last().id, false, t1 + 2100ms);
  // a neighbour, not the speaker's level itself, which the guard would take for an echo anyway
  rig.echoes({89, 57}, t1 + 2200ms);
  CHECK(rig.port.requests().empty());

  rig.session.change(volumeAt(45), t1 + 11s);
  CHECK(rig.port.requests() == std::vector<int>{45});
}

TEST_CASE("a volume that is not finite or lies outside the slider never reaches cadenced") {
  Rig rig;
  rig.playing(30, toDb(30));
  rig.consumeFirst(30, t0 + 100ms);
  for (const double db : {std::numeric_limits<double>::quiet_NaN(), std::numeric_limits<double>::infinity(),
                          -std::numeric_limits<double>::infinity(), -31.0, 1.0, -100.0})
    rig.session.change(volume(db), t0 + 1s);
  CHECK(rig.port.requests().empty());
  CHECK(rig.port.take().empty());
  rig.session.change(volumeAt(60), t0 + 2s);
  CHECK(rig.port.requests() == std::vector<int>{60});
}

TEST_CASE("requests go out one per 200 ms, the latest of them, and the last always") {
  Rig rig;
  rig.playing(30, toDb(30));
  rig.consumeFirst(30, t0 + 100ms);

  const auto t1 = t0 + 1s;
  rig.session.change(volumeAt(50), t1);
  rig.session.change(volumeAt(55), t1 + 50ms);
  rig.session.change(volumeAt(60), t1 + 100ms);
  CHECK(rig.port.requests() == std::vector<int>{50});
  CHECK(rig.session.deadline() == t1 + 200ms);
  rig.session.tick(t1 + 199ms);
  CHECK(rig.port.requests() == std::vector<int>{50});
  rig.session.tick(t1 + 200ms);
  CHECK(rig.port.requests() == std::vector<int>{50, 60});
  CHECK_FALSE(rig.session.deadline());
}

TEST_CASE("a new sender inherits the speaker's level without a request, and what the old one asked for goes") {
  Rig rig;
  rig.playing(30, toDb(30));
  rig.consumeFirst(30, t0 + 100ms);
  const auto t1 = t0 + 1s;
  rig.session.change(volumeAt(50), t1);
  rig.session.change(volumeAt(55), t1 + 50ms);
  REQUIRE(rig.port.requests() == std::vector<int>{50});

  // the new sender's identity comes first, then its state, then the level its app stored
  rig.session.change({.client = "phone-b", .state = PlayerState::Playing, .volumeDb = toDb(77)}, t1 + 60ms);
  CHECK(rig.port.take() == Journal{"hold 0", "playing", "call set_volume 30"});
  rig.session.tick(t1 + 1s);
  CHECK(rig.port.requests() == std::vector<int>{50});

  // its app's stored level was its first value that differs, so the next one is a slider move
  rig.session.reply(rig.port.last().id, true, t1 + 1s);
  rig.session.change(volumeAt(66), t1 + 1100ms);
  CHECK(rig.port.requests() == std::vector<int>{50, 66});
}

TEST_CASE("with no session the receiver's volume is an observation only") {
  Rig rig;
  rig.session.speakerLevel(30, t0);
  rig.session.attach({.state = PlayerState::NotAvailable, .volumeDb = toDb(50), .client = {}}, t0);
  rig.echoes({60, 70}, t0 + 1s);
  rig.session.speakerLevel(40, t0 + 2s);
  CHECK(rig.port.take().empty());
  CHECK(rig.port.requests().empty());
}

TEST_CASE("a new sender that has not said its volume yet is written from an unknown start: every level is echo") {
  Rig rig;
  rig.playing(30, toDb(30));
  rig.consumeFirst(30, t0 + 100ms);
  rig.session.change(client("phone-b"), t0 + 1s);
  CHECK(rig.port.take() == Journal{"hold 0", "playing", "call set_volume 30"});
  rig.session.reply(rig.port.last().id, true, t0 + 1s);
  rig.echoes({90, 5, 64}, t0 + 2s);
  CHECK(rig.port.take().empty());

  // past the window, its first value that differs is still dropped, and the next is a slider move
  rig.session.change(volumeAt(66), t0 + 12s);
  CHECK(rig.port.take() == Journal{"call set_volume 30"});
  rig.session.reply(rig.port.last().id, true, t0 + 12s);
  rig.session.change(volumeAt(80), t0 + 13s);
  CHECK(rig.port.requests() == std::vector<int>{80});
}

TEST_CASE("a pause asked on another thread holds back a release racing it, until the session took that pause") {
  Rig rig;
  rig.playing();
  // paused, then resumed: Play is on its way
  rig.port.asked = 1;
  rig.session.control(MediaAction::Pause, {}, t0, 1);
  rig.session.reply(rig.port.last().id, true, t0);
  rig.session.control(MediaAction::Resume, rig.answers.done(), t0 + 1s);
  CHECK(rig.port.take() == Journal{"hold 1", "paused", "call pause", "call play"});

  // cadenced pauses again: the source held the output and asked at once, its task still on the way, as Play answers ok
  rig.port.asked = 2;
  rig.session.reply(rig.port.last().id, true, t0 + 1100ms);
  CHECK(rig.port.take().empty());
  CHECK(rig.answers.take() == std::vector<std::string>{"ok"});
  // nor does a genuine Playing reopen it meanwhile
  rig.session.change(state(PlayerState::Paused), t0 + 1150ms);
  rig.session.change(state(PlayerState::Playing), t0 + 1160ms);
  CHECK(rig.port.take() == Journal{"hold 1"});

  // the pause arrives
  rig.session.control(MediaAction::Pause, rig.answers.done(), t0 + 1200ms, 2);
  CHECK(rig.port.take() == Journal{"hold 1", "call pause"});
  CHECK(rig.answers.take() == std::vector<std::string>{"ok"});
}

TEST_CASE("a pause asked with no session is taken all the same, and the next session plays") {
  Rig rig;
  rig.port.asked = 1;
  rig.session.control(MediaAction::Pause, rig.answers.done(), t0, 1);
  CHECK(rig.answers.take() == std::vector<std::string>{"control_unsupported"});
  rig.session.speakerLevel(30, t0);
  rig.session.attach(playingSnapshot(), t0);
  CHECK(rig.port.take() == Journal{"hold 0", "playing", "call set_volume 30"});
}
