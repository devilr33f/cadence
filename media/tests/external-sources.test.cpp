#include "audiod/external-sources.h"
#include "ring/ring-input.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/eventually.h"
#include "support/fake-media-source.h"
#include "support/media-client.h"
#include "support/playback-client.h"
#include "support/signals.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/matchers/catch_matchers_floating_point.hpp>

#include <algorithm>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <initializer_list>
#include <memory>
#include <span>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;
using Catch::Matchers::WithinAbs;

namespace {

constexpr auto kVideo = cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_VIDEO;
constexpr auto kMixer = cadence_audiod_v1_AudioRoute_AUDIO_ROUTE_MIXER;
constexpr auto kDirect = cadence_audiod_v1_AudioRoute_AUDIO_ROUTE_DIRECT;
constexpr auto kDuck = cadence_audiod_v1_AudioControl_AUDIO_CONTROL_DUCK;
constexpr auto kVolume = cadence_audiod_v1_AudioControl_AUDIO_CONTROL_VOLUME;
constexpr auto kMute = cadence_audiod_v1_AudioControl_AUDIO_CONTROL_MUTE;
constexpr double kFullScale = 32768;

Envelope external(std::string_view id, cadence_audiod_v1_PlaybackState state, cadence_audiod_v1_AudioRoute route,
                  std::initializer_list<cadence_audiod_v1_AudioControl> controls = {kVolume, kDuck, kMute}) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_external_source_tag;
  auto& body = envelope.body.external_source;
  copyText(body.id, id);
  body.op = cadence_audiod_v1_ExternalOp_EXTERNAL_OP_UPDATE;
  body.kind = kVideo;
  body.state = state;
  body.audio_route = route;
  for (const auto control : controls)
    body.audio_controls[body.audio_controls_count++] = control;
  return envelope;
}

Envelope unregister(std::string_view id) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_external_source_tag;
  copyText(envelope.body.external_source.id, id);
  envelope.body.external_source.op = cadence_audiod_v1_ExternalOp_EXTERNAL_OP_UNREGISTER;
  return envelope;
}

Envelope duck(std::string_view requestId, std::string_view sessionId, bool active, float level, std::uint32_t rampMs = 0) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_session_control_tag;
  auto& body = envelope.body.session_control;
  copyText(body.request_id, requestId);
  copyText(body.session_id, sessionId);
  body.which_control = cadence_audiod_v1_SessionControl_duck_tag;
  body.control.duck.active = active;
  body.control.duck.level = level;
  body.control.duck.ramp_ms = rampMs;
  body.control.duck.reason = cadence_audiod_v1_DuckReason_DUCK_REASON_ASSISTANT;
  return envelope;
}

Envelope eq(std::string_view requestId, std::string_view sessionId) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_session_control_tag;
  auto& body = envelope.body.session_control;
  copyText(body.request_id, requestId);
  copyText(body.session_id, sessionId);
  body.which_control = cadence_audiod_v1_SessionControl_set_eq_tag;
  body.control.set_eq.has_eq = true;
  return envelope;
}

std::vector<cadence_audiod_v1_ExternalAction> actionsIn(const Frames& frames) {
  std::vector<cadence_audiod_v1_ExternalAction> actions;
  for (const auto& envelope : frames.others)
    if (envelope.which_body == cadence_audiod_v1_Envelope_external_action_tag)
      actions.push_back(envelope.body.external_action);
  return actions;
}

std::vector<cadence_audiod_v1_RouteControl> routesIn(const Frames& frames) {
  std::vector<cadence_audiod_v1_RouteControl> routes;
  for (const auto& envelope : frames.others)
    if (envelope.which_body == cadence_audiod_v1_Envelope_route_control_tag)
      routes.push_back(envelope.body.route_control);
  return routes;
}

std::vector<cadence_audiod_v1_AudioControl> controlsOf(const MediaStateFrame& state) {
  return {state.audio_controls, state.audio_controls + state.audio_controls_count};
}

// audiod on the file backend with a hardware volume control, fake url sources, and rings when asked for
class Rig {
 public:
  explicit Rig(bool rings = false)
      : volume_(std::make_shared<FakeVolume>()),
        audiod_(compiledBackends(), {}, withFakeControl(volume_), openCapture, media(rings)) {}

  [[nodiscard]] AudiodHarness& audiod() { return audiod_; }
  [[nodiscard]] FakeMedia& fakes() { return fakes_; }
  [[nodiscard]] std::string out() const { return audiod_.file("out"); }

  // configured; the gate opens once the level held
  UniqueFd connect(bool open = true) {
    auto client = connectConfigured(audiod_, out(), "Master");
    if (open)
      expectApplied(client.get(), 40, true);
    untilPong(client.get());
    return client;
  }

  // the ring of a registered session, mapped as its player maps it
  std::unique_ptr<RingWriter> writer(std::string_view id) {
    return audiod_.onLoop([&](IpcServer&, Daemon&) -> std::unique_ptr<RingWriter> {
      auto* ring = audiod_.media().externals().ring(id);
      return ring == nullptr ? nullptr : RingWriter::attach(ring->ringFd());
    });
  }

 private:
  HarnessMedia media(bool rings) {
    HarnessMedia media;
    media.factory = fakes_.factory();
    if (rings)
      media.rings = sizedRing;
    return media;
  }

  FakeMedia fakes_;
  std::shared_ptr<FakeVolume> volume_;
  AudiodHarness audiod_;
};

}  // namespace

TEST_CASE("an external session takes the speaker under cadenced's id, and a url displaces it") {
  Rig rig;
  auto client = rig.connect();

  sendFrame(client.get(), external("video-1", kPlaying, kDirect));
  auto frames = untilPong(client.get());
  auto state = onlyState(frames);
  CHECK(std::string_view(state.session_id) == "video-1");
  CHECK(std::string_view(state.external_id) == "video-1");
  CHECK(state.source == kVideo);
  CHECK(state.state == kPlaying);
  CHECK(state.audio_route == kDirect);
  CHECK(controlsOf(state) == std::vector{kVolume, kDuck, kMute});
  CHECK(actionsIn(frames).empty());

  // the last to play wins: the player is asked to pause, and the url holds the speaker
  sendFrame(client.get(), urlPlay("play-a", "http://fake/a"));
  untilPong(client.get());
  REQUIRE(rig.audiod().onLoop([&](IpcServer&, Daemon&) {
    auto* source = rig.fakes().source("a");
    if (source != nullptr)
      source->events().playing();
    return source != nullptr;
  }));
  frames = untilPong(client.get());
  const auto actions = actionsIn(frames);
  REQUIRE(actions.size() == 1);
  CHECK(std::string_view(actions[0].id) == "video-1");
  CHECK(actions[0].action == kPause);
  CHECK(std::string_view(actions[0].reason) == "displaced");
  state = frames.states.back();
  CHECK(std::string_view(state.session_id) == "ms-1");
  CHECK(std::string_view(state.external_id).empty());
  CHECK(state.audio_route == kMixer);

  // its paused state changes nothing; playing again takes the speaker back from the url, which cannot wait paused
  sendFrame(client.get(), external("video-1", kPaused, kDirect));
  CHECK(untilPong(client.get()).states.empty());
  sendFrame(client.get(), external("video-1", kPlaying, kDirect));
  frames = untilPong(client.get());
  CHECK(std::string_view(frames.states.back().session_id) == "video-1");
  CHECK(actionsIn(frames).empty());
}

// rule 7: no player plays before the persisted level held
TEST_CASE("a shut gate pauses an external session that plays") {
  Rig rig;
  auto client = rig.connect(false);
  sendFrame(client.get(), external("video-1", kPlaying, kDirect));
  auto frames = untilPong(client.get());
  const auto actions = actionsIn(frames);
  REQUIRE(actions.size() == 1);
  CHECK(actions[0].action == kPause);
  CHECK(std::string_view(actions[0].reason) == "gated");
  CHECK(frames.states.empty());

  // once the level held, its next playing takes the speaker; a failed level pauses it again
  expectApplied(client.get(), 40, true);
  untilPong(client.get());
  sendFrame(client.get(), external("video-1", kPaused, kDirect));
  sendFrame(client.get(), external("video-1", kPlaying, kDirect));
  frames = untilPong(client.get());
  CHECK(std::string_view(frames.states.back().session_id) == "video-1");
  sendFrame(client.get(), volumeSet(101));
  frames = untilPong(client.get());
  const auto paused = actionsIn(frames);
  REQUIRE(paused.size() == 1);
  CHECK(std::string_view(paused[0].reason) == "gated");
}

TEST_CASE("a seek of an external session travels to its player with the position") {
  Rig rig;
  auto client = rig.connect();
  sendFrame(client.get(), external("video-1", kPlaying, kDirect));
  untilPong(client.get());
  const auto frames = answered(client.get(),
                               mediaCommand("s1", cadence_audiod_v1_MediaAction_MEDIA_ACTION_SEEK, 754'000), "s1", "");
  const auto actions = actionsIn(frames);
  REQUIRE(actions.size() == 1);
  CHECK(std::string_view(actions[0].id) == "video-1");
  CHECK(actions[0].action == cadence_audiod_v1_MediaAction_MEDIA_ACTION_SEEK);
  CHECK(actions[0].position_ms == 754'000);
  CHECK(std::string_view(actions[0].reason).empty());
}

TEST_CASE("unregistering an external session ends it, and cadenced going away removes them all") {
  Rig rig;
  auto client = rig.connect();
  sendFrame(client.get(), external("video-1", kPlaying, kDirect));
  untilPong(client.get());
  sendFrame(client.get(), unregister("video-1"));
  expectIdle(onlyState(untilPong(client.get())));

  sendFrame(client.get(), external("video-2", kPlaying, kDirect));
  untilPong(client.get());
  disconnect(rig.audiod(), client);
  auto again = rig.connect();
  sendFrame(again.get(), external("video-2", kPaused, kDirect));
  // a new registration under the old id, paused: no session
  CHECK(untilPong(again.get()).states.empty());
}

TEST_CASE("an idle state ends the external session") {
  Rig rig;
  auto client = rig.connect();
  sendFrame(client.get(), external("video-1", kPlaying, kDirect));
  untilPong(client.get());
  sendFrame(client.get(), external("video-1", kIdle, kDirect));
  expectIdle(onlyState(untilPong(client.get())));
}

TEST_CASE("a control routes by the session's route, and one the route lacks is unsupported") {
  Rig rig;
  auto client = rig.connect();
  sendFrame(client.get(), external("video-1", kPlaying, kDirect, {kVolume, kDuck}));
  untilPong(client.get());

  // the whole mix's duck applies in the mixer, and reaches the direct player that holds the speaker
  auto frames = answered(client.get(), duck("d1", "", true, 0.2f, 300), "d1", "");
  auto routes = routesIn(frames);
  REQUIRE(routes.size() == 1);
  CHECK(std::string_view(routes[0].session_id) == "video-1");
  CHECK(routes[0].which_control == cadence_audiod_v1_RouteControl_duck_tag);
  CHECK_THAT(routes[0].control.duck.level, WithinAbs(0.2, 1e-6));
  CHECK(routes[0].control.duck.ramp_ms == 300);

  // rule 7: a duck's level above 1 never raises it
  frames = answered(client.get(), duck("d2", "video-1", true, 3.0f), "d2", "");
  routes = routesIn(frames);
  REQUIRE(routes.size() == 1);
  CHECK(routes[0].control.duck.level == 1.0f);

  // the player listed no eq
  frames = answered(client.get(), eq("e1", "video-1"), "e1", "unsupported");
  CHECK(routesIn(frames).empty());
  answered(client.get(), duck("d3", "nobody", true, 0.2f), "d3", "no_session");

  // a mixer session without a ring has nothing to apply a control to
  sendFrame(client.get(), external("video-2", kPaused, kMixer));
  untilPong(client.get());
  answered(client.get(), duck("d4", "video-2", true, 0.2f), "d4", "unsupported");
  // the mix's eq is the mixer's: no route back for a player that listed none
  frames = answered(client.get(), eq("e2", ""), "e2", "");
  CHECK(routesIn(frames).empty());
}

// route c: a mixer session's ring is heard through the backend while it holds the speaker, and not after
TEST_CASE("a mixer session is heard through its ring while it holds the speaker") {
  Rig rig(true);
  auto client = rig.connect();
  sendFrame(client.get(), external("video-1", kPaused, kMixer));
  untilPong(client.get());
  auto writer = rig.writer("video-1");
  REQUIRE(writer);
  writer->setState(CADENCE_RING_STREAMING);

  // not heard before it holds the speaker: the ring is not read at all
  const auto tone = sine(1000, 0.25, 48 * 100, 2);
  REQUIRE(writer->write(tone) == 48 * 100);
  std::this_thread::sleep_for(150ms);
  std::uint64_t pos = 0;
  std::int64_t heard = 0;
  CHECK_FALSE(writer->timestamp(pos, heard));

  sendFrame(client.get(), external("video-1", kPlaying, kMixer));
  untilPong(client.get());
  // 1 s more, written as the reader makes room, as ao_cadence pulls
  const auto second = sine(1000, 0.25, 48 * 1000, 2);
  std::size_t written = 0;
  REQUIRE(eventually([&] {
    written += writer->write(std::span<const float>(second).subspan(written * 2));
    return written == second.size() / 2;
  }));
  REQUIRE(eventually([&] { return writer->timestamp(pos, heard) && pos >= 48 * 1000; }));

  // its own duck lowers the ring alone; the device's level is not its own
  answered(client.get(), duck("d1", "video-1", true, 0.5f), "d1", "");
  disconnect(rig.audiod(), client);

  const auto mix = firstChannel(readWav(rig.out() + "/mix/mix-000.wav"));
  REQUIRE(mix.size() > 48 * 600);
  // the tone at its level, after the look-ahead and the first period
  CHECK(amplitudeAt(mix, 1, 0, 48 * 200, 960, 1000) / kFullScale > 0.2);
}

// multiroom P4: the player's own position rides its state into the session, so a group leader's epoch of its ring
// starts where the screen plays; a state that names none leaves the position alone
TEST_CASE("an external session's position is the one its player says") {
  Rig rig;
  auto client = rig.connect();
  auto playing = external("video-1", kPlaying, kMixer);
  playing.body.external_source.position_ms = 61000;
  playing.body.external_source.position_at_ms = 1;
  sendFrame(client.get(), playing);
  auto frames = untilPong(client.get());
  REQUIRE_FALSE(frames.states.empty());
  CHECK(frames.states.back().position_ms >= 61000);
  CHECK(frames.states.back().position_ms < 62000);
  // a seek of its own: the state's new position
  playing.body.external_source.position_ms = 300000;
  sendFrame(client.get(), playing);
  frames = untilPong(client.get());
  REQUIRE_FALSE(frames.states.empty());
  CHECK(frames.states.back().position_ms >= 300000);
  CHECK(frames.states.back().position_ms < 301000);
}
