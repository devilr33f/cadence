#include "player/mpv-options.h"
#include "player/mpv-tracks.h"
#include "player/state-report.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>

using namespace cadence::screen::player;

namespace {

std::string option(const MpvOptions& o, const std::string& key) {
  const auto it = std::find_if(o.begin(), o.end(), [&](const auto& kv) { return kv.first == key; });
  return it == o.end() ? "<unset>" : it->second;
}

}  // namespace

TEST_CASE("linux: the render api, auto-safe hwdec, the stock ao at volume 0") {
  MpvConfig c;
  const auto o = mpvInitOptions(c);
  CHECK(option(o, "vo") == "libmpv");
  CHECK(option(o, "hwdec") == "auto-safe");
  CHECK(option(o, "ao") == "<unset>");
  CHECK(option(o, "volume") == "0");
  CHECK(option(o, "ytdl") == "no");
  CHECK(option(o, "config") == "no");
  CHECK(option(o, "input-default-bindings") == "no");
}

TEST_CASE("mpv's per-frame fence is never made: glFenceSync stubbed, buffer storage withheld") {
  CHECK(mpvStubbedGlFunction("glFenceSync"));
  CHECK_FALSE(mpvStubbedGlFunction("glDeleteSync"));
  CHECK_FALSE(mpvStubbedGlFunction(nullptr));
  CHECK(mpvWithheldGlFunction("glBufferStorage"));
  CHECK(mpvWithheldGlFunction("glBufferStorageEXT"));
  CHECK_FALSE(mpvWithheldGlFunction("glFenceSync"));
  CHECK_FALSE(mpvWithheldGlFunction("glClientWaitSync"));
  CHECK_FALSE(mpvWithheldGlFunction("glDrawArrays"));
  CHECK_FALSE(mpvWithheldGlFunction(nullptr));
}

TEST_CASE("a board's profile goes first, so the options after it win") {
  MpvConfig c;
  CHECK(option(mpvInitOptions(c), "profile") == "<unset>");
  c.profile = "fast";
  const auto o = mpvInitOptions(c);
  REQUIRE_FALSE(o.empty());
  CHECK(o.front().first == "profile");
  CHECK(o.front().second == "fast");
}

TEST_CASE("android: mpv on its own surface, mediacodec") {
  MpvConfig c;
  c.videoPath = VideoPath::SurfaceEmbed;
  const auto o = mpvInitOptions(c);
  CHECK(option(o, "vo") == "mediacodec_embed");
  CHECK(option(o, "hwdec") == "mediacodec");
}

TEST_CASE("ao_cadence gets its ring and eventfd, and no volume of the player's") {
  MpvConfig c;
  c.ao = AoMode::Cadence;
  c.ringFd = 7;
  c.eventFd = 8;
  const auto o = mpvInitOptions(c);
  CHECK(option(o, "ao") == "cadence");
  CHECK(option(o, "ao-cadence-ring-fd") == "7");
  CHECK(option(o, "ao-cadence-event-fd") == "8");
  CHECK(option(o, "volume") == "<unset>");
}

TEST_CASE("an ao override wins (tests run ao=null)") {
  MpvConfig c;
  c.aoOverride = "null";
  c.hwdec = "no";
  const auto o = mpvInitOptions(c);
  CHECK(option(o, "ao") == "null");
  CHECK(option(o, "hwdec") == "no");
}

TEST_CASE("loadfile: gated content starts paused, headers quoted whole") {
  PlayRequest r;
  r.url = "http://h/play/1";
  r.startMs = 60000;
  r.headers = {{"Referer", "https://example.org/a,b"}};
  const auto route = audioRouteFor(AoMode::Stock, cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT);
  const auto cmd = loadfileCommand(r, true, route);
  REQUIRE(cmd.size() == 5);
  CHECK(cmd[0] == "loadfile");
  CHECK(cmd[1] == "http://h/play/1");
  CHECK(cmd[2] == "replace");
  CHECK(cmd[4] ==
        "pause=yes,start=%6%60.000,aid=auto,mute=no,http-header-fields=%33%Referer: https://example.org/a\\,b");
}

TEST_CASE("loadfile: ambient has no audio track") {
  PlayRequest r;
  r.url = "https://sylvan.apple.com/x.mov";
  const auto route = audioRouteFor(AoMode::Stock, cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_AMBIENT);
  const auto cmd = loadfileCommand(r, false, route);
  CHECK(cmd[4] == "pause=no,start=%4%none,aid=no,mute=yes,http-header-fields=%0%");
}

// media.mpv's next-load options ride every loadfile, quoted whole (a language list has commas), before the live
// stream's own cache bound, which wins
TEST_CASE("loadfile: media.mpv's next-load options ride along, the live cache bound last") {
  PlayRequest r;
  r.url = "http://h/play/1";
  const auto route = audioRouteFor(AoMode::Stock, cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT);
  MpvOptions settings{{"alang", "rus,ru,jpn,eng"}, {"cache-secs", "600"}, {"hwdec", "v4l2m2m-copy"}};
  const auto cmd = loadfileCommand(r, false, route, settings);
  CHECK(cmd[4] ==
        "pause=no,start=%4%none,aid=auto,mute=no,http-header-fields=%0%,alang=%14%rus,ru,jpn,eng,"
        "cache-secs=%3%600,hwdec=%12%v4l2m2m-copy");
  r.live = true;
  const auto live = loadfileCommand(r, false, route, settings);
  const auto options = live[4];
  CHECK(options.find("cache-secs=%3%600") != std::string::npos);
  CHECK(options.substr(options.rfind(',')) == ",cache-secs=%2%10");
}

TEST_CASE("tracks: mpv's list becomes MediaState's, cover art dropped") {
  std::vector<MpvTrack> list = {
      {1, "video", "", "", true, false, false, ""},  {2, "video", "", "", false, false, true, ""},
      {1, "audio", "eng", "Main", true, false, false, ""}, {2, "audio", "rus", "", false, false, false, ""},
      {1, "sub", "rus", "Russian", false, true, false, ""}, {9, "data", "", "", false, false, false, ""},
  };
  const auto t = toTracks(list);
  REQUIRE(t.size() == 4);
  CHECK(t[0] == Track{"v1", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_VIDEO, "", "", true, false, ""});
  CHECK(t[1] == Track{"a1", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_AUDIO, "en", "Main", true, false, ""});
  CHECK(t[2].id == "a2");
  CHECK(t[2].language == "ru");
  CHECK(t[3] == Track{"s1", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_SUBTITLE, "ru", "Russian", false, true, ""});
}

TEST_CASE("select_track: ids name their kind, an empty id turns the kind off") {
  auto a = trackSelection("a2", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_UNSPECIFIED);
  REQUIRE(a);
  CHECK(std::string(a->property) == "aid");
  CHECK(a->value == "2");
  auto off = trackSelection("", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_SUBTITLE);
  REQUIRE(off);
  CHECK(std::string(off->property) == "sid");
  CHECK(off->value == "no");
  CHECK_FALSE(trackSelection("", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_UNSPECIFIED));
  CHECK_FALSE(trackSelection("x1", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_UNSPECIFIED));
  CHECK_FALSE(trackSelection("a", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_UNSPECIFIED));
  CHECK_FALSE(trackSelection("a0", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_UNSPECIFIED));
  CHECK_FALSE(trackSelection("a1;quit", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_UNSPECIFIED));
}

TEST_CASE("languages: iso 639-2 as bcp 47") {
  CHECK(bcp47("rus") == "ru");
  CHECK(bcp47("ENG") == "en");
  CHECK(bcp47("pt-BR") == "pt-br");
  CHECK(bcp47("") == "");
}

TEST_CASE("playback state from mpv's flags") {
  CHECK(playbackStateFor({}) == cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE);
  CHECK(playbackStateFor({true, false, false, false, false, false}) == cadence_v2_PlaybackState_PLAYBACK_STATE_BUFFERING);
  CHECK(playbackStateFor({true, true, false, false, false, false}) == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING);
  CHECK(playbackStateFor({true, true, true, false, false, false}) == cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED);
  CHECK(playbackStateFor({true, true, false, true, false, false}) == cadence_v2_PlaybackState_PLAYBACK_STATE_BUFFERING);
  CHECK(playbackStateFor({true, true, false, false, false, true}) == cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE);
}

TEST_CASE("a steady play reports nothing; a seek, a state or a buffer change does") {
  PlayerState a;
  a.sessionId = "s";
  a.state = cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING;
  a.rate = 1;
  a.positionMs = 10000;
  a.positionAtMs = 1000;
  PlayerState b = a;
  b.positionMs = 15000;
  b.positionAtMs = 6000;
  CHECK_FALSE(worthReporting(a, b));
  b.positionMs = 40000;
  CHECK(worthReporting(a, b));
  b = a;
  b.state = cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED;
  CHECK(worthReporting(a, b));
  b = a;
  b.positionMs = 15000;
  b.positionAtMs = 6000;
  b.bufferedMs = 900;
  CHECK_FALSE(worthReporting(a, b));
  b.bufferedMs = 1000;
  CHECK(worthReporting(a, b));
  // a fifth of a full buffer, at most once a second
  a.bufferedMs = 60000;
  b.bufferedMs = 49000;
  CHECK_FALSE(worthReporting(a, b));
  b.bufferedMs = 48000;
  CHECK(worthReporting(a, b));
  b.positionMs = 10500;
  b.positionAtMs = 1500;
  CHECK_FALSE(worthReporting(a, b));
  // waiting for the cache: its start and end at once, its fill once a second
  b.bufferedMs = a.bufferedMs;
  b.bufferingPercent = 10;
  CHECK(worthReporting(a, b));
  a.bufferingPercent = 10;
  b.bufferingPercent = 40;
  CHECK_FALSE(worthReporting(a, b));
  b.positionMs = 15000;
  b.positionAtMs = 6000;
  CHECK(worthReporting(a, b));
  b.bufferingPercent.reset();
  b.positionMs = 10500;
  b.positionAtMs = 1500;
  CHECK(worthReporting(a, b));
  a.bufferedMs = 0;
  a.bufferingPercent.reset();
  b = a;
  b.gated = true;
  CHECK(worthReporting(a, b));
}

// the stick: ao_cadence's default 40 ms never reached the reader's refill target (half of 8192 frames), so a mixer
// session played silence; its buffer follows the ring now
TEST_CASE("ao_cadence takes the ring's fds and a buffer of three quarters of it") {
  const auto o = ringOptions(11, 12, 8192);
  CHECK(option(o, "ao-cadence-ring-fd") == "11");
  CHECK(option(o, "ao-cadence-event-fd") == "12");
  // 6144 frames at 48 kHz: past the reader's 4096-frame refill target
  CHECK(option(o, "ao-cadence-buffer") == "128");
  CHECK(std::stoi(option(o, "ao-cadence-buffer")) * 48 > 8192 / 2);
  // within ao_cadence's 5 to 1000 ms
  CHECK(option(ringOptions(1, 2, 256), "ao-cadence-buffer") == "5");
  CHECK(option(ringOptions(1, 2, 65536), "ao-cadence-buffer") == "1000");
}

namespace {

mpv_node str(const char* s) {
  mpv_node n{};
  n.format = MPV_FORMAT_STRING;
  n.u.string = const_cast<char*>(s);
  return n;
}

mpv_node num(std::int64_t v) {
  mpv_node n{};
  n.format = MPV_FORMAT_INT64;
  n.u.int64 = v;
  return n;
}

}  // namespace

TEST_CASE("audio-params as the logs read them") {
  char k0[] = "format";
  char k1[] = "samplerate";
  char k2[] = "channel-count";
  char* keys[] = {k0, k1, k2};
  mpv_node values[] = {str("floatp"), num(44100), num(2)};
  mpv_node_list list{3, values, keys};
  mpv_node map{};
  map.format = MPV_FORMAT_NODE_MAP;
  map.u.list = &list;
  const auto p = parseAudioParams(map);
  CHECK(p.rate == 44100);
  CHECK(p.channels == 2);
  CHECK(p.format == "floatp");
  // none while mpv has no audio
  mpv_node none{};
  none.format = MPV_FORMAT_NONE;
  CHECK(parseAudioParams(none) == MpvAudioParams{});
}

TEST_CASE("the audio track selected again once the ring came: a split stream's external one first") {
  std::vector<MpvTrack> tracks = {{1, "video", "", "", true, false, false, ""}, {1, "audio", "", "", false, false, false, ""}};
  CHECK(audioTrackToRetry(tracks) == 1);
  tracks.push_back({2, "audio", "", "", false, true, false, ""});
  CHECK(audioTrackToRetry(tracks) == 2);
  CHECK_FALSE(audioTrackToRetry({{1, "video", "", "", true, false, false, ""}}).has_value());
}
