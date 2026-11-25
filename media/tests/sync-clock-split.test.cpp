// a group whose clock master is not its stream leader, in one process over 127.0.0.1: a member serves the group clock,
// the leader and the other member fit it, and every output is measured against when the master's time meant each
// sample to be heard. the master gone mutes everyone until it is back; a leader that alone loses its clock holds its
// timeline while the members play what it sent
#include "support/group-harness.h"
#include "sync/clock-server.h"
#include "sync/group-leader.h"
#include "sync/group-member.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <atomic>
#include <chrono>
#include <functional>
#include <memory>
#include <span>
#include <string>
#include <thread>
#include <utility>
#include <vector>

using namespace cadence;
using namespace cadence::sync;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

const std::vector<std::uint8_t> kKey(32, 0x5a);
// a lost clock is noticed after this (3 s on a device), to keep the tests short
constexpr std::int64_t kLostAfterNs = 1'000'000'000;

struct Device {
  PcmInput music;
  FakePlayback dac;
  explicit Device(double dacPpm) : dac(music, dacPpm) {}
  GroupPlayout::PlayheadSource playhead() {
    return [this] { return dac.playhead(); };
  }
};

ClockClientConfig clientConfig() {
  ClockClientConfig c;
  c.intervalNs = 10'000'000;
  c.lostAfterNs = kLostAfterNs;
  c.smoother.minSpanNs = 1'000'000'000;
  return c;
}

// a member that dials the leader's stream at streamPort and the master's clock at clockPort, or serves the clock there
GroupMemberConfig memberConfig(std::string id, std::uint16_t streamPort, std::uint16_t clockPort, SyncClock clock,
                               bool serve) {
  GroupMemberConfig c;
  c.groupId = "g";
  c.memberId = std::move(id);
  c.leaderHost = "127.0.0.1";
  c.port = streamPort;
  c.clockPort = clockPort;
  if (serve) {
    c.serveClock = true;
    c.clockBind = "127.0.0.1";
  } else {
    c.clockHost = "127.0.0.1";
  }
  c.key = kKey;
  c.budgetUs = 1000;
  c.playout.local = clock;
  c.clock = clientConfig();
  c.backoffMinMs = 100;
  c.backoffMaxMs = 400;
  return c;
}

GroupLeaderConfig leaderConfig(std::uint16_t clockPort, SyncClock clock) {
  GroupLeaderConfig c;
  c.groupId = "g";
  c.key = kKey;
  c.members = {"m", "a"};
  c.host = "127.0.0.1";
  c.port = 0;
  c.latencyMs = 500;
  c.waitMs = 4000;
  c.pcm = true;
  c.playout.local = clock;
  c.clockHost = "127.0.0.1";
  c.clockPort = clockPort;
  c.clock = clientConfig();
  return c;
}

// a udp port nobody holds now: the master's, which the leader must know before the master can know the leader's
std::uint16_t freeUdpPort() {
  auto socket = ClockSocket::open("127.0.0.1", 0);
  REQUIRE(socket.ok());
  return socket.value().port();
}

// a file source: the ramp pushed into the feed as fast as it takes it
struct Source {
  PcmInput& feed;
  std::atomic<bool> stop{false};
  std::thread thread;
  explicit Source(PcmInput& f) : feed(f) {
    thread = std::thread([this] {
      std::vector<float> pcm;
      for (std::uint64_t k = 0; !stop; ++k) {
        rampFrame(k, pcm);
        if (!feed.push(std::span<const float>(pcm), 48000, 2))
          return;
      }
    });
  }
  ~Source() {
    stop = true;
    feed.clear();
    if (thread.joinable())
      thread.join();
  }
};

bool until(const std::function<bool()>& f, std::chrono::milliseconds bound) {
  const auto end = std::chrono::steady_clock::now() + bound;
  while (!f() && std::chrono::steady_clock::now() < end)
    std::this_thread::sleep_for(10ms);
  return f();
}

// every ramp sample a device heard in [from, to), against when the master's time meant it: within boundNs but after a
// host's hiccup, and within a millisecond even there
void checkPlacement(const char* name, const Device& device, const std::function<double(std::uint64_t)>& due,
                    std::int64_t from, std::int64_t to, double boundNs = 100'000) {
  std::vector<HeardSample> window;
  for (const auto& s : device.dac.heard())
    if (s.heardNs >= from && s.heardNs < to)
      window.push_back(s);
  const auto events = device.dac.events();
  const auto errors = placementErrors(window, due, from, windowsAfter(events, kHostHiccupNs));
  const auto whole = placementErrors(window, due, from);
  INFO(name << ": max " << errors.maxAbsNs << " ns, mean " << errors.meanNs << " ns over " << errors.samples
            << " samples, " << errors.excluded << " excluded after " << events.size() << " dac events ("
            << whole.maxAbsNs << " ns with them), " << errors.faded << " faded");
  INFO("story" << (errors.maxAbsNs >= boundNs ? describeErrors(window, due, from, events, boundNs) : std::string()));
  const auto perMs = static_cast<std::size_t>((to - from) / 1'000'000);
  CHECK(errors.samples + errors.excluded > perMs * 30);
  CHECK(errors.samples > perMs * 10);
  CHECK(errors.maxAbsNs < boundNs);
  CHECK(whole.maxAbsNs < std::max(boundNs, 1'000'000.0));
}

std::size_t heardBetween(const Device& device, std::int64_t from, std::int64_t to) {
  const auto heard = device.dac.heard();
  return static_cast<std::size_t>(std::count_if(heard.begin(), heard.end(), [&](const HeardSample& s) {
    return s.heardNs >= from && s.heardNs < to;
  }));
}

}  // namespace

TEST_CASE("a member as the clock master and another device as the stream leader play one timeline within 100 us; "
          "the master gone mutes every device until it is back") {
  const auto start = monoNs();
  const SyncClock masterClock{.offsetNs = 2'000'000'000, .ppm = 8, .startNs = start};
  const SyncClock leaderClock{.offsetNs = -1'200'000'000, .ppm = -12, .startNs = start};
  const SyncClock aClock{.offsetNs = 700'000'000, .ppm = 15, .startNs = start};

  Device leaderDevice(30);
  Device masterDevice(-40);
  Device a(50);
  PcmInput feed;

  const auto clockPort = freeUdpPort();
  auto made = GroupLeader::start(feed, leaderDevice.music, leaderDevice.playhead(), leaderConfig(clockPort, leaderClock),
                                 {});
  REQUIRE(made.ok());
  auto& leader = *made.value();
  const auto port = leader.port();
  // the master is no stream leader: it is idle (nothing to fit) until its clock answers
  CHECK(leader.report().state == GroupRunState::Syncing);

  leaderDevice.dac.start();
  masterDevice.dac.start();
  a.dac.start();
  auto master = std::make_unique<GroupMember>(masterDevice.music, masterDevice.playhead(),
                                              memberConfig("m", port, clockPort, masterClock, true),
                                              GroupMemberEvents{});
  REQUIRE(master->start());
  CHECK(master->clockPort() == clockPort);
  GroupMember memberA(a.music, a.playhead(), memberConfig("a", port, clockPort, aClock, false), GroupMemberEvents{});
  REQUIRE(memberA.start());

  REQUIRE(until([&] { return master->report().connected && memberA.report().connected; }, 3000ms));
  // the leader starts nothing until its fit of the master's clock is synced
  REQUIRE(until([&] { return leader.report().synced; }, 5000ms));
  CHECK(master->report().synced);
  CHECK(master->report().clockErrorUs == 0);
  Source source(feed);
  REQUIRE(until([&] { return master->playing() && memberA.playing() && leader.report().state ==
                                                                          GroupRunState::Playing; },
                8000ms));
  const auto [epoch, t0] = leader.timeline();
  REQUIRE(epoch == 1);
  // the frames are stamped in the master's time: when it meant sample n of the epoch to be heard, on the one kernel
  // clock every device shares
  const auto due = [&, from = t0](std::uint64_t n) {
    return static_cast<double>(
        masterClock.toMono(from + static_cast<std::int64_t>(static_cast<double>(n) * 1e9 / 48000.0)));
  };
  std::this_thread::sleep_for(4000ms);
  const auto goneAt = monoNs();
  const auto settled = masterClock.toMono(t0) + 2'000'000'000;
  REQUIRE(goneAt > settled + 1'000'000'000);
  const auto leaderReport = leader.report();
  INFO("leader clock error " << leaderReport.clockErrorUs << " us, a " << memberA.report().clockErrorUs << " us");
  CHECK(leaderReport.synced);
  checkPlacement("leader", leaderDevice, due, settled, goneAt);
  checkPlacement("master", masterDevice, due, settled, goneAt);
  checkPlacement("a", a, due, settled, goneAt);

  // the master goes: nobody has the group's clock, so every device mutes (the leader holds its timeline)
  master->stop();
  master.reset();
  REQUIRE(until([&] { return leader.report().state == GroupRunState::Syncing && !memberA.report().synced; },
                3000ms));
  const auto lostAt = monoNs();
  const auto held = leader.framesSent();
  std::this_thread::sleep_for(1000ms);
  CHECK(leader.framesSent() == held);
  CHECK_FALSE(leader.report().synced);
  // after the fade, nothing is heard anywhere
  CHECK(heardBetween(leaderDevice, lostAt + 100'000'000, monoNs()) == 0);
  CHECK(heardBetween(a, lostAt + 100'000'000, monoNs()) == 0);

  // it comes back on its port with its clock: every device in step again, on the epoch the leader held (its frames
  // are still ahead of the group) or on a new one (they ran out first)
  master = std::make_unique<GroupMember>(masterDevice.music, masterDevice.playhead(),
                                         memberConfig("m", port, clockPort, masterClock, true), GroupMemberEvents{});
  REQUIRE(master->start());
  INFO("leader " << static_cast<int>(leader.report().state) << " a " << static_cast<int>(memberA.report().state)
                 << " master " << static_cast<int>(master->report().state));
  REQUIRE(until([&] { return master->playing() && memberA.playing() && leader.report().state ==
                                                                          GroupRunState::Playing; },
                12000ms));
  const auto backAt = monoNs();
  const auto [epoch2, t02] = leader.timeline();
  CHECK(epoch2 >= epoch);
  // a new epoch starts with the ramp sample the held one ended on
  const auto due2 = [&, from = t02, base = epoch2 == epoch ? 0.0 : static_cast<double>(held * kGroupFrameSamples)](
                        std::uint64_t n) {
    return static_cast<double>(
        masterClock.toMono(from + static_cast<std::int64_t>((static_cast<double>(n) - base) * 1e9 / 48000.0)));
  };
  std::this_thread::sleep_for(5000ms);
  const auto endAt = monoNs();
  // the fits start over: settled 2 s after every device plays again
  const auto settled2 = std::max(masterClock.toMono(t02), backAt) + 2'000'000'000;
  REQUIRE(endAt > settled2 + 1'000'000'000);

  memberA.stop();
  master->stop();
  leader.stop();
  leaderDevice.dac.stop();
  masterDevice.dac.stop();
  a.dac.stop();

  checkPlacement("leader back", leaderDevice, due2, settled2, endAt);
  checkPlacement("master back", masterDevice, due2, settled2, endAt);
  checkPlacement("a back", a, due2, settled2, endAt);
}

TEST_CASE("a stream leader that alone loses the group's clock holds its timeline: the members play what it sent, "
          "on time, and the group starts again once its clock is back") {
  const auto start = monoNs();
  const SyncClock masterClock{.offsetNs = -3'000'000'000, .ppm = -6, .startNs = start};
  const SyncClock leaderClock{.offsetNs = 900'000'000, .ppm = 20, .startNs = start};
  const SyncClock aClock{.offsetNs = 1'800'000'000, .ppm = -15, .startNs = start};

  Device leaderDevice(-20);
  Device masterDevice(25);
  Device a(-45);
  PcmInput feed;

  // the leader's clock answers from a server of its own that speaks the master's time (one clock, two doors): it goes
  // while the members' stays
  const auto clockPort = freeUdpPort();
  const auto leaderClockPort = freeUdpPort();
  const auto serveLeader = [&] {
    auto socket = ClockSocket::open("127.0.0.1", leaderClockPort);
    REQUIRE(socket.ok());
    auto server = std::make_unique<ClockServer>(std::move(socket.value()), kKey);
    server->setClock(masterClock);
    server->start();
    return server;
  };
  auto leaderClockServer = serveLeader();
  auto made = GroupLeader::start(feed, leaderDevice.music, leaderDevice.playhead(),
                                 leaderConfig(leaderClockPort, leaderClock), {});
  REQUIRE(made.ok());
  auto& leader = *made.value();
  const auto port = leader.port();

  leaderDevice.dac.start();
  masterDevice.dac.start();
  a.dac.start();
  GroupMember master(masterDevice.music, masterDevice.playhead(), memberConfig("m", port, clockPort, masterClock, true),
                     GroupMemberEvents{});
  REQUIRE(master.start());
  GroupMember memberA(a.music, a.playhead(), memberConfig("a", port, clockPort, aClock, false), GroupMemberEvents{});
  REQUIRE(memberA.start());
  REQUIRE(until([&] { return master.report().connected && memberA.report().connected && leader.report().synced; },
                5000ms));
  Source source(feed);
  REQUIRE(until([&] { return master.playing() && memberA.playing() && leader.report().state == GroupRunState::Playing; },
                8000ms));
  const auto [epoch, t0] = leader.timeline();
  const auto due = [&, from = t0](std::uint64_t n) {
    return static_cast<double>(
        masterClock.toMono(from + static_cast<std::int64_t>(static_cast<double>(n) * 1e9 / 48000.0)));
  };
  std::this_thread::sleep_for(3000ms);

  // the leader's clock goes: once it notices, it sends nothing more and its own output mutes, while the members, whose
  // clock stands, play what it sent up to its last frame
  leaderClockServer->stop();
  leaderClockServer.reset();
  REQUIRE(until([&] { return leader.report().state == GroupRunState::Syncing; }, 3000ms));
  const auto lostAt = monoNs();
  const auto held = leader.framesSent();
  // the last frame sent ends here, on the shared kernel clock
  const auto lastHeard = due(held * kGroupFrameSamples);
  std::this_thread::sleep_for(1000ms);
  CHECK(leader.framesSent() == held);
  CHECK(memberA.report().synced);
  CHECK(heardBetween(leaderDevice, lostAt + 100'000'000, monoNs()) == 0);
  // the members still play the sent frames, on time, until they run out
  const auto settled = masterClock.toMono(t0) + 2'000'000'000;
  REQUIRE(static_cast<std::int64_t>(lastHeard) > lostAt + 500'000'000);
  std::this_thread::sleep_for(std::chrono::nanoseconds(static_cast<std::int64_t>(lastHeard) - monoNs()) + 300ms);
  checkPlacement("a held", a, due, settled, static_cast<std::int64_t>(lastHeard) - 50'000'000);
  checkPlacement("master held", masterDevice, due, settled, static_cast<std::int64_t>(lastHeard) - 50'000'000);
  CHECK(heardBetween(a, static_cast<std::int64_t>(lastHeard) + 100'000'000, monoNs()) == 0);

  // its clock is back: a new epoch (what it sent ran out), every device in step again
  leaderClockServer = serveLeader();
  const auto states = [&] {
    std::string out;
    for (const auto r : {leader.report().state, memberA.report().state, master.report().state})
      out += std::to_string(static_cast<int>(r)) + " ";
    return out + "epoch " + std::to_string(leader.timeline().first) + " synced " +
           std::to_string(leader.report().synced ? 1 : 0);
  };
  const bool back = until([&] { return master.playing() && memberA.playing() &&
                                       leader.report().state == GroupRunState::Playing && leader.timeline().first > epoch; },
                          12000ms);
  INFO("states " << states());
  REQUIRE(back);
  const auto [epoch2, t02] = leader.timeline();
  // a new epoch starts with the ramp sample the held one ended on
  const auto due2 = [&, from = t02, base = epoch2 == epoch ? 0.0 : static_cast<double>(held * kGroupFrameSamples)](
                        std::uint64_t n) {
    return static_cast<double>(
        masterClock.toMono(from + static_cast<std::int64_t>((static_cast<double>(n) - base) * 1e9 / 48000.0)));
  };
  std::this_thread::sleep_for(4000ms);
  const auto endAt = monoNs();
  const auto settled2 = masterClock.toMono(t02) + 2'000'000'000;

  memberA.stop();
  master.stop();
  leader.stop();
  leaderClockServer->stop();
  leaderDevice.dac.stop();
  masterDevice.dac.stop();
  a.dac.stop();

  CHECK(epoch2 > epoch);
  checkPlacement("leader back", leaderDevice, due2, settled2, endAt);
  checkPlacement("a back", a, due2, settled2, endAt);
}
