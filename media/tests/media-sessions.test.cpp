#include "audiod/media-handlers.h"
#include "audiod/media-source.h"
#include "backend/pcm-input.h"
#include "support/audiod-client.h"
#include "support/audiod-harness.h"
#include "support/eventually.h"
#include "support/fake-media-source.h"
#include "support/media-client.h"
#include "support/playback-client.h"
#include "support/signals.h"

#include <catch2/catch_test_macros.hpp>
#include <catch2/generators/catch_generators.hpp>

#include <algorithm>
#include <atomic>
#include <chrono>
#include <cstddef>
#include <cstdint>
#include <memory>
#include <optional>
#include <span>
#include <string>
#include <string_view>
#include <thread>
#include <utility>
#include <vector>

using namespace cadence;
using namespace cadence::audiod;
using namespace cadence::test_support;
using namespace std::chrono_literals;

namespace {

using Calls = std::vector<std::string>;

constexpr auto kSoloistKind = cadence_audiod_v1_MediaSourceKind_MEDIA_SOURCE_KIND_SOLOIST;
constexpr std::int64_t kStart = 1'800'000'000'000;
constexpr double kFullScale = 32768;
// 20 ms: whole cycles of both tones the sources play
constexpr std::size_t kWindow = 960;

// a source that can wait paused, like soloist; it cannot stop
FakeSourceSpec waits() {
  FakeSourceSpec spec;
  spec.kind = MediaKind::Soloist;
  spec.keepsPaused = true;
  spec.actions = {MediaAction::Pause, MediaAction::Resume, MediaAction::Next, MediaAction::Previous};
  return spec;
}

// sends a url request the factory takes: its answer waits for the source
void request(int fd, std::string_view requestId, std::string_view url) {
  sendFrame(fd, urlPlay(requestId, url));
  const auto frames = untilPong(fd);
  CHECK(frames.results.empty());
  CHECK(frames.states.empty());
}

MediaMetadata titled(std::string title, std::string artist = {}, std::string album = {}) {
  return {std::move(title), std::move(artist), std::move(album), {}, {}, {}};
}

std::vector<cadence_audiod_v1_MediaAction> actionsOf(const MediaStateFrame& state) {
  return {state.actions, state.actions + state.actions_count};
}

bool contains(const Calls& calls, std::string_view call) {
  return std::find(calls.begin(), calls.end(), call) != calls.end();
}

// where a call sits in the journal; the journal's size when it is not there
std::size_t at(const Calls& journal, std::string_view entry) {
  return static_cast<std::size_t>(std::find(journal.begin(), journal.end(), entry) - journal.begin());
}

// audiod with fake url sources and a wall clock the test moves, on the file backend with a hardware volume
// control, so the music plays at unity once the gate opened
class Rig {
 public:
  // attached: long-lived sources, by name, the sessions attach as soloist's part attaches its own
  explicit Rig(std::chrono::milliseconds playBound = kUrlPlayBound,
               const std::vector<std::pair<std::string, FakeSourceSpec>>& attached = {})
      : volume_(std::make_shared<FakeVolume>()),
        audiod_(compiledBackends(), {}, withFakeControl(volume_), openCapture, media(playBound, attached)) {}

  [[nodiscard]] FakeMedia& media() { return media_; }
  [[nodiscard]] AudiodHarness& audiod() { return audiod_; }
  [[nodiscard]] std::string out() const { return audiod_.file("out"); }
  [[nodiscard]] std::int64_t now() const { return now_.load(); }
  void advance(std::int64_t ms) { now_ += ms; }

  // configured, its idle state taken, and the gate open
  UniqueFd connect() {
    auto client = connectConfigured(audiod_, out(), "Master");
    expectApplied(client.get(), 40, true);
    return client;
  }

  // runs report with the live source of that name on audiod's loop thread
  template <typename Report>
  void on(const std::string& name, Report report) {
    const bool found = audiod_.onLoop([&](IpcServer&, Daemon&) {
      auto* source = media_.source(name);
      if (source != nullptr)
        report(*source);
      return source != nullptr;
    });
    REQUIRE(found);
  }

  // the source of that name plays and takes the session; the url request that made it, if one still waits, is
  // answered ok. returns its state
  MediaStateFrame takes(int fd, const std::string& name, std::optional<std::string> requestId = std::nullopt) {
    on(name, [](FakeMedia::Source& source) { source.events().playing(); });
    const auto frames = untilPong(fd);
    if (requestId) {
      REQUIRE(frames.results.size() == 1);
      expectResult(frames, *requestId, "");
    } else {
      CHECK(frames.results.empty());
    }
    const auto state = onlyState(frames);
    CHECK(state.state == kPlaying);
    CHECK(state.rate == 1.0f);
    CHECK(state.position_at_ms == now());
    return state;
  }

  // a url session of that name, playing
  MediaStateFrame session(int fd, const std::string& name) {
    request(fd, "play-" + name, "http://fake/" + name);
    return takes(fd, name, "play-" + name);
  }

 private:
  HarnessMedia media(std::chrono::milliseconds playBound,
                     const std::vector<std::pair<std::string, FakeSourceSpec>>& attached) {
    HarnessMedia media{media_.factory(), [this] { return now_.load(); }, playBound};
    for (const auto& [name, spec] : attached)
      media.sources.push_back(media_.longLived(name, spec));
    return media;
  }

  std::atomic<std::int64_t> now_{kStart};
  FakeMedia media_;
  std::shared_ptr<FakeVolume> volume_;
  AudiodHarness audiod_;
};

// the 20 ms windows in which the tone sounds at more than half its level
std::vector<std::size_t> windowsWith(const std::vector<std::int16_t>& mono, double hz, double level) {
  std::vector<std::size_t> windows;
  for (std::size_t window = 0; (window + 1) * kWindow <= mono.size(); ++window)
    if (amplitudeAt(mono, 1, 0, window * kWindow, kWindow, hz) / kFullScale > level / 2)
      windows.push_back(window);
  return windows;
}

void push(PcmInput& input, const std::vector<float>& stereo) {
  REQUIRE(input.push(std::span<const float>(stereo), 48000, 2));
}

// until the player has read everything queued
void drain(PcmInput& input) {
  const auto before = input.status().underruns;
  input.attach();
  REQUIRE(eventually([&] { return input.status().underruns > before; }));
  input.detach();
}

// a failed VolumeSet shuts the gate; returns what came with its VolumeApplied
Frames shutGate(int fd) {
  sendFrame(fd, volumeSet(101));
  auto frames = untilPong(fd);
  REQUIRE(frames.others.size() == 1);
  CHECK_FALSE(frames.others[0].body.volume_applied.ok);
  return frames;
}

}  // namespace

TEST_CASE("LastPlayingEdgeWins") {
  const bool waiting = GENERATE(false, true);
  Rig rig;
  if (waiting)
    rig.media().spec("a", waits());
  auto client = rig.connect();
  auto& music = rig.audiod().playback().music();

  const auto first = rig.session(client.get(), "a");
  CHECK(std::string_view(first.session_id) == "ms-1");
  CHECK(first.source == (waiting ? kSoloistKind : kUrlKind));
  PcmInput* opened = nullptr;
  rig.on("a", [&](FakeMedia::Source& source) { opened = source.music(); });
  CHECK(opened == &music);

  // a's music, 1.5 s of 1 kHz queued at once; 300 ms of it play before b takes over
  push(music, sine(1000, 0.25, 48 * 1500, 2));
  std::this_thread::sleep_for(300ms);
  request(client.get(), "b", "http://fake/b");
  const auto second = rig.takes(client.get(), "b", "b");
  CHECK(std::string_view(second.session_id) == "ms-2");
  CHECK(second.source == kUrlKind);

  const auto journal = rig.media().journal();
  const auto bOpened = at(journal, "b:open");
  REQUIRE(bOpened < journal.size());
  if (waiting) {
    CHECK(at(journal, "a:pause") < bOpened);
    CHECK(at(journal, "a:close") < bOpened);
    CHECK_FALSE(contains(rig.media().calls("a"), "destroyed"));
  } else {
    CHECK(at(journal, "a:stop") < bOpened);
    CHECK(at(journal, "a:destroyed") < bOpened);
  }
  CHECK(rig.audiod().logs().count(R"("msg":"media_session_end","id":"ms-1","reason":"displaced")") == 1);
  CHECK(rig.audiod().logs().count(R"("msg":"media_session_start","source":"url","id":"ms-2")") == 1);

  // b's music, 1 s of 3 kHz: what a still had queued is gone, so none of it follows
  push(music, sine(3000, 0.25, 48 * 1000, 2));
  drain(music);
  std::this_thread::sleep_for(100ms);
  disconnect(rig.audiod(), client);

  const auto mix = firstChannel(readWav(rig.out() + "/mix/mix-000.wav"));
  const auto a = windowsWith(mix, 1000, 0.25);
  const auto b = windowsWith(mix, 3000, 0.25);
  REQUIRE_FALSE(a.empty());
  REQUIRE_FALSE(b.empty());
  CHECK(a.size() >= 5);
  CHECK(a.size() <= 40);
  CHECK(b.size() >= 45);
  CHECK(a.back() < b.front());
}

TEST_CASE("DisplacedNeverResumes") {
  Rig rig;
  rig.media().spec("a", waits());
  auto client = rig.connect();
  rig.session(client.get(), "a");
  rig.session(client.get(), "b");
  const Calls displaced{"made", "open", "pause", "close"};
  REQUIRE(rig.media().calls("a") == displaced);

  SECTION("the winner pauses") {
    const auto paused = onlyState(answered(client.get(), mediaCommand("p", kPause), "p", ""));
    CHECK(std::string_view(paused.session_id) == "ms-2");
    CHECK(paused.state == kPaused);
  }
  SECTION("the winner ends") {
    rig.on("b", [](FakeMedia::Source& source) { source.events().ended(); });
    expectIdle(onlyState(untilPong(client.get())));
  }
  SECTION("the winner is stopped") {
    expectIdle(onlyState(answered(client.get(), mediaCommand("s", kStop), "s", "")));
  }
  std::this_thread::sleep_for(100ms);
  CHECK(untilPong(client.get()).states.empty());
  CHECK(rig.media().calls("a") == displaced);

  // what plays again on its own takes the session back
  const auto back = rig.takes(client.get(), "a");
  CHECK(std::string_view(back.session_id) == "ms-3");
  CHECK(back.source == kSoloistKind);
  CHECK(rig.media().calls("a").back() == "open");
}

TEST_CASE("StateOnChangeOnly") {
  Rig rig;
  auto client = rig.connect();
  request(client.get(), "r", "http://fake/a");
  // what a source reports before it plays is not the session's yet
  rig.on("a", [](FakeMedia::Source& source) {
    source.events().duration(180'000);
    source.events().position(0);
    source.events().buffering();
  });
  CHECK(untilPong(client.get()).states.empty());
  const auto playing = rig.takes(client.get(), "a", "r");
  CHECK(playing.duration_ms == 180'000);
  CHECK(playing.position_ms == 0);
  CHECK(actionsOf(playing) == std::vector{kPause, kResume, kStop, kSeek});

  // steady playback says nothing, and neither does a report that changes nothing
  CHECK(receive(client.get(), 3s).kind == Received::Kind::timeout);
  rig.advance(3000);
  rig.on("a", [](FakeMedia::Source& source) {
    source.events().playing();
    source.events().duration(180'000);
    source.events().metadata({});
  });
  CHECK(untilPong(client.get()).states.empty());

  // a pause takes the position the session ran to
  const auto paused = onlyState(answered(client.get(), mediaCommand("p", kPause), "p", ""));
  CHECK(paused.state == kPaused);
  CHECK(paused.rate == 0.0f);
  CHECK(paused.position_ms == 3000);
  CHECK(paused.position_at_ms == rig.now());

  rig.advance(5000);
  const auto sought = onlyState(answered(client.get(), mediaCommand("s", kSeek, 60'000), "s", ""));
  CHECK(sought.state == kPaused);
  CHECK(sought.position_ms == 60'000);
  CHECK(sought.position_at_ms == rig.now());

  rig.on("a", [](FakeMedia::Source& source) { source.events().metadata(titled("Song", "Band")); });
  const auto named = onlyState(untilPong(client.get()));
  REQUIRE(named.has_metadata);
  CHECK(std::string_view(named.metadata.title) == "Song");
  CHECK(std::string_view(named.metadata.artist) == "Band");
  CHECK(named.position_ms == 60'000);

  rig.on("a", [](FakeMedia::Source& source) { source.events().duration(200'000); });
  CHECK(onlyState(untilPong(client.get())).duration_ms == 200'000);
}

TEST_CASE("CommandsAnswered") {
  Rig rig;
  FakeSourceSpec live;
  live.actions = {MediaAction::Pause, MediaAction::Stop};
  rig.media().spec("live", live);
  auto client = rig.connect();
  const int fd = client.get();

  SECTION("without a session") {
    answered(fd, mediaCommand("c1", kPause), "c1", "no_session");
    answered(fd, mediaCommand("c2", kStop), "c2", "no_session");
    answered(fd, mediaCommand("c3", static_cast<cadence_audiod_v1_MediaAction>(7)), "c3", "unsupported");
    answered(fd, mediaCommand("c4", kNoAction), "c4", "unsupported");
  }
  SECTION("url requests") {
    // an overlay goes to the same factory, which refuses this one
    answered(fd, urlPlay("u1", "http://fake/refused/busy", kOverlayMode), "u1", "busy");
    answered(fd, urlPlay("u2", "http://fake/a", cadence_audiod_v1_UrlMode_URL_MODE_UNSPECIFIED), "u2", "invalid");
    answered(fd, urlPlay("u3", "http://fake/refused/scheme"), "u3", "scheme");
    CHECK(rig.media().journal().empty());

    // the source gets the configuration's ca bundle
    auto configuration = configureOutput(rig.out(), "Master");
    copyText(configuration.body.configure.ca_file, "/etc/cadence/ca.pem");
    sendFrame(fd, configuration);
    expectReady(fd);
    expectApplied(fd, 40, true);
    auto play = urlPlay("u4", "http://fake/a");
    auto& body = play.body.url_play;
    body.start_ms = 1500;
    body.has_metadata = true;
    copyText(body.metadata.title, "Title");
    copyText(body.metadata.artwork_url, "https://art.example/a.jpg");
    sendFrame(fd, play);
    CHECK(untilPong(fd).results.empty());
    const auto requests = rig.media().requests();
    REQUIRE(requests.size() == 3);
    CHECK(requests.back().url == "http://fake/a");
    CHECK(requests.back().startMs == 1500);
    CHECK(requests.back().metadata.title == "Title");
    CHECK(requests.back().metadata.artworkUrl == "https://art.example/a.jpg");
    CHECK(requests.back().caFile == "/etc/cadence/ca.pem");
  }
  SECTION("ids come back as they came") {
    const std::string longest(63, 'r');
    answered(fd, mediaCommand(longest, kPause), longest, "no_session");
    answered(fd, mediaCommand("", kPause), "", "no_session");
    request(fd, "", "http://fake/a");
    rig.takes(fd, "a", "");
  }
  SECTION("with a session") {
    rig.session(fd, "a");
    answered(fd, mediaCommand("n", kNext), "n", "unsupported");
    answered(fd, mediaCommand("v", kPrevious), "v", "unsupported");
    // no duration known: any position
    answered(fd, mediaCommand("s1", kSeek, 900'000), "s1", "");
    rig.on("a", [](FakeMedia::Source& source) { source.events().duration(10'000); });
    untilPong(fd);
    answered(fd, mediaCommand("s2", kSeek, 10'001), "s2", "invalid");
    answered(fd, mediaCommand("s3", kSeek, 10'000), "s3", "");
    answered(fd, mediaCommand("p", kPause), "p", "");
    answered(fd, mediaCommand("r", kResume), "r", "");
    CHECK(rig.media().calls("a") == Calls{"made", "open", "seek 900000", "seek 10000", "pause", "resume"});
    CHECK(rig.audiod().logs().count(R"("msg":"media_command","action":"seek","code":"invalid")") == 1);
    CHECK(rig.audiod().logs().count(R"("msg":"media_command","action":"pause","code":"ok")") == 1);

    // a source that lists less
    rig.session(fd, "live");
    answered(fd, mediaCommand("r2", kResume), "r2", "unsupported");
    answered(fd, mediaCommand("s4", kSeek, 0), "s4", "unsupported");
  }
}

// a url request is answered when its source plays, fails or is dropped, or at the bound
TEST_CASE("UrlPlayAnsweredWhenItsSourcePlays") {
  Rig rig(500ms);
  auto client = rig.connect();
  const int fd = client.get();
  request(fd, "r", "http://fake/a");

  SECTION("it plays") {
    rig.takes(fd, "a", "r");
  }
  SECTION("it fails first") {
    rig.on("a", [](FakeMedia::Source& source) { source.events().failed("http_404"); });
    const auto frames = untilPong(fd);
    expectResult(frames, "r", "http_404");
    CHECK(frames.states.empty());
    CHECK(rig.audiod().logs().count(R"("msg":"media_command","action":"play","mode":"session","code":"http_404")") ==
          1);
  }
  SECTION("it ends first") {
    rig.on("a", [](FakeMedia::Source& source) { source.events().ended(); });
    expectResult(untilPong(fd), "r", "failed");
  }
  SECTION("the gate shuts") {
    expectResult(shutGate(fd), "r", "refused");
  }
  SECTION("a new Configure") {
    sendFrame(fd, configureOutput(rig.out(), "Master"));
    Frames frames;
    for (;;) {
      const auto received = receive(fd);
      REQUIRE(received.kind == Received::Kind::frame);
      if (received.envelope.which_body == cadence_audiod_v1_Envelope_ready_tag)
        break;
      collect(frames, received.envelope);
    }
    expectResult(frames, "r", "refused");
    expectFrame(fd, cadence_audiod_v1_Envelope_media_state_tag);
  }
  SECTION("the bound passes first, and the source's answer later is dropped") {
    const auto received = receive(fd, 1500ms);
    REQUIRE(received.kind == Received::Kind::frame);
    REQUIRE(received.envelope.which_body == cadence_audiod_v1_Envelope_command_result_tag);
    CHECK(std::string_view(received.envelope.body.command_result.request_id) == "r");
    CHECK(received.envelope.body.command_result.ok);
    CHECK(std::string_view(received.envelope.body.command_result.code).empty());
    CHECK(rig.audiod().logs().count(R"("code":"ok","answer":"bound")") == 1);
    rig.takes(fd, "a");
  }
  // nothing more, also after the bound
  std::this_thread::sleep_for(600ms);
  CHECK(untilPong(fd).results.empty());
}

TEST_CASE("StopEndsSessionIdle") {
  Rig rig;
  auto client = rig.connect();
  rig.session(client.get(), "a");
  std::string reason;

  SECTION("stopped") {
    expectIdle(onlyState(answered(client.get(), mediaCommand("s", kStop), "s", "")));
    CHECK(contains(rig.media().calls("a"), "stop"));
    reason = "stopped";
  }
  SECTION("ended") {
    rig.on("a", [](FakeMedia::Source& source) { source.events().ended(); });
    expectIdle(onlyState(untilPong(client.get())));
    reason = "ended";
  }
  SECTION("failed") {
    rig.on("a", [](FakeMedia::Source& source) { source.events().failed("read_failed"); });
    expectIdle(onlyState(untilPong(client.get())));
    CHECK(rig.audiod().logs().count(R"("msg":"media_failed","code":"read_failed")") == 1);
    reason = "failed";
  }
  CHECK(rig.media().calls("a").back() == "destroyed");
  CHECK(rig.audiod().logs().count(R"("msg":"media_session_end","id":"ms-1","reason":")" + reason + "\"") == 1);
  answered(client.get(), mediaCommand("p", kPause), "p", "no_session");
}

TEST_CASE("a stop cuts the music its source queued, and an end lets it play out") {
  Rig rig;
  auto client = rig.connect();
  rig.session(client.get(), "a");
  auto& music = rig.audiod().playback().music();
  push(music, sine(1000, 0.25, 48 * 1500, 2));
  std::this_thread::sleep_for(300ms);
  SECTION("stopped") {
    answered(client.get(), mediaCommand("s", kStop), "s", "");
    std::this_thread::sleep_for(1500ms);
    disconnect(rig.audiod(), client);
    const auto windows = windowsWith(firstChannel(readWav(rig.out() + "/mix/mix-000.wav")), 1000, 0.25).size();
    CHECK(windows >= 5);
    CHECK(windows <= 40);
  }
  SECTION("ended") {
    rig.on("a", [](FakeMedia::Source& source) { source.events().ended(); });
    drain(music);
    std::this_thread::sleep_for(100ms);
    disconnect(rig.audiod(), client);
    const auto windows = windowsWith(firstChannel(readWav(rig.out() + "/mix/mix-000.wav")), 1000, 0.25).size();
    CHECK(windows >= 70);
  }
}

// a source may report from inside a call into it; that waits until the call returned
TEST_CASE("a source that says it ended from inside stop ends its session once") {
  Rig rig;
  FakeSourceSpec ends;
  ends.endsOnStop = true;
  rig.media().spec("a", ends);
  auto client = rig.connect();
  rig.session(client.get(), "a");

  expectIdle(onlyState(answered(client.get(), mediaCommand("s", kStop), "s", "")));
  CHECK(rig.audiod().logs().count(R"("msg":"media_session_end")") == 1);
  CHECK(rig.audiod().logs().count(R"("msg":"media_session_end","id":"ms-1","reason":"stopped")") == 1);
  CHECK(rig.media().calls("a") == Calls{"made", "open", "stop", "destroyed"});
}

// a source's thread posts reports with a copy of its handle; one still queued when the source goes is dropped
TEST_CASE("a report posted by a source that is gone is dropped") {
  Rig rig;
  auto client = rig.connect();
  rig.session(client.get(), "a");
  rig.on("a", [&](FakeMedia::Source& source) {
    rig.audiod().loop().post([events = source.handle()] {
      events->playing();
      events->metadata(titled("Late"));
      events->failed("late");
    });
    // handled at once: the source and its entry are gone before the post runs
    source.events().ended();
  });
  expectIdle(onlyState(untilPong(client.get())));
  CHECK(rig.media().calls("a").back() == "destroyed");
  const auto later = untilPong(client.get());
  CHECK(later.states.empty());
  CHECK(later.results.empty());
  CHECK(rig.audiod().logs().count(R"("code":"late")") == 0);
}

TEST_CASE("a stop drops a url that has not played yet") {
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();

  SECTION("with no session") {
    request(fd, "r", "http://fake/a");
    sendFrame(fd, mediaCommand("s", kStop));
    const auto frames = untilPong(fd);
    CHECK(frames.results.size() == 2);
    expectResult(frames, "s", "");
    expectResult(frames, "r", "stopped");
    CHECK(frames.states.empty());
    CHECK(rig.media().calls("a") == Calls{"made", "stop", "destroyed"});
  }
  SECTION("beside a session") {
    rig.session(fd, "b");
    request(fd, "r", "http://fake/a");
    sendFrame(fd, mediaCommand("s", kStop));
    const auto frames = untilPong(fd);
    expectResult(frames, "s", "");
    expectResult(frames, "r", "stopped");
    expectIdle(onlyState(frames));
    CHECK(rig.media().calls("a") == Calls{"made", "stop", "destroyed"});
  }
  SECTION("not when the session's source cannot stop") {
    rig.media().spec("b", waits());
    rig.session(fd, "b");
    request(fd, "r", "http://fake/a");
    answered(fd, mediaCommand("s", kStop), "s", "unsupported");
    CHECK(rig.media().calls("a") == Calls{"made"});
  }
  SECTION("not when its stop fails") {
    FakeSourceSpec stubborn;
    stubborn.stopFails = true;
    rig.media().spec("b", stubborn);
    rig.session(fd, "b");
    request(fd, "r", "http://fake/a");
    CHECK(answered(fd, mediaCommand("s", kStop), "s", "failed").states.empty());
    CHECK(rig.media().calls("a") == Calls{"made"});
  }
}

TEST_CASE("a newer url replaces one that has not played yet") {
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();
  request(fd, "r1", "http://fake/a");
  answered(fd, urlPlay("r2", "http://fake/refused/scheme"), "r2", "scheme");
  CHECK(rig.media().calls("a") == Calls{"made"});
  answered(fd, urlPlay("r3", "http://fake/b"), "r1", "replaced");
  CHECK(rig.media().calls("a") == Calls{"made", "stop", "destroyed"});
  CHECK(std::string_view(rig.takes(fd, "b", "r3").session_id) == "ms-1");
}

TEST_CASE("GateClosePausesSession") {
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();

  SECTION("a url session is stopped") {
    rig.session(fd, "a");
    // and a url still waiting to play goes with it
    request(fd, "r", "http://fake/b");
    const auto shut = shutGate(fd);
    expectIdle(onlyState(shut));
    expectResult(shut, "r", "refused");
    CHECK(rig.media().calls("a") == Calls{"made", "open", "stop", "destroyed"});
    CHECK(rig.media().calls("b") == Calls{"made", "stop", "destroyed"});
    CHECK(rig.audiod().logs().count(R"("msg":"media_session_end","id":"ms-1","reason":"gated")") == 1);
    answered(fd, urlPlay("shut", "http://fake/c"), "shut", "refused");
    CHECK(rig.media().calls("c").empty());

    expectApplied(fd, 40, true);
    CHECK(untilPong(fd).states.empty());
  }
  SECTION("a session that can wait is paused with its output closed, and stays so") {
    rig.media().spec("a", waits());
    rig.session(fd, "a");
    const auto paused = onlyState(shutGate(fd));
    CHECK(std::string_view(paused.session_id) == "ms-1");
    CHECK(paused.state == kPaused);
    answered(fd, mediaCommand("r", kResume), "r", "refused");
    // playing on its own while the gate is shut, it is paused again
    rig.on("a", [](FakeMedia::Source& source) { source.events().playing(); });
    CHECK(untilPong(fd).states.empty());
    const Calls gated{"made", "open", "pause", "close", "pause"};
    CHECK(rig.media().calls("a") == gated);

    expectApplied(fd, 40, true);
    std::this_thread::sleep_for(100ms);
    CHECK(untilPong(fd).states.empty());
    CHECK(rig.media().calls("a") == gated);
    // a resume opens its output again first
    answered(fd, mediaCommand("r2", kResume), "r2", "");
    CHECK(rig.media().calls("a") == Calls{"made", "open", "pause", "close", "pause", "open", "resume"});
  }
}

// rule 7: the gate does not rely on the pause having worked
TEST_CASE("a source whose pause fails is silenced by the gate all the same") {
  Rig rig;
  auto spec = waits();
  spec.pauseFails = true;
  spec.toneHz = 1000;
  rig.media().spec("a", spec);
  auto client = rig.connect();
  rig.session(client.get(), "a");
  std::this_thread::sleep_for(400ms);

  CHECK(onlyState(shutGate(client.get())).state == kPaused);
  CHECK(rig.media().calls("a") == Calls{"made", "open", "pause", "close"});
  std::this_thread::sleep_for(200ms);
  // the gate opens again, and nothing resumes it
  expectApplied(client.get(), 40, true);
  std::this_thread::sleep_for(800ms);
  disconnect(rig.audiod(), client);

  // what played before the gate shut, and nothing after
  const auto windows = windowsWith(firstChannel(readWav(rig.out() + "/mix/mix-000.wav")), 1000, 0.25).size();
  CHECK(windows >= 10);
  CHECK(windows <= 35);
}

TEST_CASE("StateSentAfterReady") {
  Rig rig;
  auto client = connectTo(rig.audiod().socket());
  sendFrame(client.get(), configureOutput(rig.out(), "Master"));
  expectFrame(client.get(), cadence_audiod_v1_Envelope_ready_tag);
  expectIdle(expectFrame(client.get(), cadence_audiod_v1_Envelope_media_state_tag).body.media_state);
  expectApplied(client.get(), 40, true);

  SECTION("cadenced comes back to a session that waits") {
    rig.media().spec("a", waits());
    rig.session(client.get(), "a");
    disconnect(rig.audiod(), client);
    CHECK(rig.media().calls("a") == Calls{"made", "open", "pause", "close"});
    // nobody to tell, and the next cadenced hears it only after its Ready
    rig.on("a", [](FakeMedia::Source& source) { source.events().metadata(titled("Later")); });
    client = connectTo(rig.audiod().socket());
    rig.on("a", [](FakeMedia::Source& source) { source.events().duration(42'000); });
    CHECK(receive(client.get(), 200ms).kind == Received::Kind::timeout);

    sendFrame(client.get(), configureOutput(rig.out(), "Master"));
    expectFrame(client.get(), cadence_audiod_v1_Envelope_ready_tag);
    const auto state = expectFrame(client.get(), cadence_audiod_v1_Envelope_media_state_tag).body.media_state;
    CHECK(std::string_view(state.session_id) == "ms-1");
    CHECK(state.state == kPaused);
    CHECK(state.duration_ms == 42'000);
    CHECK(std::string_view(state.metadata.title) == "Later");
    CHECK(receive(client.get(), 200ms).kind == Received::Kind::timeout);
  }
  SECTION("a new Configure stops a url session, and the idle state follows its Ready") {
    rig.session(client.get(), "a");
    sendFrame(client.get(), configureOutput(rig.out(), "Master"));
    expectFrame(client.get(), cadence_audiod_v1_Envelope_ready_tag);
    expectIdle(expectFrame(client.get(), cadence_audiod_v1_Envelope_media_state_tag).body.media_state);
    CHECK(rig.media().calls("a").back() == "destroyed");
    CHECK(rig.audiod().logs().count(R"("msg":"media_session_end","id":"ms-1","reason":"gated")") == 1);
  }
}

// a state the client's full socket refused goes out again once it reads
TEST_CASE("a state dropped on a full socket is sent again") {
  Rig rig;
  auto client = rig.connect();
  rig.session(client.get(), "a");

  const auto dropped = rig.audiod().onLoop([&](IpcServer& ipc, Daemon&) {
    Envelope pong = cadence_audiod_v1_Envelope_init_zero;
    pong.which_body = cadence_audiod_v1_Envelope_pong_tag;
    while (ipc.send(pong)) {
    }
    auto* source = rig.media().source("a");
    if (source != nullptr)
      source->events().metadata(titled("Refused"));
    return source != nullptr;
  });
  REQUIRE(dropped);

  Frames frames;
  for (auto received = receive(client.get(), 500ms); received.kind == Received::Kind::frame;
       received = receive(client.get(), 500ms))
    collect(frames, received.envelope);
  REQUIRE(frames.states.size() == 1);
  CHECK(std::string_view(frames.states[0].metadata.title) == "Refused");
}

TEST_CASE("MetadataTruncatedOnUtf8Boundary") {
  Rig rig;
  auto client = rig.connect();
  const int fd = client.get();
  auto play = urlPlay("r", "http://fake/a");
  play.body.url_play.has_metadata = true;
  copyText(play.body.url_play.metadata.title, "Requested Title");
  copyText(play.body.url_play.metadata.artwork_url, "https://art.example/cover.jpg");
  sendFrame(fd, play);
  CHECK(untilPong(fd).results.empty());
  // the request's metadata stands until the source reports its own
  const auto requested = rig.takes(fd, "a", "r");
  REQUIRE(requested.has_metadata);
  CHECK(std::string_view(requested.metadata.title) == "Requested Title");
  CHECK(std::string_view(requested.metadata.artwork_url) == "https://art.example/cover.jpg");

  // 2, 3 and 4 byte characters that would end past the bound, one that ends on it, and broken sequences
  const MediaMetadata reported{.title = std::string(254, 't') + "\xc3\xa9",
                               .artist = std::string(253, 'a') + "\xe2\x82\xac",
                               .album = std::string(251, 'l') + "\xf0\x9f\x8e\xb5" + "x",
                               .artworkUrl = "https://art.example/" + std::string(2100, 'w'),
                               .artworkId = {},
                               .sender = {}};
  rig.on("a", [&](FakeMedia::Source& source) { source.events().metadata(reported); });
  const auto cut = onlyState(untilPong(fd));
  CHECK(std::string_view(cut.metadata.title) == std::string(254, 't'));
  CHECK(std::string_view(cut.metadata.artist) == std::string(253, 'a'));
  CHECK(std::string_view(cut.metadata.album) == std::string(251, 'l') + "\xf0\x9f\x8e\xb5");
  CHECK(std::string_view(cut.metadata.artwork_url) == reported.artworkUrl.substr(0, 2047));

  // a protobuf string must be utf-8, or cadenced cannot read the frame
  rig.on("a", [](FakeMedia::Source& source) {
    source.events().metadata(titled("caf\xc3", "\xff" "band", "\xed\xa0\x80"));
  });
  const auto mended = onlyState(untilPong(fd));
  CHECK(std::string_view(mended.metadata.title) == "caf\xef\xbf\xbd");
  CHECK(std::string_view(mended.metadata.artist) == "\xef\xbf\xbd" "band");
  CHECK(std::string_view(mended.metadata.album) == "\xef\xbf\xbd\xef\xbf\xbd\xef\xbf\xbd");

  // rule 8: neither the url nor any metadata reaches a log
  for (const auto* secret : {"fake/a", "Requested", "art.example", "ttttt", "aaaaa", "lllll", "caf", "band"})
    CHECK(rig.audiod().logs().count(secret) == 0);
}

TEST_CASE("a long-lived source takes the session when it plays, and stays when its session ends") {
  Rig rig(kUrlPlayBound, {{"solo", waits()}});
  auto client = rig.connect();
  const int fd = client.get();
  CHECK(rig.media().calls("solo") == Calls{"made"});

  const auto first = rig.takes(fd, "solo");
  CHECK(std::string_view(first.session_id) == "ms-1");
  CHECK(first.source == kSoloistKind);
  rig.on("solo", [](FakeMedia::Source& source) { source.events().metadata(titled("Song")); });
  CHECK(std::string_view(onlyState(untilPong(fd)).metadata.title) == "Song");

  SECTION("it ends") {
    rig.on("solo", [](FakeMedia::Source& source) { source.events().ended(); });
  }
  SECTION("it fails") {
    rig.on("solo", [](FakeMedia::Source& source) { source.events().failed("broken"); });
  }
  expectIdle(onlyState(untilPong(fd)));
  // still there, its output closed, and with nothing of its last session
  CHECK(rig.media().calls("solo") == Calls{"made", "open", "close"});
  const auto again = rig.takes(fd, "solo");
  CHECK(std::string_view(again.session_id) == "ms-2");
  CHECK(std::string_view(again.metadata.title).empty());
  // a stop is not among its actions
  answered(fd, mediaCommand("s", kStop), "s", "unsupported");
}

TEST_CASE("a long-lived source displaced by a url waits paused, and the url it displaces stops") {
  Rig rig(kUrlPlayBound, {{"solo", waits()}});
  auto client = rig.connect();
  const int fd = client.get();
  rig.takes(fd, "solo");
  rig.session(fd, "a");
  CHECK(rig.media().calls("solo") == Calls{"made", "open", "pause", "close"});
  const auto back = rig.takes(fd, "solo");
  CHECK(back.source == kSoloistKind);
  CHECK(rig.media().calls("a") == Calls{"made", "open", "stop", "destroyed"});
}

TEST_CASE("at most four sources wait paused, and the one displaced longest ago goes first") {
  Rig rig(kUrlPlayBound, {{"solo", waits()}});
  for (const auto* name : {"a", "b", "c", "d", "e"})
    rig.media().spec(name, waits());
  auto client = rig.connect();
  const int fd = client.get();
  // the long-lived one waits paused among them, and is never the one that goes
  rig.takes(fd, "solo");
  for (const auto* name : {"a", "b", "c", "d", "e"})
    rig.session(fd, name);
  // solo, a, b, c and d wait: one too many, and a, set aside after solo, is the oldest that may go
  CHECK(contains(rig.media().calls("a"), "destroyed"));
  CHECK(at(rig.media().journal(), "a:stop") < rig.media().journal().size());
  for (const auto* name : {"solo", "b", "c", "d", "e"}) {
    INFO(name);
    CHECK_FALSE(contains(rig.media().calls(name), "destroyed"));
  }
  rig.media().spec("f", waits());
  rig.session(fd, "f");
  CHECK(contains(rig.media().calls("b"), "destroyed"));
  CHECK_FALSE(contains(rig.media().calls("solo"), "destroyed"));
}

TEST_CASE("a command its source answers later is answered then") {
  auto later = waits();
  later.answersLater = true;
  Rig rig(kUrlPlayBound, {{"solo", later}});
  auto client = rig.connect();
  const int fd = client.get();
  rig.takes(fd, "solo");

  sendFrame(fd, mediaCommand("p", kPause));
  CHECK(untilPong(fd).results.empty());
  sendFrame(fd, mediaCommand("n", kNext));
  CHECK(untilPong(fd).results.empty());
  rig.on("solo", [](FakeMedia::Source& source) { source.answer("control_unsupported"); });
  expectResult(untilPong(fd), "p", "control_unsupported");
  rig.on("solo", [](FakeMedia::Source& source) { source.answer(""); });
  expectResult(untilPong(fd), "n", "");
  CHECK(rig.audiod().logs().count(R"("msg":"media_command","action":"pause","code":"control_unsupported")") == 1);
  CHECK(rig.audiod().logs().count(R"("msg":"media_command","action":"next","code":"ok")") == 1);

  // an answer for a cadenced that went away goes to no one
  sendFrame(fd, mediaCommand("r", kResume));
  CHECK(untilPong(fd).results.empty());
  disconnect(rig.audiod(), client);
  auto next = rig.connect();
  rig.on("solo", [](FakeMedia::Source& source) { source.answer(""); });
  CHECK(untilPong(next.get()).results.empty());
}
