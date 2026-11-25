// the c++ decode test w0-c1 deferred: every golden screen/v1 frame decodes with nanopb (PB_ENABLE_MALLOC) and
// encodes back to the same bytes, and the fields the screen acts on read as the fixture's txtpb says
#include "ipc/codec.h"
#include "support/env.h"

#include <catch2/catch_test_macros.hpp>

#include <cstring>
#include <filesystem>
#include <string>
#include <vector>

using namespace cadence::screen;
using cadence::screen::test::fixture;

namespace {

ipc::DecodedEnvelope decode(const std::string& name) {
  const std::string bytes = fixture(name);
  std::string error;
  auto e = ipc::decodeEnvelope({reinterpret_cast<const std::uint8_t*>(bytes.data()), bytes.size()}, &error);
  INFO(name << ": " << error);
  REQUIRE(e);
  return e;
}

std::string str(const char* s) { return s != nullptr ? s : ""; }

}  // namespace

TEST_CASE("every golden frame decodes and encodes back byte for byte") {
  std::vector<std::string> names;
  for (const auto& entry : std::filesystem::directory_iterator(CADENCE_SCREEN_FIXTURES)) {
    if (entry.path().extension() == ".binpb") names.push_back(entry.path().stem().string());
  }
  // the contract's 38 frames; a new one joins this test by itself
  REQUIRE(names.size() >= 38);
  for (const auto& name : names) {
    INFO(name);
    const std::string bytes = fixture(name);
    auto e = decode(name);
    CHECK(e->which_body != 0);
    CHECK(std::string(ipc::bodyName(e->which_body)) != "unknown");
    std::vector<std::uint8_t> again;
    REQUIRE(ipc::encodeEnvelope(*e, again));
    CHECK(std::string(again.begin(), again.end()) == bytes);
  }
}

TEST_CASE("a frame that is not an envelope is refused") {
  const std::uint8_t junk[] = {0xff, 0xff, 0xff, 0xff, 0x0f};
  std::string error;
  CHECK_FALSE(ipc::decodeEnvelope(junk, &error));
  CHECK(error == "decode");
}

TEST_CASE("golden: hello") {
  auto e = decode("hello");
  REQUIRE(e->which_body == cadence_screen_v1_Envelope_hello_tag);
  const auto& h = e->body.hello;
  CHECK(h.protocol == 1);
  CHECK(str(h.version) == "0.1.0");
  CHECK(h.platform == cadence_screen_v1_Platform_PLATFORM_ANDROID);
  CHECK(h.display.width == 1920);
  CHECK(h.display.refresh_mhz == 60000);
  REQUIRE(h.display.hdr_count == 2);
  CHECK(str(h.display.hdr[1]) == "hlg");
  REQUIRE(h.video_codecs_count == 3);
  CHECK(str(h.video_codecs[2]) == "vp9");
  CHECK(h.max_video_height == 2160);
  CHECK(h.mirroring);
}

TEST_CASE("golden: ready carries the gate") {
  auto e = decode("ready");
  REQUIRE(e->which_body == cadence_screen_v1_Envelope_ready_tag);
  const auto& r = e->body.ready;
  CHECK(str(r.device_name) == "TV");
  CHECK(str(r.room_name) == "Living room");
  CHECK(str(r.tap_path) == "@cadence-tap");
  CHECK(r.gate_open);
}

TEST_CASE("golden: media_play with its pointer fields") {
  auto e = decode("media_play");
  REQUIRE(e->which_body == cadence_screen_v1_Envelope_media_play_tag);
  const auto& p = e->body.media_play;
  CHECK(str(p.session_id) == "sess-v1");
  CHECK(str(p.url) == "http://192.168.1.10:8090/play/abc/1");
  CHECK(str(p.audio_url) == "http://192.168.1.10:8090/play/abc/2");
  REQUIRE(p.headers_count == 1);
  CHECK(str(p.headers[0].key) == "Referer");
  REQUIRE(p.subtitles_count == 1);
  CHECK(str(p.subtitles[0].label) == "Russian");
  CHECK(p.subtitles[0].format == cadence_screen_v1_SubtitleFormat_SUBTITLE_FORMAT_ASS);
  CHECK(p.start_ms == 60000);
  REQUIRE(p.has_metadata);
  CHECK(str(p.metadata.title) == "Episode title");
  REQUIRE(p.skips_count == 1);
  CHECK(p.skips[0].end_ms == 120000);
  REQUIRE(p.actions_count == 2);
  CHECK(str(p.actions[1]) == "previous");
}

TEST_CASE("golden: the player's controls") {
  auto gate = decode("playback_gate");
  CHECK(gate->body.playback_gate.open);
  auto level = decode("set_level");
  CHECK(level->body.set_level.level == 35);
  CHECK(level->body.set_level.ramp_ms == 300);
  auto eq = decode("set_eq");
  CHECK(eq->body.set_eq.preamp_db == -2);
  REQUIRE(eq->body.set_eq.bands_count == 1);
  CHECK(eq->body.set_eq.bands[0].freq_hz == 1000);
  auto duck = decode("duck");
  REQUIRE(duck->which_body == cadence_screen_v1_Envelope_duck_tag);
  CHECK(duck->body.duck.active);
  CHECK(duck->body.duck.ramp_ms == 200);
  CHECK(duck->body.duck.reason == cadence_v2_DuckReason_DUCK_REASON_ASSISTANT);
  auto cmd = decode("media_command");
  CHECK(cmd->body.media_command.action == cadence_screen_v1_MediaAction_MEDIA_ACTION_SELECT_TRACK);
  CHECK(str(cmd->body.media_command.track_id) == "a2");
}

TEST_CASE("golden: capability_config and the log line") {
  auto c = decode("capability_config");
  CHECK(str(c->body.capability_config.capability) == "display.screen");
  CHECK(str(c->body.capability_config.json) == R"({"overscan":2.5})");
  auto l = decode("log_line");
  CHECK(str(l->body.log_line.source) == "mpv");
  CHECK(l->body.log_line.level == cadence_screen_v1_LogLevel_LOG_LEVEL_WARN);
  CHECK(l->body.log_line.truncated);
}

TEST_CASE("golden: the cadence.v2 messages passing through decode too") {
  for (const char* name : {"assistant_turn", "cards", "rpc_request", "rpc_response", "rpc_event", "rpc_cancel",
                           "screen_context", "voice_level"}) {
    INFO(name);
    auto e = decode(name);
    CHECK(std::string(ipc::bodyName(e->which_body)) == name);
  }
}

TEST_CASE("golden: contract batch 4's frames") {
  auto st = decode("media_session_state");
  REQUIRE(st->which_body == cadence_screen_v1_Envelope_media_session_state_tag);
  const auto& s = st->body.media_session_state;
  CHECK(str(s.session_id) == "video-3");
  REQUIRE(s.actions_count == 2);
  CHECK(str(s.actions[0]) == "next");
  REQUIRE(s.up_next_count == 1);
  CHECK(str(s.up_next[0].ref) == "e:6");
  CHECK(str(s.up_next[0].title) == "Next episode");
  CHECK(s.has_queue_position);
  CHECK(s.queue_position == 5);
  REQUIRE(s.item_actions_count == 1);
  CHECK(s.item_actions[0].toggled);
  REQUIRE(s.panels_count == 1);
  CHECK(s.panels[0].kind == cadence_v2_MediaPanelKind_MEDIA_PANEL_KIND_COMMENTS);

  auto tasks = decode("tasks_summary");
  REQUIRE(tasks->which_body == cadence_screen_v1_Envelope_tasks_summary_tag);
  CHECK(tasks->body.tasks_summary.running == 2);
  CHECK(tasks->body.tasks_summary.waiting == 1);
  CHECK(tasks->body.tasks_summary.finished == 3);

  auto mic = decode("mic_choice");
  REQUIRE(mic->which_body == cadence_screen_v1_Envelope_mic_choice_tag);
  CHECK(mic->body.mic_choice.kind == cadence_v2_MicSourceKind_MIC_SOURCE_KIND_SPEAKER);
  CHECK(str(mic->body.mic_choice.device_id) == "0f8fad5b-d9cb-469f-a165-70867728950e");

  auto toast = decode("toast_action");
  REQUIRE(toast->body.toast.has_action);
  const auto& a = toast->body.toast.action;
  CHECK(a.kind == cadence_screen_v1_ToastActionKind_TOAST_ACTION_KIND_OPEN);
  CHECK(a.route == cadence_v2_ScreenRoute_SCREEN_ROUTE_TASK);
  CHECK(str(a.ref) == "ab12");
  CHECK(str(a.label) == "Открыть");

  auto link = decode("link_status_online");
  CHECK(str(link->body.link_status.server_url) == "https://cadence.example:8443");
  auto stop = decode("mirror_stop_stopped");
  CHECK(str(stop->body.mirror_stop.reason) == "stopped");
}
