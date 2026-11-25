// multiroom's part of audiod over its socket, as cadenced drives it: two audiods in one process, a leader playing a
// real url source over http (and a long-lived fake, as soloist is) and a member, each on the file backend. GroupConfig
// enables, reconfigures and disables them, GroupStatus comes back on every change, and the transport (a pause, a
// resume through the member's Control, a seek, a new play, a track's own end or a source's own skip) starts the group
// again every time
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/eventually.h"
#include "support/fake-media-source.h"
#include "support/http-server.h"
#include "support/media-client.h"
#include "support/playback-client.h"
#include "ring/ring-input.h"
#include "support/url-rig.h"
#include "url/url-source.h"

#include <catch2/catch_test_macros.hpp>

#include <pb_decode.h>

#include <netinet/in.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <time.h>
#include <unistd.h>

#include <algorithm>
#include <array>
#include <atomic>
#include <chrono>
#include <cmath>
#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <functional>
#include <memory>
#include <optional>
#include <span>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

using GroupStatusFrame = cadence_audiod_v1_GroupStatus;

constexpr auto kLeader = cadence_audiod_v1_GroupRole_GROUP_ROLE_LEADER;
constexpr auto kMember = cadence_audiod_v1_GroupRole_GROUP_ROLE_MEMBER;
constexpr auto kGroupOff = cadence_audiod_v1_GroupState_GROUP_STATE_OFF;
constexpr auto kGroupIdle = cadence_audiod_v1_GroupState_GROUP_STATE_IDLE;
constexpr auto kGroupPlaying = cadence_audiod_v1_GroupState_GROUP_STATE_PLAYING;
constexpr auto kGroupFailed = cadence_audiod_v1_GroupState_GROUP_STATE_FAILED;
constexpr auto kGroupSyncing = cadence_audiod_v1_GroupState_GROUP_STATE_SYNCING;

// a port both tcp and udp have free on the loopback now
std::uint16_t freePort() {
  for (int attempt = 0; attempt < 50; ++attempt) {
    const int tcp = ::socket(AF_INET, SOCK_STREAM, 0);
    sockaddr_in addr{};
    addr.sin_family = AF_INET;
    addr.sin_addr.s_addr = htonl(INADDR_LOOPBACK);
    socklen_t length = sizeof addr;
    REQUIRE(::bind(tcp, reinterpret_cast<sockaddr*>(&addr), sizeof addr) == 0);
    REQUIRE(::getsockname(tcp, reinterpret_cast<sockaddr*>(&addr), &length) == 0);
    const int udp = ::socket(AF_INET, SOCK_DGRAM, 0);
    const bool both = ::bind(udp, reinterpret_cast<sockaddr*>(&addr), sizeof addr) == 0;
    ::close(udp);
    ::close(tcp);
    if (both)
      return ntohs(addr.sin_port);
  }
  FAIL("no free port");
  return 0;
}

struct GroupSpec {
  bool enabled = true;
  cadence_audiod_v1_GroupRole role = kLeader;
  std::string memberId{};
  std::uint16_t port = 0;
  std::vector<std::string> members{};
  std::uint32_t latencyMs = 500;
  std::int32_t trimUs = 0;
  std::size_t keyBytes = 32;
  std::string leaderHost = "127.0.0.1";
  // another device serves the group clock there; this member serves it
  std::string clockHost{};
  bool clockMaster = false;
};

Envelope groupConfig(const GroupSpec& spec) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_group_config_tag;
  auto& c = envelope.body.group_config;
  c.enabled = spec.enabled;
  if (!spec.enabled)
    return envelope;
  copyText(c.group_id, "g-1");
  c.role = spec.role;
  copyText(c.member_id, spec.memberId);
  if (spec.role == kMember)
    copyText(c.leader_host, spec.leaderHost);
  c.port = spec.port;
  c.key.size = static_cast<pb_size_t>(spec.keyBytes);
  for (std::size_t i = 0; i < spec.keyBytes; ++i)
    c.key.bytes[i] = static_cast<std::uint8_t>(0x40 + i);
  copyText(c.clock_host, spec.clockHost);
  c.clock_master = spec.clockMaster;
  c.latency_ms = spec.latencyMs;
  c.trim_us = spec.trimUs;
  c.budget_us = 5000;
  c.wait_ms = 3000;
  for (const auto& id : spec.members)
    copyText(c.members[c.members_count++], id);
  return envelope;
}

// one audiod as cadenced sees it: what its socket said, GroupStatus kept apart
class Device {
 public:
  explicit Device(bool urls, std::vector<LongLivedSource> sources = {}, bool rings = false)
      : volume_(std::make_shared<FakeVolume>()),
        audiod_(compiledBackends(), {}, withFakeControl(volume_), openCapture,
                media(urls, std::move(sources), rings)) {}

  void connect() {
    client_ = connectTo(audiod_.socket());
    sendFrame(client_.get(), configureOutput(audiod_.file("out"), "Master"));
    expectReady(client_.get());
    expectApplied(client_.get(), 40, true);
  }

  void send(const Envelope& envelope) { sendFrame(client_.get(), envelope); }

  // what came within limit
  void drain(std::chrono::milliseconds limit) {
    const auto deadline = std::chrono::steady_clock::now() + limit;
    do {
      const auto received = receive(client_.get(), 2ms);
      if (received.kind != Received::Kind::frame)
        continue;
      if (received.envelope.which_body == cadence_audiod_v1_Envelope_group_status_tag) {
        status_ = received.envelope.body.group_status;
        ++statuses_;
        seen_.push_back(status_->state);
      } else {
        collect(frames_, received.envelope);
      }
    } while (std::chrono::steady_clock::now() < deadline);
  }

  // the states reported from now on are told apart from those before
  void mark() { seen_.clear(); }
  [[nodiscard]] bool leftPlaying() const {
    return std::any_of(seen_.begin(), seen_.end(), [](auto state) { return state != kGroupPlaying; });
  }

  [[nodiscard]] const std::optional<GroupStatusFrame>& status() const { return status_; }
  [[nodiscard]] std::size_t statuses() const { return statuses_; }
  [[nodiscard]] Frames& frames() { return frames_; }
  [[nodiscard]] AudiodHarness& audiod() { return audiod_; }
  [[nodiscard]] bool in(cadence_audiod_v1_GroupState state) const { return status_ && status_->state == state; }

 private:
  static HarnessMedia media(bool urls, std::vector<LongLivedSource> sources, bool rings) {
    HarnessMedia m;
    if (rings)
      m.rings = sizedRing;
    if (urls)
      m.make = [](Loop& loop, const Log& log) {
        return url::urlSourceFactory(log, [&loop](std::function<void()> task) { loop.post(std::move(task)); });
      };
    m.sources = std::move(sources);
    m.group = true;
    return m;
  }

  std::shared_ptr<FakeVolume> volume_;
  AudiodHarness audiod_;
  UniqueFd client_;
  Frames frames_;
  std::optional<GroupStatusFrame> status_;
  std::vector<cadence_audiod_v1_GroupState> seen_;
  std::size_t statuses_ = 0;
};

// drains both until done holds
bool within(Device& a, Device& b, const std::function<bool()>& done, std::chrono::milliseconds limit = 10s) {
  const auto deadline = std::chrono::steady_clock::now() + limit;
  while (std::chrono::steady_clock::now() < deadline) {
    a.drain(5ms);
    b.drain(5ms);
    if (done())
      return true;
  }
  return done();
}

std::string describe(const Device& d) {
  if (!d.status())
    return "none";
  const auto& s = *d.status();
  std::string out = "state " + std::to_string(s.state) + " queue " + std::to_string(s.queue_ms) + " error " +
                    std::string(s.error);
  for (pb_size_t i = 0; i < s.members_count; ++i)
    out += " member " + std::to_string(s.members[i].state) + (s.members[i].connected ? " connected" : "");
  return out;
}

MediaMetadata titled(std::string title) {
  MediaMetadata m;
  m.title = std::move(title);
  return m;
}

FakeSourceSpec soloSpec() {
  FakeSourceSpec spec;
  spec.keepsPaused = true;
  spec.toneHz = 500;
  spec.actions = {MediaAction::Pause, MediaAction::Resume, MediaAction::Stop, MediaAction::Next};
  return spec;
}

struct Pair {
  HttpServer server;
  FakeMedia fakes;
  Device leader{true, {fakes.longLived("solo", soloSpec())}};
  Device member{false};
  std::uint16_t port = freePort();

  Pair() {
    server.route("/tone.wav", {.contentType = "audio/wav", .body = toneWav(60.0, 48000), .ranges = true});
    server.route("/next.wav", {.contentType = "audio/wav", .body = toneWav(60.0, 48000, 550, 770), .ranges = true});
    server.route("/short.wav", {.contentType = "audio/wav", .body = toneWav(2.0, 48000), .ranges = true});
    leader.connect();
    member.connect();
    leader.send(groupConfig({.role = kLeader, .memberId = "L", .port = port, .members = {"M"}}));
    member.send(groupConfig({.role = kMember, .memberId = "M", .port = port}));
    REQUIRE(within(leader, member, [&] {
      return leader.in(kGroupIdle) && member.in(kGroupIdle) && leader.status()->members_count == 1 &&
             leader.status()->members[0].connected;
    }));
  }

  void mark() {
    leader.mark();
    member.mark();
  }

  // both report playing, the leader's member too; again: each left playing since the mark (a new epoch)
  bool playing(bool again = false, std::chrono::milliseconds limit = 10s) {
    const bool ok = within(
        leader, member,
        [&] {
          return leader.in(kGroupPlaying) && member.in(kGroupPlaying) && leader.status()->members_count == 1 &&
                 leader.status()->members[0].state == kGroupPlaying &&
                 (!again || (leader.leftPlaying() && member.leftPlaying()));
        },
        limit);
    if (!ok)
      dump();
    return ok;
  }

  bool stopped(std::chrono::milliseconds limit = 10s) {
    const bool ok =
        within(leader, member, [&] { return leader.in(kGroupIdle) && member.in(kGroupIdle); }, limit);
    if (!ok)
      dump();
    return ok;
  }

  void dump() {
    UNSCOPED_INFO("leader: " << describe(leader) << "; member: " << describe(member));
    for (auto* d : {&leader, &member})
      for (const auto& line : d->audiod().logs().lines())
        if (line.find("\"msg\":\"group_") != std::string::npos || line.find("\"msg\":\"media_") != std::string::npos ||
            line.find("\"msg\":\"url_") != std::string::npos)
          UNSCOPED_INFO((d == &leader ? "L " : "M ") << line);
  }

  void play(std::string_view path) {
    mark();
    leader.send(urlPlay("play", server.url(std::string(path))));
  }
};

}  // namespace

TEST_CASE("a group starts again after every pause: a resume through the member, a seek, a new play") {
  Pair pair;
  pair.play("/tone.wav");
  REQUIRE(pair.playing());

  for (int round = 0; round < 4; ++round) {
    INFO("round " << round);
    pair.leader.send(mediaCommand("pause", kPause));
    REQUIRE(pair.stopped());
    // the member's resume goes to the leader as Control
    pair.member.send(mediaCommand("resume", kResume));
    REQUIRE(pair.playing());
  }

  for (int round = 0; round < 2; ++round) {
    INFO("seek while paused " << round);
    pair.leader.send(mediaCommand("pause", kPause));
    REQUIRE(pair.stopped());
    pair.leader.send(mediaCommand("seek", kSeek, 20000 + 5000 * static_cast<std::uint64_t>(round)));
    pair.leader.drain(100ms);
    // the member's and the leader's resume alike
    (round == 0 ? pair.member : pair.leader).send(mediaCommand("resume", kResume));
    REQUIRE(pair.playing());
  }

  for (int round = 0; round < 2; ++round) {
    INFO("seek while playing " << round);
    pair.mark();
    (round == 0 ? pair.member : pair.leader).send(mediaCommand("seek", kSeek, 5000 + 1000 * static_cast<std::uint64_t>(round)));
    REQUIRE(pair.playing(true));
  }

  for (int round = 0; round < 3; ++round) {
    INFO("new play over a paused one " << round);
    pair.leader.send(mediaCommand("pause", kPause));
    REQUIRE(pair.stopped());
    pair.play(round % 2 == 0 ? "/next.wav" : "/tone.wav");
    REQUIRE(pair.playing(true));
  }

  for (int round = 0; round < 3; ++round) {
    INFO("new play over a playing one " << round);
    pair.play(round % 2 == 0 ? "/next.wav" : "/tone.wav");
    REQUIRE(pair.playing(true));
  }

  // the leader's MediaState carries the heard position: a resume after a seek while paused starts from the seek
  pair.leader.send(mediaCommand("pause", kPause));
  REQUIRE(pair.stopped());
  pair.leader.send(mediaCommand("seek", kSeek, 30000));
  pair.leader.drain(100ms);
  pair.leader.frames().states.clear();
  pair.leader.send(mediaCommand("resume", kResume));
  REQUIRE(pair.playing());
  pair.leader.drain(200ms);
  const auto& states = pair.leader.frames().states;
  REQUIRE_FALSE(states.empty());
  CHECK(states.back().state == kPlaying);
  CHECK(states.back().position_ms >= 30000);
  CHECK(states.back().position_ms < 32000);
}

TEST_CASE("a track's own end and a source's own skip or clear start a new epoch") {
  Pair pair;

  SECTION("a url that ends, then a new play") {
    for (int round = 0; round < 2; ++round) {
      INFO("round " << round);
      pair.play("/short.wav");
      REQUIRE(pair.playing(round > 0));
      // it ends on its own: the group runs dry and goes idle
      REQUIRE(pair.stopped(10s));
    }
    pair.play("/tone.wav");
    REQUIRE(pair.playing(true));
  }

  SECTION("a long-lived source's next track, which it moves to alone") {
    pair.leader.audiod().onLoop([&](auto&, auto&) {
      auto* solo = pair.fakes.source("solo");
      REQUIRE(solo != nullptr);
      solo->events().metadata(titled("One"));
      solo->events().duration(180000);
      solo->events().playing();
      return 0;
    });
    REQUIRE(pair.playing());
    for (int round = 0; round < 3; ++round) {
      INFO("round " << round);
      pair.mark();
      pair.leader.audiod().onLoop([&](auto&, auto&) {
        auto* solo = pair.fakes.source("solo");
        solo->events().metadata(titled("Track " + std::to_string(round)));
        solo->events().position(0);
        return 0;
      });
      REQUIRE(pair.playing(true));
    }
    CHECK(pair.leader.audiod().logs().count(R"("msg":"group_track_changed")") == 3);

    // a clear the source makes itself (its own seek): the epoch stops, and its next audio starts the next one
    pair.mark();
    pair.leader.audiod().onLoop([&](auto&, auto&) {
      pair.fakes.source("solo")->music()->clear();
      return 0;
    });
    REQUIRE(pair.playing(true));
  }
}

TEST_CASE("GroupConfig enables a leader and a member, reconfigures and disables them, GroupStatus on each change") {
  HttpServer server;
  server.route("/tone.wav", {.contentType = "audio/wav", .body = toneWav(30.0, 48000), .ranges = true});
  Device leader(true);
  Device member(false);
  leader.connect();
  member.connect();
  const auto port = freePort();

  // a bad config fails with a token, nothing started
  leader.send(groupConfig({.role = kLeader, .memberId = "L", .port = port, .keyBytes = 8}));
  REQUIRE(within(leader, member, [&] { return leader.in(kGroupFailed); }));
  CHECK(std::string_view(leader.status()->error) == "bad_config");
  member.send(groupConfig({.role = kMember, .memberId = "M", .port = port, .leaderHost = ""}));
  REQUIRE(within(leader, member, [&] { return member.in(kGroupFailed); }));
  CHECK(std::string_view(member.status()->error) == "bad_config");

  leader.send(groupConfig({.role = kLeader, .memberId = "L", .port = port, .members = {"M"}}));
  member.send(groupConfig({.role = kMember, .memberId = "M", .port = port}));
  REQUIRE(within(leader, member, [&] {
    return leader.in(kGroupIdle) && member.in(kGroupIdle) && leader.status()->members_count == 1 &&
           leader.status()->members[0].connected && member.status()->synced;
  }));
  CHECK(leader.status()->role == kLeader);
  CHECK(std::string_view(leader.status()->group_id) == "g-1");
  CHECK(std::string_view(leader.status()->members[0].member_id) == "M");
  CHECK(leader.status()->latency_ms == 500);
  CHECK(member.status()->role == kMember);
  CHECK(leader.audiod().logs().count(R"("msg":"group_started","role":"leader")") == 1);
  CHECK(member.audiod().logs().count(R"("msg":"group_started","role":"member")") == 1);
  // once each thread raised its priority (only android's do), what it came to, in numbers
  CHECK(leader.audiod().logs().eventually(R"("msg":"group_threads","role":"leader","attempted":)"));
  CHECK(member.audiod().logs().eventually(R"("msg":"group_threads","role":"member","attempted":)"));
  CHECK(leader.audiod().logs().count("timeline_nice") == 1);

  // the same config again changes nothing
  leader.send(groupConfig({.role = kLeader, .memberId = "L", .port = port, .members = {"M"}}));
  leader.drain(300ms);
  CHECK(leader.audiod().logs().count(R"("msg":"group_started")") == 1);

  // an idle trim jumps into place without taking the group link down
  leader.send(groupConfig({.role = kLeader, .memberId = "L", .port = port, .members = {"M"}, .trimUs = 1000}));
  leader.drain(100ms);
  CHECK(leader.audiod().logs().count(R"("msg":"group_trim","from_us":0,"to_us":1000,"mode":"jump")") == 1);
  CHECK(leader.audiod().logs().count(R"("msg":"group_stopped")") == 0);
  CHECK(leader.audiod().logs().count(R"("msg":"group_started")") == 1);

  // a state change goes out at once, not at the 5 s round: a play reaches playing on both within a few seconds
  leader.send(urlPlay("play", server.url("/tone.wav")));
  const auto before = leader.statuses();
  REQUIRE(within(
      leader, member,
      [&] { return leader.in(kGroupPlaying) && member.in(kGroupPlaying); }, 4s));
  CHECK(leader.statuses() > before);

#ifdef CADENCE_MEDIA_SIM
  // the sim's alignment probe: every 250th frame (5 s), the leader time each device heard it at, numbers alone; the
  // two agree within the placement's bound
  const auto heardAt = [](Device& d, std::string_view key) -> std::optional<std::int64_t> {
    for (const auto& line : d.audiod().logs().lines()) {
      if (line.find("\"msg\":\"group_heard\"," + std::string(key)) == std::string::npos)
        continue;
      const auto at = line.find("\"t_ns\":");
      return std::stoll(line.substr(at + 7));
    }
    return std::nullopt;
  };
  const std::string frame = R"("epoch":1,"seq":250,)";
  REQUIRE(within(leader, member, [&] { return heardAt(leader, frame) && heardAt(member, frame); }, 10s));
  CHECK(std::abs(*heardAt(leader, frame) - *heardAt(member, frame)) < 2'000'000);
  CHECK(leader.audiod().logs().count(R"("msg":"group_heard","epoch":1,"seq":0,"t_ns":)") == 1);
#else
  CHECK(leader.audiod().logs().count("group_heard") == 0);
#endif

  // a trim while playing slews on both playouts; no config restart, reconnect or new hello follows
  leader.send(groupConfig({.role = kLeader, .memberId = "L", .port = port, .members = {"M"}, .trimUs = 5000}));
  member.send(groupConfig({.role = kMember, .memberId = "M", .port = port, .trimUs = 5000}));
  leader.drain(100ms);
  member.drain(100ms);
  CHECK(leader.audiod().logs().count(R"("msg":"group_trim","from_us":1000,"to_us":5000,"mode":"slew")") == 1);
  CHECK(member.audiod().logs().count(R"("msg":"group_trim","from_us":0,"to_us":5000,"mode":"slew")") == 1);
  CHECK(leader.audiod().logs().count(R"("msg":"group_stopped")") == 0);
  CHECK(member.audiod().logs().count(R"("msg":"group_stopped")") == 0);
  CHECK(leader.audiod().logs().count(R"("msg":"group_started")") == 1);
  CHECK(member.audiod().logs().count(R"("msg":"group_started")") == 1);

  // another latency starts the leader over; the member finds it again
  leader.send(groupConfig({.role = kLeader, .memberId = "L", .port = port, .members = {"M"}, .latencyMs = 800}));
  REQUIRE(within(leader, member, [&] {
    return leader.status() && leader.status()->latency_ms == 800 && leader.status()->members_count == 1 &&
           leader.status()->members[0].connected;
  }));
  CHECK(leader.audiod().logs().count(R"("msg":"group_stopped","reason":"reconfigured")") == 1);

  // disabled: off, and the group's audio is gone
  leader.send(groupConfig({.enabled = false}));
  member.send(groupConfig({.enabled = false}));
  REQUIRE(within(leader, member, [&] { return leader.in(kGroupOff) && member.in(kGroupOff); }));
  CHECK(leader.audiod().logs().count(R"("msg":"group_off")") == 1);
  CHECK(member.audiod().logs().count(R"("msg":"group_stopped")") == 1);

  // rule 8: no log line names the key or an address
  for (auto* d : {&leader, &member})
    for (const auto& line : d->audiod().logs().lines()) {
      CHECK(line.find("127.0.0.1") == std::string::npos);
      CHECK(line.find("@ABC") == std::string::npos);
    }
}

TEST_CASE("a leader whose port is still held fails bind_failed, then starts on its own once the port is free") {
  // on midi a reconfigure started the next leader while the one just stopped still held its clock socket: the bind
  // failed with nothing to try again, and the group stayed failed until its config changed
  Device leader(true);
  Device member(false);
  leader.connect();
  member.connect();
  const auto port = freePort();
  const int held = ::socket(AF_INET, SOCK_DGRAM, 0);
  sockaddr_in addr{};
  addr.sin_family = AF_INET;
  addr.sin_addr.s_addr = htonl(INADDR_ANY);
  addr.sin_port = htons(port);
  REQUIRE(::bind(held, reinterpret_cast<sockaddr*>(&addr), sizeof addr) == 0);

  leader.send(groupConfig({.role = kLeader, .memberId = "L", .port = port, .members = {"M"}}));
  REQUIRE(within(leader, member, [&] { return leader.in(kGroupFailed); }));
  CHECK(std::string_view(leader.status()->error) == "bind_failed");
  // still held past the first retry: still failed, nothing started
  leader.drain(600ms);
  CHECK(leader.in(kGroupFailed));
  CHECK(leader.audiod().logs().count(R"("msg":"group_started")") == 0);

  ::close(held);
  REQUIRE(within(leader, member, [&] { return leader.in(kGroupIdle); }));
  CHECK(leader.audiod().logs().count(R"("msg":"group_started","role":"leader")") == 1);
  CHECK(leader.audiod().logs().eventually(R"("msg":"group_bind_retried")"));

  // a member then joins it as usual
  member.send(groupConfig({.role = kMember, .memberId = "M", .port = port}));
  REQUIRE(within(leader, member, [&] { return member.in(kGroupIdle) && member.status()->synced; }));
}

TEST_CASE("a member that serves the group clock and a leader that dials it: the member's bind is tried again until "
          "its port is free, the leader syncs to it and the group plays") {
  HttpServer server;
  server.route("/tone.wav", {.contentType = "audio/wav", .body = toneWav(30.0, 48000), .ranges = true});
  Device leader(true);
  Device member(false);
  leader.connect();
  member.connect();
  // one port number, as on two devices: the leader's stream on tcp, the master's clock on udp, still held a while
  const auto port = freePort();
  const int held = ::socket(AF_INET, SOCK_DGRAM, 0);
  sockaddr_in addr{};
  addr.sin_family = AF_INET;
  addr.sin_addr.s_addr = htonl(INADDR_ANY);
  addr.sin_port = htons(port);
  REQUIRE(::bind(held, reinterpret_cast<sockaddr*>(&addr), sizeof addr) == 0);

  leader.send(groupConfig({.role = kLeader, .memberId = "L", .port = port, .members = {"M"}, .clockHost = "127.0.0.1"}));
  member.send(groupConfig({.role = kMember, .memberId = "M", .port = port, .clockMaster = true}));
  REQUIRE(within(leader, member, [&] { return member.in(kGroupFailed) && leader.in(kGroupSyncing); }));
  CHECK(std::string_view(member.status()->error) == "bind_failed");
  CHECK_FALSE(leader.status()->synced);
  CHECK(leader.audiod().logs().count(R"("msg":"group_started","role":"leader")") == 1);
  CHECK(leader.audiod().logs().count(R"("clock":"dials")") == 1);

  ::close(held);
  REQUIRE(within(leader, member, [&] {
    return member.in(kGroupIdle) && member.status()->synced && leader.in(kGroupIdle) && leader.status()->synced &&
           leader.status()->members_count == 1 && leader.status()->members[0].connected;
  }, 8s));
  CHECK(member.status()->clock_error_us == 0);
  CHECK(member.audiod().logs().count(R"("msg":"group_started","role":"member")") == 1);
  CHECK(member.audiod().logs().count(R"("clock":"serves")") == 1);
  CHECK(member.audiod().logs().eventually(R"("msg":"group_bind_retried")"));

  leader.send(urlPlay("play", server.url("/tone.wav")));
  REQUIRE(within(leader, member, [&] { return leader.in(kGroupPlaying) && member.in(kGroupPlaying); }, 6s));

  leader.send(groupConfig({.enabled = false}));
  member.send(groupConfig({.enabled = false}));
  REQUIRE(within(leader, member, [&] { return leader.in(kGroupOff) && member.in(kGroupOff); }));
  for (auto* d : {&leader, &member})
    for (const auto& line : d->audiod().logs().lines())
      CHECK(line.find("127.0.0.1") == std::string::npos);
}

namespace {

Envelope external(std::string_view id, cadence_audiod_v1_PlaybackState state, cadence_audiod_v1_AudioRoute route) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_external_source_tag;
  auto& body = envelope.body.external_source;
  copyText(body.id, id);
  body.op = cadence_audiod_v1_ExternalOp_EXTERNAL_OP_UPDATE;
  body.kind = cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_VIDEO;
  body.state = state;
  body.audio_route = route;
  body.audio_controls[body.audio_controls_count++] = cadence_audiod_v1_AudioControl_AUDIO_CONTROL_VOLUME;
  return envelope;
}

constexpr auto kExternalPlaying = cadence_audiod_v1_PlaybackState_PLAYBACK_STATE_PLAYING;
constexpr auto kExternalPaused = cadence_audiod_v1_PlaybackState_PLAYBACK_STATE_PAUSED;

// a screen's player writing a tone into its session's ring as ao_cadence does: as the reader makes room
class RingTone {
 public:
  explicit RingTone(std::unique_ptr<RingWriter> writer) : writer_(std::move(writer)) {
    REQUIRE(writer_);
    writer_->setState(CADENCE_RING_STREAMING);
    thread_ = std::thread([this] {
      std::vector<float> chunk(960 * 2);
      std::size_t done = 0;
      std::uint64_t n = 0;
      while (!stop_) {
        if (done == 0)
          for (std::size_t i = 0; i < 960; ++i, ++n) {
            const auto v = 0.25f * static_cast<float>(std::sin(2.0 * 3.14159265358979 * 440.0 *
                                                                static_cast<double>(n) / 48000.0));
            chunk[2 * i] = v;
            chunk[2 * i + 1] = v;
          }
        done += writer_->write(std::span<const float>(chunk).subspan(done * 2));
        if (done == 960)
          done = 0;
        else
          std::this_thread::sleep_for(1ms);
      }
    });
  }
  ~RingTone() {
    stop_ = true;
    thread_.join();
  }
  // how far ahead of now the reader said the frame at its position is heard
  [[nodiscard]] std::optional<std::int64_t> aheadNs() const {
    std::uint64_t pos = 0;
    std::int64_t heard = 0;
    if (!writer_->timestamp(pos, heard))
      return std::nullopt;
    timespec now{};
    clock_gettime(CLOCK_MONOTONIC, &now);
    return heard - (static_cast<std::int64_t>(now.tv_sec) * 1'000'000'000 + now.tv_nsec);
  }

 private:
  std::unique_ptr<RingWriter> writer_;
  std::atomic<bool> stop_{false};
  std::thread thread_;
};

}  // namespace

TEST_CASE("a screen's ring on a leader feeds the group and is heard the group's latency later; off the group it goes "
          "back to the mixer, and a direct session stays local") {
  Device leader(false, {}, true);
  Device member(false);
  leader.connect();
  member.connect();
  const auto port = freePort();
  leader.send(groupConfig({.role = kLeader, .memberId = "L", .port = port, .members = {"M"}}));
  member.send(groupConfig({.role = kMember, .memberId = "M", .port = port}));
  REQUIRE(within(leader, member, [&] {
    return leader.in(kGroupIdle) && member.in(kGroupIdle) && leader.status()->members_count == 1 &&
           leader.status()->members[0].connected && member.status()->synced;
  }));

  leader.send(external("video-1", kExternalPaused, cadence_audiod_v1_AudioRoute_AUDIO_ROUTE_MIXER));
  leader.drain(100ms);
  RingTone tone(leader.audiod().onLoop([&](IpcServer&, Daemon&) -> std::unique_ptr<RingWriter> {
    auto* ring = leader.audiod().media().externals().ring("video-1");
    return ring == nullptr ? nullptr : RingWriter::attach(ring->ringFd());
  }));
  leader.send(external("video-1", kExternalPlaying, cadence_audiod_v1_AudioRoute_AUDIO_ROUTE_MIXER));
  REQUIRE(within(leader, member, [&] { return leader.in(kGroupPlaying) && member.in(kGroupPlaying); }, 8s));
  CHECK(leader.audiod().logs().count(R"("msg":"group_ring_pump","state":"on")") == 1);
  CHECK(leader.audiod().logs().eventually(R"("msg":"group_ring_started","delay_ms":)"));
  // the ring says its frames are heard the group's 500 ms on, not after the mixer's own few tens of ms
  leader.drain(500ms);
  const auto ahead = tone.aheadNs();
  REQUIRE(ahead);
  CHECK(*ahead > 400'000'000);
  CHECK(*ahead < 1'000'000'000);

  // the group goes: the pump stops before the leader, and the mixer reads the ring again
  leader.send(groupConfig({.enabled = false}));
  member.send(groupConfig({.enabled = false}));
  REQUIRE(within(leader, member, [&] { return leader.in(kGroupOff) && member.in(kGroupOff); }));
  CHECK(leader.audiod().logs().count(R"("msg":"group_ring_pump","state":"off")") == 1);
  REQUIRE(within(leader, member, [&] {
    const auto now = tone.aheadNs();
    return now && *now < 300'000'000;
  }));

  // a passthrough session (it displaces the ring's) on a device that starts to lead plays there alone: the group
  // stays idle (D4)
  leader.send(external("video-2", kExternalPlaying, cadence_audiod_v1_AudioRoute_AUDIO_ROUTE_DIRECT));
  leader.drain(200ms);
  leader.send(groupConfig({.role = kLeader, .memberId = "L", .port = port, .members = {"M"}}));
  member.send(groupConfig({.role = kMember, .memberId = "M", .port = port}));
  REQUIRE(within(leader, member, [&] { return leader.in(kGroupIdle) && member.in(kGroupIdle); }));
  CHECK(leader.audiod().logs().eventually(R"("msg":"group_source_local","reason":"direct")"));
  leader.drain(1s);
  CHECK(leader.in(kGroupIdle));
  CHECK(member.in(kGroupIdle));
}

namespace {

UniqueFd dialTap(const std::string& path) {
  sockaddr_un address{};
  address.sun_family = AF_UNIX;
  std::memcpy(address.sun_path, path.data(), path.size());
  UniqueFd fd(::socket(AF_UNIX, SOCK_SEQPACKET | SOCK_CLOEXEC, 0));
  REQUIRE(fd);
  if (::connect(fd.get(), reinterpret_cast<const sockaddr*>(&address), sizeof address) != 0)
    return UniqueFd();
  return fd;
}

// the newest GroupClock the tap holds, waiting up to limit for one; every other frame skipped
std::optional<cadence_audiod_v1_GroupClock> groupClockFrame(int fd, std::chrono::milliseconds limit = 2s) {
  const auto deadline = std::chrono::steady_clock::now() + limit;
  std::optional<cadence_audiod_v1_GroupClock> newest;
  for (;;) {
    std::array<std::uint8_t, 8192> frame{};
    const auto size = ::recv(fd, frame.data(), frame.size(), MSG_DONTWAIT);
    if (size <= 0) {
      if (newest || std::chrono::steady_clock::now() >= deadline)
        return newest;
      std::this_thread::sleep_for(5ms);
      continue;
    }
    cadence_audiod_v1_TapEnvelope envelope = cadence_audiod_v1_TapEnvelope_init_zero;
    pb_istream_t stream = pb_istream_from_buffer(frame.data(), static_cast<std::size_t>(size));
    if (pb_decode(&stream, &cadence_audiod_v1_TapEnvelope_msg, &envelope) &&
        envelope.which_body == cadence_audiod_v1_TapEnvelope_group_clock_tag)
      newest = envelope.body.group_clock;
  }
}

std::int64_t monoNow() {
  timespec ts{};
  ::clock_gettime(CLOCK_MONOTONIC, &ts);
  return static_cast<std::int64_t>(ts.tv_sec) * 1'000'000'000 + ts.tv_nsec;
}

}  // namespace

// multiroom P4: a follower screen's picture follows the group's media clock, which the member's tap sends: the
// position heard at a monotonic instant runs on with the clock while the group plays, a seek is a new epoch at its
// position, and a pause stops it
TEST_CASE("a member's tap carries the group's media clock: running, a seek's new epoch, a pause") {
  Pair pair;
  const auto path = pair.member.audiod().file("tap.sock");
  Envelope tap = cadence_audiod_v1_Envelope_init_zero;
  tap.which_body = cadence_audiod_v1_Envelope_tap_config_tag;
  copyText(tap.body.tap_config.path, path);
  pair.member.send(tap);
  UniqueFd screen;
  REQUIRE(within(pair.leader, pair.member, [&] {
    screen = dialTap(path);
    return static_cast<bool>(screen);
  }));
  pair.play("/tone.wav");
  REQUIRE(pair.playing());

  // running: two samples a while apart advance with the monotonic clock, and name the instant they were taken
  std::optional<cadence_audiod_v1_GroupClock> first;
  REQUIRE(within(pair.leader, pair.member, [&] {
    first = groupClockFrame(screen.get(), 50ms);
    return first && first->playing;
  }));
  CHECK(std::llabs(first->mono_ns - monoNow()) < 1'000'000'000);
  pair.member.drain(600ms);
  std::optional<cadence_audiod_v1_GroupClock> later;
  REQUIRE(within(pair.leader, pair.member, [&] {
    later = groupClockFrame(screen.get(), 50ms);
    return later && later->playing && later->mono_ns - first->mono_ns > 400'000'000;
  }));
  const auto media = later->media_ns - first->media_ns;
  const auto mono = later->mono_ns - first->mono_ns;
  CHECK(later->epoch == first->epoch);
  CHECK(std::llabs(media - mono) < 60'000'000);

  // a seek: a new epoch from its position
  pair.mark();
  pair.leader.send(mediaCommand("seek", kSeek, 30000));
  REQUIRE(pair.playing(true));
  std::optional<cadence_audiod_v1_GroupClock> sought;
  REQUIRE(within(pair.leader, pair.member, [&] {
    sought = groupClockFrame(screen.get(), 50ms);
    return sought && sought->playing && sought->epoch != first->epoch;
  }));
  CHECK(sought->media_ns >= 30'000'000'000);
  CHECK(sought->media_ns < 32'000'000'000);

  // a pause: the clock says it stopped, at once
  pair.leader.send(mediaCommand("pause", kPause));
  REQUIRE(pair.stopped());
  std::optional<cadence_audiod_v1_GroupClock> paused;
  REQUIRE(within(pair.leader, pair.member, [&] {
    paused = groupClockFrame(screen.get(), 50ms);
    return paused && !paused->playing;
  }));
  CHECK(paused->media_ns >= sought->media_ns);
}
