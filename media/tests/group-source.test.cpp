// a member's GROUP session source: the leader's state takes and leaves the session, transport goes to the leader,
// a local set-aside stays local; and the sessions moved onto a group leader's feed
#include "audiod/group-source.h"
#include "audiod/media-sessions.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <cstring>
#include <string>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;

namespace {

struct Recorder final : MediaSourceEvents {
  std::vector<std::string> calls;
  MediaMetadata last;
  void playing() override { calls.emplace_back("playing"); }
  void paused() override { calls.emplace_back("paused"); }
  void buffering() override { calls.emplace_back("buffering"); }
  void ended() override { calls.emplace_back("ended"); }
  void failed(std::string_view) override { calls.emplace_back("failed"); }
  void metadata(const MediaMetadata& m) override { last = m; }
  void duration(std::uint64_t) override {}
  void position(std::uint64_t ms) override { calls.push_back("position:" + std::to_string(ms)); }
};

cadence_sync_v1_Metadata leader(const char* title, bool playing, std::uint64_t durationMs = 0) {
  cadence_sync_v1_Metadata m = cadence_sync_v1_Metadata_init_zero;
  std::strcpy(m.title, title);
  m.playing = playing;
  m.duration_ms = durationMs;
  return m;
}

}  // namespace

TEST_CASE("the leader's state takes the session, pauses it and ends it") {
  auto events = std::make_shared<Recorder>();
  std::vector<int> controls;
  std::vector<bool> active;
  bool destroyed = false;
  {
    GroupSource source(
        events,
        [&](cadence_sync_v1_ControlAction action, std::uint64_t) {
          controls.push_back(static_cast<int>(action));
          return true;
        },
        [&](bool a) { active.push_back(a); }, [&] { destroyed = true; });
    CHECK(source.kind() == MediaKind::Group);
    CHECK(source.keepsPaused());
    source.leaderState(leader("song", true, 1000));
    source.leaderState(leader("song", true, 1000));
    source.leaderState(leader("song", false, 1000));
    source.leaderState(leader("song", true, 1000));
    source.leaderState(leader("", false));
    CHECK(events->calls == std::vector<std::string>{"playing", "paused", "playing", "ended"});
    CHECK(events->last.title.empty());

    PcmInput music;
    source.openOutput(music);
    // a pause the device makes as another source takes the speaker stays here
    source.setAsideBy("displaced");
    CHECK(source.pause() == kMediaOk);
    source.closeOutput();
    CHECK(controls.empty());
    // a press goes to the leader
    CHECK(source.pause() == kMediaOk);
    CHECK(source.next() == kMediaOk);
    CHECK(controls == std::vector<int>{cadence_sync_v1_ControlAction_CONTROL_ACTION_PAUSE,
                                       cadence_sync_v1_ControlAction_CONTROL_ACTION_NEXT});
    source.openOutput(music);
    CHECK(active == std::vector<bool>{true, false, true});
  }
  // destroyed while it held the output: the playout lets go first
  CHECK(active.back() == false);
  CHECK(destroyed);
}

TEST_CASE("a source that cannot reach its leader says so") {
  auto events = std::make_shared<Recorder>();
  GroupSource source(
      events, [](cadence_sync_v1_ControlAction, std::uint64_t) { return false; }, [](bool) {});
  CHECK(source.resume() == kMediaFailed);
}

TEST_CASE("the member's session runs at the position it hears the group at") {
  auto events = std::make_shared<Recorder>();
  GroupSource source(
      events, [](cadence_sync_v1_ControlAction, std::uint64_t) { return true; }, [](bool) {});
  constexpr std::int64_t kMs = 1'000'000;
  // nothing before the leader says it plays
  source.heard(1, 5000, 0);
  source.leaderState(leader("song", true, 200'000));
  // joined mid-song: told once, then left to run on while what it hears agrees
  source.heard(1, 61'000, 1000 * kMs);
  source.heard(1, 61'100, 1100 * kMs);
  source.heard(1, 62'400, 2000 * kMs);
  // a seek within the epoch's run, and a new epoch (the next track) at once
  source.heard(1, 90'000, 2100 * kMs);
  source.heard(2, 90'200, 2200 * kMs);
  // a pause, then a resume in the same epoch tells it again
  source.leaderState(leader("song", false, 200'000));
  source.heard(2, 91'000, 3000 * kMs);
  source.leaderState(leader("song", true, 200'000));
  source.heard(2, 91'000, 9000 * kMs);
  CHECK(events->calls == std::vector<std::string>{"playing", "position:61000", "position:90000", "position:90200",
                                                  "paused", "playing", "position:91000"});
}

namespace {

// records where it was told to push
class Pusher final : public MediaSource {
 public:
  explicit Pusher(std::vector<PcmInput*>& outputs) : outputs_(outputs) {}
  [[nodiscard]] MediaKind kind() const override { return MediaKind::Url; }
  [[nodiscard]] MediaActions actions() const override { return {MediaAction::Stop}; }
  [[nodiscard]] bool keepsPaused() const override { return false; }
  void openOutput(PcmInput& music) override { outputs_.push_back(&music); }
  void closeOutput() override { outputs_.push_back(nullptr); }
  std::string_view pause() override { return kMediaOk; }
  std::string_view resume() override { return kMediaOk; }
  std::string_view stop() override { return kMediaOk; }
  std::string_view seek(std::uint64_t) override { return kMediaUnsupported; }
  std::string_view next() override { return kMediaUnsupported; }
  std::string_view previous() override { return kMediaUnsupported; }

 private:
  std::vector<PcmInput*>& outputs_;
};

}  // namespace

TEST_CASE("retargeted, the session's source closes its output and opens it on the leader's feed, and back") {
  std::vector<std::string> lines;
  const Log log([&](std::string_view line) { lines.emplace_back(line); });
  const auto logged = [&](std::string_view fragment) {
    return std::count_if(lines.begin(), lines.end(),
                         [&](const std::string& line) { return line.find(fragment) != std::string::npos; });
  };
  PcmInput music;
  PcmInput feed;
  MediaSessions sessions(log, music, [] { return true; }, refuseUrls, [](const MediaSession&) {});
  std::vector<PcmInput*> outputs;
  std::shared_ptr<MediaSourceEvents> events;
  sessions.add([&](std::shared_ptr<MediaSourceEvents> e) {
    events = e;
    return std::make_unique<Pusher>(outputs);
  });
  events->playing();
  REQUIRE(outputs == std::vector<PcmInput*>{&music});
  sessions.retarget(feed);
  CHECK(outputs == std::vector<PcmInput*>{&music, nullptr, &feed});
  CHECK(logged(R"("msg":"group_source","kind":"url","target":"feed")") == 1);
  sessions.retarget(feed);
  CHECK(outputs.size() == 3);
  sessions.retarget(music);
  CHECK(outputs.back() == &music);
  CHECK(logged(R"("msg":"group_source","kind":"url","target":"music")") == 1);
}
