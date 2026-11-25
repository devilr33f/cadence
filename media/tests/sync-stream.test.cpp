// the group stream: its framing and tags as sync.proto's header says, and a leader and a member over loopback
#include "sync/stream-leader.h"
#include "sync/stream-member.h"
#include "sync/stream-wire.h"

#include <catch2/catch_test_macros.hpp>

#include <atomic>
#include <chrono>
#include <cstring>
#include <mutex>
#include <string>
#include <thread>
#include <vector>

using namespace cadence::sync;
using namespace std::chrono_literals;

namespace {

const std::vector<std::uint8_t> kKey(32, 0x5a);

SyncEnvelope join(std::int64_t from) {
  SyncEnvelope e = cadence_sync_v1_Envelope_init_zero;
  e.which_body = cadence_sync_v1_Envelope_join_tag;
  e.body.join.from_ns = from;
  return e;
}

template <typename F>
bool eventually(F f, std::chrono::milliseconds bound = 5000ms) {
  const auto end = std::chrono::steady_clock::now() + bound;
  while (std::chrono::steady_clock::now() < end) {
    if (f())
      return true;
    std::this_thread::sleep_for(5ms);
  }
  return f();
}

struct Received {
  std::mutex mutex;
  std::vector<std::uint64_t> seqs;
  std::vector<std::int64_t> times;
  std::uint32_t formats = 0;
  std::uint32_t pauses = 0;
  std::uint32_t flushes = 0;
  bool duck = false;
  std::string title;
  std::string lost;
  std::atomic<int> welcomed{0};
};

MemberStreamEvents recording(Received& r, MemberStream** self, std::int64_t joinFrom) {
  MemberStreamEvents events;
  events.welcomed = [&r, self, joinFrom](std::uint32_t) {
    ++r.welcomed;
    (*self)->send(join(joinFrom));
  };
  events.message = [&r](const SyncEnvelope& e) {
    std::lock_guard lock(r.mutex);
    switch (e.which_body) {
      case cadence_sync_v1_Envelope_frame_tag:
        r.seqs.push_back(e.body.frame.seq);
        r.times.push_back(e.body.frame.t_ns);
        break;
      case cadence_sync_v1_Envelope_format_tag:
        ++r.formats;
        break;
      case cadence_sync_v1_Envelope_pause_tag:
        ++r.pauses;
        break;
      case cadence_sync_v1_Envelope_flush_tag:
        ++r.flushes;
        break;
      case cadence_sync_v1_Envelope_duck_tag:
        r.duck = e.body.duck.active;
        break;
      case cadence_sync_v1_Envelope_metadata_tag:
        r.title = e.body.metadata.title;
        break;
      default:
        break;
    }
  };
  events.lost = [&r](std::string_view reason) {
    std::lock_guard lock(r.mutex);
    r.lost = std::string(reason);
  };
  return events;
}

LeaderStreamConfig leaderConfig(std::vector<std::string> members) {
  LeaderStreamConfig c;
  c.groupId = "g1";
  c.key = kKey;
  c.members = std::move(members);
  return c;
}

MemberStreamConfig memberConfig(std::uint16_t port, std::string group = "g1", std::string id = "m1",
                                std::vector<std::uint8_t> key = kKey) {
  MemberStreamConfig c;
  c.host = "127.0.0.1";
  c.port = port;
  c.groupId = std::move(group);
  c.memberId = std::move(id);
  c.key = std::move(key);
  return c;
}

cadence_sync_v1_Format format(std::uint32_t epoch) {
  cadence_sync_v1_Format f = cadence_sync_v1_Format_init_zero;
  f.epoch = epoch;
  f.codec = cadence_sync_v1_Codec_CODEC_PCM_S16;
  f.rate = 48000;
  f.channels = 2;
  f.frame_samples = 960;
  return f;
}

StreamFrame frame(std::uint32_t epoch, std::uint64_t seq, std::int64_t t) {
  return {.epoch = epoch, .seq = seq, .tNs = t, .mediaMs = seq * 20, .data = std::vector<std::uint8_t>(3840, 7)};
}

}  // namespace

TEST_CASE("a sealed message opens once, in order, and a changed byte, a replay or an oversize refuses the stream") {
  StreamSealer sealer(kKey);
  StreamOpener opener(kKey);
  std::vector<std::uint8_t> wire;
  REQUIRE(sealer.seal(join(42), wire));
  REQUIRE(sealer.seal(join(43), wire));
  // in two pieces, as tcp may hand it over
  opener.feed(std::span<const std::uint8_t>(wire.data(), 5));
  SyncEnvelope e;
  CHECK(opener.next(e) == OpenResult::Need);
  opener.feed(std::span<const std::uint8_t>(wire.data() + 5, wire.size() - 5));
  REQUIRE(opener.next(e) == OpenResult::Message);
  CHECK(e.body.join.from_ns == 42);
  REQUIRE(opener.next(e) == OpenResult::Message);
  CHECK(e.body.join.from_ns == 43);
  CHECK(opener.next(e) == OpenResult::Need);

  // the tag is the first 16 bytes of hmac(key, counter || body)
  std::vector<std::uint8_t> one;
  StreamSealer fresh(kKey);
  REQUIRE(fresh.seal(join(42), one));
  const auto size = static_cast<std::size_t>(one[3]);
  const auto tag = streamTag(kKey, 0, std::span<const std::uint8_t>(one.data() + 4, size));
  CHECK(std::memcmp(tag.data(), one.data() + 4 + size, 16) == 0);

  SECTION("a changed byte") {
    auto bad = one;
    bad[5] ^= 1;
    StreamOpener o(kKey);
    o.feed(bad);
    CHECK(o.next(e) == OpenResult::Refused);
    // and it stays refused
    o.feed(one);
    CHECK(o.next(e) == OpenResult::Refused);
  }
  SECTION("a replay: the counter moved on") {
    StreamOpener o(kKey);
    o.feed(one);
    o.feed(one);
    CHECK(o.next(e) == OpenResult::Message);
    CHECK(o.next(e) == OpenResult::Refused);
  }
  SECTION("another key") {
    StreamOpener o(std::vector<std::uint8_t>(32, 1));
    o.feed(one);
    CHECK(o.next(e) == OpenResult::Refused);
  }
  SECTION("a length over 64 KiB") {
    StreamOpener o(kKey);
    const std::uint8_t huge[4] = {0, 1, 0, 1};
    o.feed(huge);
    CHECK(o.next(e) == OpenResult::Refused);
  }
  SECTION("after a rekey the counter starts again under the new key") {
    StreamSealer s(kKey);
    StreamOpener o(kKey);
    std::vector<std::uint8_t> w;
    REQUIRE(s.seal(join(1), w));
    const std::vector<std::uint8_t> nonceA(16, 1);
    const std::vector<std::uint8_t> nonceB(16, 2);
    const auto session = sessionKey(kKey, nonceA, nonceB);
    s.rekey(session);
    REQUIRE(s.seal(join(2), w));
    o.feed(w);
    REQUIRE(o.next(e) == OpenResult::Message);
    o.rekey(session);
    REQUIRE(o.next(e) == OpenResult::Message);
    CHECK(e.body.join.from_ns == 2);
  }
}

TEST_CASE("a member joins over loopback, takes the backlog from its join, then live frames and the controls") {
  std::atomic<int> connected{0};
  std::atomic<int> readies{0};
  LeaderStreamEvents le;
  le.connected = [&](const std::string&) { ++connected; };
  le.ready = [&](const std::string&, const cadence_sync_v1_Ready&) { ++readies; };
  const auto lc = leaderConfig({"m1", "m2"});
  auto made = LeaderStream::listen("127.0.0.1", 0, lc, le);
  REQUIRE(made.ok());
  auto& leader = *made.value();
  const auto now = monoNs();
  leader.beginEpoch(format(1));
  for (std::uint64_t i = 0; i < 50; ++i)
    leader.pushFrame(frame(1, i, now + static_cast<std::int64_t>(i) * 20'000'000));
  cadence_sync_v1_Metadata md = cadence_sync_v1_Metadata_init_zero;
  std::strcpy(md.title, "t");
  leader.metadata(md);

  Received r;
  MemberStream* self = nullptr;
  MemberStream member(memberConfig(leader.port()),
                      recording(r, &self, now + 30 * 20'000'000));
  self = &member;
  member.start();
  REQUIRE(eventually([&] {
    std::lock_guard lock(r.mutex);
    return r.seqs.size() == 20;
  }));
  {
    std::lock_guard lock(r.mutex);
    CHECK(r.seqs.front() == 30);
    CHECK(r.formats == 1);
    CHECK(r.title == "t");
  }
  CHECK(connected == 1);
  leader.pushFrame(frame(1, 50, now + 50 * 20'000'000));
  leader.duck(true);
  SyncEnvelope ready = cadence_sync_v1_Envelope_init_zero;
  ready.which_body = cadence_sync_v1_Envelope_ready_tag;
  ready.body.ready.epoch = 1;
  member.send(ready);
  REQUIRE(eventually([&] { return readies == 1; }));
  leader.pause(1, now + 60 * 20'000'000);
  leader.flush(2);
  leader.beginEpoch(format(2));
  leader.pushFrame(frame(2, 0, now));
  REQUIRE(eventually([&] {
    std::lock_guard lock(r.mutex);
    return r.seqs.size() == 22 && r.flushes == 1 && r.pauses == 1 && r.duck;
  }));
  {
    std::lock_guard lock(r.mutex);
    // a new epoch is served from its first frame, whatever the join said
    CHECK(r.seqs.back() == 0);
    CHECK(r.formats == 2);
  }
  const auto views = leader.members();
  REQUIRE(views.size() == 2);
  CHECK(views[0].connected);
  CHECK(views[0].readyEpoch == 1);
  CHECK_FALSE(views[1].connected);
  member.stop();
  leader.stop();
}

TEST_CASE("a wrong key, another group or a stranger is refused, and none of them hears a frame") {
  std::vector<std::string> refusals;
  std::mutex mutex;
  LeaderStreamEvents le;
  le.refused = [&](std::string_view reason) {
    std::lock_guard lock(mutex);
    refusals.emplace_back(reason);
  };
  auto made = LeaderStream::listen("127.0.0.1", 0, leaderConfig({"m1"}), le);
  REQUIRE(made.ok());
  auto& leader = *made.value();
  leader.beginEpoch(format(1));
  leader.pushFrame(frame(1, 0, monoNs()));

  struct Case {
    std::string group;
    std::string id;
    std::vector<std::uint8_t> key;
    std::string want;
  };
  const std::vector<Case> cases = {{"g1", "m1", std::vector<std::uint8_t>(32, 1), "auth_failed"},
                                   {"g2", "m1", kKey, "wrong_group"},
                                   {"g1", "m9", kKey, "refused"}};
  for (const auto& c : cases) {
    Received r;
    MemberStream* self = nullptr;
    MemberStream member(memberConfig(leader.port(), c.group, c.id, c.key),
                        recording(r, &self, 0));
    self = &member;
    member.start();
    REQUIRE(eventually([&] {
      std::lock_guard lock(r.mutex);
      return !r.lost.empty();
    }));
    member.stop();
    std::lock_guard lock(r.mutex);
    CHECK(r.lost == c.want);
    CHECK(r.welcomed == 0);
    CHECK(r.seqs.empty());
  }
  // the wrong key fails at the challenge's tag on the member's side: the leader sees two refusals by name
  std::lock_guard lock(mutex);
  CHECK(refusals.size() >= 2);
  CHECK_FALSE(leader.members()[0].connected);
  leader.stop();
}

TEST_CASE("a second connection of one member replaces the first") {
  auto made = LeaderStream::listen("127.0.0.1", 0, leaderConfig({"m1"}), {});
  REQUIRE(made.ok());
  auto& leader = *made.value();
  Received a;
  Received b;
  MemberStream* sa = nullptr;
  MemberStream* sb = nullptr;
  auto mc = memberConfig(leader.port());
  mc.backoffMinMs = 5000;
  MemberStream first(mc, recording(a, &sa, 0));
  sa = &first;
  first.start();
  REQUIRE(eventually([&] { return a.welcomed == 1; }));
  MemberStream second(mc, recording(b, &sb, 0));
  sb = &second;
  second.start();
  REQUIRE(eventually([&] {
    std::lock_guard lock(a.mutex);
    return a.lost == "replaced";
  }));
  CHECK(b.welcomed == 1);
  first.stop();
  second.stop();
  leader.stop();
}
