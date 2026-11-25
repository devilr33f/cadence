#include "audiod/sender-volume.h"

#include <catch2/catch_test_macros.hpp>

#include <chrono>
#include <cstdint>
#include <initializer_list>
#include <memory>
#include <optional>
#include <vector>

// v1's own tests of volume.rs, echo.rs and spotify/session.rs, ported
using namespace cadence::audiod;
using namespace std::chrono_literals;

namespace {

using Time = SenderClock::time_point;

Time at(Time t0, std::int64_t ms) { return t0 + std::chrono::milliseconds(ms); }

SessionReaction quiet() { return {}; }
SessionReaction sends(std::initializer_list<std::uint8_t> levels) { return {.send = levels}; }
SessionReaction requests(std::uint8_t level) { return {.request = level}; }
SessionReaction gone() { return {.gone = true}; }

// a guard and one session on it, armed as a connection arms it
struct Session {
  SenderGuard guard;
  SoloistSession session{guard};

  SessionReaction up(Time now) { return session.auth(true, true, now); }
};

// logged in at level, with the announce and the first volume (the app's stored one) behind it
std::unique_ptr<Session> spent(std::uint8_t level, Time t0) {
  auto s = std::make_unique<Session>();
  REQUIRE(s->session.speakerLevel(level, t0) == std::nullopt);
  REQUIRE(s->up(t0) == sends({level}));
  REQUIRE(s->session.volume(99, at(t0, 1)) == sends({level}));
  return s;
}

}  // namespace

TEST_CASE("the guard drops the first differing value after a connect") {
  SenderGuard g;
  g.setCurrent(30);
  g.connected();
  CHECK(g.incoming(100) == Incoming::Dropped);
  CHECK(g.incoming(30) == Incoming::Echo);
  CHECK(g.incoming(40) == Incoming::Request);
  CHECK(g.current() == 30);
}

TEST_CASE("an echo does not use up the first-value drop") {
  SenderGuard g;
  g.setCurrent(30);
  g.connected();
  CHECK(g.incoming(30) == Incoming::Echo);
  CHECK(g.incoming(31) == Incoming::Dropped);
  CHECK(g.incoming(32) == Incoming::Request);
}

TEST_CASE("the guard passes requests before any connect") {
  SenderGuard g;
  g.setCurrent(30);
  CHECK(g.incoming(40) == Incoming::Request);
}

TEST_CASE("a guard without a level drops everything and stays fresh") {
  SenderGuard unknown;
  CHECK(unknown.current() == std::nullopt);
  CHECK(unknown.incoming(50) == Incoming::Dropped);
  unknown.connected();
  CHECK(unknown.incoming(50) == Incoming::Dropped);
  unknown.setCurrent(50);
  CHECK(unknown.incoming(50) == Incoming::Echo);
  CHECK(unknown.incoming(60) == Incoming::Dropped);
  CHECK(unknown.incoming(70) == Incoming::Request);
}

TEST_CASE("every connect drops one more value") {
  SenderGuard g;
  g.setCurrent(30);
  g.connected();
  CHECK(g.incoming(90) == Incoming::Dropped);
  CHECK(g.incoming(40) == Incoming::Request);
  g.connected();
  CHECK(g.incoming(90) == Incoming::Dropped);
}

TEST_CASE("the coalescer sends the first at once, then the latest every 200 ms") {
  const auto t0 = SenderClock::now();
  Coalescer c;
  CHECK(c.deadline() == std::nullopt);
  CHECK(c.push(10, t0) == 10);
  CHECK(c.push(11, at(t0, 50)) == std::nullopt);
  CHECK(c.push(12, at(t0, 100)) == std::nullopt);
  CHECK(c.deadline() == at(t0, 200));
  CHECK(c.poll(at(t0, 199)) == std::nullopt);
  CHECK(c.poll(at(t0, 200)) == 12);
  CHECK(c.deadline() == std::nullopt);
  CHECK(c.poll(at(t0, 900)) == std::nullopt);
}

TEST_CASE("clearing a coalescer drops only the pending value") {
  const auto t0 = SenderClock::now();
  Coalescer c;
  CHECK(c.push(10, t0) == 10);
  CHECK(c.push(11, at(t0, 50)) == std::nullopt);
  c.clearPending();
  CHECK(c.deadline() == std::nullopt);
  CHECK(c.poll(at(t0, 200)) == std::nullopt);
  CHECK(c.push(12, at(t0, 200)) == 12);
}

TEST_CASE("the coalescer starts the interval over from the trailing send") {
  const auto t0 = SenderClock::now();
  Coalescer c;
  CHECK(c.push(1, t0) == 1);
  CHECK(c.push(2, at(t0, 10)) == std::nullopt);
  CHECK(c.poll(at(t0, 230)) == 2);
  CHECK(c.push(3, at(t0, 300)) == std::nullopt);
  CHECK(c.deadline() == at(t0, 430));
  CHECK(c.poll(at(t0, 430)) == 3);
}

TEST_CASE("a push after a quiet interval goes at once") {
  const auto t0 = SenderClock::now();
  Coalescer c;
  CHECK(c.push(1, t0) == 1);
  CHECK(c.push(2, at(t0, 150)) == std::nullopt);
  CHECK(c.push(3, at(t0, 200)) == 3);
  CHECK(c.deadline() == std::nullopt);
}

TEST_CASE("a near echo settles the entry and every older one like an exact one") {
  const auto t0 = SenderClock::now();
  Echoes echoes;
  echoes.sent(40, t0);
  echoes.sent(60, at(t0, 10));
  CHECK_FALSE(echoes.echoNear(62, 1, at(t0, 20)));
  CHECK(echoes.echoNear(59, 1, at(t0, 30)));
  // 40 was older: settled with it
  CHECK(echoes.empty());
  CHECK_FALSE(echoes.echoNear(40, 1, at(t0, 40)));
}

TEST_CASE("an exact match wins over a nearer older one") {
  const auto t0 = SenderClock::now();
  Echoes echoes;
  echoes.sent(50, t0);
  echoes.sent(51, at(t0, 10));
  CHECK(echoes.echoNear(51, 1, at(t0, 20)));
  CHECK(echoes.empty());

  echoes.sent(50, at(t0, 30));
  echoes.sent(52, at(t0, 40));
  // no exact one: the oldest within the tolerance
  CHECK(echoes.echoNear(51, 1, at(t0, 50)));
  CHECK_FALSE(echoes.empty());
  CHECK(echoes.echoNear(52, 1, at(t0, 60)));
}

TEST_CASE("the tolerance matches nothing beyond it and nothing after the window") {
  const auto t0 = SenderClock::now();
  Echoes echoes;
  echoes.sent(0, t0);
  CHECK_FALSE(echoes.echoNear(2, 1, at(t0, 10)));
  CHECK(echoes.echoNear(1, 1, at(t0, 20)));

  echoes.sent(100, at(t0, 30));
  CHECK_FALSE(echoes.echoNear(99, 1, at(t0, 30 + 10'001)));
  CHECK(echoes.empty());
}

TEST_CASE("an echo is the exact match, and the bound keeps the newest") {
  const auto t0 = SenderClock::now();
  Echoes echoes;
  echoes.sent(50, t0);
  CHECK_FALSE(echoes.echo(49, at(t0, 10)));
  CHECK_FALSE(echoes.echo(51, at(t0, 20)));
  CHECK(echoes.echo(50, at(t0, 30)));

  for (std::uint16_t raw = 0; raw < kEchoBound + 2; ++raw)
    echoes.sent(raw, at(t0, 40));
  CHECK_FALSE(echoes.echo(0, at(t0, 50)));
  CHECK_FALSE(echoes.echo(1, at(t0, 50)));
  CHECK(echoes.echo(kEchoBound + 1, at(t0, 50)));
}

TEST_CASE("a session coming up announces the speaker's level, and its echo is no request") {
  const auto t0 = SenderClock::now();
  Session s;
  s.session.speakerLevel(30, t0);
  CHECK(s.up(t0) == sends({30}));
  // the announce comes back after cadenced moved on: still ours, not the slider's
  CHECK(s.session.speakerLevel(60, at(t0, 500)) == 60);
  CHECK(s.session.volume(30, at(t0, 900)) == quiet());
  CHECK(s.session.volume(60, at(t0, 950)) == quiet());
  CHECK(s.guard.current() == 60);
  // the echoes left the new session's first-volume drop in place
  CHECK(s.session.volume(80, at(t0, 1'000)) == sends({60}));
}

TEST_CASE("the first volume after a login is dropped and answered with the speaker's level") {
  const auto t0 = SenderClock::now();
  Session s;
  s.session.speakerLevel(30, t0);
  s.up(t0);
  CHECK(s.session.volume(80, at(t0, 2'000)) == sends({30}));
  // the answer is an echo too: it comes back after cadenced moved on
  s.session.speakerLevel(60, at(t0, 2'100));
  CHECK(s.session.volume(30, at(t0, 2'200)) == quiet());
}

TEST_CASE("a second volume is a request") {
  const auto t0 = SenderClock::now();
  auto s = spent(30, t0);
  CHECK(s->session.volume(55, at(t0, 3'000)) == requests(55));
  CHECK(s->session.volume(0, at(t0, 3'100)) == requests(0));
}

TEST_CASE("our own set_volume coming back within 10 s is no request") {
  const auto t0 = SenderClock::now();
  auto s = spent(30, t0);
  CHECK(s->session.speakerLevel(40, at(t0, 100)) == 40);
  CHECK(s->session.speakerLevel(60, at(t0, 200)) == 60);
  CHECK(s->session.volume(40, at(t0, 9'000)) == quiet());
  // settled: the same value again is the slider's
  CHECK(s->session.volume(40, at(t0, 9'500)) == requests(40));
}

TEST_CASE("our set_volume coming back one off is no request, two off is") {
  for (const std::uint8_t back : std::initializer_list<std::uint8_t>{49, 51}) {
    INFO(int(back));
    const auto t0 = SenderClock::now();
    auto s = spent(30, t0);
    CHECK(s->session.speakerLevel(50, at(t0, 100)) == 50);
    CHECK(s->session.volume(back, at(t0, 200)) == quiet());
    CHECK(s->session.volume(back, at(t0, 300)) == requests(back));
  }
  for (const std::uint8_t far : std::initializer_list<std::uint8_t>{48, 52}) {
    INFO(int(far));
    const auto t0 = SenderClock::now();
    auto s = spent(30, t0);
    s->session.speakerLevel(50, at(t0, 100));
    CHECK(s->session.volume(far, at(t0, 200)) == requests(far));
  }
}

TEST_CASE("a value one off after the window is a request") {
  for (const auto& [ms, request] : {std::pair{10'100, false}, std::pair{10'101, true}}) {
    INFO(ms);
    const auto t0 = SenderClock::now();
    auto s = spent(30, t0);
    s->session.speakerLevel(50, at(t0, 100));
    CHECK(s->session.volume(49, at(t0, ms)) == (request ? requests(49) : quiet()));
  }
}

TEST_CASE("a near echo settles every older level we sent") {
  const auto t0 = SenderClock::now();
  auto s = spent(30, t0);
  s->session.speakerLevel(50, at(t0, 100));
  s->session.speakerLevel(70, at(t0, 200));
  CHECK(s->session.volume(69, at(t0, 300)) == quiet());
  // 50 and the announce's 30 are settled with it: they come back as the slider's
  CHECK(s->session.volume(50, at(t0, 400)) == requests(50));
}

TEST_CASE("a soloist rounding our level by one never ratchets the volume") {
  for (const int step : {-1, 1}) {
    INFO(step);
    const auto t0 = SenderClock::now();
    auto s = spent(60, t0);
    std::int64_t ms = 100;
    for (const std::uint8_t level : std::initializer_list<std::uint8_t>{55, 50, 45, 40, 35}) {
      CHECK(s->session.speakerLevel(level, at(t0, ms)) == level);
      const auto back = static_cast<std::uint8_t>(level + step);
      CHECK(s->session.volume(back, at(t0, ms + 50)) == quiet());
      ms += 100;
    }
  }
}

TEST_CASE("the same value after the window is a request") {
  for (const auto& [ms, request] : {std::pair{10'009, false}, std::pair{10'010, false}, std::pair{10'011, true}}) {
    INFO(ms);
    const auto t0 = SenderClock::now();
    auto s = spent(30, t0);
    s->session.speakerLevel(40, at(t0, 10));
    s->session.speakerLevel(60, at(t0, 5'000));
    CHECK(s->session.volume(40, at(t0, ms)) == (request ? requests(40) : quiet()));
  }
}

TEST_CASE("a logout and a new login arm the first-volume drop again") {
  const auto t0 = SenderClock::now();
  auto s = spent(30, t0);
  CHECK(s->session.volume(55, at(t0, 100)) == requests(55));
  CHECK(s->session.auth(false, false, at(t0, 200)) == SessionReaction{.gone = true, .loggedOut = true});
  CHECK_FALSE(s->session.loggedIn());
  CHECK(s->session.speakerLevel(35, at(t0, 300)) == std::nullopt);
  CHECK(s->up(at(t0, 400)) == sends({35}));
  CHECK(s->session.loggedIn());
  CHECK(s->session.volume(70, at(t0, 500)) == sends({35}));
  CHECK(s->session.volume(71, at(t0, 600)) == requests(71));
}

TEST_CASE("a new session arms the first-volume drop at once") {
  Session s;
  s.session.speakerLevel(30, SenderClock::now());
  CHECK(s.guard.incoming(80) == Incoming::Dropped);
  CHECK(s.guard.incoming(81) == Incoming::Request);
}

TEST_CASE("a volume before any auth_state is dropped without a send") {
  const auto t0 = SenderClock::now();
  Session s;
  s.session.speakerLevel(30, t0);
  // the app's stored level, ahead of any auth_state: no command while logged out, and no echo recorded
  CHECK(s.session.volume(80, at(t0, 100)) == quiet());
  s.session.speakerLevel(60, at(t0, 200));
  CHECK(s.up(at(t0, 300)) == sends({60}));
  // 30 was never sent: it is a stored level again, not an echo
  CHECK(s.session.volume(30, at(t0, 400)) == sends({60}));
}

TEST_CASE("a volume while logged out or inactive is never a request") {
  const auto t0 = SenderClock::now();
  // never logged in: the drop is spent, the next value would be a request on a live session
  Session s;
  s.session.speakerLevel(30, t0);
  CHECK(s.session.volume(80, at(t0, 100)) == quiet());
  CHECK(s.session.volume(81, at(t0, 200)) == quiet());
  // logged in, never active
  CHECK(s.session.auth(true, false, at(t0, 300)) == sends({30}));
  CHECK(s.session.volume(82, at(t0, 400)) == sends({30}));
  CHECK(s.session.volume(83, at(t0, 500)) == quiet());
  // active again: a slider move
  CHECK(s.session.device(true, at(t0, 600)) == sends({30}));
  CHECK(s.session.volume(84, at(t0, 700)) == sends({30}));
  CHECK(s.session.volume(85, at(t0, 800)) == requests(85));
  // lost the activation, then the login
  s.session.device(false, at(t0, 900));
  CHECK(s.session.volume(86, at(t0, 1'000)) == sends({30}));
  CHECK(s.session.volume(87, at(t0, 1'100)) == quiet());
  s.session.auth(false, false, at(t0, 1'200));
  CHECK(s.session.volume(88, at(t0, 1'300)) == quiet());
  CHECK(s.session.volume(89, at(t0, 1'400)) == quiet());
}

TEST_CASE("a volume after gone before the next activation is dropped and answered") {
  for (const bool byAuth : {false, true}) {
    INFO(byAuth);
    const auto t0 = SenderClock::now();
    auto s = spent(30, t0);
    const auto lost = byAuth ? s->session.auth(true, false, at(t0, 100)) : s->session.device(false, at(t0, 100));
    CHECK(lost == gone());
    CHECK(s->session.volume(55, at(t0, 200)) == sends({30}));
  }
}

TEST_CASE("gone and a logout each arm the guard") {
  for (const int lost : {0, 1, 2}) {
    INFO(lost);
    const auto t0 = SenderClock::now();
    auto s = spent(30, t0);
    const auto reaction = lost == 0   ? s->session.device(false, at(t0, 100))
                          : lost == 1 ? s->session.auth(true, false, at(t0, 100))
                                      : s->session.auth(false, false, at(t0, 100));
    CHECK(reaction.gone);
    CHECK(s->guard.incoming(55) == Incoming::Dropped);
  }
  // a logout with no activation lost
  const auto t0 = SenderClock::now();
  Session s;
  s.session.speakerLevel(30, t0);
  s.session.auth(true, false, t0);
  CHECK(s.guard.incoming(99) == Incoming::Dropped);
  CHECK(s.guard.incoming(98) == Incoming::Request);
  CHECK(s.session.auth(false, false, at(t0, 100)) == SessionReaction{.loggedOut = true});
  CHECK(s.guard.incoming(55) == Incoming::Dropped);
}

TEST_CASE("gone, then a stored volume, then a new activation drops twice and then requests") {
  const auto t0 = SenderClock::now();
  auto s = spent(30, t0);
  CHECK(s->session.device(false, at(t0, 100)) == gone());
  CHECK(s->session.volume(55, at(t0, 200)) == sends({30}));
  CHECK(s->session.device(true, at(t0, 300)) == sends({30}));
  CHECK(s->session.volume(70, at(t0, 400)) == sends({30}));
  CHECK(s->session.volume(71, at(t0, 500)) == requests(71));
}

TEST_CASE("losing the activation is gone, once") {
  const auto t0 = SenderClock::now();
  Session s;
  s.session.speakerLevel(30, t0);
  // never active: nothing to lose
  CHECK(s.session.device(false, t0) == quiet());
  s.up(t0);
  CHECK(s.session.device(false, at(t0, 100)) == gone());
  CHECK(s.session.device(false, at(t0, 200)) == quiet());
  // active again through the device event: a new session, announced
  CHECK(s.session.device(true, at(t0, 300)) == sends({30}));
  CHECK(s.session.auth(true, false, at(t0, 400)) == gone());
  CHECK(s.session.loggedIn());
}

TEST_CASE("the speaker's level while logged out is kept for the announce and the initial volume") {
  const auto t0 = SenderClock::now();
  Session s;
  CHECK(initialVolume(s.guard, 101) == 50);
  CHECK(initialVolume(s.guard, 40) == 40);
  CHECK(s.session.speakerLevel(30, t0) == std::nullopt);
  CHECK(initialVolume(s.guard, 40) == 30);
  CHECK(s.guard.current() == 30);
  CHECK(s.session.auth(true, false, at(t0, 100)) == sends({30}));
  CHECK(s.session.speakerLevel(130, at(t0, 200)) == 100);
}

TEST_CASE("an unknown level announces nothing and drops the first volume") {
  const auto t0 = SenderClock::now();
  Session s;
  CHECK(s.up(t0) == quiet());
  CHECK(s.session.loggedIn());
  CHECK(s.session.volume(80, at(t0, 100)) == quiet());
  CHECK(s.session.volume(81, at(t0, 200)) == quiet());
  CHECK(s.session.speakerLevel(30, at(t0, 300)) == 30);
  CHECK(s.session.volume(70, at(t0, 400)) == sends({30}));
  CHECK(s.session.volume(71, at(t0, 500)) == requests(71));
}

TEST_CASE("a repeated state is no new session") {
  const auto t0 = SenderClock::now();
  Session s;
  s.session.speakerLevel(30, t0);
  CHECK(s.up(t0) == sends({30}));
  CHECK(s.up(at(t0, 100)) == quiet());
  CHECK(s.session.device(true, at(t0, 200)) == quiet());
}
