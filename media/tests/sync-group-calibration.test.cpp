// a whole group calibrated in one process over 127.0.0.1: a leader and two members on sim clocks and fake dacs, one
// member idle (no GROUP session) and one with a trim. each device's "room" adds an output latency its playhead does
// not know, and every microphone hears every dac's actual output through that latency, the air and its own input
// latency: the leader's solution names each device's hidden latency, its trim left out. a playing group and a busy
// member are refused
#include "support/group-harness.h"
#include "sync/calibration.h"
#include "sync/group-leader.h"
#include "sync/group-member.h"

#include <catch2/catch_approx.hpp>
#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <cmath>
#include <cstring>
#include <atomic>
#include <chrono>
#include <future>
#include <memory>
#include <mutex>
#include <optional>
#include <thread>
#include <string_view>
#include <utility>

using namespace cadence;
using namespace cadence::sync;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

const std::vector<std::uint8_t> kKey(32, 0x5a);

struct Device {
  PcmInput music;
  FakePlayback dac;
  // what the room adds past the playhead's latency, and the microphone's own delay
  std::atomic<std::int64_t> hiddenNs{0};
  std::int64_t inNs = 0;
  double x = 0;
  bool silent = false;
  explicit Device(double dacPpm) : dac(music, dacPpm) {}
  GroupPlayout::PlayheadSource playhead() {
    return [this] { return dac.playhead(); };
  }
};

// every device's output as its dac played it, heard at a microphone
class Room {
 public:
  explicit Room(std::vector<Device*> devices) : devices_(std::move(devices)) {}

  // device j's microphone over [from, to), 16 kHz mono, once that time has passed
  void record(std::size_t j, std::int64_t from, std::int64_t to, std::function<void(MicResult)> done) {
    recordRate(j, from, to, kCalibrationMicRate, std::move(done));
  }

  // the same dac output at the speakers' native 48 khz, for ultrasound
  void recordNative(std::size_t j, std::int64_t from, std::int64_t to, std::function<void(MicResult)> done) {
    recordRate(j, from, to, 48000, std::move(done));
  }

  ~Room() {
    std::lock_guard lock(mutex_);
    for (auto& t : threads_)
      if (t.joinable())
        t.join();
  }

 private:
  void recordRate(std::size_t j, std::int64_t from, std::int64_t to, double rate,
                  std::function<void(MicResult)> done) {
    // the leader's and the members' threads ask at once
    std::lock_guard lock(mutex_);
    threads_.emplace_back([this, j, from, to, rate, done = std::move(done)] {
      while (monoNs() < to + 400'000'000)
        std::this_thread::sleep_for(10ms);
      MicResult r;
      r.recording.rate = rate;
      for (auto v : mic(j, from, to, rate))
        r.recording.pcm.push_back(static_cast<std::int16_t>(std::lround(std::clamp(v, -1.0f, 1.0f) * 32767.0f)));
      const double period = 1e9 / rate;
      const auto step = static_cast<std::size_t>(rate / 100);
      for (std::size_t end = step; end <= r.recording.pcm.size(); end += step)
        r.recording.stamps.push_back(
            {end, from + static_cast<std::int64_t>(std::llround(static_cast<double>(end) * period)) + 300'000});
      done(std::move(r));
    });
  }

  std::vector<float> mic(std::size_t j, std::int64_t from, std::int64_t to, double rate) {
    const double period = 1e9 / rate;
    const auto samples = static_cast<std::size_t>(static_cast<double>(to - from) / period);
    std::vector<float> out(samples, 0.0f);
    const auto& me = *devices_[j];
    for (const auto* src : devices_) {
      if (src->silent)
        continue;
      const auto heard = src->dac.heard();
      if (heard.empty())
        continue;
      const double distance = std::max(std::abs(src->x - me.x), 0.05);
      const double delay = static_cast<double>(src->hiddenNs + me.inNs) + distance / 343.0 * 1e9;
      const double gain = 1.0 / std::max(distance, 1.0);
      for (std::size_t n = 0; n < samples; ++n) {
        // what src's dac put out at the time this sample left it
        const double t = static_cast<double>(from) + static_cast<double>(n) * period - delay;
        auto it = std::lower_bound(heard.begin(), heard.end(), t,
                                   [](const HeardSample& s, double v) { return static_cast<double>(s.heardNs) < v; });
        if (it == heard.begin() || it == heard.end())
          continue;
        const auto& b = *it;
        const auto& a = *(it - 1);
        if (b.heardNs - a.heardNs > 30'000)
          continue;
        const double f = (t - static_cast<double>(a.heardNs)) / static_cast<double>(b.heardNs - a.heardNs);
        out[n] += static_cast<float>(gain * (a.left + f * (b.left - a.left)));
      }
    }
    return out;
  }

  std::vector<Device*> devices_;
  std::mutex mutex_;
  std::vector<std::thread> threads_;
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

template <typename F>
bool until(F f, std::chrono::milliseconds bound) {
  const auto end = std::chrono::steady_clock::now() + bound;
  while (!f() && std::chrono::steady_clock::now() < end)
    std::this_thread::sleep_for(10ms);
  return f();
}

const CalibrationMemberOutcome* find(const CalibrationOutcome& o, const std::string& id) {
  for (const auto& m : o.members)
    if (m.id == id)
      return &m;
  return nullptr;
}

}  // namespace

TEST_CASE("a group calibrates: each device's hidden output latency is measured, its trim left out") {
  const auto start = monoNs();
  const SyncClock leaderClock{.offsetNs = 900'000'000, .ppm = 4, .startNs = start};
  const SyncClock aClock{.offsetNs = -400'000'000, .ppm = -8, .startNs = start};
  const SyncClock bClock{.offsetNs = 2'000'000'000, .ppm = 9, .startNs = start};

  Device leaderDevice(20);
  Device a(-40);
  Device b(40);
  leaderDevice.hiddenNs = 12'000'000;
  leaderDevice.inNs = 3'000'000;
  a.hiddenNs = 41'000'000;
  a.inNs = 8'000'000;
  a.x = 2.5;
  b.hiddenNs = 20'500'000;
  b.inNs = 1'000'000;
  b.x = -1.8;
  Room room({&leaderDevice, &a, &b});
  PcmInput feed;

  std::atomic<bool> bBusy{false};
  bool leaderGone = false;
  auto hooks = [&](std::size_t j, std::atomic<bool>* busy) {
    CalibrationHooks h;
    h.record = [&room, j](std::int64_t from, std::int64_t to, std::function<void(MicResult)> done) {
      room.record(j, from, to, std::move(done));
    };
    h.gate = [busy]() -> std::string { return busy != nullptr && *busy ? "busy" : ""; };
    return h;
  };

  GroupLeaderConfig lc;
  lc.groupId = "g";
  lc.memberId = "leader";
  lc.key = kKey;
  lc.members = {"a", "b"};
  lc.host = "127.0.0.1";
  lc.port = 0;
  lc.latencyMs = 500;
  lc.waitMs = 4000;
  lc.pcm = true;
  lc.playout.local = leaderClock;
  lc.calibrationRounds = 2;
  auto made = GroupLeader::start(feed, leaderDevice.music, leaderDevice.playhead(), lc, {});
  REQUIRE(made.ok());
  auto& leader = *made.value();
  leaderDevice.dac.start();
  a.dac.start();
  b.dac.start();

  GroupMemberEvents aEvents;
  aEvents.calibration = hooks(1, nullptr);
  GroupMemberEvents bEvents;
  bEvents.calibration = hooks(2, &bBusy);
  // a is idle as audiod leaves a member with no GROUP session; b plays with a trim of 7 ms
  auto aConfig = memberConfig("a", leader.port(), aClock);
  aConfig.active = false;
  auto bConfig = memberConfig("b", leader.port(), bClock);
  bConfig.playout.trimNs = 7'000'000;
  GroupMember memberA(a.music, a.playhead(), aConfig, std::move(aEvents));
  GroupMember memberB(b.music, b.playhead(), bConfig, std::move(bEvents));
  REQUIRE(memberA.start());
  REQUIRE(memberB.start());
  REQUIRE(until([&] { return memberA.report().connected && memberB.report().connected && memberA.report().synced &&
                             memberB.report().synced; },
                8000ms));

  const auto run = [&] {
    std::promise<CalibrationOutcome> p;
    auto f = p.get_future();
    leader.calibrate(hooks(0, nullptr), [&p](CalibrationOutcome o) { p.set_value(std::move(o)); });
    REQUIRE(f.wait_for(15s) == std::future_status::ready);
    return f.get();
  };

  SECTION("every member measured") {
    const auto outcome = run();
    INFO("code " << outcome.code << " residual " << outcome.residualUs);
    for (const auto& m : outcome.members)
      UNSCOPED_INFO(m.id << " " << m.code << " " << m.offsetUs << " us conf " << m.confidence);
    REQUIRE(outcome.code == "ok");
    REQUIRE(outcome.members.size() == 3);
    CHECK(outcome.members[0].id == "leader");
    const double mean = (12'000.0 + 41'000.0 + 20'500.0) / 3.0;
    // the playouts' own placement (within 100 us) and the room's linear interpolation leave a little
    CHECK(std::abs(find(outcome, "leader")->offsetUs - (12'000.0 - mean)) < 100);
    CHECK(std::abs(find(outcome, "a")->offsetUs - (41'000.0 - mean)) < 100);
    CHECK(std::abs(find(outcome, "b")->offsetUs - (20'500.0 - mean)) < 100);
    CHECK(find(outcome, "a")->ci95Us > 0);
    // the idle member let go of its music input again
    CHECK(until([&] { return memberA.playout().stats().state == PlayoutState::Idle; }, 3000ms));

    // nothing was stored: a second run measures the same
    const auto again = run();
    REQUIRE(again.code == "ok");
    CHECK(std::abs(find(again, "a")->offsetUs - find(outcome, "a")->offsetUs) < 100);
  }
  SECTION("a silent member is missed and another member is not credited with its code") {
    b.silent = true;
    const auto outcome = run();
    for (const auto& m : outcome.members)
      UNSCOPED_INFO(m.id << " " << m.code << " " << m.offsetUs << " us");
    CHECK(outcome.code == "incomplete");
    REQUIRE(find(outcome, "b") != nullptr);
    CHECK(find(outcome, "b")->code == "missed");
    REQUIRE(find(outcome, "a") != nullptr);
    CHECK(find(outcome, "a")->code == "ok");
    // leader and a alone: b's silence leaves no louder neighbour's arrival attributed to it
    CHECK(std::abs(find(outcome, "a")->offsetUs - 14'500) < 100);
  }
  SECTION("a busy member refuses, and the result is incomplete") {
    bBusy = true;
    const auto outcome = run();
    CHECK(outcome.code == "incomplete");
    REQUIRE(find(outcome, "b") != nullptr);
    CHECK(find(outcome, "b")->code == "busy");
    CHECK(find(outcome, "a")->code == "ok");
  }
  SECTION("a restarted leader's calibration is played by members still on the old leader's later epoch") {
    // each run moves the epoch on: the members' playouts end on the old leader's second epoch
    REQUIRE(run().code == "ok");
    REQUIRE(run().code == "ok");
    REQUIRE(memberA.playout().epoch() >= 2);
    // the group restarts (a pushed config: new trims), its new leader numbering epochs from 1 again, and the members
    // reconnect to it keeping the epoch they had
    const auto port = leader.port();
    leader.stop();
    // its sockets close with it
    made.value().reset();
    leaderGone = true;
    auto lc2 = lc;
    lc2.port = port;
    // the old leader's port frees a moment after it stops
    auto made2 = GroupLeader::start(feed, leaderDevice.music, leaderDevice.playhead(), lc2, {});
    for (int i = 0; i < 50 && !made2.ok(); ++i) {
      std::this_thread::sleep_for(100ms);
      made2 = GroupLeader::start(feed, leaderDevice.music, leaderDevice.playhead(), lc2, {});
    }
    REQUIRE(made2.ok());
    auto& leader2 = *made2.value();
    REQUIRE(until([&] {
      const auto r = leader2.report();
      return r.members.size() == 2 &&
             std::all_of(r.members.begin(), r.members.end(), [](const GroupLeaderMember& m) { return m.connected; }) &&
             memberA.report().synced && memberB.report().synced;
    }, 8000ms));
    std::promise<CalibrationOutcome> p;
    auto f = p.get_future();
    leader2.calibrate(hooks(0, nullptr), [&p](CalibrationOutcome o) { p.set_value(std::move(o)); });
    REQUIRE(f.wait_for(15s) == std::future_status::ready);
    const auto outcome = f.get();
    for (const auto& m : outcome.members)
      UNSCOPED_INFO(m.id << " " << m.code);
    // before: both answered epoch (their 2 past the new leader's 1) and stayed silent; and b, playing, asked to rejoin
    // from twice its last catch-up ahead, which ran from its first join to the first chirp it played: every chirp
    // came before that and was dropped
    CHECK(outcome.code == "ok");
    CHECK(find(outcome, "a")->code == "ok");
    CHECK(find(outcome, "b")->code == "ok");
    leader2.stop();
  }
  SECTION("a playing group is refused") {
    Source source(feed);
    REQUIRE(until([&] { return leader.report().state == GroupRunState::Playing; }, 8000ms));
    std::promise<CalibrationOutcome> p;
    leader.calibrate(hooks(0, nullptr), [&p](CalibrationOutcome o) { p.set_value(std::move(o)); });
    auto f = p.get_future();
    REQUIRE(f.wait_for(1s) == std::future_status::ready);
    CHECK(f.get().code == "playing");
  }

  memberA.stop();
  memberB.stop();
  if (!leaderGone)
    leader.stop();
  leaderDevice.dac.stop();
  a.dac.stop();
  b.dac.stop();
}

namespace {

// when device's dac first put out a sample of the chirp after fromNs (local mono): none if it never did
std::optional<std::int64_t> chirpHeard(const Device& device, std::int64_t fromNs) {
  for (const auto& s : device.dac.heard())
    if (s.heardNs >= fromNs && std::abs(s.left) > 0.01f)
      return s.heardNs;
  return std::nullopt;
}

}  // namespace

TEST_CASE("a listener calibration: everyone chirps at each of its slots, the microphones record, the result waits") {
  const auto start = monoNs();
  const SyncClock leaderClock{.offsetNs = 900'000'000, .ppm = 4, .startNs = start};
  const SyncClock aClock{.offsetNs = -400'000'000, .ppm = -8, .startNs = start};
  const SyncClock bClock{.offsetNs = 2'000'000'000, .ppm = 9, .startNs = start};
  Device leaderDevice(20);
  Device a(-40);
  Device b(40);
  PcmInput feed;

  GroupLeaderConfig lc;
  lc.groupId = "g";
  lc.memberId = "leader";
  lc.key = kKey;
  // c never connects: it gets no slot
  lc.members = {"a", "c", "b"};
  lc.host = "127.0.0.1";
  lc.port = 0;
  lc.latencyMs = 500;
  lc.waitMs = 4000;
  lc.pcm = true;
  lc.playout.local = leaderClock;
  // a trim the chirps leave out
  lc.playout.trimNs = 9'000'000;
  lc.calibrationRounds = 2;
  auto made = GroupLeader::start(feed, leaderDevice.music, leaderDevice.playhead(), lc, {});
  REQUIRE(made.ok());
  auto& leader = *made.value();
  leaderDevice.dac.start();
  a.dac.start();
  b.dac.start();

  std::atomic<bool> bBusy{false};
  std::atomic<int> recorded{0};
  // b's recording is handed over when the test says so
  std::mutex heldMutex;
  std::function<void(MicResult)> held;
  // a has no microphone (the tv stick): it still plays, and answers no_capture
  GroupMemberEvents aEvents;
  aEvents.calibration.gate = [] { return std::string(); };
  GroupMemberEvents bEvents;
  bEvents.calibration.record = [&](std::int64_t, std::int64_t, std::function<void(MicResult)> done) {
    ++recorded;
    std::lock_guard lock(heldMutex);
    held = std::move(done);
  };
  bEvents.calibration.gate = [&bBusy]() -> std::string { return bBusy ? "busy" : ""; };
  auto release = [&] {
    std::function<void(MicResult)> done;
    {
      std::lock_guard lock(heldMutex);
      done = std::exchange(held, {});
    }
    if (done)
      done(MicResult{"no_capture", {}});
    return static_cast<bool>(done);
  };
  auto aConfig = memberConfig("a", leader.port(), aClock);
  aConfig.active = false;
  auto bConfig = memberConfig("b", leader.port(), bClock);
  bConfig.active = false;
  bConfig.playout.trimNs = -6'000'000;
  GroupMember memberA(a.music, a.playhead(), aConfig, std::move(aEvents));
  GroupMember memberB(b.music, b.playhead(), bConfig, std::move(bEvents));
  REQUIRE(memberA.start());
  REQUIRE(memberB.start());
  REQUIRE(until([&] { return memberA.report().synced && memberB.report().synced; }, 8000ms));

  // the leader has no microphone here: it schedules all the same
  CalibrationHooks hooks;
  hooks.gate = [] { return std::string(); };
  std::int64_t called = 0;
  std::uint32_t runId = 0;
  const auto schedule = [&] {
    called = monoNs();
    const auto outcome = leader.calibrateListener(hooks);
    REQUIRE(outcome.code == "scheduled");
    CHECK(outcome.startsInMs == 1500);
    CHECK(outcome.rounds == 2);
    REQUIRE(outcome.chirp.has_value());
    CHECK(outcome.chirp->durationS == 0.25);
    REQUIRE(outcome.probe.has_value());
    CHECK(outcome.probe->kind == calibration::ProbeKind::Gold);
    CHECK(outcome.probe->bandLoHz == 1000);
    CHECK(outcome.probe->bandHiHz == 6000);
    CHECK(outcome.probe->fadeS == 0.01);
    CHECK(outcome.runId != 0);
    runId = outcome.runId;
    REQUIRE(outcome.slots.size() == 6);
    const std::vector<std::string> order{"leader", "a", "b"};
    for (std::size_t k = 0; k < 6; ++k) {
      CHECK(outcome.slots[k].id == order[k % 3]);
      CHECK(outcome.slots[k].offsetMs == 1200 * k);
      CHECK(outcome.slots[k].round == k / 3);
      CHECK(outcome.slots[k].code == k % 3);
    }
    CHECK(outcome.members.empty());
  };
  const auto result = [&](std::uint32_t id) {
    auto p = std::make_shared<std::promise<CalibrationOutcome>>();
    auto f = p->get_future();
    leader.listenerResult(id, [p](CalibrationOutcome o) { p->set_value(std::move(o)); });
    return f;
  };

  SECTION("each chirp at its slots, its trim left out; busy until it is over") {
    schedule();
    // the next waits for this one's end, in either mode
    CHECK(leader.calibrateListener(hooks).code == "busy");
    std::promise<CalibrationOutcome> p;
    leader.calibrate(CalibrationHooks{.record = [](std::int64_t, std::int64_t, std::function<void(MicResult)>) {},
                                      .recordNative = {},
                                      .cancelNative = {},
                                      .gate = {},
                                      .analysisBudgetMs = kCalibrationAnalysisBudgetMs,
                                      .analysed = {}},
                     [&p](CalibrationOutcome o) { p.set_value(std::move(o)); });
    auto f = p.get_future();
    REQUIRE(f.wait_for(1s) == std::future_status::ready);
    CHECK(f.get().code == "busy");

    // the last chirp (7.5 s) is over, its busy tail (to 8.25 s) is not
    std::this_thread::sleep_for(std::chrono::nanoseconds(called + 7'900'000'000 - monoNs()));
    const auto expect = [&](const Device& d, std::int64_t slotNs) {
      const auto heard = chirpHeard(d, called + slotNs - 500'000'000);
      REQUIRE(heard.has_value());
      // the call's own time and the members' clock budget: a few ms, far inside a slot
      CHECK(std::abs(*heard - (called + slotNs)) < 8'000'000);
      // its next round's chirp three slots later, and nothing between
      const auto next = chirpHeard(d, *heard + 400'000'000);
      REQUIRE(next.has_value());
      CHECK(std::abs(*next - (*heard + 3'600'000'000)) < 8'000'000);
      CHECK_FALSE(chirpHeard(d, *next + 400'000'000).has_value());
    };
    expect(leaderDevice, 1'500'000'000);
    expect(a, 2'700'000'000);
    expect(b, 3'900'000'000);
    // the member with a microphone recorded
    CHECK(recorded == 1);
    CHECK(leader.calibrateListener(hooks).code == "busy");
    release();
    std::this_thread::sleep_for(std::chrono::nanoseconds(called + 8'500'000'000 - monoNs()));
    CHECK(leader.calibrateListener(hooks).code == "scheduled");
  }
  SECTION("the speakers' result waits for every answer; another run's is expired") {
    schedule();
    auto pending = result(runId);
    // b has not answered yet
    CHECK(pending.wait_for(500ms) == std::future_status::timeout);
    CHECK(result(runId + 100).get().code == "expired");
    REQUIRE(until([&] { return recorded == 1; }, 3000ms));
    REQUIRE(release());
    REQUIRE(pending.wait_for(3s) == std::future_status::ready);
    const auto outcome = pending.get();
    CHECK(outcome.code == "incomplete");
    REQUIRE(outcome.members.size() == 4);
    CHECK(find(outcome, "leader")->code == "no_capture");
    CHECK(find(outcome, "a")->code == "no_capture");
    CHECK(find(outcome, "b")->code == "no_capture");
    CHECK(find(outcome, "c")->code == "offline");
    // asked again, it answers at once
    auto again = result(runId);
    REQUIRE(again.wait_for(100ms) == std::future_status::ready);
    CHECK(again.get().code == "incomplete");
    // a new run replaces it
    std::this_thread::sleep_for(std::chrono::nanoseconds(called + 8'500'000'000 - monoNs()));
    const auto old = runId;
    schedule();
    CHECK(runId != old);
    CHECK(result(old).get().code == "expired");
    REQUIRE(until([&] { return recorded == 2; }, 3000ms));
    release();
  }
  SECTION("a member that may not play stays silent, and says why") {
    bBusy = true;
    schedule();
    auto pending = result(runId);
    std::this_thread::sleep_for(std::chrono::nanoseconds(called + 4'500'000'000 - monoNs()));
    REQUIRE(chirpHeard(a, called + 2'200'000'000).has_value());
    CHECK_FALSE(chirpHeard(b, called + 2'200'000'000).has_value());
    REQUIRE(pending.wait_for(3s) == std::future_status::ready);
    CHECK(find(pending.get(), "b")->code == "busy");
  }
  SECTION("the gate refuses before anything is scheduled") {
    CalibrationHooks gated;
    gated.gate = [] { return std::string("gated"); };
    const auto refusal = leader.calibrateListener(gated);
    CHECK(refusal.code == "gated");
    CHECK(refusal.slots.empty());
  }

  release();
  memberA.stop();
  memberB.stop();
  leader.stop();
  leaderDevice.dac.stop();
  a.dac.stop();
  b.dac.stop();
}

TEST_CASE("a group with a legacy member keeps the chirp-only calibration plan") {
  const auto start = monoNs();
  const SyncClock leaderClock{.offsetNs = 900'000'000, .startNs = start};
  const SyncClock aClock{.offsetNs = -400'000'000, .startNs = start};
  Device leaderDevice(0);
  Device a(0);
  PcmInput feed;
  GroupLeaderConfig lc;
  lc.groupId = "g";
  lc.memberId = "leader";
  lc.key = kKey;
  lc.members = {"a"};
  lc.host = "127.0.0.1";
  lc.port = 0;
  lc.latencyMs = 500;
  lc.pcm = true;
  lc.playout.local = leaderClock;
  lc.calibrationRounds = 1;
  auto made = GroupLeader::start(feed, leaderDevice.music, leaderDevice.playhead(), lc, {});
  REQUIRE(made.ok());
  auto& leader = *made.value();
  leaderDevice.dac.start();
  a.dac.start();
  auto config = memberConfig("a", leader.port(), aClock);
  config.active = false;
  config.calibrationVersion = 1;
  GroupMember member(a.music, a.playhead(), config, {});
  REQUIRE(member.start());
  REQUIRE(until([&] { return member.report().synced; }, 8000ms));
  const auto outcome = leader.calibrateListener({});
  REQUIRE(outcome.code == "scheduled");
  CHECK_FALSE(outcome.probe.has_value());
  REQUIRE(outcome.chirp.has_value());
  CHECK(outcome.chirp->durationS == 0.25);
  member.stop();
  leader.stop();
  leaderDevice.dac.stop();
  a.dac.stop();
}

TEST_CASE("a calibration start carries its rounds, chirp and boosts; one from a leader before rounds is read as it was") {
  auto plan = calibration::makePlan({"l", "a", "b"}, 5'000'000'000, calibration::kSlotSpacingNs, 2);
  plan.gainDb = {0, 6, 0, 0, 6, 0};
  const auto start = startOf(plan, 9, 3);
  CHECK(start.id == 9);
  CHECK(start.epoch == 3);
  CHECK(start.rounds == 2);
  CHECK_FALSE(start.play_only);
  REQUIRE(start.has_chirp);
  CHECK(start.chirp.f0_hz == 800);
  CHECK(start.chirp.f1_hz == 7000);
  CHECK(start.chirp.duration_us == 250'000);
  CHECK(start.chirp.fade_us == 10'000);
  REQUIRE(start.slots_count == 6);
  CHECK(std::string_view(start.slots[4].member_id) == "a");
  CHECK(start.slots[4].gain_db == 6);
  CHECK(start.slots[4].t_ns == plan.slotNs[4]);
  CHECK(start.from_ns == plan.windowFromNs());
  CHECK(start.to_ns == plan.windowToNs());

  const auto back = planOf(start);
  CHECK(back.members == plan.members);
  CHECK(back.rounds == 2);
  CHECK(back.slotNs == plan.slotNs);
  CHECK(back.gainDb == plan.gainDb);
  CHECK(back.chirp.durationS == 0.25);

  // before rounds: no chirp, rounds 0, one slot a member
  auto legacy = start;
  legacy.has_chirp = false;
  legacy.rounds = 0;
  legacy.slots_count = 3;
  const auto old = planOf(legacy);
  CHECK(old.rounds == 1);
  CHECK(old.members.size() == 3);
  CHECK(old.slotNs.size() == 3);
  CHECK(old.chirp.f0Hz == 1000);
  CHECK(old.chirp.f1Hz == 6000);
  CHECK(old.chirp.durationS == 0.1);
  CHECK(old.chirp.fadeS == 0.005);
  CHECK(old.chirp.amplitude == 0.1);

  // a boost past 14 dB is held there
  auto loud = start;
  loud.slots[1].gain_db = 40;
  CHECK(planOf(loud).gainDb[1] == calibration::kMaxGainDb);

  // slots that are not round-major over the same members are one round of the first members
  auto odd = start;
  std::strcpy(odd.slots[3].member_id, "x");
  const auto oddPlan = planOf(odd);
  CHECK(oddPlan.rounds == 1);
  CHECK(oddPlan.members.size() == 6);
}

TEST_CASE("a coded calibration start carries its probes, codes, priors and tolerance") {
  auto plan = calibration::makeProbePlan({"l", "a", "b"}, 5'000'000'000,
                                         {0, 250'000'000, -20'000'000}, {150'000'000, 200'000'000, 50'000'000},
                                         calibration::kSlotSpacingNs, 2);
  plan.gainDb = {0, 6, 0, 0, 6, 0};
  plan.probeCodes = {0, 1, 2, 0, 1, 2};
  plan.toleranceNs = 250'000;
  const auto start = startOf(plan, 9, 3);
  REQUIRE(start.has_probe);
  CHECK(start.probe.kind == cadence_sync_v1_ProbeKind_PROBE_KIND_GOLD);
  CHECK(start.probe.band_lo_hz == 1000);
  CHECK(start.probe.band_hi_hz == 6000);
  CHECK(start.probe.fade_us == 10'000);
  CHECK(start.tolerance_us == 250);
  REQUIRE(start.priors_count == 3);
  CHECK(std::string_view(start.priors[1].member_id) == "a");
  CHECK(start.priors[1].prior_us == 250'000);
  CHECK(start.priors[1].prior_span_us == 200'000);
  REQUIRE(start.slots_count == 6);
  for (std::size_t k = 0; k < start.slots_count; ++k)
    CHECK(start.slots[k].code == plan.probeCodes[k]);

  const auto back = planOf(start);
  CHECK(back.probe.kind == calibration::ProbeKind::Gold);
  CHECK(back.probe.bandLoHz == 1000);
  CHECK(back.probe.bandHiHz == 6000);
  CHECK(back.probe.fadeS == 0.01);
  CHECK(back.probeCodes == plan.probeCodes);
  CHECK(back.priorNs == plan.priorNs);
  CHECK(back.priorSpanNs == plan.priorSpanNs);
  CHECK(back.toleranceNs == 250'000);
  CHECK(back.slotNs == plan.slotNs);
}

TEST_CASE("an ultrasonic round round-trips its capable slots and leaves bluetooth out") {
  auto plan = calibration::makeProbePlan({"l", "bt", "a"}, 5'000'000'000, {}, {}, calibration::kSlotSpacingNs, 1);
  calibration::addUltrasonicRound(plan, 6'400'000'000, calibration::kSlotSpacingNs, {false, true, false});
  REQUIRE(plan.hasUltrasonic());
  const auto start = startOf(plan, 9, 3);
  REQUIRE(start.has_ultrasonic);
  CHECK(start.ultrasonic.probe.band_lo_hz == 18000);
  CHECK(start.ultrasonic.probe.band_hi_hz == 20500);
  REQUIRE(start.ultrasonic.slots_count == 2);
  CHECK(std::string_view(start.ultrasonic.slots[0].member_id) == "l");
  CHECK(std::string_view(start.ultrasonic.slots[1].member_id) == "a");
  const auto back = planOf(start);
  REQUIRE(back.hasUltrasonic());
  const auto round = back.ultrasonicPlan();
  CHECK(round.slotNs.size() == 3);
  CHECK(round.slotNs[0] == plan.ultrasonicSlotNs[0]);
  CHECK(round.slotNs[1] == 0);
  CHECK(round.slotNs[2] == plan.ultrasonicSlotNs[2]);
  CHECK(round.skipped(1));
  CHECK(round.probe.amplitude == Catch::Approx(std::pow(10.0, -30.0 / 20.0)));
}

TEST_CASE("a recording keeps as many microphones as fit its memory bound") {
  CHECK(recordingChannels(1, 10'000'000'000, 16000) == 1);
  CHECK(recordingChannels(6, 25'000'000'000, 16000) == 6);
  CHECK(recordingChannels(12, 10'000'000'000, 16000) == kMaxMicChannels);
  // a minute at 48 kHz: 5.76 MB a channel, one fits
  CHECK(recordingChannels(4, 60'000'000'000, 48000) == 1);
  CHECK(recordingChannels(4, 40'000'000'000, 16000) * 40 * 16000 * 2 <= kMaxRecordingBytes);
  CHECK(recordingChannels(0, 1'000'000'000, 16000) == 0);
}

namespace {

const calibration::TrackingMember* findTracked(const calibration::TrackingReport& report, const std::string& id) {
  for (const auto& m : report.members)
    if (m.id == id)
      return &m;
  return nullptr;
}

struct TrackingFixture {
  Device leaderDevice{0};
  Device a{0};
  Room room{{&leaderDevice, &a}};
  PcmInput feed;
  std::unique_ptr<GroupLeader> leader;
  std::unique_ptr<GroupMember> member;
  std::mutex reportsMutex;
  std::vector<calibration::TrackingReport> reports;

  explicit TrackingFixture(bool enabled = true, bool legacy = false) {
    leaderDevice.hiddenNs = 12'000'000;
    a.hiddenNs = 12'000'000;
    const auto start = monoNs();
    const SyncClock leaderClock{.offsetNs = 500'000'000, .startNs = start};
    const SyncClock aClock{.offsetNs = -300'000'000, .startNs = start};
    GroupLeaderConfig lc;
    lc.groupId = "g";
    lc.memberId = "leader";
    lc.key = kKey;
    lc.members = {"a"};
    lc.host = "127.0.0.1";
    lc.port = 0;
    lc.latencyMs = 500;
    lc.waitMs = 4000;
    lc.pcm = true;
    lc.playout.local = leaderClock;
    lc.calibrationRounds = 1;
    lc.budgetUs = 100;
    lc.trackingEnabled = enabled;
    lc.trackingMembers = {"leader", "a"};
    lc.trackingCycleNs = 3'000'000'000;
    GroupLeaderEvents events;
    events.calibration = hooks(0);
    events.tracked = [this](const calibration::TrackingReport& report) {
      std::lock_guard lock(reportsMutex);
      reports.push_back(report);
    };
    auto made = GroupLeader::start(feed, leaderDevice.music, leaderDevice.playhead(), lc, std::move(events));
    REQUIRE(made.ok());
    leader = std::move(made.value());
    leaderDevice.dac.start();
    a.dac.start();
    GroupMemberEvents memberEvents;
    memberEvents.calibration = hooks(1);
    auto config = memberConfig("a", leader->port(), aClock);
    config.trackingEnabled = enabled;
    config.calibrationVersion = legacy ? kCalibrationVersion : kTrackingVersion;
    member = std::make_unique<GroupMember>(a.music, a.playhead(), config, std::move(memberEvents));
    REQUIRE(member->start());
    REQUIRE(until([&] { return member->report().connected && member->report().synced; }, 8000ms));
  }

  ~TrackingFixture() {
    if (member)
      member->stop();
    if (leader)
      leader->stop();
    leaderDevice.dac.stop();
    a.dac.stop();
  }

  CalibrationHooks hooks(std::size_t j) {
    CalibrationHooks h;
    h.record = [this, j](std::int64_t from, std::int64_t to, std::function<void(MicResult)> done) {
      room.record(j, from, to, std::move(done));
    };
    h.recordNative = [this, j](std::int64_t from, std::int64_t to, std::function<void(MicResult)> done) {
      room.recordNative(j, from, to, std::move(done));
    };
    h.gate = [] { return std::string(); };
    return h;
  }

  std::vector<calibration::TrackingReport> allReports() {
    std::lock_guard lock(reportsMutex);
    return reports;
  }
};

}  // namespace

TEST_CASE("a native calibration reports every member's ultrasonic capability") {
  TrackingFixture fixture;
  std::promise<CalibrationOutcome> promise;
  auto future = promise.get_future();
  fixture.leader->calibrate(fixture.hooks(0), [&promise](CalibrationOutcome outcome) { promise.set_value(std::move(outcome)); });
  REQUIRE(future.wait_for(20s) == std::future_status::ready);
  const auto outcome = future.get();
  INFO("code " << outcome.code << " residual " << outcome.residualUs);
  REQUIRE(outcome.code == "ok");
  REQUIRE(outcome.members.size() == 2);
  for (const auto& member : outcome.members) {
    UNSCOPED_INFO(member.id << " audible " << member.code << " ultrasonic " << member.ultrasonic);
    CHECK(member.code == "ok");
    CHECK(member.ultrasonic == "ok");
  }
}

TEST_CASE("live tracking measures a hidden two ms output change and suggests one bounded correction") {
  TrackingFixture fixture;
  Source source(fixture.feed);
  REQUIRE(until([&] { return fixture.leader->report().state == GroupRunState::Playing; }, 8000ms));
  REQUIRE(until([&] {
    for (const auto& report : fixture.allReports())
      if (report.state == "tracking")
        return true;
    return false;
  }, 12'000ms));
  fixture.a.hiddenNs += 2'000'000;
  REQUIRE(until([&] {
    for (const auto& report : fixture.allReports())
      if (const auto* measured = findTracked(report, "a"); measured && measured->correctionUs != 0)
        return true;
    return false;
  }, 16'000ms));
  const auto all = fixture.allReports();
  const auto& report = all.back();
  const auto* changed = findTracked(report, "a");
  const auto* unchanged = findTracked(report, "leader");
  REQUIRE(changed != nullptr);
  REQUIRE(unchanged != nullptr);
  UNSCOPED_INFO("offset " << changed->offsetUs << " ci " << changed->ci95Us << " correction " << changed->correctionUs);
  CHECK(std::abs(changed->offsetUs - 1000) < 200);
  CHECK(changed->correctionUs < 0);
  CHECK(changed->correctionUs >= -5000);
  CHECK(unchanged->correctionUs == 0);
}

TEST_CASE("tracking disabled sends no cycle, and a legacy member keeps the group untracked") {
  SECTION("disabled") {
    TrackingFixture fixture(false);
    Source source(fixture.feed);
    REQUIRE(until([&] { return fixture.leader->report().state == GroupRunState::Playing; }, 8000ms));
    std::this_thread::sleep_for(3500ms);
    CHECK(fixture.leader->trackingCycles() == 0);
    CHECK(fixture.member->trackingCycles() == 0);
  }
  SECTION("legacy") {
    TrackingFixture fixture(true, true);
    Source source(fixture.feed);
    REQUIRE(until([&] { return fixture.leader->report().state == GroupRunState::Playing; }, 8000ms));
    std::this_thread::sleep_for(3500ms);
    CHECK(fixture.leader->trackingCycles() == 0);
    CHECK(fixture.member->trackingCycles() == 0);
    const auto reports = fixture.allReports();
    REQUIRE(!reports.empty());
    CHECK(reports.back().state == "unsupported");
  }
}

TEST_CASE("a voice duck pauses tracking and its native recording, then a later cycle resumes") {
  TrackingFixture fixture;
  Source source(fixture.feed);
  REQUIRE(until([&] { return fixture.leader->report().state == GroupRunState::Playing; }, 8000ms));
  REQUIRE(until([&] { return fixture.leader->trackingCycles() > 0; }, 8000ms));
  fixture.leader->duckRequest(true);
  const auto leaderCycles = fixture.leader->trackingCycles();
  const auto memberCycles = fixture.member->trackingCycles();
  std::this_thread::sleep_for(3500ms);
  CHECK(fixture.leader->trackingCycles() == leaderCycles);
  CHECK(fixture.member->trackingCycles() == memberCycles);
  fixture.leader->duckRequest(false);
  CHECK(until([&] { return fixture.leader->trackingCycles() > leaderCycles; }, 8000ms));
}
