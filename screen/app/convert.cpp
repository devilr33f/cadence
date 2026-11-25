#include "app/convert.h"

#include <algorithm>

namespace cadence::screen::app {

namespace {

std::string text(const char* s) { return s != nullptr ? s : ""; }

}  // namespace

player::PlayRequest playRequestOf(const cadence_screen_v1_MediaPlay& p) {
  player::PlayRequest r;
  r.sessionId = p.session_id;
  r.purpose = p.purpose;
  r.url = text(p.url);
  r.audioUrl = text(p.audio_url);
  for (pb_size_t i = 0; i < p.headers_count; i++) r.headers[text(p.headers[i].key)] = text(p.headers[i].value);
  for (pb_size_t i = 0; i < p.subtitles_count; i++) {
    const auto& s = p.subtitles[i];
    r.subtitles.push_back(player::Subtitle{text(s.url), s.language, text(s.label), s.is_default});
  }
  r.startMs = p.start_ms;
  r.live = p.live;
  for (pb_size_t i = 0; i < p.renditions_count; i++) {
    const auto& x = p.renditions[i];
    player::Rendition out{x.name, x.height, x.fps, {}, x.selected, x.hdr};
    for (pb_size_t c = 0; c < x.codecs_count; c++) out.codecs.emplace_back(x.codecs[c]);
    r.renditions.push_back(std::move(out));
  }
  r.followGroupId = p.follow_group_id;
  return r;
}

std::optional<player::Command> commandOf(const cadence_screen_v1_MediaCommand& c) {
  player::Command out;
  switch (c.action) {
    case cadence_screen_v1_MediaAction_MEDIA_ACTION_PAUSE: out.action = player::CommandAction::Pause; break;
    case cadence_screen_v1_MediaAction_MEDIA_ACTION_RESUME: out.action = player::CommandAction::Resume; break;
    case cadence_screen_v1_MediaAction_MEDIA_ACTION_STOP: out.action = player::CommandAction::Stop; break;
    case cadence_screen_v1_MediaAction_MEDIA_ACTION_SEEK: out.action = player::CommandAction::Seek; break;
    case cadence_screen_v1_MediaAction_MEDIA_ACTION_SELECT_TRACK: out.action = player::CommandAction::SelectTrack; break;
    default: return std::nullopt;
  }
  out.positionMs = c.position_ms;
  out.trackId = c.track_id;
  out.trackKind = c.track_kind;
  return out;
}

player::EqRequest eqOf(const cadence_screen_v1_SetEq& e) {
  player::EqRequest out;
  out.preset = e.preset;
  out.preampDb = e.preamp_db;
  for (pb_size_t i = 0; i < e.bands_count; i++) {
    out.bands.push_back(player::EqBand{e.bands[i].type, e.bands[i].freq_hz, e.bands[i].gain_db, e.bands[i].q});
  }
  return out;
}

ipc::OutEnvelope mediaStateEnvelope(const player::PlayerState& s) {
  ipc::OutEnvelope out(cadence_screen_v1_Envelope_media_state_tag);
  auto& m = out->body.media_state;
  ipc::copyField(m.session_id, s.sessionId);
  m.state = s.state;
  m.position_ms = s.positionMs;
  m.position_at_ms = s.positionAtMs;
  m.rate = s.rate;
  m.duration_ms = s.durationMs;
  m.buffered_ms = s.bufferedMs;
  m.has_buffering_percent = s.bufferingPercent.has_value();
  m.buffering_percent = std::min<std::uint32_t>(s.bufferingPercent.value_or(0), 100);
  m.purpose = s.purpose;
  m.audio_route = s.audioRoute;
  m.control_latency_ms = s.controlLatencyMs;
  ipc::copyField(m.error_code, s.errorCode);
  m.gated = s.gated;
  m.ended = s.ended;
  for (auto c : s.audioControls) {
    if (m.audio_controls_count >= 8) break;
    m.audio_controls[m.audio_controls_count++] = c;
  }
  if (!s.tracks.empty()) {
    m.tracks = out.keepArray<cadence_v2_MediaTrack>(s.tracks.size());
    for (const auto& t : s.tracks) {
      auto& dst = m.tracks[m.tracks_count++];
      ipc::copyField(dst.id, t.id);
      dst.kind = t.kind;
      ipc::copyField(dst.language, t.language);
      dst.label = t.label.empty() ? nullptr : out.keep(t.label);
      dst.selected = t.selected;
      dst.external = t.external;
    }
  }
  return out;
}

ipc::OutEnvelope playerStatsEnvelope(const player::PlayerStats& s) {
  ipc::OutEnvelope out(cadence_screen_v1_Envelope_player_stats_tag);
  auto& m = out->body.player_stats;
  ipc::copyField(m.session_id, s.sessionId);
  m.cache_speed_bps = s.cacheSpeedBps;
  m.input_rate_bps = s.inputRateBps;
  m.cache_ahead_bytes = s.cacheAheadBytes;
  m.cache_total_bytes = s.cacheTotalBytes;
  m.buffered_ms = s.bufferedMs;
  m.low_level_seeks = s.lowLevelSeeks;
  m.byte_level_seeks = s.byteLevelSeeks;
  m.underrun = s.underrun;
  m.frame_drops = s.frameDrops;
  m.decoder_drops = s.decoderDrops;
  m.vo_delayed_frames = s.voDelayedFrames;
  m.estimated_vf_fps = static_cast<float>(s.estimatedVfFps);
  m.display_fps = static_cast<float>(s.displayFps);
  m.avsync_ms = static_cast<float>(s.avsyncMs);
  ipc::copyField(m.hwdec, s.hwdec);
  return out;
}

ipc::OutEnvelope capabilityStateEnvelope(std::string_view capability, std::string_view json) {
  ipc::OutEnvelope out(cadence_screen_v1_Envelope_capability_state_tag);
  auto& m = out->body.capability_state;
  ipc::copyField(m.capability, capability);
  m.json = out.keep(json);
  return out;
}

ipc::OutEnvelope commandResultEnvelope(std::string_view requestId, std::string_view code) {
  ipc::OutEnvelope out(cadence_screen_v1_Envelope_command_result_tag);
  ipc::copyField(out->body.command_result.request_id, requestId);
  ipc::copyField(out->body.command_result.code, code);
  return out;
}

ipc::OutEnvelope logLineEnvelope(const ipc::LogRecord& r) {
  ipc::OutEnvelope out(cadence_screen_v1_Envelope_log_line_tag);
  auto& l = out->body.log_line;
  ipc::copyField(l.source, r.source);
  l.level = ipc::toProto(r.level);
  ipc::copyField(l.text, r.text);
  l.truncated = r.truncated;
  l.continued = r.continued;
  return out;
}

ipc::OutEnvelope inputEventEnvelope(const platform::KeyEvent& e, std::int64_t atMs) {
  ipc::OutEnvelope out(cadence_screen_v1_Envelope_input_event_tag);
  auto& i = out->body.input_event;
  i.key = e.key;
  i.action = e.action;
  i.source = e.source;
  i.at_ms = atMs;
  return out;
}

namespace {

void fillDisplay(cadence_screen_v1_Display& d, const platform::DisplayInfo& info) {
  d.width = static_cast<std::uint32_t>(info.pixels.width > 0 ? info.pixels.width : 0);
  d.height = static_cast<std::uint32_t>(info.pixels.height > 0 ? info.pixels.height : 0);
  d.refresh_mhz = info.refreshMhz;
}

}  // namespace

ipc::OutEnvelope helloEnvelope(const HelloInfo& info) {
  ipc::OutEnvelope out(cadence_screen_v1_Envelope_hello_tag);
  auto& h = out->body.hello;
  h.protocol = kProtocol;
  ipc::copyField(h.version, info.version);
  h.platform = info.platform;
  h.has_display = true;
  fillDisplay(h.display, info.display);
  for (const auto& name : info.hwdec) {
    if (h.hwdec_count >= 8) break;
    ipc::copyField(h.hwdec[h.hwdec_count++], name);
  }
  for (const auto& codec : info.videoCodecs) {
    if (h.video_codecs_count >= 16) break;
    ipc::copyField(h.video_codecs[h.video_codecs_count++], codec);
  }
  h.max_video_height = info.maxVideoHeight;
  h.mirroring = info.mirroring;
  for (auto v : info.cardVersions) {
    if (h.card_versions_count >= 8) break;
    h.card_versions[h.card_versions_count++] = v;
  }
  constexpr auto kMaxFeatures = sizeof h.card_features / sizeof h.card_features[0];
  for (const auto& feature : info.cardFeatures) {
    if (h.card_features_count >= kMaxFeatures) break;
    if (feature.empty() || feature.size() >= sizeof h.card_features[0]) continue;
    ipc::copyField(h.card_features[h.card_features_count++], feature);
  }
  return out;
}

ipc::OutEnvelope screenStatusEnvelope(cadence_screen_v1_ScreenPower power, const UiStatus& ui,
                                      const platform::DisplayInfo& display) {
  ipc::OutEnvelope out(cadence_screen_v1_Envelope_screen_status_tag);
  auto& s = out->body.screen_status;
  s.power = power;
  s.route = ui.route;
  s.drawer_open = ui.drawerOpen;
  s.has_display = true;
  fillDisplay(s.display, display);
  return out;
}

}  // namespace cadence::screen::app
