#include "support/audiod-client.h"
#include "support/eventually.h"
#include "support/http-server.h"
#include "support/media-client.h"
#include "support/playback-client.h"
#include "support/signals.h"
#include "support/url-rig.h"
#include "url/url-source.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <atomic>
#include <chrono>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <cstdlib>
#include <functional>
#include <future>
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

namespace {

constexpr double kFullScale = 32768;
// 100 ms: whole cycles of 440 and 660 Hz
constexpr std::size_t kToneWindow = 4800;
// 20 ms: whole cycles of the stepped tones, all multiples of 50 Hz
constexpr std::size_t kStepWindow = 960;

// the windows of one channel of the stereo mix in which hz sounds at more than level
std::size_t windowsWith(const std::vector<std::int16_t>& stereo, std::size_t channel, double hz, double level,
                        std::size_t window) {
  std::size_t count = 0;
  for (std::size_t first = 0; (first + window) * 2 <= stereo.size(); first += window)
    if (amplitudeAt(stereo, 2, channel, first, window, hz) / kFullScale > level)
      ++count;
  return count;
}

// the loudest window's level of hz in one channel
double loudest(const std::vector<std::int16_t>& stereo, std::size_t channel, double hz, std::size_t window) {
  double level = 0;
  for (std::size_t first = 0; (first + window) * 2 <= stereo.size(); first += window)
    level = std::max(level, amplitudeAt(stereo, 2, channel, first, window, hz) / kFullScale);
  return level;
}

std::vector<std::int16_t> mixOf(const UrlRig& rig) { return readWav(rig.mix()).samples(); }

// six seconds, each second a tone of its own on both channels: 400, 600, ... 1400 Hz
std::string steppedWav() {
  return wavBytes(44100, 6 * 44100, [](std::size_t frame) {
    const double t = static_cast<double>(frame) / 44100;
    const double hz = 400 + 200 * std::floor(t);
    const double value = 0.5 * std::sin(2 * std::numbers::pi * hz * t);
    return std::pair{value, value};
  });
}

// plays url as a session and waits until it ended; returns everything that came
Frames playToEnd(int fd, const std::string& url) {
  sendFrame(fd, urlPlay("play", url));
  const auto frames = until(fd, [](const Frames& got) { return hasState(got, kIdle); });
  REQUIRE(hasResult(frames, "play"));
  expectResult(frames, "play", "");
  REQUIRE(hasState(frames, kPlaying));
  REQUIRE(hasState(frames, kIdle));
  return frames;
}

// 440 Hz on the left and 660 Hz on the right, each near 0.5, and neither on the other side
void expectTones(const std::vector<std::int16_t>& mix) {
  CHECK(windowsWith(mix, 0, 440, 0.4, kToneWindow) >= 17);
  CHECK(windowsWith(mix, 1, 660, 0.4, kToneWindow) >= 17);
  CHECK(std::abs(db(loudest(mix, 0, 440, kToneWindow) / 0.5)) < 1);
  CHECK(std::abs(db(loudest(mix, 1, 660, kToneWindow) / 0.5)) < 1);
  CHECK(loudest(mix, 0, 660, kToneWindow) < 0.01);
  CHECK(loudest(mix, 1, 440, kToneWindow) < 0.01);
}

}  // namespace

TEST_CASE("PlaysWavOverHttp") {
  UrlRig rig;
  rig.server().route("/tone.wav", {.contentType = "audio/wav", .body = toneWav(2.0)});
  auto client = rig.connect();

  const auto frames = playToEnd(client.get(), rig.server().url("/tone.wav"));
  const auto playing = firstState(frames, kPlaying);
  CHECK(playing.source == kUrlKind);
  CHECK(playing.duration_ms == 2000);
  CHECK(playing.position_ms == 0);
  // the source has one name in its own lines and in its session's
  CHECK(rig.audiod().logs().count(R"("msg":"url_open","src":"s-1",)") == 1);
  CHECK(rig.audiod().logs().count(R"("msg":"media_session_start","source":"url","id":"ms-1","src":"s-1")") == 1);
  CHECK(rig.audiod().logs().count(R"("msg":"url_ended","src":"s-1",)") == 1);
  disconnect(rig.audiod(), client);

  // rule 8: no log line names the url's host or path
  CHECK(rig.audiod().logs().count("127.0.0.1") == 0);
  CHECK(rig.audiod().logs().count("tone.wav") == 0);

  expectTones(mixOf(rig));
}

TEST_CASE("PlaysFlacOverHttp") {
  UrlRig rig;
  rig.server().route("/tone.flac", {.contentType = "audio/flac", .body = fixture("tone-44k.flac")});
  auto client = rig.connect();

  const auto frames = playToEnd(client.get(), rig.server().url("/tone.flac"));
  CHECK(firstState(frames, kPlaying).duration_ms == 2000);
  disconnect(rig.audiod(), client);

  expectTones(mixOf(rig));
}

// the request's metadata first, the container's tags for what it left empty
TEST_CASE("TagsFillWhatTheRequestLeftEmpty") {
  UrlRig rig;
  const WavTags tags{"Tag Title", "Tag Artist", "Tag Album"};
  rig.server().route("/tagged.wav", {.contentType = "audio/wav", .body = toneWav(1.0, 44100, 440, 660, 0.5, tags)});
  auto client = rig.connect();
  auto play = urlPlay("play", rig.server().url("/tagged.wav"));
  play.body.url_play.has_metadata = true;
  copyText(play.body.url_play.metadata.title, "Asked Title");
  sendFrame(client.get(), play);

  const auto frames = until(client.get(), [](const Frames& got) { return hasState(got, kPlaying); });
  const auto playing = firstState(frames, kPlaying);
  CHECK(std::string_view(playing.metadata.title) == "Asked Title");
  CHECK(std::string_view(playing.metadata.artist) == "Tag Artist");
  CHECK(std::string_view(playing.metadata.album) == "Tag Album");
  // rule 8: neither the url nor any metadata reaches a log line
  for (const char* secret : {"127.0.0.1", "tagged.wav", "Asked Title", "Tag Title", "Tag Artist", "Tag Album"})
    CHECK(rig.audiod().logs().count(secret) == 0);
}

TEST_CASE("RefusesNonHttpUrl") {
  UrlRig rig;
  auto client = rig.connect();
  const int fd = client.get();
  const std::vector<std::string> refused = {"file:///etc/hostname", "ftp://127.0.0.1/a.mp3", "data:audio/wav;base64,AA",
                                            "concat:http://a/1.mp3|http://a/2.mp3", "hls+file:///tmp/a.m3u8",
                                            "httpx://a/b", " http://a/b", "http://a/b c", "http://a/\r\nX: y", ""};
  for (const auto& url : refused) {
    INFO(url);
    // answered in the same turn, before ffmpeg could have been asked
    answered(fd, urlPlay("bad", url), "bad", "invalid");
  }
  answered(fd, urlPlay("bad", "file:///etc/hostname", kOverlayMode), "bad", "invalid");
  CHECK(rig.audiod().logs().count(event("url_open")) == 0);
  CHECK(rig.audiod().logs().count(event("url_failed")) == 0);

  // the scheme in any case
  rig.server().route("/tone.wav", {.contentType = "audio/wav", .body = toneWav(2.0)});
  const auto url = rig.server().url("/tone.wav");
  sendFrame(fd, urlPlay("upper", "HtTp" + url.substr(4)));
  const auto frames = until(fd, [](const Frames& got) { return hasResult(got, "upper"); });
  expectResult(frames, "upper", "");
}

// review focus 1: nothing but http and https, also behind a redirect or in a playlist
TEST_CASE("RefusesFileRedirect") {
  UrlRig rig;
  rig.server().route("/redirect", {.status = 302, .contentType = "text/plain", .location = "file:///etc/hostname"});
  auto client = rig.connect();

  sendFrame(client.get(), urlPlay("play", rig.server().url("/redirect")));
  const auto frames = until(client.get(), [](const Frames& got) { return hasResult(got, "play"); });
  expectResult(frames, "play", "open_failed");
  CHECK(frames.states.empty());
  CHECK(rig.server().requests("/redirect") == 1);
}

TEST_CASE("RefusesFileSegmentInPlaylist") {
  UrlRig rig;
  const auto playlist = [](const std::string& segment) {
    return "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:2.0,\n" + segment +
           "\n#EXT-X-ENDLIST\n";
  };
  constexpr std::string_view kPlaylistType = "application/vnd.apple.mpegurl";
  rig.server().route("/seg.flac", {.contentType = "audio/flac", .body = fixture("tone-44k.flac")});
  rig.server().route("/good.m3u8", {.contentType = std::string(kPlaylistType), .body = playlist("seg.flac")});
  rig.server().route("/file.m3u8",
                     {.contentType = std::string(kPlaylistType), .body = playlist("file:///etc/hostname")});
  rig.server().route("/concat.m3u8", {.contentType = std::string(kPlaylistType),
                                      .body = playlist("concat:" + rig.server().url("/seg.flac") + "|" +
                                                       rig.server().url("/seg.flac"))});
  auto client = rig.connect();
  const int fd = client.get();

  for (const std::string_view path : {"/file.m3u8", "/concat.m3u8"}) {
    INFO(path);
    sendFrame(fd, urlPlay("bad", rig.server().url(path)));
    const auto frames = until(fd, [](const Frames& got) { return hasResult(got, "bad"); });
    REQUIRE(frames.results.size() == 1);
    CHECK_FALSE(frames.results[0].ok);
    CHECK_FALSE(std::string_view(frames.results[0].code).empty());
    CHECK(frames.states.empty());
  }
  CHECK(rig.server().requests("/seg.flac") == 0);

  // the same playlist with an http segment plays
  sendFrame(fd, urlPlay("good", rig.server().url("/good.m3u8")));
  const auto frames = until(fd, [](const Frames& got) { return hasState(got, kPlaying); });
  expectResult(frames, "good", "");
  CHECK(rig.server().requests("/seg.flac") >= 1);
}

// a server that accepts and never answers: stop returns at once and the connection goes with the decoder thread
TEST_CASE("StopInterruptsBlockedRead") {
  UrlRig rig;
  rig.server().route("/stall", {.stall = true});
  auto client = rig.connect();
  const int fd = client.get();

  sendFrame(fd, urlPlay("play", rig.server().url("/stall")));
  REQUIRE(eventually([&] { return rig.server().stalled() == 1; }));
  std::this_thread::sleep_for(200ms);

  const auto asked = std::chrono::steady_clock::now();
  sendFrame(fd, mediaCommand("stop", kStop));
  const auto frames = until(fd, [](const Frames& got) { return hasResult(got, "stop") && hasResult(got, "play"); });
  const auto took = std::chrono::steady_clock::now() - asked;
  expectResult(frames, "stop", "");
  expectResult(frames, "play", "stopped");
  CHECK(took < 1s);
  // the decoder closed its connection before stop returned
  CHECK(eventually([&] { return rig.server().hangups("/stall") == 1; }, 500ms));
  CHECK(rig.audiod().logs().count(event("url_failed")) == 0);
}

// ffmpeg's name lookup ignores the interrupt: stop must not wait for it, or audiod's loop would miss cadenced's pings
TEST_CASE("StopNeverWaitsForALookupFfmpegCannotInterrupt") {
  const auto lookedUp = std::make_shared<std::atomic<bool>>(false);
  url::UrlOptions options;
  options.beforeOpen = [lookedUp] {
    std::this_thread::sleep_for(4s);
    *lookedUp = true;
  };
  UrlRig rig(options);
  rig.server().route("/tone.wav", {.contentType = "audio/wav", .body = toneWav(2.0)});
  auto client = rig.connect();
  const int fd = client.get();
  auto& music = rig.audiod().playback().music();

  sendFrame(fd, urlPlay("play", rig.server().url("/tone.wav")));
  std::this_thread::sleep_for(200ms);
  const auto asked = std::chrono::steady_clock::now();
  sendFrame(fd, mediaCommand("stop", kStop));
  const auto frames = until(fd, [](const Frames& got) { return hasResult(got, "stop") && hasResult(got, "play"); });
  CHECK(std::chrono::steady_clock::now() - asked < 1s);
  expectResult(frames, "stop", "");
  expectResult(frames, "play", "stopped");
  // the loop answers meanwhile
  expectPong(fd, 77);

  // once the lookup let the thread go, it opens nothing, pushes nothing and says nothing
  REQUIRE(eventually([&] { return lookedUp->load(); }, 6s));
  std::this_thread::sleep_for(300ms);
  CHECK(rig.server().requests("/tone.wav") == 0);
  CHECK(music.status().framesIn == 0);
  CHECK(untilPong(fd).results.empty());
  CHECK(rig.audiod().logs().count(event("url_open")) == 0);
  CHECK(rig.audiod().logs().count(event("url_failed")) == 0);
  CHECK(eventually([] { return url::runningDecoders() == 0; }, 1s));
}

TEST_CASE("audiod shuts down while a decoder waits on a lookup") {
  const auto lookedUp = std::make_shared<std::atomic<bool>>(false);
  url::UrlOptions options;
  options.beforeOpen = [lookedUp] {
    std::this_thread::sleep_for(4s);
    *lookedUp = true;
  };
  auto rig = std::make_unique<UrlRig>(options);
  rig->server().route("/tone.wav", {.contentType = "audio/wav", .body = toneWav(2.0)});
  auto client = rig->connect();
  sendFrame(client.get(), urlPlay("play", rig->server().url("/tone.wav")));
  std::this_thread::sleep_for(200ms);

  const auto shutting = std::chrono::steady_clock::now();
  client.reset();
  rig.reset();
  CHECK(std::chrono::steady_clock::now() - shutting < 1s);
  CHECK_FALSE(lookedUp->load());
  CHECK(url::runningDecoders() >= 1);

  // the decoder comes back to a source and an audiod that are gone, and only finishes
  REQUIRE(eventually([&] { return lookedUp->load(); }, 6s));
  CHECK(eventually([] { return url::runningDecoders() == 0; }, 1s));
}

TEST_CASE("an open that outlasts its deadline fails as timeout") {
  UrlRig rig(url::UrlOptions{.openDeadline = 300ms});
  rig.server().route("/stall", {.stall = true});
  auto client = rig.connect();

  sendFrame(client.get(), urlPlay("play", rig.server().url("/stall")));
  const auto frames = until(client.get(), [](const Frames& got) { return hasResult(got, "play"); }, 3s);
  expectResult(frames, "play", "timeout");
  CHECK(frames.states.empty());
  CHECK(rig.audiod().logs().count(R"("msg":"url_failed","code":"timeout")") == 1);
}

TEST_CASE("NotFoundFails") {
  UrlRig rig;
  rig.server().route("/broken", {.status = 500, .contentType = "text/plain", .body = "no"});
  auto client = rig.connect();
  const int fd = client.get();

  sendFrame(fd, urlPlay("missing", rig.server().url("/missing.mp3")));
  auto frames = until(fd, [](const Frames& got) { return hasResult(got, "missing"); });
  expectResult(frames, "missing", "not_found");
  CHECK(frames.states.empty());
  CHECK(rig.audiod().logs().eventually(R"("msg":"url_failed","code":"not_found")"));
  CHECK(rig.audiod().logs().count(R"("msg":"media_failed","code":"not_found","src":"s-1")") == 1);

  sendFrame(fd, urlPlay("broken", rig.server().url("/broken")));
  frames = until(fd, [](const Frames& got) { return hasResult(got, "broken"); });
  expectResult(frames, "broken", "http_error");
  CHECK(frames.states.empty());
}

TEST_CASE("IcyTitleUpdatesMetadata") {
  UrlRig rig;
  rig.server().route("/radio", {.contentType = "audio/wav",
                                .body = toneWav(6.0),
                                .icyMetaint = 16000,
                                .icyTitles = {{0, "Artist A - Title A"}, {40, "Artist B - Title B"}, {60, "Jingle"}}});
  auto client = rig.connect();
  auto play = urlPlay("play", rig.server().url("/radio"));
  play.body.url_play.has_metadata = true;
  copyText(play.body.url_play.metadata.artist, "Station");
  copyText(play.body.url_play.metadata.album, "Album");
  sendFrame(client.get(), play);

  std::vector<std::string> seen;
  const auto frames = until(client.get(), [&](const Frames& got) {
    seen.clear();
    for (const auto& state : got.states)
      if (state.state != kIdle)
        seen.push_back(std::string(state.metadata.artist) + "|" + state.metadata.title + "|" + state.metadata.album);
    return std::find(seen.begin(), seen.end(), "Station|Jingle|Album") != seen.end();
  });
  expectResult(frames, "play", "");
  const auto at = [&](const std::string& entry) { return std::find(seen.begin(), seen.end(), entry) - seen.begin(); };
  const auto a = at("Artist A|Title A|Album");
  const auto b = at("Artist B|Title B|Album");
  const auto jingle = at("Station|Jingle|Album");
  const auto end = static_cast<std::ptrdiff_t>(seen.size());
  CHECK(a < end);
  CHECK(b < end);
  CHECK(jingle < end);
  CHECK(a < b);
  CHECK(b < jingle);
  CHECK(rig.server().requests("/radio") == 1);
  // rule 8: neither the url nor any metadata reaches a log line
  for (const char* secret :
       {"127.0.0.1", "/radio", "Title A", "Title B", "Jingle", "Artist A", "Artist B", "Station", "Album"})
    CHECK(rig.audiod().logs().count(secret) == 0);
}

TEST_CASE("SeekMovesPosition") {
  UrlRig rig;
  const auto wav = steppedWav();
  rig.server().route("/steps.wav", {.contentType = "audio/wav", .body = wav, .ranges = true});
  auto client = rig.connect();
  const int fd = client.get();

  sendFrame(fd, urlPlay("play", rig.server().url("/steps.wav")));
  auto frames = until(fd, [](const Frames& got) { return hasState(got, kPlaying); });
  const auto playing = firstState(frames, kPlaying);
  CHECK(playing.duration_ms == 6000);
  const std::vector<cadence_audiod_v1_MediaAction> actions(playing.actions, playing.actions + playing.actions_count);
  CHECK(actions == std::vector{kPause, kResume, kStop, kSeek});

  std::this_thread::sleep_for(300ms);
  answered(fd, mediaCommand("seek", kSeek, 4000), "seek", "");
  frames = until(fd, [](const Frames& got) {
    return std::any_of(got.states.begin(), got.states.end(),
                       [](const MediaStateFrame& state) { return state.position_ms == 4000; });
  });
  REQUIRE_FALSE(frames.states.empty());

  // a resume says where the pause left it, and goes back there: by then the decoder read to the end, so ffmpeg asks
  // for the bytes from there on (44 bytes of header, 176400 a second; a forward seek may read on instead)
  std::this_thread::sleep_for(500ms);
  answered(fd, mediaCommand("pause", kPause), "pause", "");
  REQUIRE(hasState(until(fd, [](const Frames& got) { return hasState(got, kPaused); }), kPaused));
  std::this_thread::sleep_for(200ms);
  answered(fd, mediaCommand("resume", kResume), "resume", "");
  frames = until(fd, [](const Frames& got) { return hasState(got, kPlaying); });
  const auto resumed = firstState(frames, kPlaying);
  CHECK(resumed.position_ms >= 4300);
  CHECK(resumed.position_ms <= 4800);
  const auto expected = 44 + static_cast<long long>(resumed.position_ms) * 1764 / 10;
  const auto askedThere = [&] {
    const auto ranges = rig.server().ranges("/steps.wav");
    return std::any_of(ranges.begin(), ranges.end(), [&](const std::string& range) {
      return std::llabs(std::stoll(range.substr(6)) - expected) < 16384;
    });
  };
  CHECK(eventually(askedThere, 2s));

  REQUIRE(hasState(until(fd, [](const Frames& got) { return hasState(got, kIdle); }), kIdle));
  disconnect(rig.audiod(), client);

  // 300 ms of the first second, none of the three skipped, then the fifth and the sixth
  const auto mix = mixOf(rig);
  CHECK(windowsWith(mix, 0, 400, 0.4, kStepWindow) >= 8);
  for (const double skipped : {600.0, 800.0, 1000.0}) {
    INFO(skipped);
    CHECK(windowsWith(mix, 0, skipped, 0.4, kStepWindow) <= 2);
  }
  CHECK(windowsWith(mix, 0, 1200, 0.4, kStepWindow) >= 25);
  CHECK(windowsWith(mix, 0, 1400, 0.4, kStepWindow) >= 40);
}

// the session ends only once the backend read the last of it, not when the decoder reached the end
TEST_CASE("EndedAfterDrain") {
  UrlRig rig;
  rig.server().route("/tone.wav", {.contentType = "audio/wav", .body = toneWav(2.0)});
  auto client = rig.connect();
  const int fd = client.get();
  auto& music = rig.audiod().playback().music();

  sendFrame(fd, urlPlay("play", rig.server().url("/tone.wav")));
  REQUIRE(hasState(until(fd, [](const Frames& got) { return hasState(got, kPlaying); }), kPlaying));
  const auto started = std::chrono::steady_clock::now();
  REQUIRE(hasState(until(fd, [](const Frames& got) { return hasState(got, kIdle); }), kIdle));
  const auto took = std::chrono::steady_clock::now() - started;
  CHECK(music.status().queued == 0);
  CHECK(took >= 1900ms);
  CHECK(took <= 2600ms);
}

namespace {

// what a source reported, from any thread: the test posts reports at once instead of through a loop
class Recorded final : public MediaSourceEvents {
 public:
  void playing() override {}
  void paused() override {}
  void buffering() override {}
  void ended() override {}
  void failed(std::string_view /*code*/) override { failed_ = true; }
  void metadata(const MediaMetadata& /*metadata*/) override {}
  void duration(std::uint64_t /*ms*/) override {}
  void position(std::uint64_t /*ms*/) override {}

  [[nodiscard]] bool hasFailed() const { return failed_.load(); }

 private:
  std::atomic<bool> failed_{false};
};

}  // namespace

// Task 4's tail contract: a source that ends or fails on its own leaves what it queued to play out; a stop cuts it
TEST_CASE("a source that failed on its own leaves its queue, and a stop cuts it") {
  HttpServer server;
  server.route("/stall", {.stall = true});
  LogCapture logs;
  const auto log = logs.log();
  const auto immediately = [](std::function<void()> task) { task(); };
  const std::vector<float> queued(4800 * 2, 0.25f);

  SECTION("failed") {
    PcmInput input;
    REQUIRE(input.push(std::span<const float>(queued), 48000, 2));
    const auto events = std::make_shared<Recorded>();
    UrlRequest request;
    request.url = server.url("/missing");
    auto source = std::make_unique<url::UrlSource>(log, request, events, immediately);
    source->openOutput(input);
    REQUIRE(eventually([&] { return events->hasFailed(); }));
    source.reset();
    CHECK(input.status().queued == 4800);
  }
  SECTION("stopped") {
    PcmInput input;
    REQUIRE(input.push(std::span<const float>(queued), 48000, 2));
    UrlRequest request;
    request.url = server.url("/stall");
    auto source = std::make_unique<url::UrlSource>(log, request, std::make_shared<Recorded>(), immediately);
    source->openOutput(input);
    REQUIRE(eventually([&] { return server.stalled() == 1; }));
    CHECK(source->stop().empty());
    CHECK(input.status().queued == 0);
  }
}

// regression: a group leader's teardown closed a url source on the feed its stopped leader no longer read, and a push
// that began just after the close's clear waited on that full input for good
TEST_CASE("closing a source on an input nobody reads never waits for good") {
  HttpServer server;
  // two minutes: every close drops up to the input's 2 s, so it lasts the rounds below
  const auto longWav = wavBytes(8000, 120 * 8000, [](std::size_t frame) {
    const double value = 0.25 * std::sin(2 * std::numbers::pi * 400 * static_cast<double>(frame) / 8000);
    return std::pair{value, value};
  });
  server.route("/long.wav", {.contentType = "audio/wav", .body = longWav});
  LogCapture logs;
  const auto log = logs.log();
  const auto immediately = [](std::function<void()> task) { task(); };
  PcmInput input;
  UrlRequest request;
  request.url = server.url("/long.wav");
  auto source = std::make_unique<url::UrlSource>(log, request, std::make_shared<Recorded>(), immediately);
  source->openOutput(input);
  for (int i = 0; i < 40; ++i) {
    // nothing reads the input: once it is full every push waits on it
    REQUIRE(eventually([&] { return input.status().queued > 0; }));
    auto closed = std::async(std::launch::async, [&] { source->closeOutput(); });
    REQUIRE(closed.wait_for(2s) == std::future_status::ready);
    source->openOutput(input);
  }
  source.reset();
}
