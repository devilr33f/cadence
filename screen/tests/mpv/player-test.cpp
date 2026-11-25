// libmpv through the render api on the offscreen context: the fixture clip decodes into our texture, the frame is
// stable, and the gate, levels, tracks and states behave. every player runs ao=null: nothing ever sounds
#include "player/mpv-player.h"
#include "render/capture.h"
#include "support/env.h"
#include "support/gl-env.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <chrono>
#include <cmath>
#include <cstdlib>
#include <functional>
#include <thread>
#include <vector>

using namespace cadence::screen;
using test::GlEnv;
using test::near;
using test::pixel;

namespace {

constexpr render::Size kSize{320, 180};

std::int64_t unixMs() {
  return std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::system_clock::now().time_since_epoch())
      .count();
}

struct Harness {
  GlEnv env{kSize.width, kSize.height};
  std::unique_ptr<player::MpvPlayer> p;
  std::vector<player::PlayerState> states;

  explicit Harness(std::int64_t statsEveryMs = 10000) {
    player::MpvPlayerConfig c;
    c.statsEveryMs = statsEveryMs;
    c.mpv.aoOverride = "null";
    c.mpv.hwdec = "no";
    c.output = std::make_unique<player::RenderApiOutput>(*env.backend, kSize, &platform::Platform::getProcAddress,
                                                         nullptr);
    c.unixNowMs = unixMs;
    p = player::MpvPlayer::create(std::move(c));
    REQUIRE(p);
    p->onStateChanged([this](const player::PlayerState& s) { states.push_back(s); });
  }
  ~Harness() { p.reset(); }

  // pumps and renders until pred holds or timeoutMs passes
  bool runUntil(const std::function<bool()>& pred, int timeoutMs = 10000) {
    const auto deadline = std::chrono::steady_clock::now() + std::chrono::milliseconds(timeoutMs);
    while (std::chrono::steady_clock::now() < deadline) {
      p->pump(unixMs());
      if (p->frameDue()) p->renderFrame();
      if (pred()) return true;
      std::this_thread::sleep_for(std::chrono::milliseconds(5));
    }
    return false;
  }

  // the player's texture drawn through the backend into a target, read top row first
  render::Image frame() {
    auto tex = p->videoTexture();
    REQUIRE(tex);
    auto target = env.backend->createRenderTarget(kSize, false);
    env.backend->bindRenderTarget(&target);
    render::QuadParams q;
    q.dst = {0, 0, kSize.width, kSize.height};
    q.blend = render::Blend::None;
    env.backend->drawTexturedQuad(*tex, q);
    auto img = render::readTarget(target);
    env.backend->bindRenderTarget(nullptr);
    env.backend->destroyRenderTarget(target);
    return img;
  }

  player::PlayRequest clip(const std::string& session = "sess-1") {
    player::PlayRequest r;
    r.sessionId = session;
    r.url = test::clip("colors.mkv");
    return r;
  }

  // an observed property reaches value within timeoutMs (mpv applies sets asynchronously)
  bool settles(const char* name, double value, int timeoutMs = 3000) {
    return runUntil([&] { return p->doubleProperty(name) == value; }, timeoutMs);
  }

  double timePos() { return p->doubleProperty("time-pos").value_or(-1); }
};

player::Command cmd(player::CommandAction action, std::uint64_t positionMs = 0, std::string trackId = "",
                    cadence_v2_MediaTrackKind kind = cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_UNSPECIFIED) {
  player::Command c;
  c.action = action;
  c.positionMs = positionMs;
  c.trackId = std::move(trackId);
  c.trackKind = kind;
  return c;
}

bool isRed(test::Rgba c) { return c.r > 200 && c.g < 60 && c.b < 60; }
bool isBlue(test::Rgba c) { return c.b > 200 && c.r < 60 && c.g < 60; }

}  // namespace

TEST_CASE("a session waits paused behind the shut gate, and plays once it opens (rule 7)") {
  Harness h;
  CHECK(h.p->play(h.clip()) == "");
  REQUIRE(h.runUntil([&] { return h.p->firstFrameShown(); }));
  CHECK(h.settles("pause", 1.0));
  CHECK(h.p->state().state == cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED);
  CHECK(h.p->state().gated);
  CHECK(h.p->state().errorCode.empty());
  CHECK(h.settles("volume", 0.0));
  // a resume cannot open it
  CHECK(h.p->command(cmd(player::CommandAction::Resume)) == "gated");
  CHECK(h.settles("pause", 1.0));

  h.p->setGate(true);
  REQUIRE(h.runUntil([&] { return h.p->state().state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING; }));
  CHECK_FALSE(h.p->state().gated);
  CHECK(h.p->state().errorCode.empty());
  CHECK(h.settles("pause", 0.0));

  h.p->setGate(false);
  CHECK(h.settles("pause", 1.0));
  CHECK(h.settles("volume", 0.0));
}

TEST_CASE("a seek reports its target until it lands, never a time-pos from before it, and quick ones add up") {
  Harness h;
  REQUIRE(h.p->play(h.clip()) == "");
  REQUIRE(h.runUntil([&] { return h.p->firstFrameShown(); }));
  const auto before = h.p->positionSample().positionMs;
  player::Command step = cmd(player::CommandAction::Seek);
  step.relative = true;
  step.deltaMs = 400;
  CHECK(h.p->command(step) == "");
  CHECK(h.p->command(step) == "");
  // two presses: one target, 800 ms on (the 2 s clip's end at most), held while mpv seeks
  const auto target = std::min<std::uint64_t>(before + 800, 2000);
  CHECK(h.p->state().seekSeq == 2);
  CHECK(h.p->state().seekPending);
  CHECK(h.p->positionSample().seekPending);
  CHECK(std::llabs(static_cast<long long>(h.p->state().positionMs) - static_cast<long long>(target)) < 40);
  REQUIRE(h.runUntil([&] {
    // while it is under way the position is the target, whatever mpv read before the seek
    if (h.p->state().seekPending) CHECK(std::llabs(static_cast<long long>(h.p->positionSample().positionMs) - static_cast<long long>(target)) < 40);
    return !h.p->state().seekPending;
  }));
  CHECK(std::llabs(static_cast<long long>(h.p->positionSample().positionMs) - static_cast<long long>(target)) < 200);
}

TEST_CASE("the clip decodes into the texture: red first, blue after a seek, and the frame is stable") {
  std::uint64_t firstHash = 0;
  {
    Harness h;
    REQUIRE(h.p->play(h.clip()) == "");
    REQUIRE(h.runUntil([&] { return h.p->firstFrameShown(); }));
    CHECK(h.p->videoInGl());
    auto img = h.frame();
    INFO("centre r=" << pixel(img, 160, 90).r << " g=" << pixel(img, 160, 90).g << " b=" << pixel(img, 160, 90).b);
    CHECK(isRed(pixel(img, 160, 90)));
    CHECK(isRed(pixel(img, 1, 1)));
    firstHash = render::hashImage(img);
    // the same frame read again is the same bytes
    CHECK(render::hashImage(h.frame()) == firstHash);

    CHECK(h.p->command(cmd(player::CommandAction::Seek, 1500)) == "");
    REQUIRE(h.runUntil([&] { return std::fabs(h.timePos() - 1.5) < 0.05 && isBlue(pixel(h.frame(), 160, 90)); }));
    CHECK(h.p->state().positionMs >= 1400);
  }
  // a second player on a fresh context renders the first frame to the same bytes
  {
    Harness h;
    REQUIRE(h.p->play(h.clip("sess-2")) == "");
    REQUIRE(h.runUntil([&] { return h.p->firstFrameShown(); }));
    CHECK(render::hashImage(h.frame()) == firstHash);
  }
}

TEST_CASE("tracks: the clip's video, two audio tracks and a subtitle, selectable") {
  Harness h;
  REQUIRE(h.p->play(h.clip()) == "");
  REQUIRE(h.runUntil([&] { return h.p->state().tracks.size() == 4 && h.p->state().tracks[1].selected; }));
  const auto& t = h.p->state().tracks;
  CHECK(t[0].id == "v1");
  CHECK(t[1].id == "a1");
  CHECK(t[1].language == "en");
  CHECK(t[1].label == "Main");
  CHECK(t[1].selected);
  CHECK(t[2].id == "a2");
  CHECK(t[2].language == "ru");
  CHECK(t[3].id == "s1");
  CHECK(t[3].kind == cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_SUBTITLE);

  CHECK(h.p->command(cmd(player::CommandAction::SelectTrack, 0, "a2")) == "");
  REQUIRE(h.runUntil([&] { return h.p->state().tracks.size() == 4 && h.p->state().tracks[2].selected; }));
  CHECK_FALSE(h.p->state().tracks[1].selected);
  CHECK(h.p->command(cmd(player::CommandAction::SelectTrack, 0, "", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_SUBTITLE)) == "");
  CHECK(h.p->command(cmd(player::CommandAction::SelectTrack, 0, "q7")) == "invalid");
}

TEST_CASE("the stock route: DIRECT, levelled through mpv's volume") {
  Harness h;
  REQUIRE(h.p->play(h.clip()) == "");
  CHECK(h.p->state().audioRoute == cadence_v2_AudioRoute_AUDIO_ROUTE_DIRECT);
  CHECK(h.p->state().audioControls.size() == 4);
  h.p->setGate(true);
  // no level yet: silent
  CHECK(h.settles("volume", 0.0));
  CHECK(h.p->setLevel(40, 0) == "");
  CHECK(h.settles("volume", 40.0));
  CHECK(h.p->setDuck(true, 0.5f, 0) == "");
  CHECK(h.settles("volume", 20.0));
  CHECK(h.p->setDuck(false, 0, 0) == "");
  CHECK(h.p->setMute(true) == "");
  CHECK(h.settles("volume", 0.0));
  CHECK(h.p->setMute(false) == "");
  CHECK(h.settles("volume", 40.0));
  CHECK(h.p->setLevel(101, 0) == "invalid");

  player::EqRequest eq;
  eq.bands = {{cadence_screen_v1_EqType_EQ_TYPE_PEAKING, 1000, 3, 1}};
  CHECK(h.p->setEq(eq) == "");
  CHECK(h.p->setEq(eq) == "");
  CHECK(h.p->setEq(player::EqRequest{"flat", 0, {}}) == "");
  CHECK(h.p->setEq(player::EqRequest{"loud", 0, {}}) == "invalid");
}

TEST_CASE("ambient plays muted with no audio track, gate or not") {
  Harness h;
  auto r = h.clip("amb-1");
  r.purpose = cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_AMBIENT;
  REQUIRE(h.p->play(r) == "");
  REQUIRE(h.runUntil([&] { return h.p->state().state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING; }));
  CHECK(h.p->state().audioRoute == cadence_v2_AudioRoute_AUDIO_ROUTE_NONE);
  CHECK(h.p->state().audioControls.empty());
  CHECK(h.p->state().errorCode.empty());
  CHECK(h.settles("mute", 1.0));
}

TEST_CASE("stop ends the session: idle with its id once, then none") {
  Harness h;
  h.p->setGate(true);
  REQUIRE(h.p->play(h.clip()) == "");
  REQUIRE(h.runUntil([&] { return h.p->firstFrameShown(); }));
  h.states.clear();
  CHECK(h.p->command(cmd(player::CommandAction::Stop)) == "");
  REQUIRE(h.states.size() >= 2);
  CHECK(h.states[h.states.size() - 2].sessionId == "sess-1");
  CHECK(h.states[h.states.size() - 2].state == cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE);
  CHECK(h.states.back().sessionId.empty());
  CHECK(h.p->command(cmd(player::CommandAction::Pause)) == "invalid");
  CHECK_FALSE(h.p->videoTexture());
  // the old file's end-file must not end a session played after it
  REQUIRE(h.p->play(h.clip("sess-3")) == "");
  REQUIRE(h.runUntil([&] { return h.p->state().state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING; }));
  CHECK(h.p->state().sessionId == "sess-3");
}

TEST_CASE("a play replaces the one before without ending the new session") {
  Harness h;
  h.p->setGate(true);
  REQUIRE(h.p->play(h.clip("sess-a")) == "");
  REQUIRE(h.runUntil([&] { return h.p->firstFrameShown(); }));
  REQUIRE(h.p->play(h.clip("sess-b")) == "");
  REQUIRE(h.runUntil([&] { return h.p->state().state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING; }));
  // let the replaced file's end-file arrive
  h.runUntil([] { return false; }, 300);
  CHECK(h.p->state().sessionId == "sess-b");
  CHECK(h.p->state().state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING);
}

TEST_CASE("a file that cannot open ends with a code, and the clip plays to its end") {
  Harness h;
  h.p->setGate(true);
  auto r = h.clip();
  r.url = "/nonexistent/clip.mkv";
  REQUIRE(h.p->play(r) == "");
  REQUIRE(h.runUntil([&] { return !h.states.empty() && h.states.back().sessionId.empty(); }));
  bool failed = false;
  bool endedOnce = false;
  for (const auto& s : h.states) {
    failed = failed || (!s.errorCode.empty() && s.ended);
    endedOnce = endedOnce || s.ended;
  }
  CHECK(failed);
  CHECK(endedOnce);

  h.states.clear();
  REQUIRE(h.p->play(h.clip("sess-end")) == "");
  REQUIRE(h.runUntil([&] { return !h.states.empty() && h.states.back().sessionId.empty(); }, 15000));
  CHECK(h.states[h.states.size() - 2].state == cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE);
  CHECK(h.states[h.states.size() - 2].ended);
  CHECK(h.states[h.states.size() - 2].sessionId == "sess-end");
  CHECK(h.states[h.states.size() - 2].errorCode.empty());
  CHECK_FALSE(h.states.back().ended);
}

TEST_CASE("a playing session's figures: the cache's speed each second, its stats each round, numbers only") {
  Harness h(200);
  std::vector<player::PlayerStats> stats;
  h.p->onStats([&](const player::PlayerStats& s) { stats.push_back(s); });
  h.p->setGate(true);
  CHECK_FALSE(h.p->waiting());
  REQUIRE(h.p->play(h.clip("sess-stats")) == "");
  // loading: it waits for its source until playback starts
  CHECK(h.p->waiting());
  REQUIRE(h.runUntil([&] { return h.p->state().state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING; }));
  CHECK_FALSE(h.p->waiting());
  REQUIRE(h.runUntil([&] { return !stats.empty() && h.p->cacheSpeed().has_value(); }, 5000));
  const auto& s = stats.front();
  CHECK(s.sessionId == "sess-stats");
  // the software decoder this test forces
  CHECK(s.hwdec == "no");
  CHECK(s.estimatedVfFps >= 0);
  CHECK(std::isfinite(s.avsyncMs));
  // a stop leaves no speed and asks for nothing more
  CHECK(h.p->command(cmd(player::CommandAction::Stop)) == "");
  CHECK_FALSE(h.p->cacheSpeed());
  CHECK_FALSE(h.p->waiting());
  const auto before = stats.size();
  h.runUntil([] { return false; }, 600);
  CHECK(stats.size() <= before + 1);
}

TEST_CASE("ambient clips report no figures") {
  Harness h(100);
  int n = 0;
  h.p->onStats([&](const player::PlayerStats&) { n++; });
  auto r = h.clip("amb");
  r.purpose = cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_AMBIENT;
  REQUIRE(h.p->play(r) == "");
  REQUIRE(h.runUntil([&] { return h.p->state().state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING; }));
  h.runUntil([] { return false; }, 500);
  CHECK(n == 0);
  CHECK_FALSE(h.p->cacheSpeed());
}

TEST_CASE("requests the player cannot take") {
  Harness h;
  player::PlayRequest r;
  CHECK(h.p->play(r) == "invalid");
  r = h.clip();
  r.purpose = cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_BOOT;
  CHECK(h.p->play(r) == "unsupported");
  CHECK(h.p->command(cmd(player::CommandAction::Pause)) == "invalid");
}

// media.mpv: a live key's set is answered (mpv refusing it names the key refused), a next-load key rides the file
// and turns applied once it opened, and a new config replaces the one before whole
TEST_CASE("media.mpv: live keys apply at once, next-load keys ride the file, the report says which") {
  Harness h;
  std::vector<player::MpvKeyState> reports;
  h.p->onMpvState([&](const player::MpvKeyState& st) {
    if (reports.empty() || reports.back().applied != st.applied || reports.back().pending != st.pending ||
        reports.back().refused != st.refused) {
      reports.push_back(st);
    }
  });
  // the device's own check: an mpv option name is not a config key, so it is refused without a set
  REQUIRE(h.p->applyMpvConfig("{\"scale\":\"bilinear\",\"deband\":true,\"vo\":\"x11\"}"));
  REQUIRE(h.runUntil([&] { return !reports.empty() && reports.back().applied.size() == 2; }));
  CHECK(reports.back().refused == std::vector<std::string>{"vo"});

  // the next-load options wait for a file; the config before is replaced whole
  REQUIRE(h.p->applyMpvConfig("{\"cacheSecs\":120,\"hwdec\":\"no\",\"audioLanguages\":\"rus,eng\"}"));
  REQUIRE(h.runUntil([&] { return !reports.empty() && reports.back().pending.size() == 3; }));
  CHECK(reports.back().applied.empty());
  CHECK(reports.back().refused.empty());
  REQUIRE(h.p->mpvLoadOptions().size() == 3);

  h.p->setGate(true);
  REQUIRE(h.p->play(h.clip("sess-set")) == "");
  REQUIRE(h.runUntil([&] { return !reports.empty() && reports.back().applied.size() == 3; }));
  CHECK(reports.back().pending.empty());
  CHECK(h.p->state().errorCode.empty());

  // a play after a fresh config carries its next-load options (a key of the one before is gone)
  REQUIRE(h.p->applyMpvConfig("{\"deband\":false,\"videoSync\":\"audio\"}"));
  REQUIRE(h.runUntil([&] { return !reports.empty() && reports.back().applied == std::vector<std::string>{"deband"}; }));
  REQUIRE(h.p->mpvLoadOptions().size() == 1);
}

TEST_CASE("subtitles: mpv draws none of its own, and the line on screen comes back as text for the ui to draw") {
  Harness h;
  std::vector<std::string> lines;
  h.p->onSubtitle([&](const std::string& s) { lines.push_back(s); });
  h.p->setGate(true);
  REQUIRE(h.p->play(h.clip()) == "");
  REQUIRE(h.runUntil([&] { return h.p->state().tracks.size() == 4; }));
  CHECK(h.p->command(cmd(player::CommandAction::SelectTrack, 0, "s1")) == "");
  REQUIRE(h.runUntil([&] { return h.p->subtitleText() == "sub"; }));
  CHECK(lines.back() == "sub");
}

namespace {

std::int64_t monoNs() {
  return std::chrono::duration_cast<std::chrono::nanoseconds>(std::chrono::steady_clock::now().time_since_epoch())
      .count();
}

}  // namespace

// multiroom P4: a follower's play has no audio (the group's session carries it), waits paused for the group's clock,
// then puts its picture where the group is: on a stopped group's position, and running with a playing one
TEST_CASE("a follower's play is silent and follows the group's clock") {
  Harness h;
  auto r = h.clip("sess-f");
  r.followGroupId = "0b6f3a52-6a1e-4d2b-9c51-3c1d2a4e5f60";
  REQUIRE(h.p->play(r) == "");
  CHECK(h.p->following());
  CHECK(h.p->state().audioRoute == cadence_v2_AudioRoute_AUDIO_ROUTE_NONE);
  CHECK_FALSE(h.p->state().gated);
  // no clock yet: it waits paused
  CHECK(h.settles("pause", 1.0));
  // a group stopped at 1.6 s: the picture goes there and waits
  const player::FollowClock stopped{1'600'000'000, monoNs(), 1, false};
  REQUIRE(h.runUntil([&] {
    h.p->groupClock({stopped.mediaNs, monoNs(), stopped.epoch, stopped.playing});
    return std::fabs(h.timePos() - 1.6) < 0.1 && !h.p->state().seekPending;
  }));
  CHECK(h.settles("pause", 1.0));
  // the group plays from 0.2 s: the picture seeks there and runs with it
  const auto start = monoNs();
  const auto groupAt = [&] { return 0.2 + static_cast<double>(monoNs() - start) / 1e9; };
  REQUIRE(h.runUntil([&] {
    h.p->groupClock({static_cast<std::int64_t>(groupAt() * 1e9), monoNs(), 2, true});
    return groupAt() < 1.8 && std::fabs(h.timePos() - groupAt()) < 0.15 && h.p->doubleProperty("pause") == 0.0;
  }));
  // a play of its own is no follower's
  REQUIRE(h.p->play(h.clip("sess-own")) == "");
  CHECK_FALSE(h.p->following());
}
