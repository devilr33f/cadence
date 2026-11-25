// the bar's position (PositionModel, PlaybackContext::seekBy): run on while it plays, a seek's target held until a
// reading of that seek, readings from before it dropped, rapid presses added up into one target
#include "app/routes/playback/context.h"
#include "app/routes/playback/position.h"

#include <catch2/catch_test_macros.hpp>

#include <cstdlib>
#include <vector>

using namespace cadence::screen;
using namespace cadence::screen::routes;

namespace {

player::PositionSample reading(std::uint64_t pos, std::int64_t at, double rate, std::uint32_t seq = 0, bool pending = false) {
  return player::PositionSample{pos, at, rate, seq, pending};
}

// a player that counts its seeks as mpv's does, landing them only when told
class CountingPlayer final : public app::PlayerControl {
 public:
  player::Result play(const player::PlayRequest&) override { return ""; }
  player::Result command(const player::Command& c) override {
    commands.push_back(c);
    if (c.action == player::CommandAction::Seek) {
      st.seekSeq++;
      st.seekPending = true;
    }
    return "";
  }
  [[nodiscard]] const player::PlayerState& state() const override { return st; }
  player::PlayerState st;
  std::vector<player::Command> commands;
};

class Host final : public PlaybackHost {
 public:
  ui::Ui& ui() override { std::abort(); }
  bool send(const cadence_screen_v1_Envelope& e) override {
    sent.push_back(e.body.now_playing_command.position_ms);
    return true;
  }
  app::PlayerControl* player() override { return &p; }
  void leave() override {}
  [[nodiscard]] std::int64_t epochMs() const override { return now; }
  CountingPlayer p;
  std::int64_t now = 1'000'000;
  std::vector<std::uint64_t> sent;
};

}  // namespace

TEST_CASE("position: a reading runs on at its rate while it plays, stands while it does not, within the item") {
  PositionModel m;
  CHECK(m.at(5000) == 0);
  m.reading(reading(10'000, 1000, 1.0), 1000);
  CHECK(m.at(1000) == 10'000);
  CHECK(m.at(1500) == 10'500);
  // a reading's clock behind the reader's: never backwards
  CHECK(m.at(900) == 10'000);
  // run on a minute at most
  CHECK(m.at(1000 + 120'000) == 10'000 + PositionModel::kRunOnMs);
  CHECK(m.at(4000, 12'000) == 12'000);
  m.reading(reading(20'000, 2000, 0.0), 2000);
  CHECK(m.at(9000) == 20'000);
  m.reading(reading(20'000, 2000, 1.5), 2000);
  CHECK(m.at(3000) == 21'500);
}

TEST_CASE("position: a seek's target holds until a reading of that seek, and readings from before it are dropped") {
  PositionModel m;
  m.reading(reading(60'000, 1000, 1.0, 3), 1000);
  m.seek(70'000, 1100, 4);
  CHECK(m.holding(1100));
  CHECK(m.at(1100) == 70'000);
  // the target stands while the seek is under way, whatever the reading says
  CHECK(m.at(1500) == 70'000);
  // a reading of the seek before (3), and one of this seek still pending: dropped
  CHECK_FALSE(m.reading(reading(60'500, 1500, 1.0, 3), 1500));
  CHECK_FALSE(m.reading(reading(60'600, 1600, 0.0, 4, true), 1600));
  CHECK(m.at(1600) == 70'000);
  // the seek landed (a keyframe away): its reading is the bar's from now
  CHECK(m.reading(reading(68'000, 1700, 1.0, 4), 1700));
  CHECK_FALSE(m.holding(1700));
  CHECK(m.at(2700) == 69'000);
  // a later seek's reading lands an earlier hold too
  m.seek(80'000, 3000, 5);
  CHECK(m.reading(reading(90'000, 3100, 1.0, 6), 3100));
  CHECK(m.at(3100) == 90'000);
}

TEST_CASE("position: a hold nobody confirms ends after kHoldMs, on the newest reading") {
  PositionModel m;
  m.reading(reading(5000, 0, 0.0, 1), 0);
  m.seek(50'000, 0, 2);
  CHECK_FALSE(m.reading(reading(5000, 100, 0.0, 1), 100));
  CHECK(m.at(PositionModel::kHoldMs - 1) == 50'000);
  CHECK(m.at(PositionModel::kHoldMs) == 5000);
}

TEST_CASE("position: cadenced's echoes count once read after the seek and near its target") {
  PositionModel m;
  m.reading(reading(30'000, 1000, 1.0), 1000);
  m.seek(40'000, 2000, std::nullopt);
  // read before the seek went (an echo on its way): dropped, even near the target
  CHECK_FALSE(m.reading(reading(39'000, 1900, 1.0), 2100));
  // read after, but far from the target (the old position run on): dropped
  CHECK_FALSE(m.reading(reading(31'200, 2200, 1.0), 2200));
  CHECK(m.at(2300) == 40'000);
  // near the target, run on to its time
  CHECK(m.reading(reading(41'400, 3000, 1.0), 3000));
  CHECK(m.at(3500) == 41'900);
}

TEST_CASE("seek: rapid presses add up from the held target, as relative seeks of the screen's own player") {
  Host h;
  PlaybackContext c;
  c.host = &h;
  Session s;
  s.sessionId = "video-1";
  c.session = s;
  c.state.durationMs = 1'440'000;
  c.pos.reading(reading(100'000, h.now, 1.0, 0), h.now);
  CHECK(c.seekBy(10'000).empty());
  CHECK(c.position() == 110'000);
  h.now += 120;
  // the player's reading from before the seek (still at the old place, running on): the bar stays on the target
  c.pos.reading(reading(100'120, h.now, 1.0, h.p.st.seekSeq, true), h.now);
  CHECK(c.position() == 110'000);
  CHECK(c.seekBy(10'000).empty());
  CHECK(c.seekBy(10'000).empty());
  CHECK(c.position() == 130'000);
  REQUIRE(h.p.commands.size() == 3);
  for (const auto& cmd : h.p.commands) {
    CHECK(cmd.relative);
    CHECK(cmd.deltaMs == 10'000);
  }
  // back past the start: to 0, by the steps that reach it
  CHECK(c.seekBy(-500'000).empty());
  CHECK(c.position() == 0);
  CHECK(h.p.commands.back().deltaMs == -130'000);
  // never past the last second
  CHECK(c.seekBy(5'000'000).empty());
  CHECK(c.position() == 1'439'000);
}

TEST_CASE("seek: a session cadenced renders seeks to a position, and its stale echo never moves the bar back") {
  Host h;
  PlaybackContext c;
  c.host = &h;
  Session s;
  s.sessionId = "airplay-1";
  s.device = true;
  c.session = s;
  c.state.durationMs = 300'000;
  c.pos.reading(reading(20'000, h.now, 1.0), h.now);
  CHECK(c.seekBy(10'000).empty());
  CHECK(c.seekBy(10'000).empty());
  REQUIRE(h.sent.size() == 2);
  CHECK(h.sent[0] == 30'000);
  CHECK(h.sent[1] == 40'000);
  c.pos.reading(reading(20'300, h.now - 50, 1.0), h.now + 300);
  CHECK(c.position() == 40'000);
}
