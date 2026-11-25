#include "audiod/media-source.h"
#include "audiod/soloist-handlers.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/eventually.h"
#include "support/fake-media-source.h"
#include "support/jail-rig.h"
#include "support/media-client.h"
#include "support/playback-client.h"
#include "support/signals.h"
#include "support/soloist-rig.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <chrono>
#include <cstdint>
#include <filesystem>
#include <functional>
#include <memory>
#include <optional>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

// soloist as the media session's source and its sender volume, end to end over audiod's socket with the fake soloist
using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

constexpr auto kSoloistKind = cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_SOLOIST;
constexpr auto kReady = cadence_audiod_v1_SoloistState_SOLOIST_STATE_READY;
constexpr double kFullScale = 32768;
constexpr std::size_t kWindow = 960;

constexpr std::string_view kUp = R"({"type":"auth_state","logged_in":true,"is_active":true,"device_name":"Kitchen"})";
constexpr std::string_view kPlayingEvent = R"({"type":"playback_state","status":"playing"})";
constexpr std::string_view kPausedEvent = R"({"type":"playback_changed","status":"paused"})";
constexpr std::string_view kPlayingSongEvent =
    R"({"type":"playback_state","status":"playing","position":{"position_ms":45000},"item":{"uri":"spotify:track:x",)"
    R"("decorations":{"identity":{"name":"Song Title"},"creators":[{"entity":{"decorations":{"identity":{"name":)"
    R"("Artist One"}}}},{"entity":{"decorations":{"identity":{"name":"Artist Two"}}}}],"parent":{"entity":)"
    R"({"decorations":{"identity":{"name":"Album Name"}}}},"visual_identity":{"cover":[{"url":)"
    R"("https://i.scdn.co/image/abc"}]},"playback":{"duration_ms":200000}}}})";

std::string volumeChanged(int level) { return R"({"type":"volume_changed","volume":)" + std::to_string(level) + "}"; }
std::string setVolume(int level) {
  return R"({"type":"command","command":"set_volume","volume":)" + std::to_string(level) + "}";
}
std::string command(std::string_view name) { return R"({"type":"command","command":")" + std::string(name) + "\"}"; }

Envelope senderVolumeSet(std::uint32_t level,
                         cadence_audiod_v1_Sender sender = cadence_audiod_v1_Sender_SENDER_SOLOIST) {
  Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
  envelope.which_body = cadence_audiod_v1_Envelope_sender_volume_set_tag;
  envelope.body.sender_volume_set.sender = sender;
  envelope.body.sender_volume_set.level = level;
  return envelope;
}

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

bool is(const Envelope& envelope, pb_size_t body) { return envelope.which_body == body; }

MediaStateFrame untilState(int fd, const std::function<bool(const MediaStateFrame&)>& matches) {
  return until(fd, [&](const Envelope& envelope) {
           return is(envelope, cadence_audiod_v1_Envelope_media_state_tag) && matches(envelope.body.media_state);
         })
      .back()
      .body.media_state;
}

CommandResultFrame untilResult(int fd, std::string_view requestId) {
  return until(fd, [&](const Envelope& envelope) {
           return is(envelope, cadence_audiod_v1_Envelope_command_result_tag) &&
                  std::string_view(envelope.body.command_result.request_id) == requestId;
         })
      .back()
      .body.command_result;
}

std::vector<std::uint32_t> senderVolumes(const std::vector<Envelope>& frames) {
  std::vector<std::uint32_t> levels;
  for (const auto& envelope : frames)
    if (is(envelope, cadence_audiod_v1_Envelope_sender_volume_tag)) {
      CHECK(envelope.body.sender_volume.sender == cadence_audiod_v1_Sender_SENDER_SOLOIST);
      levels.push_back(envelope.body.sender_volume.level);
    }
  return levels;
}

// the 20 ms windows in which the tone sounds at more than half its level
std::vector<std::size_t> windowsWith(const std::vector<std::int16_t>& mono, double hz, double level) {
  std::vector<std::size_t> windows;
  for (std::size_t window = 0; (window + 1) * kWindow <= mono.size(); ++window)
    if (amplitudeAt(mono, 1, 0, window * kWindow, kWindow, hz) / kFullScale > level / 2)
      windows.push_back(window);
  return windows;
}

// audiod with soloist's part over the jail rig and the fake soloist, fake url sources, the file backend and a
// hardware volume control, so the music plays at unity once the gate opened
class Rig {
 public:
  Rig()
      : dir_(std::filesystem::path(jail_.root()).parent_path().string()),
        volume_(std::make_shared<FakeVolume>()),
        audiod_(compiledBackends(), {}, withFakeControl(volume_), openCapture, harnessMedia()) {
    std::filesystem::copy_file(CADENCE_FAKE_SOLOIST, dir_ + "/soloist");
    std::filesystem::permissions(dir_ + "/soloist", std::filesystem::perms(0755));
  }

  [[nodiscard]] AudiodHarness& audiod() { return audiod_; }
  [[nodiscard]] FakeMedia& media() { return fakes_; }
  [[nodiscard]] FakeSoloist fake() const { return FakeSoloist(dir_ + "/scratch/home"); }
  [[nodiscard]] std::string out() const { return audiod_.file("out"); }

  [[nodiscard]] Envelope config(std::string_view key = kFakeKey, std::string_view name = "Kitchen") const {
    Envelope envelope = cadence_audiod_v1_Envelope_init_zero;
    envelope.which_body = cadence_audiod_v1_Envelope_soloist_config_tag;
    auto& body = envelope.body.soloist_config;
    body.enabled = true;
    copyText(body.api_key, key);
    copyText(body.device_name, name);
    copyText(body.dir, dir_);
    copyText(body.scratch_dir, dir_ + "/scratch");
    copyText(body.ca_file, jail_.file("ca.crt"));
    body.initial_volume = 40;
    return envelope;
  }

  // configured, the gate open and soloist ready
  UniqueFd connect(std::string_view key = kFakeKey) {
    auto client = connectConfigured(audiod_, out(), "Master");
    sendFrame(client.get(), config(key));
    sendFrame(client.get(), volumeSet(40));
    until(client.get(), [](const Envelope& envelope) {
      return is(envelope, cadence_audiod_v1_Envelope_soloist_status_tag) &&
             envelope.body.soloist_status.state == kReady;
    });
    return client;
  }

  // soloist logged in and playing holds the session
  MediaStateFrame plays(int fd, std::string_view event = kPlayingEvent) {
    fake().send(kUp);
    fake().send(event);
    return untilState(fd, [](const MediaStateFrame& state) {
      return state.source == kSoloistKind && state.state == kPlaying;
    });
  }

 private:
  HarnessMedia harnessMedia() {
    HarnessMedia media{fakes_.factory()};
    media.soloist = SoloistOptions{.timings = quickTimings(), .resolvConf = jail_.file("resolv.conf"), .pcm = {}};
    return media;
  }

  JailRig jail_;
  std::string dir_;
  FakeMedia fakes_;
  std::shared_ptr<FakeVolume> volume_;
  AudiodHarness audiod_;
};

}  // namespace

TEST_CASE("soloist playing while logged in takes the session, with its metadata in the state") {
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();
  // not logged in: no session
  rig.fake().send(kPlayingEvent);
  CHECK(within(fd, 300ms).empty());

  const auto state = rig.plays(fd, kPlayingSongEvent);
  CHECK(std::string_view(state.session_id) == "ms-1");
  CHECK(state.has_metadata);
  CHECK(std::string_view(state.metadata.title) == "Song Title");
  CHECK(std::string_view(state.metadata.artist) == "Artist One, Artist Two");
  CHECK(std::string_view(state.metadata.album) == "Album Name");
  CHECK(std::string_view(state.metadata.artwork_url) == "https://i.scdn.co/image/abc");
  CHECK(state.duration_ms == 200'000);
  CHECK(state.position_ms == 45'000);
  const std::vector<cadence_audiod_v1_MediaAction> actions{state.actions, state.actions + state.actions_count};
  CHECK(actions == std::vector{kPause, kResume, kNext, kPrevious});
  // never in a log
  CHECK(rig.audiod().logs().count("Song Title") == 0);
  CHECK(rig.audiod().logs().count("scdn") == 0);

  rig.fake().send(R"({"type":"playback_changed","status":"paused"})");
  CHECK(untilState(fd, [](const MediaStateFrame& s) { return s.state == kPaused; }).position_ms >= 45'000);
  // a lost activation ends the session
  rig.fake().send(R"({"type":"device_changed","is_active":false})");
  expectIdle(untilState(fd, [](const MediaStateFrame& s) { return s.state == kIdle; }));
}

TEST_CASE("the media commands go to soloist as its exact commands and are answered as it answers") {
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();
  rig.plays(fd);

  const std::vector<std::pair<cadence_audiod_v1_MediaAction, std::string_view>> commands{
      {kPause, "pause"}, {kResume, "play"}, {kNext, "skip_next"}, {kPrevious, "skip_prev"}};
  for (const auto& [action, name] : commands) {
    INFO(name);
    const auto asked = std::chrono::steady_clock::now();
    sendFrame(fd, mediaCommand(std::string(name), action));
    const auto result = untilResult(fd, name);
    CHECK(result.ok);
    // its command_result answers it, long before the 2 s an error may take
    CHECK(std::chrono::steady_clock::now() - asked < 1500ms);
    CHECK(rig.fake().waitReceived(command(name)));
  }
  answered(fd, mediaCommand("seek", kSeek, 1000), "seek", "unsupported");
  answered(fd, mediaCommand("stop", kStop), "stop", "unsupported");

  // soloist's error within 2 s is the command's answer
  rig.fake().send("!error-next");
  sendFrame(fd, mediaCommand("err", kNext));
  const auto refused = untilResult(fd, "err");
  CHECK_FALSE(refused.ok);
  CHECK(std::string_view(refused.code) == "control_unsupported");
}

TEST_CASE("a url displaces soloist, which gets pause and waits, and soloist playing again stops the url") {
  Rig rig;
  rig.media().spec("a", FakeSourceSpec{.toneHz = 3000});
  auto client = rig.connect();
  const int fd = client.get();
  auto& music = rig.audiod().playback().music();

  rig.plays(fd);
  rig.fake().send("!tone on");
  // the output opened as soloist took the session: its tone reaches the mix
  REQUIRE(eventually([&] { return music.status().framesIn > 48'000 / 2; }));

  sendFrame(fd, urlPlay("play-a", "http://fake/a"));
  rig.audiod().onLoop([&](IpcServer&, Daemon&) {
    rig.media().source("a")->events().playing();
    return 0;
  });
  const auto url = untilState(fd, [](const MediaStateFrame& s) { return s.source == kUrlKind; });
  CHECK(std::string_view(url.session_id) == "ms-2");
  CHECK(rig.fake().waitReceived(command("pause")));
  // a playing status that was on its way as our pause landed takes nothing back: soloist did not start again
  rig.fake().send(kPlayingEvent);
  CHECK(within(fd, 500ms).empty());
  CHECK(rig.media().calls("a") == std::vector<std::string>{"made", "open"});
  // soloist keeps playing unheard: its pcm is still taken, at its pace
  // a quarter second of it within a few: the pace is real time, so a loaded host only delays it
  const auto tone = rig.fake().tone();
  CHECK(eventually([&] { return rig.fake().tone() > tone + 44'100 * 4 / 4; }, 3s));

  // it paused, as told, and plays again on its own: that takes the session back
  rig.fake().send(kPausedEvent);
  rig.fake().send(kPlayingEvent);
  const auto back = untilState(fd, [](const MediaStateFrame& s) { return s.source == kSoloistKind; });
  CHECK(std::string_view(back.session_id) == "ms-3");
  CHECK(rig.media().calls("a") == std::vector<std::string>{"made", "open", "stop", "destroyed"});
  std::this_thread::sleep_for(400ms);
  disconnect(rig.audiod(), client);

  const auto mix = firstChannel(readWav(rig.out() + "/mix/mix-000.wav"));
  const auto soloist = windowsWith(mix, 1000, 0.5);
  const auto a = windowsWith(mix, 3000, 0.25);
  REQUIRE_FALSE(soloist.empty());
  REQUIRE_FALSE(a.empty());
  // soloist, then the url alone, then soloist again: never both
  CHECK(soloist.front() < a.front());
  const auto during = std::count_if(soloist.begin(), soloist.end(),
                                    [&](std::size_t window) { return window > a.front() && window < a.back(); });
  CHECK(during == 0);
  CHECK(soloist.back() > a.back());
}

// starting an airplay cast from the phone that spotify played from moves spotify's playback to that phone: soloist
// says it is no longer the active device, then keeps reporting the account's state, playing elsewhere. that playing
// is not this speaker's and must not take the session back from the cast
TEST_CASE("soloist playing after it lost the active device takes nothing back") {
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();

  rig.plays(fd);
  sendFrame(fd, urlPlay("play-a", "http://fake/a"));
  rig.audiod().onLoop([&](IpcServer&, Daemon&) {
    rig.media().source("a")->events().playing();
    return 0;
  });
  untilState(fd, [](const MediaStateFrame& s) { return s.source == kUrlKind; });
  REQUIRE(rig.fake().waitReceived(command("pause")));
  rig.fake().send(kPausedEvent);
  rig.fake().send(R"({"type":"device_changed","is_active":false})");
  rig.fake().send(kPlayingEvent);
  CHECK(within(fd, 500ms).empty());
  CHECK(rig.media().calls("a") == std::vector<std::string>{"made", "open"});

  // made the active device again (playback moved back to this speaker), its playing takes the session as before
  rig.fake().send(kPausedEvent);
  rig.fake().send(R"({"type":"device_changed","is_active":true})");
  rig.fake().send(kPlayingEvent);
  const auto back = untilState(fd, [](const MediaStateFrame& s) { return s.source == kSoloistKind; });
  CHECK(std::string_view(back.session_id) == "ms-3");
}

TEST_CASE("a gate that shuts pauses soloist and closes its output, and nothing resumes") {
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();
  auto& music = rig.audiod().playback().music();
  rig.plays(fd);
  rig.fake().send("!tone on");
  REQUIRE(eventually([&] { return music.status().framesIn > 48'000 / 2; }));

  sendFrame(fd, volumeSet(101));
  CHECK(untilState(fd, [](const MediaStateFrame& s) { return s.state == kPaused; }).source == kSoloistKind);
  CHECK(rig.fake().waitReceived(command("pause")));
  // the tone stops reaching the music input, and soloist's pcm is still taken
  std::this_thread::sleep_for(100ms);
  const auto heard = music.status().framesIn;
  const auto tone = rig.fake().tone();
  CHECK(eventually([&] { return rig.fake().tone() > tone + 44'100 * 4 / 4; }, 3s));
  CHECK(music.status().framesIn == heard);

  // soloist that pauses and plays again is paused again: nothing plays while the gate is shut
  rig.fake().send(kPausedEvent);
  rig.fake().send(kPlayingEvent);
  CHECK(eventually([&] {
    const auto received = rig.fake().received();
    return std::count(received.begin(), received.end(), command("pause")) >= 2;
  }));
  CHECK(music.status().framesIn == heard);
  // soloist keeps running
  CHECK(rig.audiod().logs().count(event("soloist_exit")) == 0);
  CHECK(rig.audiod().logs().count(event("soloist_start")) == 1);
}

TEST_CASE("soloist's volume moves the speaker only as a deliberate slider move") {
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();
  sendFrame(fd, senderVolumeSet(30));
  // another sender's level is not soloist's
  sendFrame(fd, senderVolumeSet(90, cadence_audiod_v1_Sender_SENDER_AIRPLAY));
  // a session that comes up is told the speaker's level, and its echo is no request
  rig.fake().send(kUp);
  CHECK(rig.fake().waitReceived(setVolume(30)));
  // the app's stored level comes first: dropped, and answered with the speaker's
  rig.fake().send(volumeChanged(80));
  CHECK(eventually([&] {
    const auto received = rig.fake().received();
    return std::count(received.begin(), received.end(), setVolume(30)) >= 2;
  }));
  CHECK(senderVolumes(within(fd, 400ms)).empty());

  rig.fake().send(volumeChanged(55));
  const auto moved = until(fd, [](const Envelope& e) { return is(e, cadence_audiod_v1_Envelope_sender_volume_tag); });
  CHECK(senderVolumes(moved) == std::vector<std::uint32_t>{55});

  // the speaker's own level goes to soloist, and what comes back of it, one off too, is no request
  sendFrame(fd, senderVolumeSet(55));
  sendFrame(fd, senderVolumeSet(20));
  CHECK(rig.fake().waitReceived(setVolume(20)));
  rig.fake().send("!echo 1");
  sendFrame(fd, senderVolumeSet(25));
  CHECK(rig.fake().waitReceived(setVolume(25)));
  CHECK(senderVolumes(within(fd, 400ms)).empty());

  // a burst goes out as its first and its last, at most one per 200 ms
  rig.fake().send(volumeChanged(60));
  rig.fake().send(volumeChanged(61));
  rig.fake().send(volumeChanged(62));
  CHECK(senderVolumes(within(fd, 600ms)) == std::vector<std::uint32_t>{60, 62});
  CHECK(rig.audiod().logs().count(R"("msg":"soloist_sender_volume","level":62)") == 1);
}

// what make e2e relies on (deploy/dev/e2e.sh): the fake's own start for the name the e2e gives it
TEST_CASE("the fake's e2e start plays a track with its tone, moves the slider twice and logs the commands it obeys") {
  Rig rig;
  auto client = connectConfigured(rig.audiod(), rig.out(), "Master");
  const int fd = client.get();
  // as cadenced does: the speaker's level before the config
  sendFrame(fd, senderVolumeSet(30));
  sendFrame(fd, rig.config(kFakeKey, "Cadence E2E"));
  sendFrame(fd, volumeSet(40));
  const auto commands = [&] { return rig.fake().lines("commands.log"); };
  const auto logged = [&](const std::string& line) {
    return eventually([&] {
      const auto lines = commands();
      return std::find(lines.begin(), lines.end(), line) != lines.end();
    });
  };

  const auto playing = untilState(fd, [](const MediaStateFrame& s) {
    return s.source == kSoloistKind && s.state == kPlaying;
  });
  CHECK(std::string_view(playing.metadata.title) == "Cadence Fake Track");
  CHECK(std::string_view(playing.metadata.artist) == "Cadence Fake Artist");
  // its tone reaches the music input
  auto& music = rig.audiod().playback().music();
  CHECK(eventually([&] { return music.status().framesIn > 48'000 / 2; }));

  // the app's stored level is dropped and answered with the speaker's, which the login was told too; the move after
  // it is a request
  const auto moved = until(fd, [](const Envelope& e) { return is(e, cadence_audiod_v1_Envelope_sender_volume_tag); });
  CHECK(senderVolumes(moved) == std::vector<std::uint32_t>{25});
  CHECK(senderVolumes(within(fd, 500ms)).empty());
  CHECK(eventually([&] {
    const auto lines = commands();
    return std::count(lines.begin(), lines.end(), setVolume(30)) == 2;
  }));

  sendFrame(fd, senderVolumeSet(35));
  CHECK(logged(setVolume(35)));
  sendFrame(fd, mediaCommand("pause", kPause));
  CHECK(untilState(fd, [](const MediaStateFrame& s) { return s.state == kPaused; }).source == kSoloistKind);
  CHECK(logged(command("pause")));
  CHECK(rig.audiod().logs().count("Cadence Fake") == 0);
}

TEST_CASE("a volume while logged out or inactive moves nothing, and the key stays out of every log") {
  const std::string key = "secret-test-key-0123456789";
  Rig rig;
  auto client = rig.connect(key);
  const int fd = client.get();
  sendFrame(fd, senderVolumeSet(30));
  rig.fake().send(R"({"type":"auth_state","logged_in":true,"is_active":false})");
  rig.fake().send(volumeChanged(70));
  rig.fake().send(volumeChanged(71));
  rig.fake().send(volumeChanged(72));
  CHECK(senderVolumes(within(fd, 500ms)).empty());
  CHECK(rig.audiod().logs().count("secret-test-key") == 0);
  CHECK(rig.audiod().logs().count("Kitchen") == 0);
}
