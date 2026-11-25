#include "audiod/system-volume.h"

#include <catch2/catch_test_macros.hpp>

#include <vector>

using namespace cadence::audiod;

namespace {

// a stream as android keeps it: an index on 0..max and a mute, and every index written
struct FakeStream {
  int index = 0;
  int max = 15;
  bool muted = false;
  int readError = 0;
  int writeError = 0;
  std::vector<int> writes;
};

int readStream(void* ctx, int* index, int* max, int* muted) {
  auto* s = static_cast<FakeStream*>(ctx);
  if (s->readError != 0)
    return s->readError;
  *index = s->index;
  *max = s->max;
  *muted = s->muted ? 1 : 0;
  return 0;
}

int writeStream(void* ctx, int index) {
  auto* s = static_cast<FakeStream*>(ctx);
  if (s->writeError != 0)
    return s->writeError;
  s->writes.push_back(index);
  s->index = index;
  return 0;
}

SystemReading at(int index, int max = 15, bool muted = false) { return {index, max, muted}; }

}  // namespace

TEST_CASE("every index has a level that maps back to it") {
  for (const int max : {1, 7, 15, 25, 100}) {
    for (int i = 0; i <= max; ++i)
      REQUIRE(SystemLevels::indexOf(SystemLevels::levelOf(i, max), max) == i);
  }
  REQUIRE(SystemLevels::indexOf(0, 15) == 0);
  REQUIRE(SystemLevels::indexOf(100, 15) == 15);
  REQUIRE(SystemLevels::indexOf(30, 15) == 5);
  REQUIRE(SystemLevels::levelOf(5, 15) == 33);
  // out of range is clamped, never past the top
  REQUIRE(SystemLevels::indexOf(400, 15) == 15);
  REQUIRE(SystemLevels::indexOf(30, 0) == 0);
}

TEST_CASE("a level the system holds writes nothing; cadenced's own level is written") {
  SystemLevels l;
  REQUIRE(l.start(at(5)) == SystemChange{33, false});
  // 30 is index 5 already: nothing moves, and 30 stays 30
  REQUIRE_FALSE(l.set(30, at(5)).has_value());
  REQUIRE(l.holds(30, at(5)));
  REQUIRE(l.current().level == 30);
  REQUIRE(l.set(10, at(5)) == 2);
  REQUIRE_FALSE(l.holds(10, at(5)));
  REQUIRE(l.holds(10, at(2)));
}

TEST_CASE("our own write read back is no change; the system's move is, with its level") {
  SystemLevels l;
  l.start(at(5));
  REQUIRE(l.set(30, at(5)) == std::nullopt);
  REQUIRE_FALSE(l.observe(at(5)).has_value());
  REQUIRE(l.observe(at(6)) == SystemChange{40, false});
  REQUIRE_FALSE(l.observe(at(6)).has_value());
  // a mute alone is a change, at the same level
  REQUIRE(l.observe(at(6, 15, true)) == SystemChange{40, true});
}

TEST_CASE("a held key: cadenced's echoes of the system's moves never pull it back") {
  SystemLevels l;
  l.start(at(5));
  REQUIRE(l.observe(at(6))->level == 40);
  REQUIRE(l.observe(at(7))->level == 47);
  // cadenced stored 40 and sends it back while the stream is at 7 already: nothing is written, and it holds
  REQUIRE_FALSE(l.set(40, at(7)).has_value());
  REQUIRE(l.holds(40, at(7)));
  REQUIRE_FALSE(l.set(47, at(7)).has_value());
  // the echoes are spent: the next 40 is cadenced's own, and written
  REQUIRE(l.set(40, at(7)) == 6);
}

TEST_CASE("cadenced's own level wins over a move still on its way") {
  SystemLevels l;
  l.start(at(5));
  REQUIRE(l.observe(at(8))->level == 53);
  // a voice turn lowers to 20 before cadenced heard of 53
  REQUIRE(l.set(20, at(8)) == 3);
  // then cadenced follows the 53 it heard: no echo is left, so it is written, as cadenced's last word
  REQUIRE(l.set(53, at(3)) == 8);
}

TEST_CASE("the control reads, writes and verifies through the host's backend") {
  FakeStream s;
  s.index = 5;
  const cadence_system_volume_backend_t backend{&s, readStream, writeStream};
  SystemVolumeControl c(backend, at(5));
  REQUIRE(c.current() == SystemChange{33, false});
  REQUIRE_FALSE(c.setPercent(30).has_value());
  REQUIRE(s.writes.empty());
  REQUIRE_FALSE(c.verifyPercent(30).has_value());
  REQUIRE_FALSE(c.setPercent(60).has_value());
  REQUIRE(s.writes == std::vector<int>{9});
  REQUIRE_FALSE(c.verifyPercent(60).has_value());

  // the remote's key: the stream moves by itself
  s.index = 10;
  REQUIRE(c.poll() == SystemChange{67, false});
  REQUIRE_FALSE(c.poll().has_value());
  REQUIRE_FALSE(c.setPercent(67).has_value());
  REQUIRE(s.writes.size() == 1);

  // another writer between the set and its read back: a mismatch, which the playback retries
  REQUIRE_FALSE(c.setPercent(20).has_value());
  s.index = 12;
  const auto off = c.verifyPercent(20);
  REQUIRE(off.has_value());
  REQUIRE(off->mismatch.has_value());
  REQUIRE(off->mismatch->wanted == 3);
  REQUIRE(off->mismatch->held == 12);

  s.writeError = -5;
  REQUIRE(c.setPercent(90).has_value());
  s.readError = -19;
  REQUIRE(c.setPercent(10).has_value());
  REQUIRE_FALSE(c.poll().has_value());
}

TEST_CASE("open needs a backend that reads a stream of its own") {
  cadence_audiod_system_volume(nullptr);
  REQUIRE(SystemVolumeControl::open() == nullptr);
  FakeStream s;
  s.index = 4;
  const cadence_system_volume_backend_t backend{&s, readStream, writeStream};
  cadence_audiod_system_volume(&backend);
  auto c = SystemVolumeControl::open();
  REQUIRE(c != nullptr);
  REQUIRE(c->current().level == 27);
  // a fixed volume (hdmi-cec passthrough): no stream to move, the level stays a software gain
  s.readError = -95;
  REQUIRE(SystemVolumeControl::open() == nullptr);
  s.readError = 0;
  s.max = 0;
  REQUIRE(SystemVolumeControl::open() == nullptr);
  cadence_audiod_system_volume(nullptr);
}

TEST_CASE("the host's notice reaches the one listener") {
  int heard = 0;
  onSystemVolumeChanged([&heard] { ++heard; });
  cadence_audiod_system_volume_changed();
  cadence_audiod_system_volume_changed();
  onSystemVolumeChanged({});
  cadence_audiod_system_volume_changed();
  REQUIRE(heard == 2);
}
