// the whole screen against a fake cadenced: hello, ready, a play answered and reported, levels, the link going, and
// no url or path in any log line it ships (rule 8). offscreen, ao=null
#include "app/app.h"
#include "support/env.h"
#include "support/fake-cadenced.h"

#include <catch2/catch_test_macros.hpp>

#include <chrono>
#include <functional>
#include <string>
#include <vector>

using namespace cadence::screen;
using test::FakeCadenced;

namespace {

struct Seen {
  std::vector<std::string> logs;
  std::vector<std::pair<std::string, std::string>> results;
  std::vector<player::PlayerState> states;
  int hellos = 0;
};

void record(Seen& seen, const ipc::Envelope& e) {
  switch (e.which_body) {
    case cadence_screen_v1_Envelope_log_line_tag:
      seen.logs.emplace_back(e.body.log_line.text);
      break;
    case cadence_screen_v1_Envelope_command_result_tag:
      seen.results.emplace_back(e.body.command_result.request_id, e.body.command_result.code);
      break;
    case cadence_screen_v1_Envelope_media_state_tag: {
      player::PlayerState s;
      s.sessionId = e.body.media_state.session_id;
      s.state = e.body.media_state.state;
      s.errorCode = e.body.media_state.error_code;
      s.gated = e.body.media_state.gated;
      s.ended = e.body.media_state.ended;
      s.audioRoute = e.body.media_state.audio_route;
      seen.states.push_back(s);
      break;
    }
    case cadence_screen_v1_Envelope_hello_tag:
      seen.hellos++;
      break;
    default:
      break;
  }
}

// steps the app and reads the fake's side until pred holds
bool pump(app::App& a, FakeCadenced& f, Seen& seen, const std::function<bool()>& pred, int timeoutMs = 10000) {
  const auto deadline = std::chrono::steady_clock::now() + std::chrono::milliseconds(timeoutMs);
  while (std::chrono::steady_clock::now() < deadline) {
    a.step(5);
    while (auto e = f.next(0)) record(seen, *e);
    if (pred()) return true;
  }
  return false;
}

bool hasResult(const Seen& s, const std::string& id, const std::string& code) {
  for (const auto& [i, c] : s.results) {
    if (i == id && c == code) return true;
  }
  return false;
}

}  // namespace

TEST_CASE("the screen plays what cadenced asks, behind its gate, and reports it") {
  const std::string clip = test::clip("colors.mkv");
  FakeCadenced fake(test::socketPath("app"));
  app::AppOptions o = app::defaultOptions();
  o.socket = fake.path();
  o.platform.videoDriver = "offscreen";
  o.platform.width = 320;
  o.platform.height = 180;
  o.platform.vsync = false;
  o.ao = "null";
  o.hwdec = "no";
  app::App a(o, nullptr);
  std::string error;
  REQUIRE(a.start(&error));

  REQUIRE(fake.accept(5000));
  Seen seen;
  auto hello = fake.nextOf(cadence_screen_v1_Envelope_hello_tag, 5000);
  REQUIRE(hello);
  CHECK(hello->body.hello.protocol == app::kProtocol);
  CHECK(hello->body.hello.platform == cadence_screen_v1_Platform_PLATFORM_LINUX);
  CHECK(hello->body.hello.display.width == 320);
  CHECK(hello->body.hello.max_video_height == 180);
  // the first frame is shown with no ui (NullUi is ready at once)
  REQUIRE(pump(a, fake, seen, [&] { return a.presented() > 0; }));

  // a play while the gate is shut waits, paused
  {
    ipc::OutEnvelope play(cadence_screen_v1_Envelope_media_play_tag);
    ipc::copyField(play->body.media_play.request_id, "q-1");
    ipc::copyField(play->body.media_play.session_id, "sess-1");
    play->body.media_play.purpose = cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT;
    play->body.media_play.url = play.keep(clip);
    REQUIRE(fake.send(play.get()));
  }
  REQUIRE(pump(a, fake, seen, [&] { return hasResult(seen, "q-1", ""); }));
  REQUIRE(pump(a, fake, seen, [&] {
    return !seen.states.empty() && seen.states.back().state == cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED &&
           seen.states.back().gated && seen.states.back().errorCode.empty();
  }));
  CHECK(seen.states.back().audioRoute == cadence_v2_AudioRoute_AUDIO_ROUTE_DIRECT);

  // ready opens the gate
  {
    ipc::OutEnvelope ready(cadence_screen_v1_Envelope_ready_tag);
    ready->body.ready.gate_open = true;
    REQUIRE(fake.send(ready.get()));
  }
  REQUIRE(pump(a, fake, seen, [&] {
    return seen.states.back().state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING &&
           seen.states.back().sessionId == "sess-1";
  }));
  CHECK(a.gateOpen());
  const auto shown = a.presented();
  REQUIRE(pump(a, fake, seen, [&] { return a.presented() > shown + 5; }));

  // the stock route takes levels; next is the server's
  {
    ipc::OutEnvelope level(cadence_screen_v1_Envelope_set_level_tag);
    ipc::copyField(level->body.set_level.request_id, "q-2");
    level->body.set_level.level = 30;
    REQUIRE(fake.send(level.get()));
    ipc::OutEnvelope next(cadence_screen_v1_Envelope_media_command_tag);
    ipc::copyField(next->body.media_command.request_id, "q-3");
    next->body.media_command.action = cadence_screen_v1_MediaAction_MEDIA_ACTION_NEXT;
    REQUIRE(fake.send(next.get()));
    ipc::OutEnvelope pause(cadence_screen_v1_Envelope_media_command_tag);
    ipc::copyField(pause->body.media_command.request_id, "q-4");
    pause->body.media_command.action = cadence_screen_v1_MediaAction_MEDIA_ACTION_PAUSE;
    REQUIRE(fake.send(pause.get()));
  }
  REQUIRE(pump(a, fake, seen, [&] {
    return hasResult(seen, "q-2", "") && hasResult(seen, "q-3", "unsupported") && hasResult(seen, "q-4", "");
  }));
  REQUIRE(a.mpv());
  CHECK(pump(a, fake, seen, [&] { return a.mpv()->doubleProperty("volume") == 30.0; }));
  REQUIRE(pump(a, fake, seen,
               [&] { return seen.states.back().state == cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED; }));

  // display.screen's overscan reaches the geometry without a restart
  {
    ipc::OutEnvelope config(cadence_screen_v1_Envelope_capability_config_tag);
    ipc::copyField(config->body.capability_config.capability, "display.screen");
    config->body.capability_config.json = config.keep(R"({"overscan":5})");
    REQUIRE(fake.send(config.get()));
  }

  // cadenced goes: nobody holds the level any more, so the gate shuts (rule 7); it comes back with a new hello
  fake.drop();
  REQUIRE(pump(a, fake, seen, [&] { return !a.gateOpen(); }));
  REQUIRE(fake.accept(5000));
  REQUIRE(fake.nextOf(cadence_screen_v1_Envelope_hello_tag, 5000));
  REQUIRE(pump(a, fake, seen, [&] { return !seen.states.empty() && seen.states.back().sessionId == "sess-1"; }));

  a.shutdown();

  // rule 8: the clip's path never reached a shipped log line
  REQUIRE_FALSE(seen.logs.empty());
  bool loadLogged = false;
  for (const auto& line : seen.logs) {
    INFO(line);
    CHECK(line.find("colors") == std::string::npos);
    CHECK(line.find("/w/") == std::string::npos);
    CHECK(line.find("://") == std::string::npos);
    loadLogged = loadLogged || line.rfind("player_load ", 0) == 0;
  }
  CHECK(loadLogged);
}

namespace {

// a ui that animates for a number of updates once kicked, with an update due when asked
struct AnimUi final : app::UiLayer {
  int remaining = 0;
  bool isDirty = false;
  std::int64_t dueMs = -1;
  int renders = 0;

  void kick(int frames) {
    remaining = frames;
    isDirty = true;
  }
  void attach(render::RenderBackend&, const app::UiGeometry&, app::Outbox&) override {}
  void detach() override {}
  void resize(const app::UiGeometry&) override {}
  bool key(const platform::KeyEvent&) override { return false; }
  void message(const cadence_screen_v1_Envelope&) override {}
  void playerState(const player::PlayerState&) override {}
  void update(std::int64_t) override {
    if (remaining > 0) {
      remaining--;
      isDirty = true;
    }
  }
  [[nodiscard]] bool dirty() const override { return isDirty; }
  [[nodiscard]] std::int64_t nextUpdateMs(std::int64_t) const override { return dueMs; }
  [[nodiscard]] bool ready() const override { return true; }
  void render(render::RenderBackend&) override {
    renders++;
    isDirty = false;
  }
  [[nodiscard]] app::UiStatus status() const override { return {}; }
  [[nodiscard]] std::vector<std::uint32_t> cardVersions() const override { return {}; }
};

}  // namespace

TEST_CASE("an idle screen sleeps in its wait; an animating ui is drawn turn after turn") {
  // regression (the stick): the wait was taken before the ui's update, so a transition showed its first frame and
  // then waited out the loop's cap, and an idle screen's wait is what keeps SDLThread off the cpu
  FakeCadenced fake(test::socketPath("app-idle"));
  app::AppOptions o = app::defaultOptions();
  o.socket = fake.path();
  o.platform.videoDriver = "offscreen";
  o.platform.width = 320;
  o.platform.height = 180;
  o.platform.vsync = false;
  o.ao = "null";
  o.hwdec = "no";
  auto owned = std::make_unique<AnimUi>();
  AnimUi* ui = owned.get();
  app::App a(o, std::move(owned));
  std::string error;
  REQUIRE(a.start(&error));
  REQUIRE(fake.accept(5000));
  Seen seen;
  REQUIRE(pump(a, fake, seen, [&] { return a.presented() > 0; }));

  // idle: nothing owed, nothing due, so the whole cap
  for (int i = 0; i < 3; i++) a.step(30);
  CHECK(a.lastWaitMs() == 30);
  const auto idle = a.presented();
  a.step(30);
  CHECK(a.presented() == idle);
  // an update due sooner than the cap (a timer, a spinner's frame): that long
  ui->dueMs = 7;
  a.step(30);
  CHECK(a.lastWaitMs() == 7);
  ui->dueMs = -1;

  // an animation of 6 frames: every turn draws one, none sleeps
  ui->kick(6);
  std::vector<int> waits;
  const auto before = a.presented();
  for (int i = 0; i < 20 && a.presented() < before + 6; i++) {
    a.step(30);
    waits.push_back(a.lastWaitMs());
  }
  CHECK(a.presented() == before + 6);
  for (const int w : waits) CHECK(w == 0);
  // one turn finds nothing new, then the loop sleeps again
  a.step(30);
  a.step(30);
  CHECK(a.lastWaitMs() == 30);
  CHECK(a.presented() == before + 6);
}
