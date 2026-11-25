#include "audiod/airplay-handlers.h"
#include "audiod/airplay-source.h"
#include "audiod/airplay-volume.h"
#include "support/airplay-rig.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/eventually.h"
#include "support/fake-media-source.h"
#include "support/fake-shairport.h"
#include "support/fifo-writer.h"
#include "support/media-client.h"
#include "support/playback-client.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <chrono>
#include <cstdint>
#include <fstream>
#include <functional>
#include <memory>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

// the airplay receiver as the media session's source, end to end over audiod's socket: the supervisor over a runtime
// of fakes and the builder's real dbus-daemon, shairport's d-bus service stood in for by the test (fake-shairport.h)
// on the receiver's own bus. no test ever runs the real receiver or casts
using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

constexpr auto kAirplayKind = cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_AIRPLAY;
constexpr auto kFailed = cadence_audiod_v1_AirplayState_AIRPLAY_STATE_FAILED;
constexpr auto kPauseReflection = 500ms;

Envelope senderVolumeSet(std::uint32_t level,
                         cadence_audiod_v1_Sender sender = cadence_audiod_v1_Sender_SENDER_AIRPLAY) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_sender_volume_set_tag;
  envelope.body.sender_volume_set.sender = sender;
  envelope.body.sender_volume_set.level = level;
  return envelope;
}

bool is(const Envelope& envelope, pb_size_t body) { return envelope.which_body == body; }

// what came within limit
std::vector<Envelope> within(int fd, std::chrono::milliseconds limit) {
  std::vector<Envelope> frames;
  const auto deadline = std::chrono::steady_clock::now() + limit;
  while (std::chrono::steady_clock::now() < deadline) {
    const auto received = receive(fd, 20ms);
    if (received.kind == Received::Kind::frame)
      frames.push_back(received.envelope);
    if (received.kind == Received::Kind::closed)
      break;
  }
  return frames;
}

// every frame until one matches, that one last
std::vector<Envelope> until(int fd, const std::function<bool(const Envelope&)>& matches,
                            std::chrono::milliseconds limit = std::chrono::seconds(10)) {
  std::vector<Envelope> frames;
  const auto deadline = std::chrono::steady_clock::now() + limit;
  while (std::chrono::steady_clock::now() < deadline) {
    const auto received = receive(fd, 50ms);
    if (received.kind == Received::Kind::closed)
      break;
    if (received.kind != Received::Kind::frame)
      continue;
    frames.push_back(received.envelope);
    if (matches(received.envelope))
      return frames;
  }
  FAIL("the frame never came");
  return frames;
}

MediaStateFrame untilState(int fd, const std::function<bool(const MediaStateFrame&)>& matches) {
  return until(fd, [&](const Envelope& envelope) {
           return is(envelope, cadence_audiod_v1_Envelope_media_state_tag) && matches(envelope.body.media_state);
         })
      .back()
      .body.media_state;
}

bool airplayPlaying(const MediaStateFrame& state) { return state.source == kAirplayKind && state.state == kPlaying; }

// the levels of the SenderVolume requests among frames, each for airplay
std::vector<std::uint32_t> senderVolumes(const std::vector<Envelope>& frames) {
  std::vector<std::uint32_t> levels;
  for (const auto& envelope : frames)
    if (is(envelope, cadence_audiod_v1_Envelope_sender_volume_tag)) {
      CHECK(envelope.body.sender_volume.sender == cadence_audiod_v1_Sender_SENDER_AIRPLAY);
      levels.push_back(envelope.body.sender_volume.level);
    }
  return levels;
}

bool noSession(const MediaStateFrame& state) { return std::string_view(state.session_id).empty(); }

// a session's state names a title (has_metadata holds for any session)
bool titled(const MediaStateFrame& state) { return !std::string_view(state.metadata.title).empty(); }

// an airplay session that plays among frames
bool playedAmong(const std::vector<Envelope>& frames) {
  return std::any_of(frames.begin(), frames.end(), [](const Envelope& envelope) {
    return is(envelope, cadence_audiod_v1_Envelope_media_state_tag) && airplayPlaying(envelope.body.media_state);
  });
}

using Matches = std::function<bool(const Envelope&)>;

Matches stateWhere(std::function<bool(const MediaStateFrame&)> matches) {
  return [matches = std::move(matches)](const Envelope& envelope) {
    return is(envelope, cadence_audiod_v1_Envelope_media_state_tag) && matches(envelope.body.media_state);
  };
}

Matches resultOf(std::string requestId, std::string_view code = {}) {
  return [requestId = std::move(requestId), code = std::string(code)](const Envelope& envelope) {
    return is(envelope, cadence_audiod_v1_Envelope_command_result_tag) &&
           envelope.body.command_result.request_id == requestId && envelope.body.command_result.code == code;
  };
}

Matches statusWhere(cadence_audiod_v1_AirplayState state, std::string code) {
  return [state, code = std::move(code)](const Envelope& envelope) {
    return is(envelope, cadence_audiod_v1_Envelope_airplay_status_tag) && envelope.body.airplay_status.state == state &&
           envelope.body.airplay_status.code == code;
  };
}

// every frame until each condition matched one of them, in whatever order they came
std::vector<Envelope> untilEach(int fd, std::vector<Matches> conditions) {
  std::vector<Envelope> frames;
  std::vector<bool> met(conditions.size(), false);
  until(fd, [&](const Envelope& envelope) {
    frames.push_back(envelope);
    for (std::size_t i = 0; i < conditions.size(); ++i)
      met[i] = met[i] || conditions[i](envelope);
    return std::all_of(met.begin(), met.end(), [](bool done) { return done; });
  });
  return frames;
}

// an item of shairport's metadata pipe, its data already base64
std::string item(std::string_view type, std::string_view code, std::size_t length = 0, std::string_view base64 = {}) {
  auto text = "<item><type>" + std::string(type) + "</type><code>" + std::string(code) + "</code><length>" +
              std::to_string(length) + "</length>";
  if (length == 0)
    return text + "</item>\n";
  return text + "\n<data encoding=\"base64\">\n" + std::string(base64) + "</data></item>\n";
}

// "Night Drive" by "The Hum" on "Tapes", as a sequence
std::string track() {
  return item("73736e63", "6d647374") + item("636f7265", "6d696e6d", 11, "TmlnaHQgRHJpdmU=") +
         item("636f7265", "61736172", 7, "VGhlIEh1bQ==") + item("636f7265", "6173616c", 5, "VGFwZXM=") +
         item("73736e63", "6d64656e");
}

AirplayControlTimings quickControl() {
  AirplayControlTimings timings;
  timings.connectWait = 1500ms;
  timings.call = 1000ms;
  timings.session.pauseReflection = kPauseReflection;
  return timings;
}

// what the runtime runs as shairport: a fake process that never takes its name, which the test's stand-in takes on the
// receiver's bus, or the e2e's fake shairport (support/fake-shairport.cpp), which plays its own cast
enum class Receiver : std::uint8_t { StandIn, E2e };

// audiod with the airplay part over the runtime of fakes, fake url sources, the file backend and a hardware volume
// control, so the music plays at unity once the gate opened
class Rig {
 public:
  explicit Rig(Receiver receiver = Receiver::StandIn)
      : volume_(std::make_shared<FakeVolume>()),
        audiod_(compiledBackends(), {}, withFakeControl(volume_), openCapture, harnessMedia()) {
    if (receiver == Receiver::E2e)
      runtime_.replace("shairport-sync", CADENCE_FAKE_SHAIRPORT);
    else
      runtime_.mode("shairport-sync", "silent");
  }

  [[nodiscard]] AudiodHarness& audiod() { return audiod_; }
  [[nodiscard]] FakeMedia& media() { return fakes_; }
  [[nodiscard]] const AirplayRuntime& runtime() const { return runtime_; }
  [[nodiscard]] PcmInput& music() { return audiod_.playback().music(); }
  [[nodiscard]] std::string audioFifo() const { return runtime_.scratch() + "/run/audio.fifo"; }
  [[nodiscard]] std::string metadataFifo() const { return runtime_.scratch() + "/run/metadata.fifo"; }

  [[nodiscard]] Envelope config() const {
    Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
    envelope.which_body = cadence_audiod_v1_Envelope_airplay_config_tag;
    auto& body = envelope.body.airplay_config;
    body.enabled = true;
    copyText(body.name, "Kitchen");
    copyText(body.dir, runtime_.dir());
    copyText(body.scratch_dir, runtime_.scratch());
    return envelope;
  }

  // configured, the gate open and the receiver starting; the speaker's level for airplay when level is set
  UniqueFd connect(std::optional<std::uint32_t> level = 30) {
    auto client = connectConfigured(audiod_, audiod_.file("out"), "Master");
    if (level)
      sendFrame(client.get(), senderVolumeSet(*level));
    sendFrame(client.get(), config());
    sendFrame(client.get(), volumeSet(40));
    return client;
  }

  // shairport's stand-in, once the receiver started shairport for the starts-th time
  std::unique_ptr<FakeShairport> receiver(FakeShairport::Properties properties, std::size_t starts = 1) {
    REQUIRE(eventually(
        [&] {
          const auto all = runtime_.starts();
          return static_cast<std::size_t>(std::count_if(all.begin(), all.end(), [](const auto& start) {
                   return start.first == "shairport-sync";
                 })) >= starts;
        },
        10s));
    return std::make_unique<FakeShairport>("unix:path=" + runtime_.scratch() + "/bus.sock", std::move(properties));
  }

  // the receiver connected to the stand-in
  void connected(std::size_t times = 1) {
    REQUIRE(eventually([&] { return audiod_.logs().count(event("airplay_control_ready")) >= times; }, 10s));
  }

  // what the e2e's fake shairport was called with, in order
  [[nodiscard]] std::vector<std::string> e2eCalls() const {
    std::vector<std::string> calls;
    std::ifstream in(runtime_.scratch() + "/fake-shairport.log");
    for (std::string line; std::getline(in, line);)
      calls.push_back(line);
    return calls;
  }

 private:
  HarnessMedia harnessMedia() {
    HarnessMedia media{fakes_.factory()};
    media.airplay = runtime_.options();
    media.airplayControl = quickControl();
    return media;
  }

  AirplayRuntime runtime_;
  FakeMedia fakes_;
  std::shared_ptr<FakeVolume> volume_;
  AudiodHarness audiod_;
};

FakeShairport::Properties playing(std::string client = "phone-a") {
  return {.protocol = "AirPlay 2", .state = "Playing", .volume = -15, .client = std::move(client)};
}

}  // namespace

TEST_CASE("a cast takes the session only once the speaker's level is known, and only then is it heard") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  auto client = rig.connect(std::nullopt);
  const int fd = client.get();
  const auto fake = rig.receiver(playing());
  rig.connected();
  const auto writer = fifoWriter(rig.audioFifo());
  writeFrames(writer.get(), 4800);
  CHECK_FALSE(playedAmong(within(fd, 300ms)));
  CHECK(rig.music().status().framesIn == 0);

  sendFrame(fd, senderVolumeSet(30));
  const auto state = untilState(fd, airplayPlaying);
  CHECK(state.actions_count == 4);
  writeFrames(writer.get(), 4800);
  CHECK(eventually([&] { return rig.music().status().framesIn >= 4800; }));
}

TEST_CASE("a change the receiver makes while it answers a read is not lost") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  auto client = rig.connect();
  auto properties = playing();
  properties.state = "Paused";
  properties.racingState = "Playing";
  const auto fake = rig.receiver(properties);
  untilState(client.get(), airplayPlaying);
  // it came between the match and the read's answer, and was held for after it
  CHECK(rig.audiod().logs().count("\"held\":1") == 1);
}

TEST_CASE("a receiver of another protocol is refused and never plays") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  auto client = rig.connect();
  auto properties = playing();
  properties.protocol = "AirPlay 1";
  const auto fake = rig.receiver(properties);
  const auto frames = until(client.get(), [](const Envelope& envelope) {
    return is(envelope, cadence_audiod_v1_Envelope_airplay_status_tag) &&
           envelope.body.airplay_status.state == kFailed;
  });
  CHECK(std::string_view(frames.back().body.airplay_status.code) == "wrong_protocol");
  CHECK_FALSE(playedAmong(frames));
  CHECK(rig.audiod().logs().count(event("airplay_control_ready")) == 0);
}

TEST_CASE("a PropertiesChanged any other bus peer sends is ignored") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  auto client = rig.connect();
  auto properties = playing();
  properties.state = "Paused";
  const auto fake = rig.receiver(properties);
  rig.connected();

  // a peer of the bus says shairport plays, straight to every connection there
  auto forger = dbus::Client::connect("unix:path=" + rig.runtime().scratch() + "/bus.sock");
  REQUIRE(forger.ok());
  const auto names = forger.value().callAndWait(dbus::listNamesCall());
  REQUIRE(names.status == dbus::CallStatus::Ok);
  std::size_t sent = 0;
  for (const auto& name : names.body.front().items()) {
    const auto& text = *name.asString();
    if (!text.starts_with(':') || text == forger.value().uniqueName() || text == fake->uniqueName())
      continue;
    auto signal = dbus::Message::signal(
        "/org/gnome/ShairportSync", std::string(dbus::kPropertiesInterface), "PropertiesChanged",
        {dbus::Value::string("org.gnome.ShairportSync.RemoteControl"),
         dbus::Value::propertyMap({{"PlayerState", dbus::Value::string("Playing")}}), dbus::Value::array("s", {})});
    signal.destination = text;
    REQUIRE(forger.value().send(std::move(signal)));
    ++sent;
  }
  REQUIRE(sent > 0);
  CHECK_FALSE(playedAmong(within(client.get(), 400ms)));

  fake->state("Playing");
  untilState(client.get(), airplayPlaying);
}

TEST_CASE("the receiver's name going ends the session once, and the restarted receiver plays at the cached level") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();
  auto fake = rig.receiver(playing());
  untilState(fd, airplayPlaying);

  fake->releaseName();
  const auto frames = untilEach(fd, {statusWhere(kFailed, "session_lost"), stateWhere(noSession)});
  CHECK(std::count_if(frames.begin(), frames.end(), stateWhere(noSession)) == 1);
  fake.reset();

  // no other level comes from cadenced
  const auto next = rig.receiver(playing("phone-b"), 2);
  untilState(fd, airplayPlaying);
}

TEST_CASE("a pause is silent before the receiver answers, and a receiver that plays on is dropped") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();
  const auto fake = rig.receiver(playing());
  untilState(fd, airplayPlaying);
  const auto writer = fifoWriter(rig.audioFifo());
  writeFrames(writer.get(), 4800);
  REQUIRE(eventually([&] { return rig.music().status().framesIn >= 4800; }));

  sendFrame(fd, mediaCommand("p1", kPause));
  untilEach(fd, {resultOf("p1")});
  const auto in = rig.music().status().framesIn;
  writeFrames(writer.get(), 4800);
  std::this_thread::sleep_for(100ms);
  CHECK(rig.music().status().framesIn == in);
  CHECK(fake->waitCall("Pause"));

  // it ignored the pause: its session is dropped once the watchdog fires
  CHECK(fake->waitCall("DropSession"));
  untilState(fd, noSession);
  CHECK(rig.music().status().framesIn == in);
}

TEST_CASE("a pause the receiver reflects keeps its session, and a resume plays once Play answered") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();
  const auto fake = rig.receiver(playing());
  fake->honoursControls();
  untilState(fd, airplayPlaying);

  sendFrame(fd, mediaCommand("p1", kPause));
  untilEach(fd, {resultOf("p1"), stateWhere([](const MediaStateFrame& state) { return state.state == kPaused; })});
  std::this_thread::sleep_for(kPauseReflection + 300ms);
  CHECK(fake->count("DropSession") == 0);

  sendFrame(fd, mediaCommand("r1", kResume));
  untilEach(fd, {resultOf("r1"), stateWhere(airplayPlaying)});
  CHECK(fake->count("Play") == 1);
  const auto writer = fifoWriter(rig.audioFifo());
  writeFrames(writer.get(), 4800);
  CHECK(eventually([&] { return rig.music().status().framesIn >= 4800; }));
}

TEST_CASE("next and previous are only delivered, and answered control_unsupported when the receiver refuses") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();
  const auto fake = rig.receiver(playing());
  fake->fails("Next");
  untilState(fd, airplayPlaying);

  sendFrame(fd, mediaCommand("n1", kNext));
  untilEach(fd, {resultOf("n1", "control_unsupported")});
  sendFrame(fd, mediaCommand("v1", kPrevious));
  untilEach(fd, {resultOf("v1")});
  // the speaker's level went first, as the session came up
  CHECK(fake->calls() == std::vector<std::string>{"SetAirplayVolume -21.00", "Next", "Previous"});
}

TEST_CASE("what the metadata pipe says reaches the session's state, is cleared as it ends, and is never logged") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();
  const auto fake = rig.receiver(playing("Kim's phone"));
  untilState(fd, airplayPlaying);

  const auto writer = fifoWriter(rig.metadataFifo());
  const auto items = track();
  REQUIRE(::write(writer.get(), items.data(), items.size()) == static_cast<ssize_t>(items.size()));
  const auto shown = untilState(fd, titled);
  CHECK(std::string_view(shown.metadata.title) == "Night Drive");
  CHECK(std::string_view(shown.metadata.artist) == "The Hum");
  CHECK(std::string_view(shown.metadata.album) == "Tapes");
  CHECK(std::string_view(shown.metadata.artwork_url).empty());

  fake->state("Not Available");
  const auto ended = untilState(fd, noSession);
  CHECK_FALSE(ended.has_metadata);
  for (const auto* text : {"Night", "Hum", "Tapes", "Kim", "phone-a"})
    CHECK(rig.audiod().logs().count(text) == 0);
}

TEST_CASE("a url session displaces airplay, which is paused and no longer heard") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();
  const auto fake = rig.receiver(playing());
  untilState(fd, airplayPlaying);

  sendFrame(fd, urlPlay("u1", "http://fake/song"));
  REQUIRE(eventually([&] { return rig.audiod().onLoop([&](auto&, auto&) { return rig.media().source("song"); }); }));
  rig.audiod().onLoop([&](auto&, auto&) {
    rig.media().source("song")->events().playing();
    return 0;
  });
  untilState(fd, [](const MediaStateFrame& state) { return state.source == kUrlKind && state.state == kPlaying; });
  CHECK(fake->waitCall("Pause"));
  const auto in = rig.music().status().framesIn;
  const auto writer = fifoWriter(rig.audioFifo());
  writeFrames(writer.get(), 4800);
  std::this_thread::sleep_for(100ms);
  CHECK(rig.music().status().framesIn == in);
}

TEST_CASE("a shut gate pauses airplay, and nothing resumes as it opens again") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();
  const auto fake = rig.receiver(playing());
  fake->honoursControls();
  untilState(fd, airplayPlaying);

  // a new configuration shuts the gate
  sendFrame(fd, configureOutput(rig.audiod().file("out"), "Master"));
  CHECK(fake->waitCall("Pause"));
  untilState(fd, [](const MediaStateFrame& state) { return state.source == kAirplayKind && state.state == kPaused; });

  sendFrame(fd, volumeSet(40));
  CHECK_FALSE(playedAmong(within(fd, 600ms)));
  CHECK(fake->count("Play") == 0);
}

TEST_CASE("a player state shairport cannot have, or a property of the wrong type, ends the session and the receiver") {
  REQUIRE_AIRPLAY_BUS();
  for (const auto& entry : {std::pair<std::string, dbus::Value>{"PlayerState", dbus::Value::string("Buffering")},
                            std::pair<std::string, dbus::Value>{"AirplayVolume", dbus::Value::string("-15")}}) {
    INFO(entry.first);
    Rig rig;
    auto client = rig.connect();
    const int fd = client.get();
    const auto fake = rig.receiver(playing());
    untilState(fd, airplayPlaying);
    fake->emit({entry});
    untilEach(fd, {statusWhere(kFailed, "invalid_state"), stateWhere(noSession)});
  }
}

TEST_CASE("the sender's volume moves the speaker only as a deliberate slider move, and the speaker's level goes back") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();
  // it comes up with the level its app stored, 50: the first value that differs, dropped
  const auto fake = rig.receiver(playing());
  untilState(fd, airplayPlaying);
  CHECK(fake->waitCall("SetAirplayVolume -21.00"));

  // our own level coming back, and a step beside it, is no request
  fake->volume(airplay::toDb(30));
  fake->volume(airplay::toDb(31));
  CHECK(senderVolumes(within(fd, 400ms)).empty());
  fake->volume(airplay::toDb(55));
  CHECK(senderVolumes(until(fd, [](const Envelope& envelope) {
          return is(envelope, cadence_audiod_v1_Envelope_sender_volume_tag);
        })) == std::vector<std::uint32_t>{55});

  // the speaker's level goes to the receiver, and what comes back of it is no request
  sendFrame(fd, senderVolumeSet(20));
  CHECK(fake->waitCall("SetAirplayVolume -24.00"));
  fake->volume(airplay::toDb(20));
  fake->volume(airplay::toDb(21));
  CHECK(senderVolumes(within(fd, 400ms)).empty());

  // a new sender is told the level, and the level its app stored is no request: written from where the receiver has
  // not said yet, every level is taken for an echo of that write for the window
  fake->client("phone-b");
  CHECK(fake->waitCall("SetAirplayVolume -24.00", 2));
  fake->volume(airplay::toDb(90));
  CHECK(senderVolumes(within(fd, 400ms)).empty());
  CHECK(rig.audiod().logs().count("phone-b") == 0);
}

TEST_CASE("a receiver that took its name before its objects answer is read again until they do") {
  REQUIRE_AIRPLAY_BUS();
  for (const std::string_view late : {"main", "remote"}) {
    INFO(late);
    Rig rig;
    auto client = rig.connect();
    auto properties = playing();
    (late == "main" ? properties.mainAfter : properties.remoteAfter) = 300ms;
    const auto fake = rig.receiver(properties);
    const auto frames = until(client.get(), stateWhere(airplayPlaying));
    CHECK(std::none_of(frames.begin(), frames.end(), [](const Envelope& envelope) {
      return is(envelope, cadence_audiod_v1_Envelope_airplay_status_tag) &&
             envelope.body.airplay_status.state == kFailed;
    }));
    CHECK(rig.audiod().logs().count(event("airplay_control_ready")) == 1);
  }
}

TEST_CASE("a read that times out is read again, and the receiver plays") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  auto client = rig.connect();
  auto properties = playing();
  // longer than a call may take (1 s here)
  properties.slowStateRead = 1200ms;
  const auto fake = rig.receiver(properties);
  untilState(client.get(), airplayPlaying);
  CHECK(rig.audiod().logs().count(event("airplay_control_failed")) == 0);
}

TEST_CASE("a player state shairport cannot have, read as it connects, fails the receiver") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  auto client = rig.connect();
  auto properties = playing();
  properties.state = "Buffering";
  const auto fake = rig.receiver(properties);
  const auto frames = untilEach(client.get(), {statusWhere(kFailed, "invalid_state")});
  CHECK_FALSE(playedAmong(frames));
  CHECK(rig.audiod().logs().count(event("airplay_control_ready")) == 0);
}

TEST_CASE("once the protocol read right, reads that keep failing are no wrong protocol") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  auto client = rig.connect();
  auto properties = playing();
  properties.protocol = "AirPlay 1";
  // past the connect's 1.5 s here: the remote control never answers in time
  properties.remoteAfter = 30s;
  const auto fake = rig.receiver(properties);
  std::this_thread::sleep_for(200ms);
  fake->protocol("AirPlay 2");
  untilEach(client.get(), {statusWhere(kFailed, "bus_failed")});
}

TEST_CASE("a new session never shows the track the last one played") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();
  const auto fake = rig.receiver(playing());
  untilState(fd, airplayPlaying);
  const auto writer = fifoWriter(rig.metadataFifo());
  const auto items = track();
  REQUIRE(::write(writer.get(), items.data(), items.size()) == static_cast<ssize_t>(items.size()));
  untilState(fd, titled);

  // the session ends without the pipe saying so, and the next one begins
  fake->state("Not Available");
  untilState(fd, noSession);
  fake->client("phone-b");
  fake->state("Playing");
  const auto next = untilState(fd, airplayPlaying);
  CHECK(std::string_view(next.metadata.title).empty());
  CHECK(std::string_view(next.metadata.artist).empty());
  CHECK(std::string_view(next.metadata.album).empty());
  const auto later = within(fd, 300ms);
  CHECK(std::none_of(later.begin(), later.end(), stateWhere(titled)));
}

TEST_CASE("a pause holds the output and counts itself before it returns, its task left to the supervisor") {
  std::vector<std::string> journal;
  std::vector<std::function<void()>> tasks;
  std::uint64_t pauses = 0;
  const Log log{[](std::string_view) {}};
  auto control = std::make_shared<AirplayControl>(log, AirplayControlHooks{});
  std::vector<std::function<void()>> posted;
  AirplaySource source(AirplaySourceLink{.openOutput = [&](PcmInput&) { journal.emplace_back("open"); },
                                         .closeOutput = [&] { journal.emplace_back("close"); },
                                         .pauseOutput =
                                             [&] {
                                               journal.emplace_back("pause");
                                               return ++pauses;
                                             },
                                         .run = [&](std::function<void()> task) { tasks.push_back(std::move(task)); }},
                       control, [&](std::function<void()> task) { posted.push_back(std::move(task)); });

  CHECK(source.pause() == kMediaOk);
  CHECK(journal == std::vector<std::string>{"pause"});
  CHECK(tasks.size() == 1);

  std::vector<std::string> codes;
  CHECK(source.command(MediaAction::Pause, 0, [&](std::string_view code) { codes.emplace_back(code); }) ==
        kMediaPending);
  CHECK(journal == std::vector<std::string>{"pause", "pause"});
  REQUIRE(tasks.size() == 2);
  CHECK(codes.empty());

  // run where the supervisor would: no session, so control_unsupported, answered on the loop thread
  tasks[1]();
  CHECK(codes.empty());
  REQUIRE(posted.size() == 1);
  posted[0]();
  CHECK(codes == std::vector<std::string>{"control_unsupported"});

  // the others hold nothing
  CHECK(source.command(MediaAction::Next, 0, [](std::string_view) {}) == kMediaPending);
  CHECK(source.resume() == kMediaOk);
  CHECK(journal == std::vector<std::string>{"pause", "pause"});
}

TEST_CASE("the e2e's fake shairport casts its track, moves the speaker once by its slider and takes a pause") {
  REQUIRE_AIRPLAY_BUS();
  Rig rig(Receiver::E2e);
  auto client = rig.connect(25);
  const int fd = client.get();
  const auto shown =
      untilState(fd, [](const MediaStateFrame& state) { return airplayPlaying(state) && titled(state); });
  CHECK(std::string_view(shown.metadata.title) == "Cadence Fake AirPlay Track");
  CHECK(std::string_view(shown.metadata.artist) == "Cadence Fake AirPlay Artist");
  // its tone, in real time
  CHECK(eventually([&] { return rig.music().status().framesIn >= 24000; }, 3s));

  // the level its app stored is dropped, the slider's move is the one request
  CHECK(senderVolumes(until(fd, [](const Envelope& envelope) {
          return is(envelope, cadence_audiod_v1_Envelope_sender_volume_tag);
        })) == std::vector<std::uint32_t>{70});
  CHECK(senderVolumes(within(fd, 1s)).empty());

  sendFrame(fd, senderVolumeSet(40));
  sendFrame(fd, mediaCommand("p1", kPause));
  untilEach(fd, {resultOf("p1"), stateWhere([](const MediaStateFrame& state) {
                   return state.source == kAirplayKind && state.state == kPaused;
                 })});
  REQUIRE(eventually([&] { return rig.e2eCalls().size() == 4; }));
  // the speaker's level as the cast came up and again for the dropped value, the new level, then the pause
  CHECK(rig.e2eCalls() == std::vector<std::string>{"SetAirplayVolume -22.50", "SetAirplayVolume -22.50",
                                                   "SetAirplayVolume -18.00", "Pause"});
  const auto in = rig.music().status().framesIn;
  // it reflected the pause, so its session stays
  std::this_thread::sleep_for(kPauseReflection + 300ms);
  CHECK(rig.music().status().framesIn == in);
  CHECK(rig.e2eCalls().size() == 4);
  CHECK(rig.audiod().logs().count("Cadence Fake") == 0);
  CHECK(rig.audiod().logs().count("cadence-fake-airplay-client") == 0);
}
