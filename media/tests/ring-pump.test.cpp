// the screen's ring on a group's leader (P2): a ring pump reads it into the leader's feed instead of the mixer, and
// publishes on the ring when the group hears each frame, frozen until the epoch's T0, so the player's picture waits
// for the group. a whole group over 127.0.0.1 (sim clocks offset and skewed, dacs off their rates): a member plays
// what the player wrote, at the time the ring said; a seek of the player's (a ring flush) and a pause start the group
// again, and the heard times follow each epoch
#include "audiod/group-feed.h"
#include "audiod/ring-pump.h"
#include "ring/ring-input.h"
#include "support/group-harness.h"
#include "sync/group-leader.h"
#include "sync/group-member.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <atomic>
#include <chrono>
#include <cstdint>
#include <cstdlib>
#include <functional>
#include <memory>
#include <mutex>
#include <optional>
#include <span>
#include <string>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::sync;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

const std::vector<std::uint8_t> kKey(32, 0x5a);
constexpr std::int64_t kReadPeriodNs = 20'000'000;
constexpr std::int64_t kLatencyNs = 500'000'000;

struct Device {
  PcmInput music;
  FakePlayback dac;
  explicit Device(double dacPpm) : dac(music, dacPpm) {}
  GroupPlayout::PlayheadSource playhead() {
    return [this] { return dac.playhead(); };
  }
};

GroupMemberConfig memberConfig(std::string id, std::uint16_t port, SyncClock clock) {
  GroupMemberConfig c;
  c.groupId = "g";
  c.memberId = std::move(id);
  c.leaderHost = "127.0.0.1";
  c.port = port;
  c.key = kKey;
  c.budgetUs = 1000;
  c.playout.local = clock;
  c.clock.intervalNs = 10'000'000;
  c.clock.smoother.minSpanNs = 1'000'000'000;
  c.backoffMinMs = 100;
  c.backoffMaxMs = 400;
  return c;
}

// the player, as ao_cadence writes: the ramp, 20 ms at a time while the ring has room, streaming. a flush (a seek)
// moves it to another ramp frame, a pause stops it with its frames held
class Player {
 public:
  explicit Player(int ringFd) : writer_(RingWriter::attach(ringFd)) {
    REQUIRE(writer_);
    writer_->setState(CADENCE_RING_STREAMING);
    thread_ = std::thread([this] { run(); });
  }
  ~Player() {
    stop_ = true;
    thread_.join();
  }

  // a seek: nothing queued before it is heard after it, and the ramp goes on from ramp frame frame (960 samples
  // each); the ring position its first sample takes
  std::uint64_t seek(std::uint64_t frame) {
    std::lock_guard lock(mutex_);
    writer_->flush();
    next_ = frame;
    done_ = 0;
    segment_ = {writer_->written(), frame * kGroupFrameSamples};
    return segment_.ringStart;
  }
  void pause(bool paused) {
    std::lock_guard lock(mutex_);
    paused_ = paused;
    writer_->setState(paused ? CADENCE_RING_PAUSED : CADENCE_RING_STREAMING);
  }
  // the ramp sample at ring position pos, in the segment the last seek began
  [[nodiscard]] std::uint64_t rampAt(std::uint64_t pos) const {
    std::lock_guard lock(mutex_);
    return segment_.rampStart + (pos - segment_.ringStart);
  }
  [[nodiscard]] bool timestamp(std::uint64_t& pos, std::int64_t& heardNs) const {
    return writer_->timestamp(pos, heardNs);
  }

 private:
  struct Segment {
    std::uint64_t ringStart = 0;
    std::uint64_t rampStart = 0;
  };

  void run() {
    std::vector<float> pcm;
    while (!stop_) {
      bool wrote = false;
      {
        std::lock_guard lock(mutex_);
        if (!paused_) {
          rampFrame(next_, pcm);
          done_ += writer_->write(std::span<const float>(pcm).subspan(done_ * 2));
          wrote = true;
          if (done_ == kGroupFrameSamples) {
            done_ = 0;
            ++next_;
            continue;
          }
        }
      }
      // no room (or paused): the reader's next period makes some
      std::this_thread::sleep_for(wrote ? 1ms : 2ms);
    }
  }

  std::unique_ptr<RingWriter> writer_;
  mutable std::mutex mutex_;
  std::uint64_t next_ = 0;
  std::size_t done_ = 0;
  bool paused_ = false;
  Segment segment_;
  std::atomic<bool> stop_{false};
  std::thread thread_;
};

struct Stamp {
  std::int64_t nowNs = 0;
  std::uint64_t pos = 0;
  std::int64_t heardNs = 0;
  [[nodiscard]] bool frozen() const { return heardNs - nowNs < 100'000'000; }
};

std::optional<Stamp> stamp(const Player& player) {
  Stamp s;
  s.nowNs = monoNs();
  if (!player.timestamp(s.pos, s.heardNs))
    return std::nullopt;
  return s;
}

bool until(const std::function<bool()>& f, std::chrono::milliseconds bound) {
  const auto end = std::chrono::steady_clock::now() + bound;
  while (!f() && std::chrono::steady_clock::now() < end)
    std::this_thread::sleep_for(5ms);
  return f();
}

// what the member heard of the ramp over a second from the stamp's heard time on, against the times the ring said:
// ramp sample n is due at the stamp's heardNs plus its distance from the stamp's frame
void checkHeard(const char* name, const Device& device, const Player& player, const Stamp& s) {
  const auto n0 = player.rampAt(s.pos);
  const auto due = [&](std::uint64_t n) {
    return static_cast<double>(s.heardNs) +
           (static_cast<double>(n) - static_cast<double>(n0)) * 1e9 / static_cast<double>(kGroupRate);
  };
  const auto from = s.heardNs + 50'000'000;
  const auto to = s.heardNs + 1'050'000'000;
  REQUIRE(until([&] { return monoNs() > to + 100'000'000; }, 3000ms));
  std::vector<HeardSample> window;
  for (const auto& h : device.dac.heard())
    if (h.heardNs >= from && h.heardNs < to)
      window.push_back(h);
  const auto errors = placementErrors(window, due, from, {}, static_cast<double>(kReadPeriodNs));
  INFO(name << ": max " << errors.maxAbsNs << " ns, mean " << errors.meanNs << " ns over " << errors.samples
            << " samples, " << errors.faded << " faded");
  // a second of the ramp heard, each sample within one read period of the time published for it
  CHECK(errors.samples > 48'000 / 2);
  CHECK(errors.maxAbsNs < static_cast<double>(kReadPeriodNs));
}

// a leader on its sim clock, one member that plays, and the leader's feed as the pump finds it
struct Group {
  SyncClock leaderClock;
  SyncClock memberClock;
  Device leaderDevice{30};
  Device a{-60};
  PcmInput feedInput;
  std::unique_ptr<GroupLeader> leader;
  std::unique_ptr<GroupMember> member;
  std::unique_ptr<LeaderFeed> feed;
  std::mutex mutex;
  std::vector<std::int64_t> started;
  std::uint32_t flushes = 0;

  // members beyond "a" never come: the start's barrier waits waitMs for them
  Group(std::vector<std::string> members, std::uint32_t waitMs) {
    const auto start = monoNs();
    leaderClock = SyncClock{.offsetNs = 1'500'000'000, .ppm = 5, .startNs = start};
    memberClock = SyncClock{.offsetNs = -700'000'000, .ppm = -10, .startNs = start};
    GroupLeaderConfig lc;
    lc.groupId = "g";
    lc.key = kKey;
    lc.members = std::move(members);
    lc.host = "127.0.0.1";
    lc.port = 0;
    lc.latencyMs = static_cast<std::uint32_t>(kLatencyNs / 1'000'000);
    lc.waitMs = waitMs;
    lc.pcm = true;
    lc.playout.local = leaderClock;
    auto made = GroupLeader::start(feedInput, leaderDevice.music, leaderDevice.playhead(), lc, {});
    REQUIRE(made.ok());
    leader = std::move(made.value());
    leaderDevice.dac.start();
    a.dac.start();
    member = std::make_unique<GroupMember>(a.music, a.playhead(), memberConfig("a", leader->port(), memberClock),
                                           GroupMemberEvents{});
    REQUIRE(member->start());
    REQUIRE(until([&] { return member->report().connected && member->report().synced; }, 5000ms));
    LeaderFeed::Hooks hooks;
    hooks.started = [this](std::int64_t delayNs) {
      std::lock_guard lock(mutex);
      started.push_back(delayNs);
    };
    hooks.flushed = [this] {
      std::lock_guard lock(mutex);
      ++flushes;
    };
    feed = std::make_unique<LeaderFeed>(*leader, feedInput, std::move(hooks));
  }
  ~Group() {
    member->stop();
    leader->stop();
    leaderDevice.dac.stop();
    a.dac.stop();
  }

  [[nodiscard]] bool playing(std::uint32_t epoch) const {
    return leader->timeline().first == epoch && leader->report().state == GroupRunState::Playing && member->playing();
  }
};

}  // namespace

TEST_CASE("a ring on a group's leader reaches a member, each frame published when the group hears it, frozen until "
          "T0") {
  // a second member never comes: the start waits 1.5 s for it, the ring held frozen meanwhile
  Group group({"a", "b"}, 1500);
  auto ring = RingInput::create(8192);
  REQUIRE(ring.ok());
  auto& r = *ring.value();
  r.setOpen(true);
  Player player(r.ringFd());
  std::vector<Stamp> stamps;
  {
    RingPump pump(r, *group.feed);
    REQUIRE(until(
        [&] {
          if (const auto s = stamp(player))
            stamps.push_back(*s);
          return group.playing(1);
        },
        8000ms));
    std::this_thread::sleep_for(1500ms);
    const auto steady = stamp(player);
    REQUIRE(steady);
    REQUIRE_FALSE(steady->frozen());
    // the group's time of the frame: T0 plus its offset in the epoch, which began at the ring's first frame
    const auto [epoch, t0] = group.leader->timeline();
    CHECK(epoch == 1);
    const auto groupTime = group.leaderClock.toMono(
        t0 + static_cast<std::int64_t>(steady->pos * 1'000'000'000ULL / kGroupRate));
    INFO("published " << steady->heardNs << " group " << groupTime << " pos " << steady->pos);
    CHECK(std::abs(steady->heardNs - groupTime) < kReadPeriodNs);
    // the group's latency ahead, not the mixer's few tens of ms
    CHECK(steady->heardNs - steady->nowNs > kLatencyNs - 100'000'000);
    checkHeard("member", group.a, player, *steady);
  }

  // frozen until T0: the barrier's 1.5 s at the first frame, its heard time a read period from each stamp's now
  const auto frozen = std::count_if(stamps.begin(), stamps.end(), [](const Stamp& s) { return s.frozen(); });
  INFO(frozen << " frozen stamps of " << stamps.size());
  CHECK(frozen > 50);
  for (const auto& s : stamps)
    if (s.frozen()) {
      CHECK(s.pos <= kGroupFrameSamples);
      CHECK(s.heardNs - s.nowNs <= RingPump::kFrozenAheadNs + 5'000'000);
    }
  std::lock_guard lock(group.mutex);
  // the start's delay: the barrier's wait and the latency past it
  REQUIRE(group.started.size() == 1);
  CHECK(group.started[0] > 1'500'000'000);
  CHECK(group.started[0] < 3'000'000'000);
}

TEST_CASE("a seek of the player's and a pause start the group again, the published heard times following each "
          "epoch") {
  Group group({"a"}, 3000);
  auto ring = RingInput::create(8192);
  REQUIRE(ring.ok());
  auto& r = *ring.value();
  r.setOpen(true);
  Player player(r.ringFd());
  RingPump pump(r, *group.feed);
  REQUIRE(until([&] { return group.playing(1); }, 8000ms));
  std::this_thread::sleep_for(1000ms);
  auto s = stamp(player);
  REQUIRE(s);
  REQUIRE_FALSE(s->frozen());
  checkHeard("first epoch", group.a, player, *s);

  // the player seeks 10 s on: its ring flush flushes the group, and the next epoch starts at the flush
  SECTION("a seek") {
    const auto at = player.seek(500);
    REQUIRE(until([&] { return group.playing(2); }, 8000ms));
    std::this_thread::sleep_for(1000ms);
    s = stamp(player);
    REQUIRE(s);
    REQUIRE_FALSE(s->frozen());
    CHECK(s->pos >= at);
    CHECK(player.rampAt(s->pos) >= 500 * kGroupFrameSamples);
    checkHeard("after the seek", group.a, player, *s);
    std::lock_guard lock(group.mutex);
    CHECK(group.flushes == 1);
    CHECK(group.started.size() == 2);
  }

  // the group pauses (the session's command), then the player: the ring is frozen where the group plays next
  SECTION("a pause") {
    group.leader->pause();
    player.pause(true);
    std::this_thread::sleep_for(400ms);
    std::vector<Stamp> paused;
    for (int i = 0; i < 40; ++i) {
      if (const auto p = stamp(player))
        paused.push_back(*p);
      std::this_thread::sleep_for(5ms);
    }
    REQUIRE(paused.size() == 40);
    for (const auto& p : paused) {
      CHECK(p.frozen());
      CHECK(p.pos == paused.front().pos);
    }
    CHECK(until([&] { return !group.member->playing(); }, 2000ms));

    player.pause(false);
    group.leader->resume();
    REQUIRE(until([&] { return group.playing(2); }, 8000ms));
    std::this_thread::sleep_for(1000ms);
    s = stamp(player);
    REQUIRE(s);
    REQUIRE_FALSE(s->frozen());
    // it goes on from the frame it was frozen at
    CHECK(s->pos > paused.front().pos);
    checkHeard("after the pause", group.a, player, *s);
  }
}

TEST_CASE("a tv that leads a group shows its mirror's pictures when the group hears their sound") {
  PresentationSample sample{.mediaNs = 5'000'000'000, .monoNs = 100'000'000'000, .latencyNs = 40'000'000};
  // the group hears what the feed takes 600 ms from now, where this device's own output would in 40 ms
  const auto led = heardByGroup(sample, 600'000'000);
  CHECK(led.mediaNs == sample.mediaNs);
  CHECK(led.monoNs == sample.monoNs + 560'000'000);
  CHECK(led.latencyNs == 600'000'000);
  // a lead under the output's own latency changes nothing
  const auto same = heardByGroup(sample, 10'000'000);
  CHECK(same.monoNs == sample.monoNs);
  CHECK(same.latencyNs == sample.latencyNs);
}
