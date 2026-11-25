// what the screen builds is what cadenced's contract says, byte for byte against the golden frames
#include "app/convert.h"
#include "support/env.h"

#include <catch2/catch_test_macros.hpp>

using namespace cadence::screen;
using cadence::screen::test::fixture;

namespace {

std::string encoded(const ipc::OutEnvelope& e) {
  std::vector<std::uint8_t> frame;
  REQUIRE(ipc::encodeEnvelope(e.get(), frame));
  return {frame.begin(), frame.end()};
}

ipc::DecodedEnvelope decode(const std::string& name) {
  const std::string bytes = fixture(name);
  auto e = ipc::decodeEnvelope({reinterpret_cast<const std::uint8_t*>(bytes.data()), bytes.size()}, nullptr);
  REQUIRE(e);
  return e;
}

}  // namespace

TEST_CASE("MediaState from the player matches the golden frame") {
  player::PlayerState s;
  s.sessionId = "sess-v1";
  s.state = cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING;
  s.positionMs = 61000;
  s.positionAtMs = 1762600001000;
  s.rate = 1;
  s.durationMs = 1440000;
  s.tracks = {{"a1", cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_AUDIO, "ru", "", true, false, ""}};
  s.audioRoute = cadence_v2_AudioRoute_AUDIO_ROUTE_MIXER;
  s.audioControls = {cadence_v2_AudioControl_AUDIO_CONTROL_VOLUME, cadence_v2_AudioControl_AUDIO_CONTROL_DUCK,
                     cadence_v2_AudioControl_AUDIO_CONTROL_MUTE,   cadence_v2_AudioControl_AUDIO_CONTROL_EQ,
                     cadence_v2_AudioControl_AUDIO_CONTROL_VIZ_PCM, cadence_v2_AudioControl_AUDIO_CONTROL_VIZ_BANDS};
  s.bufferedMs = 30000;
  s.purpose = cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT;
  CHECK(encoded(app::mediaStateEnvelope(s)) == fixture("media_state"));
}

TEST_CASE("a MediaState waiting for the cache carries its fill") {
  player::PlayerState s;
  s.sessionId = "video-7";
  s.state = cadence_v2_PlaybackState_PLAYBACK_STATE_BUFFERING;
  s.positionMs = 61000;
  s.positionAtMs = 1762600001000;
  s.durationMs = 1440000;
  s.audioRoute = cadence_v2_AudioRoute_AUDIO_ROUTE_MIXER;
  s.bufferedMs = 1200;
  s.bufferingPercent = 37;
  s.purpose = cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT;
  CHECK(encoded(app::mediaStateEnvelope(s)) == fixture("media_state_buffering"));
}

TEST_CASE("PlayerStats match their golden frame") {
  player::PlayerStats s;
  s.sessionId = "sess-v1";
  s.cacheSpeedBps = 2500000;
  s.inputRateBps = 2400000;
  s.cacheAheadBytes = 52428800;
  s.cacheTotalBytes = 73400320;
  s.bufferedMs = 42000;
  s.lowLevelSeeks = 2;
  s.byteLevelSeeks = 5;
  s.underrun = true;
  s.frameDrops = 4;
  s.decoderDrops = 1;
  s.voDelayedFrames = 3;
  s.estimatedVfFps = 23.5;
  s.displayFps = 60;
  s.avsyncMs = -12.5;
  s.hwdec = "mediacodec";
  CHECK(encoded(app::playerStatsEnvelope(s)) == fixture("player_stats"));
}

TEST_CASE("a gated and an ended MediaState match their golden frames") {
  player::PlayerState s;
  s.sessionId = "video-7";
  s.state = cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED;
  s.positionAtMs = 1762600001000;
  s.audioRoute = cadence_v2_AudioRoute_AUDIO_ROUTE_MIXER;
  s.purpose = cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT;
  s.gated = true;
  CHECK(encoded(app::mediaStateEnvelope(s)) == fixture("media_state_gated"));
  player::PlayerState e;
  e.sessionId = "video-7";
  e.state = cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE;
  e.errorCode = "network";
  e.purpose = cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT;
  e.ended = true;
  CHECK(encoded(app::mediaStateEnvelope(e)) == fixture("media_state_ended"));
}

TEST_CASE("MediaPlay's renditions reach the play request, by name and never a url") {
  const auto e = decode("media_play_renditions");
  const auto r = app::playRequestOf(e->body.media_play);
  REQUIRE(r.renditions.size() == 3);
  CHECK(r.renditions[0].name == "1080p");
  CHECK(r.renditions[0].height == 1080);
  CHECK(r.renditions[0].codecs == std::vector<std::string>{"vp9", "opus"});
  CHECK_FALSE(r.renditions[0].selected);
  CHECK(r.renditions[1].name == "720p60");
  CHECK(r.renditions[1].fps == 60);
  CHECK(r.renditions[1].selected);
}

TEST_CASE("CommandResult, LogLine and InputEvent match their golden frames") {
  CHECK(encoded(app::commandResultEnvelope("q-4", "gated")) == fixture("command_result"));
  CHECK(encoded(app::logLineEnvelope({"mpv", ipc::Level::Warn, "hwdec_fallback code=unsupported", true, false})) ==
        fixture("log_line"));
  platform::KeyEvent k{cadence_screen_v1_Key_KEY_ASSISTANT, cadence_screen_v1_KeyAction_KEY_ACTION_DOWN,
                       cadence_screen_v1_InputSource_INPUT_SOURCE_REMOTE};
  CHECK(encoded(app::inputEventEnvelope(k, 1762600000000)) == fixture("input_event"));
}

TEST_CASE("ScreenStatus matches its golden frame") {
  platform::DisplayInfo d;
  d.pixels = {1920, 1080};
  d.refreshMhz = 59940;
  app::UiStatus ui{cadence_v2_ScreenRoute_SCREEN_ROUTE_PLAYER, false};
  CHECK(encoded(app::screenStatusEnvelope(cadence_screen_v1_ScreenPower_SCREEN_POWER_ON, ui, d)) ==
        fixture("screen_status"));
}

TEST_CASE("Hello carries what the screen is") {
  app::HelloInfo info;
  info.version = "0.1.0";
  info.platform = cadence_screen_v1_Platform_PLATFORM_LINUX;
  info.display.pixels = {1920, 1080};
  info.display.refreshMhz = 60000;
  info.hwdec = {"auto-safe"};
  info.maxVideoHeight = 1080;
  info.cardVersions = {1};
  info.cardFeatures = {"card", "post.quote", std::string(40, 'x')};
  const auto out = app::helloEnvelope(info);
  std::vector<std::uint8_t> frame;
  REQUIRE(ipc::encodeEnvelope(out.get(), frame));
  auto back = ipc::decodeEnvelope(frame, nullptr);
  REQUIRE(back);
  CHECK(back->body.hello.protocol == app::kProtocol);
  CHECK(std::string(back->body.hello.hwdec[0]) == "auto-safe");
  CHECK(back->body.hello.display.height == 1080);
  CHECK(back->body.hello.card_versions[0] == 1);
  // a feature name too long for the frame is left out, never cut
  REQUIRE(back->body.hello.card_features_count == 2);
  CHECK(std::string(back->body.hello.card_features[1]) == "post.quote");
}

TEST_CASE("MediaPlay, MediaCommand and SetEq become the player's requests") {
  auto play = decode("media_play");
  const auto r = app::playRequestOf(play->body.media_play);
  CHECK(r.sessionId == "sess-v1");
  CHECK(r.url == "http://192.168.1.10:8090/play/abc/1");
  CHECK(r.audioUrl == "http://192.168.1.10:8090/play/abc/2");
  CHECK(r.headers.at("Referer") == "https://example.org/");
  REQUIRE(r.subtitles.size() == 1);
  CHECK(r.subtitles[0].isDefault);
  CHECK(r.subtitles[0].language == "ru");
  CHECK(r.startMs == 60000);

  auto cmd = decode("media_command");
  const auto c = app::commandOf(cmd->body.media_command);
  REQUIRE(c);
  CHECK(c->action == player::CommandAction::SelectTrack);
  CHECK(c->trackId == "a2");

  cadence_screen_v1_MediaCommand next{};
  next.action = cadence_screen_v1_MediaAction_MEDIA_ACTION_NEXT;
  CHECK_FALSE(app::commandOf(next));

  auto eq = decode("set_eq");
  const auto e = app::eqOf(eq->body.set_eq);
  CHECK(e.preampDb == -2);
  REQUIRE(e.bands.size() == 1);
  CHECK(e.bands[0].gainDb == 3);
}
