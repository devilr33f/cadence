#include <algorithm>
#include <catch2/catch_test_macros.hpp>

#include <pb_decode.h>
#include <pb_encode.h>

#include <cstdint>
#include <filesystem>
#include <fstream>
#include <iterator>
#include <string>
#include <vector>

#include "cadence/audiod/v1/audiod.pb.h"

namespace {

std::vector<std::uint8_t> read_file(const std::filesystem::path& path) {
  std::ifstream in(path, std::ios::binary);
  REQUIRE(in.good());
  return {std::istreambuf_iterator<char>(in), std::istreambuf_iterator<char>()};
}

std::vector<std::filesystem::path> golden_frames() {
  std::vector<std::filesystem::path> frames;
  for (const auto& entry : std::filesystem::directory_iterator(CADENCE_AUDIOD_FIXTURES)) {
    if (entry.path().extension() == ".binpb") frames.push_back(entry.path());
  }
  std::sort(frames.begin(), frames.end());
  return frames;
}

}  // namespace

TEST_CASE("golden audiod frames decode and re-encode byte-identical") {
  const auto frames = golden_frames();
  REQUIRE(frames.size() >= 45);
  for (const auto& path : frames) {
  DYNAMIC_SECTION(path.filename().string()) {
  const auto golden = read_file(path);
  cadence_audiod_v1_Envelope env = cadence_audiod_v1_Envelope_init_zero;
  pb_istream_t in = pb_istream_from_buffer(golden.data(), golden.size());
  REQUIRE(pb_decode(&in, &cadence_audiod_v1_Envelope_msg, &env));
  REQUIRE(env.which_body != 0);

  std::vector<std::uint8_t> out(cadence_audiod_v1_Envelope_size);
  pb_ostream_t os = pb_ostream_from_buffer(out.data(), out.size());
  REQUIRE(pb_encode(&os, &cadence_audiod_v1_Envelope_msg, &env));
  out.resize(os.bytes_written);
  REQUIRE(out == golden);
  }
  }
}

TEST_CASE("decoded fields match the fixtures") {
  const auto golden = read_file(std::filesystem::path(CADENCE_AUDIOD_FIXTURES) / "mic_opus.binpb");
  cadence_audiod_v1_Envelope env = cadence_audiod_v1_Envelope_init_zero;
  pb_istream_t in = pb_istream_from_buffer(golden.data(), golden.size());
  REQUIRE(pb_decode(&in, &cadence_audiod_v1_Envelope_msg, &env));
  REQUIRE(env.which_body == cadence_audiod_v1_Envelope_mic_opus_tag);
  REQUIRE(std::string(env.body.mic_opus.session_id) == "sess-1");
  REQUIRE(env.body.mic_opus.seq == 3);
  REQUIRE(env.body.mic_opus.opus.size == 5);
}

namespace {

cadence_audiod_v1_Envelope decode(const char* name) {
  const auto golden = read_file(std::filesystem::path(CADENCE_AUDIOD_FIXTURES) / name);
  cadence_audiod_v1_Envelope env = cadence_audiod_v1_Envelope_init_zero;
  pb_istream_t in = pb_istream_from_buffer(golden.data(), golden.size());
  REQUIRE(pb_decode(&in, &cadence_audiod_v1_Envelope_msg, &env));
  return env;
}

}  // namespace

TEST_CASE("media frames carry their fields") {
  const auto play = decode("url_play_max.binpb");
  REQUIRE(play.which_body == cadence_audiod_v1_Envelope_url_play_tag);
  REQUIRE(std::string(play.body.url_play.url).size() == 2047);
  REQUIRE(std::string(play.body.url_play.metadata.artwork_url).size() == 2047);
  REQUIRE(play.body.url_play.mode == cadence_audiod_v1_UrlMode_URL_MODE_OVERLAY);

  const auto state = decode("media_state_max.binpb");
  REQUIRE(state.which_body == cadence_audiod_v1_Envelope_media_state_tag);
  REQUIRE(state.body.media_state.actions_count == 8);
  REQUIRE(state.body.media_state.has_volume);
  REQUIRE(state.body.media_state.volume == 4294967295U);

  const auto idle = decode("media_state_idle.binpb");
  REQUIRE_FALSE(idle.body.media_state.has_volume);
  REQUIRE(idle.body.media_state.state == cadence_audiod_v1_PlaybackState_PLAYBACK_STATE_IDLE);

  const auto viz = decode("viz_frame.binpb");
  REQUIRE(viz.body.viz_frame.bands.size == 16);
  REQUIRE(viz.body.viz_frame.bands.bytes[15] == 0xff);

  const auto settings = decode("media_settings.binpb");
  REQUIRE(settings.body.media_settings.eq.bands_count == 2);
  REQUIRE(settings.body.media_settings.viz.source == cadence_audiod_v1_VizSource_VIZ_SOURCE_ALL);

  const auto cfg = decode("configure_media.binpb");
  REQUIRE(std::string(cfg.body.configure.ca_file) == "/data/cadence/ca-certificates.crt");
  REQUIRE(cfg.body.configure.output.channels == 2);
  REQUIRE(cfg.body.configure.output.eq.bands_count == 1);
}

TEST_CASE("soloist and sender frames carry their fields") {
  const auto cfg = decode("soloist_config.binpb");
  REQUIRE(cfg.which_body == cadence_audiod_v1_Envelope_soloist_config_tag);
  REQUIRE(cfg.body.soloist_config.enabled);
  REQUIRE(std::string(cfg.body.soloist_config.api_key) == "test-key-not-real");
  REQUIRE(std::string(cfg.body.soloist_config.scratch_dir) == "/tmp/cadence-media/soloist");
  REQUIRE(cfg.body.soloist_config.initial_volume == 35);

  const auto st = decode("soloist_status.binpb");
  REQUIRE(st.body.soloist_status.state == cadence_audiod_v1_SoloistState_SOLOIST_STATE_FAILED);
  REQUIRE(std::string(st.body.soloist_status.code) == "auth_failed");
  REQUIRE(st.body.soloist_status.exit_code == -1);
  REQUIRE(st.body.soloist_status.silent_starts == 2);

  const auto res = decode("soloist_probe_result.binpb");
  REQUIRE(res.body.soloist_probe_result.ok);
  REQUIRE(res.body.soloist_probe_result.build_epoch == 1793000000);

  const auto sv = decode("sender_volume.binpb");
  REQUIRE(sv.body.sender_volume.sender == cadence_audiod_v1_Sender_SENDER_SOLOIST);
  REQUIRE(sv.body.sender_volume.level == 42);
  const auto svs = decode("sender_volume_set.binpb");
  REQUIRE(svs.body.sender_volume_set.sender == cadence_audiod_v1_Sender_SENDER_BLUETOOTH);
}

TEST_CASE("airplay frames carry their fields") {
  const auto cfg = decode("airplay_config.binpb");
  REQUIRE(cfg.which_body == cadence_audiod_v1_Envelope_airplay_config_tag);
  REQUIRE(cfg.body.airplay_config.enabled);
  REQUIRE(std::string(cfg.body.airplay_config.name) == "Cadence");
  REQUIRE(std::string(cfg.body.airplay_config.dir) == "/data/cadence/airplay");
  REQUIRE(std::string(cfg.body.airplay_config.scratch_dir) == "/tmp/cadence-media/airplay");

  const auto ready = decode("airplay_status.binpb");
  REQUIRE(ready.which_body == cadence_audiod_v1_Envelope_airplay_status_tag);
  REQUIRE(ready.body.airplay_status.state == cadence_audiod_v1_AirplayState_AIRPLAY_STATE_READY);
  REQUIRE(std::string(ready.body.airplay_status.code).empty());

  const auto failed = decode("airplay_status_failed.binpb");
  REQUIRE(failed.body.airplay_status.state == cadence_audiod_v1_AirplayState_AIRPLAY_STATE_FAILED);
  REQUIRE(std::string(failed.body.airplay_status.code) == "foreign_service");
}

TEST_CASE("board audio and wake frames carry their fields") {
  const auto cfg = decode("configure_pickle.binpb");
  const auto& cap = cfg.body.configure.capture;
  REQUIRE(cap.period_frames == 256);
  REQUIRE(cap.buffer_frames == 8192);
  REQUIRE(cap.has_layout);
  REQUIRE(cap.layout.channels == 4);
  REQUIRE(cap.layout.mic_channels_count == 3);
  REQUIRE(cap.layout.mic_channels[2] == 2);
  REQUIRE(cap.layout.reference_channels_count == 1);
  REQUIRE(cap.layout.reference_channels[0] == 3);
  REQUIRE(cap.layout.sample_rate == 48000);
  REQUIRE(cap.has_engines);
  REQUIRE(cap.engines.enabled);
  REQUIRE(cap.engines.stock_roots_count == 2);
  REQUIRE(std::string(cap.engines.stock_roots[1]) == "/system/vendor/quasar");
  REQUIRE(std::string(cap.engines.command_model) == "/data/cadence/stock/command");
  REQUIRE(cap.engines.command_words_count == 5);
  REQUIRE(std::string(cap.engines.command_words[3]) == "тише");
  REQUIRE(cfg.body.configure.output.period_frames == 960);
  REQUIRE(cfg.body.configure.output.buffer_frames == 7680);

  const auto max = decode("configure_pickle_max.binpb");
  REQUIRE(max.body.configure.capture.layout.mic_channels_count == 16);
  REQUIRE(max.body.configure.capture.engines.stock_roots_count == 4);
  REQUIRE(std::string(max.body.configure.capture.engines.stock_roots[3]).size() == 512);
  REQUIRE(max.body.configure.capture.engines.command_words_count == 16);
  REQUIRE(std::string(max.body.configure.capture.engines.command_words[15]).size() == 64);

  const auto wake = decode("wake_event.binpb");
  REQUIRE(wake.which_body == cadence_audiod_v1_Envelope_wake_event_tag);
  REQUIRE(std::string(wake.body.wake_event.engine) == "vendor");
  REQUIRE(wake.body.wake_event.score == 0.75f);
  REQUIRE(wake.body.wake_event.at_ms == 1762700000000LL);

  // the vk capsula mini: the keyword engine on the raw microphone, no vendor engines
  const auto vk = decode("configure_vk.binpb");
  const auto& vkCap = vk.body.configure.capture;
  REQUIRE(std::string(vkCap.device) == "single_plugin");
  REQUIRE(vkCap.period_frames == 640);
  REQUIRE(vkCap.buffer_frames == 2560);
  REQUIRE_FALSE(vkCap.has_engines);
  REQUIRE(vkCap.has_keyword);
  REQUIRE(std::string(vkCap.keyword.kind) == "vk-kws");
  REQUIRE(std::string(vkCap.keyword.model_path) == "/usr/share/voice-agent/model.bin");
  REQUIRE(std::string(vkCap.keyword.model_sha256) ==
          "8b33272b9fa8569f85630fd6208f475021ea806a7d79168e070a374569732f3b");
  REQUIRE(vkCap.keyword.pos_threshold == 0.999f);
  REQUIRE(vkCap.keyword.sp_threshold == 0.2f);
  REQUIRE(vkCap.keyword.rearm_frames == 147);
  REQUIRE(vkCap.keyword.consecutive_calls == 1);
  const auto vkWake = decode("wake_event_vk.binpb");
  REQUIRE(vkWake.which_body == cadence_audiod_v1_Envelope_wake_event_tag);
  REQUIRE(std::string(vkWake.body.wake_event.engine) == "vk-kws");
  REQUIRE(vkWake.body.wake_event.score == 0.9995f);
  REQUIRE(vkWake.body.wake_event.at_ms == 1763500000000LL);

  const auto word = decode("command_word.binpb");
  REQUIRE(word.which_body == cadence_audiod_v1_Envelope_command_word_tag);
  REQUIRE(word.body.command_word.id == 3);
  REQUIRE(word.body.command_word.at_ms == 1762700000123LL);

  const auto logs_on = decode("logs_set.binpb");
  REQUIRE(logs_on.which_body == cadence_audiod_v1_Envelope_logs_set_tag);
  REQUIRE(logs_on.body.logs_set.third_party);
  const auto logs_off = decode("logs_set_off.binpb");
  REQUIRE(logs_off.which_body == cadence_audiod_v1_Envelope_logs_set_tag);
  REQUIRE_FALSE(logs_off.body.logs_set.third_party);

  // a child's longest line, cut: 4096 bytes of text, the last three its ellipsis
  const auto line = decode("log_line_max.binpb");
  REQUIRE(line.which_body == cadence_audiod_v1_Envelope_log_line_tag);
  REQUIRE(std::string(line.body.log_line.source) == "shairport-sync");
  REQUIRE(line.body.log_line.level == cadence_audiod_v1_LogLevel_LOG_LEVEL_WARN);
  const std::string text(line.body.log_line.text);
  REQUIRE(text.size() == 4096);
  REQUIRE(text.ends_with("\xe2\x80\xa6"));
  REQUIRE(line.body.log_line.truncated);
  REQUIRE_FALSE(line.body.log_line.continued);
  // the rest of a line sent partially before
  const auto rest = decode("log_line_continued.binpb");
  REQUIRE(rest.which_body == cadence_audiod_v1_Envelope_log_line_tag);
  REQUIRE(std::string(rest.body.log_line.source) == "soloist");
  REQUIRE(rest.body.log_line.continued);
  REQUIRE(rest.body.log_line.truncated);

  const auto mute = decode("mute_state.binpb");
  REQUIRE(mute.which_body == cadence_audiod_v1_Envelope_mute_state_tag);
  REQUIRE(mute.body.mute_state.muted);

  const auto ok = decode("vendor_status.binpb");
  REQUIRE(ok.body.vendor_status.engines);
  REQUIRE(std::string(ok.body.vendor_status.code).empty());
  const auto safe = decode("vendor_status_safe.binpb");
  REQUIRE_FALSE(safe.body.vendor_status.engines);
  REQUIRE(std::string(safe.body.vendor_status.code) == "probe_failed");

  const auto alarm = decode("tone_start_alarm.binpb");
  REQUIRE(alarm.which_body == cadence_audiod_v1_Envelope_tone_start_tag);
  REQUIRE(std::string(alarm.body.tone_start.id) == "alarm-1");
  REQUIRE(alarm.body.tone_start.kind == cadence_audiod_v1_ToneKind_TONE_KIND_ALARM);
  const auto timer = decode("tone_start_timer_max.binpb");
  REQUIRE(std::string(timer.body.tone_start.id).size() == 64);
  REQUIRE(timer.body.tone_start.kind == cadence_audiod_v1_ToneKind_TONE_KIND_TIMER);
  const auto stop = decode("tone_stop.binpb");
  REQUIRE(stop.which_body == cadence_audiod_v1_Envelope_tone_stop_tag);
  REQUIRE(std::string(stop.body.tone_stop.id) == "alarm-1");
  const auto sounding = decode("tone_state.binpb");
  REQUIRE(sounding.which_body == cadence_audiod_v1_Envelope_tone_state_tag);
  REQUIRE(sounding.body.tone_state.sounding);
  REQUIRE(std::string(sounding.body.tone_state.code).empty());
  const auto gated = decode("tone_state_gated.binpb");
  REQUIRE_FALSE(gated.body.tone_state.sounding);
  REQUIRE(std::string(gated.body.tone_state.code) == "gated");
  const auto transient = decode("volume_set_transient.binpb");
  REQUIRE(transient.body.volume_set.level == 55);
  REQUIRE(transient.body.volume_set.transient);
  const auto refused = decode("volume_applied_ramp_refused.binpb");
  REQUIRE(refused.which_body == cadence_audiod_v1_Envelope_volume_applied_tag);
  REQUIRE(refused.body.volume_applied.level == 60);
  REQUIRE_FALSE(refused.body.volume_applied.ok);
  REQUIRE(refused.body.volume_applied.transient);
  REQUIRE(std::string(refused.body.volume_applied.code) == "no_ring");
}

static_assert(cadence_audiod_v1_Envelope_size <= 16 * 1024);

TEST_CASE("garbage does not decode") {
  const std::uint8_t garbage[] = {0xff, 0xff, 0xff, 0xff, 0xff, 0xff};
  cadence_audiod_v1_Envelope env = cadence_audiod_v1_Envelope_init_zero;
  pb_istream_t in = pb_istream_from_buffer(garbage, sizeof garbage);
  REQUIRE_FALSE(pb_decode(&in, &cadence_audiod_v1_Envelope_msg, &env));
}
