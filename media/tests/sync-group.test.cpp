// a whole group in one process over 127.0.0.1: a leader and two members whose sim clocks are offset and skewed and
// whose dacs run off their nominal rates. every device's output is measured against when the leader meant each sample
// to be heard (true mono time); a member killed and started again comes back on the right frame; a wrong key is refused
#include "support/group-harness.h"
#include "sync/group-leader.h"
#include "sync/group-member.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <atomic>
#include <chrono>
#include <cmath>
#include <functional>
#include <map>
#include <memory>
#include <mutex>
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

const std::vector<std::uint8_t> kKey(32, 0x3c);

struct Device {
  PcmInput music;
  FakePlayback dac;
  explicit Device(double dacPpm, std::int64_t latencyNs = 20'000'000, std::size_t period = 240)
      : dac(music, dacPpm, latencyNs, period) {}
  GroupPlayout::PlayheadSource playhead() {
    return [this] { return dac.playhead(); };
  }
};

GroupMemberConfig memberConfig(std::string id, std::uint16_t port, SyncClock clock,
                               std::vector<std::uint8_t> key = kKey) {
  GroupMemberConfig c;
  c.groupId = "g";
  c.memberId = std::move(id);
  c.leaderHost = "127.0.0.1";
  c.port = port;
  c.key = std::move(key);
  c.budgetUs = 1000;
  c.playout.local = clock;
  c.clock.intervalNs = 10'000'000;
  c.clock.smoother.minSpanNs = 1'000'000'000;
  c.backoffMinMs = 100;
  c.backoffMaxMs = 400;
  return c;
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

// a track: frames of the ramp pushed as fast as the feed takes them, then nothing more (the source ended)
struct Track {
  PcmInput& feed;
  std::thread thread;
  Track(PcmInput& f, std::uint64_t frames) : feed(f) {
    thread = std::thread([this, frames] {
      std::vector<float> pcm;
      for (std::uint64_t k = 0; k < frames; ++k) {
        rampFrame(k, pcm);
        if (!feed.push(std::span<const float>(pcm), 48000, 2))
          return;
      }
    });
  }
  ~Track() {
    if (thread.joinable())
      thread.join();
  }
};

}  // namespace

TEST_CASE("a leader and two members play one timeline within 100 us, a restarted member rejoins on time, and a "
          "wrong key is refused") {
  const auto start = monoNs();
  const SyncClock leaderClock{.offsetNs = 1'500'000'000, .ppm = 5, .startNs = start};
  const SyncClock aClock{.offsetNs = -700'000'000, .ppm = -10, .startNs = start};
  const SyncClock bClock{.offsetNs = 3'000'000'000, .ppm = 10, .startNs = start};

  Device leaderDevice(30);
  Device a(-60);
  Device b(60);
  Device stranger(0);
  PcmInput feed;

  GroupLeaderConfig lc;
  lc.groupId = "g";
  lc.key = kKey;
  lc.members = {"a", "b"};
  lc.host = "127.0.0.1";
  lc.port = 0;
  lc.latencyMs = 500;
  lc.waitMs = 4000;
  lc.pcm = true;
  lc.playout.local = leaderClock;
  auto made = GroupLeader::start(feed, leaderDevice.music, leaderDevice.playhead(), lc, {});
  REQUIRE(made.ok());
  auto& leader = *made.value();
  const auto port = leader.port();

  leaderDevice.dac.start();
  a.dac.start();
  b.dac.start();
  stranger.dac.start();
  auto memberA = std::make_unique<GroupMember>(a.music, a.playhead(), memberConfig("a", port, aClock),
                                               GroupMemberEvents{});
  auto memberB = std::make_unique<GroupMember>(b.music, b.playhead(), memberConfig("b", port, bClock),
                                               GroupMemberEvents{});
  GroupMember wrong(stranger.music, stranger.playhead(),
                    memberConfig("a", port, SyncClock{}, std::vector<std::uint8_t>(32, 0x01)), GroupMemberEvents{});
  REQUIRE(memberA->start());
  REQUIRE(memberB->start());
  REQUIRE(wrong.start());

  // the source starts once both members are connected: the start waits for their Ready (synced clocks)
  const auto until = [](auto f, std::chrono::milliseconds bound) {
    const auto end = std::chrono::steady_clock::now() + bound;
    while (!f() && std::chrono::steady_clock::now() < end)
      std::this_thread::sleep_for(10ms);
    return f();
  };
  REQUIRE(until([&] { return memberA->report().connected && memberB->report().connected; }, 3000ms));
  Source source(feed);
  REQUIRE(until([&] { return memberA->playing() && memberB->playing() && leader.report().state ==
                                                                           GroupRunState::Playing; },
                8000ms));
  const auto [epoch, t0] = leader.timeline();
  REQUIRE(epoch == 1);
  // when the leader meant sample n of the epoch to be heard, on the one kernel clock every device shares
  const auto due = [&, t0 = t0](std::uint64_t n) {
    return static_cast<double>(leaderClock.toMono(t0 + static_cast<std::int64_t>(static_cast<double>(n) * 1e9 / 48000.0)));
  };

  std::this_thread::sleep_for(3000ms);
  // b goes away and comes back while the group plays on
  memberB->stop();
  memberB.reset();
  const auto restartAt = monoNs();
  memberB = std::make_unique<GroupMember>(b.music, b.playhead(), memberConfig("b", port, bClock),
                                          GroupMemberEvents{});
  REQUIRE(memberB->start());
  REQUIRE(until([&] { return memberB->playing(); }, 5000ms));
  const auto backAt = monoNs();
  std::this_thread::sleep_for(3000ms);
  const auto endAt = monoNs();

  const auto report = leader.report();
  const auto reportA = memberA->report();
  const auto reportB = memberB->report();
  const auto wrongReport = wrong.report();
  wrong.stop();
  memberA->stop();
  memberB->stop();
  leader.stop();
  leaderDevice.dac.stop();
  a.dac.stop();
  b.dac.stop();
  stranger.dac.stop();

  // settled: from 2 s after the first frame was heard
  const auto settled = leaderClock.toMono(t0) + 2'000'000'000;
  REQUIRE(endAt > settled);
  INFO("leader rejoins " << report.rejoins << " underruns " << report.underruns << "; a " << reportA.rejoins << "/"
                          << reportA.underruns << "; b " << reportB.rejoins << "/" << reportB.underruns);
  // the algorithm settled, not the host: the bound holds everywhere but just after a dac overslept or its input ran
  // short (a busy host's hiccup), and every sample is within a millisecond even there
  const auto check = [&](const char* name, const Device& device, std::int64_t from, std::int64_t to,
                         double boundNs = 100'000) {
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
  };
  check("leader", leaderDevice, settled, endAt);
  check("a", a, settled, endAt);
  check("b before", b, settled, restartAt);
  // back on the right frame at once (its fade in left out), within a millisecond while its steering settles onto its
  // dac again, then within 100 us
  check("b back", b, backAt + 50'000'000, backAt + 1'500'000'000, 1'000'000);
  check("b after", b, backAt + 1'500'000'000, endAt);

  CHECK(wrongReport.error == "auth_failed");
  CHECK_FALSE(wrongReport.connected);
  CHECK(stranger.dac.heard().empty());
  REQUIRE(report.members.size() == 2);
  CHECK(report.members[0].connected);
  CHECK(report.members[1].connected);
}

namespace {

// a url source as the leader's sessions drive it: it decodes the ramp from a frame on as fast as the feed takes it;
// a pause stops it and clears what it queued, a resume pushes again from where the group heard
class SeekableSource {
 public:
  explicit SeekableSource(PcmInput& feed) : feed_(feed) {}
  ~SeekableSource() { pause(); }
  SeekableSource(const SeekableSource&) = delete;
  SeekableSource& operator=(const SeekableSource&) = delete;

  void play(std::uint64_t fromFrame) {
    pause();
    stop_ = false;
    thread_ = std::thread([this, fromFrame] {
      std::vector<float> pcm;
      for (auto k = fromFrame; !stop_; ++k) {
        rampFrame(k, pcm);
        feed_.push(std::span<const float>(pcm), 48000, 2);
      }
    });
  }
  void pause() {
    stop_ = true;
    feed_.clear();
    if (thread_.joinable())
      thread_.join();
  }

 private:
  PcmInput& feed_;
  std::atomic<bool> stop_{true};
  std::thread thread_;
};

// what a playout's probe told: (epoch, seq) -> the leader time it was heard at
struct HeardLog {
  std::mutex mutex;
  std::map<std::pair<std::uint32_t, std::uint64_t>, std::int64_t> heard;
  std::function<void(std::uint32_t, std::uint64_t, std::int64_t)> probe() {
    return [this](std::uint32_t epoch, std::uint64_t seq, std::int64_t ns) {
      std::lock_guard lock(mutex);
      heard[{epoch, seq}] = ns;
    };
  }
  std::map<std::pair<std::uint32_t, std::uint64_t>, std::int64_t> copy() {
    std::lock_guard lock(mutex);
    return heard;
  }
};

}  // namespace

TEST_CASE("a pause, a resume, a seek and a new play start the group again every time, the probe agreeing") {
  const auto start = monoNs();
  const SyncClock leaderClock{.offsetNs = 2'000'000'000, .ppm = 20, .startNs = start};
  const SyncClock memberClock{.offsetNs = -900'000'000, .ppm = -30, .startNs = start};
  Device leaderDevice(25);
  Device a(-40);
  PcmInput feed;
  HeardLog leaderHeard;
  HeardLog memberHeard;

  GroupLeaderConfig lc;
  lc.groupId = "g";
  lc.key = kKey;
  lc.members = {"a"};
  lc.host = "127.0.0.1";
  lc.port = 0;
  lc.latencyMs = 400;
  lc.waitMs = 3000;
  lc.playout.local = leaderClock;
  lc.playout.heard = leaderHeard.probe();
  lc.playout.heardEvery = 25;
  auto made = GroupLeader::start(feed, leaderDevice.music, leaderDevice.playhead(), lc, {});
  REQUIRE(made.ok());
  auto& leader = *made.value();
  leaderDevice.dac.start();
  a.dac.start();
  auto mc = memberConfig("a", leader.port(), memberClock);
  mc.playout.heard = memberHeard.probe();
  mc.playout.heardEvery = 25;
  GroupMember member(a.music, a.playhead(), mc, GroupMemberEvents{});
  REQUIRE(member.start());

  const auto until = [](auto f, std::chrono::milliseconds bound) {
    const auto end = std::chrono::steady_clock::now() + bound;
    while (!f() && std::chrono::steady_clock::now() < end)
      std::this_thread::sleep_for(5ms);
    return f();
  };
  const auto playing = [&] {
    return until([&] { return member.playing() && leader.report().state == GroupRunState::Playing; }, 8000ms);
  };
  const auto stopped = [&] {
    return until([&] { return !member.playing() && leader.report().state == GroupRunState::Idle; }, 3000ms);
  };
  REQUIRE(until([&] { return member.report().connected && member.report().synced; }, 5000ms));

  SeekableSource source(feed);
  source.play(0);
  REQUIRE(playing());
  std::uint32_t epoch = leader.timeline().first;
  const auto next = [&] {
    const auto e = leader.timeline().first;
    const bool moved = e > epoch;
    epoch = e;
    return moved;
  };

  // as audiod's runCommand: the group's hook first, then the source; a paused source resumes from what was heard.
  // the feed is full at every pause (the source decodes ahead), which the resumed source used to find no room in
  for (int round = 0; round < 3; ++round) {
    INFO("pause and resume " << round);
    std::this_thread::sleep_for(1500ms);
    const auto heardMs = leader.pause();
    source.pause();
    REQUIRE(stopped());
    leader.resume();
    source.play(heardMs * 48 / kGroupFrameSamples);
    REQUIRE(playing());
    CHECK(next());
  }
  {
    INFO("pause, seek, resume");
    leader.pause();
    source.pause();
    REQUIRE(stopped());
    leader.flush(20000);
    leader.resume();
    source.play(1000);
    REQUIRE(playing());
    CHECK(next());
  }
  {
    INFO("pause, then a new play");
    leader.pause();
    source.pause();
    REQUIRE(stopped());
    leader.flush(0);
    source.play(0);
    REQUIRE(playing());
    CHECK(next());
  }
  {
    INFO("a new play over a playing one: the old audio's clear right before the new");
    std::this_thread::sleep_for(1000ms);
    leader.flush(0);
    source.play(0);
    REQUIRE(playing());
    CHECK(next());
  }
  std::this_thread::sleep_for(2500ms);

  member.stop();
  leader.stop();
  leaderDevice.dac.stop();
  a.dac.stop();

  // the probe on both devices: the same frame heard at the same leader time, within the placement's bound
  const auto ours = leaderHeard.copy();
  const auto theirs = memberHeard.copy();
  std::size_t matched = 0;
  double worst = 0;
  for (const auto& [key, ns] : ours) {
    const auto found = theirs.find(key);
    if (found == theirs.end())
      continue;
    ++matched;
    worst = std::max(worst, std::abs(static_cast<double>(found->second - ns)));
  }
  INFO("matched " << matched << " of " << ours.size() << " and " << theirs.size() << ", worst " << worst << " ns");
  CHECK(matched >= 10);
  CHECK(worst < 1'000'000);
}

TEST_CASE("a member whose link drops and comes back while it plays counts a rejoin; its first welcome does not") {
  const auto start = monoNs();
  const SyncClock leaderClock{.offsetNs = 1'500'000'000, .ppm = 5, .startNs = start};
  const SyncClock aClock{.offsetNs = -700'000'000, .ppm = -10, .startNs = start};
  Device leaderDevice(0);
  Device a(0);
  PcmInput feed;

  GroupLeaderConfig lc;
  lc.groupId = "g";
  lc.key = kKey;
  lc.members = {"a"};
  lc.host = "127.0.0.1";
  lc.port = 0;
  lc.latencyMs = 500;
  lc.playout.local = leaderClock;
  auto made = GroupLeader::start(feed, leaderDevice.music, leaderDevice.playhead(), lc, {});
  REQUIRE(made.ok());
  auto first = std::move(made.value());
  const auto port = first->port();

  GroupMember member(a.music, a.playhead(), memberConfig("a", port, aClock), GroupMemberEvents{});
  REQUIRE(member.start());
  const auto until = [](auto f, std::chrono::milliseconds bound) {
    const auto end = std::chrono::steady_clock::now() + bound;
    while (!f() && std::chrono::steady_clock::now() < end)
      std::this_thread::sleep_for(10ms);
    return f();
  };
  REQUIRE(until([&] { return member.report().connected; }, 3000ms));
  CHECK(member.report().rejoins == 0);

  // the leader goes away and a new one answers on the same port: the member's link drops and comes back
  first->stop();
  first.reset();
  REQUIRE(until([&] { return !member.report().connected; }, 3000ms));
  lc.port = port;
  auto again = GroupLeader::start(feed, leaderDevice.music, leaderDevice.playhead(), lc, {});
  REQUIRE(again.ok());
  REQUIRE(until([&] { return member.report().connected; }, 5000ms));
  CHECK(member.report().rejoins == 1);

  member.stop();
  again.value()->stop();
}

TEST_CASE("an auto-tuned leader: the start wait from a member's ready time, a rejoin's raise at the next epoch") {
  const auto start = monoNs();
  const SyncClock leaderClock{.offsetNs = 300'000'000, .ppm = 3, .startNs = start};
  const SyncClock aClock{.offsetNs = -200'000'000, .ppm = -6, .startNs = start};
  Device leaderDevice(0);
  Device a(0);
  PcmInput feed;

  GroupLeaderConfig lc;
  lc.groupId = "g";
  lc.key = kKey;
  lc.members = {"a"};
  lc.host = "127.0.0.1";
  lc.port = 0;
  lc.latencyMs = 500;
  lc.waitMs = 4000;
  lc.autoLatency = true;
  lc.autoBitrate = true;
  lc.autoWait = true;
  lc.playout.local = leaderClock;
  std::mutex mutex;
  std::vector<GroupTuned> tuned;
  GroupLeaderEvents events;
  events.tuned = [&](const GroupTuned& t) {
    std::lock_guard lock(mutex);
    tuned.push_back(t);
  };
  auto made = GroupLeader::start(feed, leaderDevice.music, leaderDevice.playhead(), lc, std::move(events));
  REQUIRE(made.ok());
  auto& leader = *made.value();
  leaderDevice.dac.start();
  a.dac.start();
  GroupMember member(a.music, a.playhead(), memberConfig("a", leader.port(), aClock), GroupMemberEvents{});
  REQUIRE(member.start());
  const auto until = [](auto f, std::chrono::milliseconds bound) {
    const auto end = std::chrono::steady_clock::now() + bound;
    while (!f() && std::chrono::steady_clock::now() < end)
      std::this_thread::sleep_for(10ms);
    return f();
  };
  REQUIRE(until([&] { return member.report().synced; }, 8000ms));
  CHECK(leader.report().waitMs == 4000);
  CHECK(leader.report().bitrateKbps == 256);

  Source source(feed);
  REQUIRE(until([&] { return member.report().state == GroupRunState::Playing; }, 8000ms));
  // its ready time, well under a second on the loopback: wait = max(delay * 1.5 + 300, 500)
  const auto wait = leader.report().waitMs;
  CHECK(wait >= 500);
  CHECK(wait < 2000);
  CHECK(leader.report().latencyMs == 500);

  // the member sets the group aside and plays again: a join that is no first one, while the group plays
  member.setActive(false);
  std::this_thread::sleep_for(300ms);
  member.setActive(true);
  std::this_thread::sleep_for(300ms);
  // the raise waits for the next epoch
  CHECK(leader.report().latencyMs == 500);
  leader.trackChanged(0);
  REQUIRE(until([&] { return leader.report().latencyMs != 500; }, 5000ms));
  CHECK(leader.report().latencyMs == 750);
  {
    std::lock_guard lock(mutex);
    REQUIRE_FALSE(tuned.empty());
    CHECK(tuned.front().what == "latency");
    CHECK(tuned.front().from == 500);
    CHECK(tuned.front().to == 750);
    // the rejoin, unless a loaded host's underrun came first (at most one raise in 10 s)
    CHECK((tuned.front().reason == "rejoin" || tuned.front().reason == "underrun"));
  }
  REQUIRE(until([&] { return member.report().state == GroupRunState::Playing; }, 8000ms));

  member.stop();
  leader.stop();
  leaderDevice.dac.stop();
  a.dac.stop();
}

TEST_CASE("tracks that end on their own leave the member idle: no rejoin, no underrun, nothing the tuner takes for a "
          "fault") {
  // the leader on a speaker, the member on a tv whose output reads 43 ms a period. each track ends, the member's
  // session lets the group go (setActive(false), as audiod's does once the group is idle), and the next track brings it
  // back: the run-out rejoined on the tv, and the leader took that and the join back for rejoins, stepping the
  // bitrate down at the next track
  const auto start = monoNs();
  const SyncClock leaderClock{.offsetNs = 400'000'000, .ppm = 4, .startNs = start};
  const SyncClock aClock{.offsetNs = -300'000'000, .ppm = -7, .startNs = start};
  Device leaderDevice(0);
  Device a(0, 60'000'000, 2064);
  PcmInput feed;

  GroupLeaderConfig lc;
  lc.groupId = "g";
  lc.key = kKey;
  lc.members = {"a"};
  lc.host = "127.0.0.1";
  lc.port = 0;
  lc.latencyMs = 1000;
  lc.waitMs = 3000;
  lc.autoLatency = true;
  lc.autoBitrate = true;
  lc.playout.local = leaderClock;
  std::mutex mutex;
  std::vector<GroupTuned> tuned;
  GroupLeaderEvents events;
  events.tuned = [&](const GroupTuned& t) {
    std::lock_guard lock(mutex);
    tuned.push_back(t);
  };
  auto made = GroupLeader::start(feed, leaderDevice.music, leaderDevice.playhead(), lc, std::move(events));
  REQUIRE(made.ok());
  auto& leader = *made.value();
  leaderDevice.dac.start();
  a.dac.start();
  auto mc = memberConfig("a", leader.port(), aClock);
  mc.active = false;
  GroupMember member(a.music, a.playhead(), mc, GroupMemberEvents{});
  REQUIRE(member.start());
  const auto until = [](auto f, std::chrono::milliseconds bound) {
    const auto end = std::chrono::steady_clock::now() + bound;
    while (!f() && std::chrono::steady_clock::now() < end)
      std::this_thread::sleep_for(10ms);
    return f();
  };
  REQUIRE(until([&] { return member.report().synced; }, 8000ms));

  constexpr int kTracks = 3;
  for (int i = 0; i < kTracks; ++i) {
    INFO("track " << i + 1);
    // 2 s of audio, the member's session taking the speaker as the track starts
    Track track(feed, 100 + static_cast<std::uint64_t>(i) * 7);
    member.setActive(true);
    REQUIRE(until([&] { return member.report().state == GroupRunState::Playing; }, 8000ms));
    // the track is heard out, and the group falls idle on both devices
    REQUIRE(until([&] { return !leader.sounding(); }, 8000ms));
    CHECK(until([&] { return member.report().state == GroupRunState::Idle; }, 2000ms));
    CHECK(leader.report().state == GroupRunState::Idle);
    CHECK(leader.playout().stats().state == PlayoutState::Paused);
    // the member's session lets the group go
    std::this_thread::sleep_for(300ms);
    member.setActive(false);
  }
  const auto report = leader.report();
  const auto mine = member.report();
  INFO("leader rejoins " << report.rejoins << " underruns " << report.underruns << "; member " << mine.rejoins << "/"
                         << mine.underruns);
  CHECK(mine.rejoins == 0);
  CHECK(mine.underruns == 0);
  CHECK(member.playout().stats().rejoins == 0);
  CHECK(report.rejoins == 0);
  CHECK(report.bitrateKbps == 256);
  CHECK(report.latencyMs == 1000);
  {
    std::lock_guard lock(mutex);
    for (const auto& t : tuned)
      UNSCOPED_INFO("tuned " << t.what << " " << t.from << " -> " << t.to << " " << t.reason);
    CHECK(tuned.empty());
  }

  member.stop();
  leader.stop();
  leaderDevice.dac.stop();
  a.dac.stop();
}
