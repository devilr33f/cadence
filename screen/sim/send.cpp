#include "sim/send.h"

#include "app/routes/core/rpc.h"
#include "sim/names.h"
#include "ui/cards/model.h"

#include <algorithm>
#include <chrono>
#include <filesystem>
#include <fstream>
#include <iterator>
#include <vector>

namespace cadence::screen::sim {

namespace {

namespace cards = ::cadence::screen::ui::cards;

// a string field, whichever the generated struct chose: a fixed char array or a pointer the envelope keeps
template <std::size_t N>
void setStr(ipc::OutEnvelope&, char (&field)[N], const std::string& v) {
  ipc::copyField(field, v);
}
inline void setStr(ipc::OutEnvelope& out, char*& field, const std::string& v) { field = out.keep(v); }
using ::cadence::screen::routes::Json;

std::string readBytes(const std::string& path) {
  std::ifstream in(path, std::ios::binary);
  if (!in) return {};
  return {std::istreambuf_iterator<char>(in), {}};
}

// the card fixtures a "cards" array names, decoded (the files stay the fixture set's .binpb)
bool loadCards(const Json& names, const std::string& cardDir, std::vector<cards::CardPtr>& into, std::string* error) {
  for (const auto& name : names) {
    if (!name.is_string()) {
      if (error != nullptr) *error = "cards";
      return false;
    }
    std::string path = name.get<std::string>();
    if (!path.starts_with('/')) {
      if (cardDir.empty()) {
        if (error != nullptr) *error = "cards (no fixture dir)";
        return false;
      }
      path = cardDir + (cardDir.back() == '/' ? "" : "/") + path;
    }
    const auto bytes = readBytes(path);
    auto card = cards::decodeCard({reinterpret_cast<const std::uint8_t*>(bytes.data()), bytes.size()});
    if (!card) {
      if (error != nullptr) *error = "card " + name.get<std::string>();
      return false;
    }
    into.push_back(std::move(card));
  }
  return true;
}

// cards into a repeated field: a deep copy per card (the envelope's array points into the copies, which SendBuilt
// keeps until the frame is encoded)
template <typename Field>
bool setCards(ipc::OutEnvelope& out, Field& field, const std::vector<cards::CardPtr>& owned,
              std::vector<cards::CardPtr>& keeps) {
  if (owned.empty()) return true;
  auto* storage = out.keepArray<cadence_cards_v1_Card>(owned.size());
  if (storage == nullptr) return false;
  for (std::size_t i = 0; i < owned.size(); i++) {
    if (!owned[i]) return false;
    keeps.push_back(cards::copyCard(*owned[i]));
    if (!keeps.back()) return false;
    storage[i] = *keeps.back();
  }
  field = storage;
  return true;
}

cadence_v2_AssistantState assistantState(const std::string& s) {
  if (s == "invoked") return cadence_v2_AssistantState_ASSISTANT_STATE_INVOKED;
  if (s == "listening") return cadence_v2_AssistantState_ASSISTANT_STATE_LISTENING;
  if (s == "thinking") return cadence_v2_AssistantState_ASSISTANT_STATE_THINKING;
  if (s == "speaking") return cadence_v2_AssistantState_ASSISTANT_STATE_SPEAKING;
  if (s == "follow_up") return cadence_v2_AssistantState_ASSISTANT_STATE_FOLLOW_UP;
  if (s == "resting") return cadence_v2_AssistantState_ASSISTANT_STATE_RESTING;
  if (s == "dismissed") return cadence_v2_AssistantState_ASSISTANT_STATE_DISMISSED;
  return cadence_v2_AssistantState_ASSISTANT_STATE_UNSPECIFIED;
}

}  // namespace

std::optional<SendBuilt> buildSend(const std::string& text, const std::string& cardDir, std::string* error) {
  SendBuilt built;
  ipc::OutEnvelope& out = built.envelope;
  Json j = Json::parse(text, nullptr, false);
  if (j.is_null() || !j.is_object() || j.size() != 1) {
    if (error != nullptr) *error = "send needs one {message: {...}} object";
    return std::nullopt;
  }
  const std::string kind = j.begin().key();
  const Json& b = j.begin().value();
  if (!b.is_object()) {
    if (error != nullptr) *error = kind;
    return std::nullopt;
  }

  if (kind == "toast") {
    out->which_body = cadence_screen_v1_Envelope_toast_tag;
    auto& t = out->body.toast;
    ipc::copyField(t.id, b.value("id", "sim"));
    const std::string k = b.value("kind", "info");
    t.kind = k == "success"   ? cadence_screen_v1_ToastKind_TOAST_KIND_SUCCESS
             : k == "warning" ? cadence_screen_v1_ToastKind_TOAST_KIND_WARNING
             : k == "danger"  ? cadence_screen_v1_ToastKind_TOAST_KIND_DANGER
                              : cadence_screen_v1_ToastKind_TOAST_KIND_INFO;
    setStr(out, t.text, b.value("text", ""));
    ipc::copyField(t.icon, b.value("icon", ""));
    t.duration_ms = b.value("durationMs", 0u);
    if (b.contains("action")) {
      const auto& a = b["action"];
      t.has_action = true;
      ipc::copyField(t.action.id, a.value("id", "act"));
      setStr(out, t.action.label, a.value("label", ""));
      const std::string ak = a.value("kind", "route");
      t.action.kind = ak == "rpc" ? cadence_screen_v1_ToastActionKind_TOAST_ACTION_KIND_RPC
                                   : cadence_screen_v1_ToastActionKind_TOAST_ACTION_KIND_OPEN;
      if (auto route = routeOf(a.value("route", ""))) t.action.route = *route;
      setStr(out, t.action.ref, a.value("ref", ""));
    }
    return built;
  }

  if (kind == "route") {
    out->which_body = cadence_screen_v1_Envelope_route_tag;
    auto& r = out->body.route;
    const auto route = routeOf(b.value("route", ""));
    if (!route) {
      if (error != nullptr) *error = "route " + b.value("route", "");
      return std::nullopt;
    }
    r.route = *route;
    setStr(out, r.ref, b.value("ref", ""));
    setStr(out, r.query, b.value("query", ""));
    ipc::copyField(r.episode, b.value("episode", ""));
    r.back = b.value("back", false);
    return built;
  }

  if (kind == "link_status") {
    out->which_body = cadence_screen_v1_Envelope_link_status_tag;
    auto& l = out->body.link_status;
    const std::string s = b.value("state", "online");
    l.state = s == "unenrolled" ? cadence_screen_v1_LinkState_LINK_STATE_UNENROLLED
               : s == "connecting" ? cadence_screen_v1_LinkState_LINK_STATE_CONNECTING
               : s == "offline"   ? cadence_screen_v1_LinkState_LINK_STATE_OFFLINE
               : s == "removed"   ? cadence_screen_v1_LinkState_LINK_STATE_REMOVED
                                   : cadence_screen_v1_LinkState_LINK_STATE_ONLINE;
    setStr(out, l.server_url, b.value("serverUrl", ""));
    ipc::copyField(l.code, b.value("code", ""));
    return built;
  }

  if (kind == "claim_state") {
    out->which_body = cadence_screen_v1_Envelope_claim_state_tag;
    auto& c = out->body.claim_state;
    const std::string s = b.value("phase", "pending");
    c.phase = s == "approved"   ? cadence_screen_v1_ClaimPhase_CLAIM_PHASE_APPROVED
              : s == "declined" ? cadence_screen_v1_ClaimPhase_CLAIM_PHASE_DECLINED
              : s == "expired"  ? cadence_screen_v1_ClaimPhase_CLAIM_PHASE_EXPIRED
              : s == "failed"   ? cadence_screen_v1_ClaimPhase_CLAIM_PHASE_FAILED
                                : cadence_screen_v1_ClaimPhase_CLAIM_PHASE_PENDING;
    ipc::copyField(c.code, b.value("code", "K7F2QM"));
    setStr(out, c.url, b.value("url", "http://cadence-sim.invalid/claim"));
    c.expires_at_ms = b.value("expiresAtMs", 0LL);
    return built;
  }

  if (kind == "voice_state") {
    out->which_body = cadence_screen_v1_Envelope_voice_state_tag;
    auto& v = out->body.voice_state;
    v.state = assistantState(b.value("state", "listening"));
    v.mic = b.value("mic", 120u);
    v.output = b.value("output", 0u);
    return built;
  }

  if (kind == "assistant_turn") {
    out->which_body = cadence_screen_v1_Envelope_assistant_turn_tag;
    auto& t = out->body.assistant_turn;
    setStr(out, t.turn_id, b.value("turnId", "sim-turn"));
    t.revision = b.value("revision", 1u);
    t.conversation_reset = b.value("conversationReset", false);
    t.state = assistantState(b.value("state", "thinking"));
    const std::string trigger = b.value("trigger", "wake");
    t.trigger = trigger == "button"    ? cadence_v2_Trigger_TRIGGER_BUTTON
                : trigger == "follow_up" ? cadence_v2_Trigger_TRIGGER_FOLLOW_UP
                : trigger == "event"      ? cadence_v2_Trigger_TRIGGER_EVENT
                                         : cadence_v2_Trigger_TRIGGER_WAKE;
    if (b.contains("transcript")) {
      t.has_transcript = true;
      setStr(out, t.transcript.stable, b["transcript"].get<std::string>());
    }
    // whose microphone listens: {"kind": "speaker"|"local"|"none", "name"}
    if (b.contains("mic") && b["mic"].is_object()) {
      const auto& mc = b["mic"];
      const std::string k = mc.value("kind", "speaker");
      t.has_mic_source = true;
      t.mic_source.kind = k == "local"  ? cadence_v2_MicSourceKind_MIC_SOURCE_KIND_LOCAL
                          : k == "none" ? cadence_v2_MicSourceKind_MIC_SOURCE_KIND_NONE
                                        : cadence_v2_MicSourceKind_MIC_SOURCE_KIND_SPEAKER;
      setStr(out, t.mic_source.device_name, mc.value("name", ""));
    }
    if (b.contains("reply")) {
      t.has_reply = true;
      setStr(out, t.reply.text, b["reply"].get<std::string>());
    }
    // the steps a turn shows above its cards: [{"id", "label", "status": pending|running|done|failed, "cardId"}]
    if (b.contains("steps") && b["steps"].is_array()) {
      const auto& steps = b["steps"];
      auto* st = out.keepArray<cadence_v2_AssistantStep>(steps.size());
      for (std::size_t i = 0; i < steps.size(); ++i) {
        const auto& step = steps[i];
        setStr(out, st[i].id, step.value("id", "s" + std::to_string(i)));
        setStr(out, st[i].label, step.value("label", ""));
        setStr(out, st[i].card_id, step.value("cardId", ""));
        const std::string status = step.value("status", "done");
        st[i].status = status == "running"  ? cadence_v2_StepStatus_STEP_STATUS_RUNNING
                       : status == "failed" ? cadence_v2_StepStatus_STEP_STATUS_FAILED
                       : status == "pending" ? cadence_v2_StepStatus_STEP_STATUS_UNSPECIFIED
                                             : cadence_v2_StepStatus_STEP_STATUS_DONE;
      }
      t.steps = st;
      t.steps_count = static_cast<pb_size_t>(steps.size());
    }
    // the reply's [n] marks and the cards they cite: [{"n", "cardId"}]
    if (b.contains("citations") && b["citations"].is_array()) {
      const auto& cs = b["citations"];
      auto* c = out.keepArray<cadence_v2_Citation>(cs.size());
      for (std::size_t i = 0; i < cs.size(); ++i) {
        c[i].n = cs[i].value("n", 0u);
        setStr(out, c[i].card_id, cs[i].value("cardId", ""));
      }
      t.reply.citations = c;
      t.reply.citations_count = static_cast<pb_size_t>(cs.size());
    }
    t.follow_up_window_ms = b.value("followUpWindowMs", 0u);
    ipc::copyField(t.error_code, b.value("errorCode", ""));
    if (b.contains("failure") && b["failure"].is_object()) {
      const auto& f = b["failure"];
      t.has_failure = true;
      setStr(out, t.failure.code, f.value("code", ""));
      setStr(out, t.failure.stage, f.value("stage", ""));
      setStr(out, t.failure.provider, f.value("provider", ""));
      setStr(out, t.failure.model, f.value("model", ""));
      setStr(out, t.failure.tool, f.value("tool", ""));
      t.failure.step = f.value("step", 0u);
      t.failure.http_status = f.value("httpStatus", 0u);
      t.failure.waited_ms = f.value("waitedMs", 0LL);
      t.failure.limit_ms = f.value("limitMs", 0LL);
    }
    if (b.contains("cards")) {
      std::vector<cards::CardPtr> owned;
      if (!loadCards(b["cards"], cardDir, owned, error)) return std::nullopt;
      if (!setCards(out, t.cards, owned, built.cards)) return std::nullopt;
      t.cards_count = static_cast<pb_size_t>(owned.size());
    }
    return built;
  }

  if (kind == "airplay_pin") {
    out->which_body = cadence_screen_v1_Envelope_airplay_pin_tag;
    auto& p = out->body.airplay_pin;
    p.active = b.value("active", true);
    ipc::copyField(p.pin, b.value("pin", "4913"));
    setStr(out, p.client_name, b.value("clientName", "iPhone"));
    p.expires_at_ms = b.value("expiresAtMs", 0LL);
    return built;
  }

  if (kind == "mirror_start") {
    out->which_body = cadence_screen_v1_Envelope_mirror_start_tag;
    auto& m = out->body.mirror_start;
    setStr(out, m.session_id, b.value("session", "air-1"));
    const std::string codec = b.value("codec", "h264");
    m.codec = codec == "h265" ? cadence_screen_v1_MirrorCodec_MIRROR_CODEC_H265
                              : cadence_screen_v1_MirrorCodec_MIRROR_CODEC_H264;
    m.width = b.value("width", 1920u);
    m.height = b.value("height", 1080u);
    setStr(out, m.client_name, b.value("clientName", "iPhone"));
    return built;
  }

  if (kind == "mirror_stop") {
    out->which_body = cadence_screen_v1_Envelope_mirror_stop_tag;
    auto& m = out->body.mirror_stop;
    setStr(out, m.session_id, b.value("session", "air-1"));
    ipc::copyField(m.reason, b.value("reason", "sender"));
    return built;
  }

  if (kind == "display_power") {
    out->which_body = cadence_screen_v1_Envelope_display_power_tag;
    out->body.display_power.on = b.value("on", true);
    return built;
  }

  if (kind == "capability_config") {
    out->which_body = cadence_screen_v1_Envelope_capability_config_tag;
    auto& c = out->body.capability_config;
    ipc::copyField(c.capability, b.value("capability", "display.screen"));
    setStr(out, c.json, b.value("json", Json::object()).dump());
    return built;
  }

  if (kind == "playback_gate") {
    // the gate cadenced holds (rule 7): shut, the player holds its session paused
    out->which_body = cadence_screen_v1_Envelope_playback_gate_tag;
    out->body.playback_gate.open = b.value("open", false);
    return built;
  }

  if (kind == "duck") {
    out->which_body = cadence_screen_v1_Envelope_duck_tag;
    auto& d = out->body.duck;
    d.active = b.value("active", true);
    d.level = b.value("level", 0.3f);
    d.ramp_ms = b.value("rampMs", 400u);
    return built;
  }

  if (kind == "volume_state") {
    out->which_body = cadence_screen_v1_Envelope_volume_state_tag;
    auto& v = out->body.volume_state;
    v.level = b.value("level", 30u);
    v.muted = b.value("muted", false);
    v.show = b.value("show", true);
    return built;
  }

  if (kind == "tasks_summary") {
    out->which_body = cadence_screen_v1_Envelope_tasks_summary_tag;
    auto& t = out->body.tasks_summary;
    t.running = b.value("running", 1u);
    t.waiting = b.value("waiting", 0u);
    t.finished = b.value("finished", 0u);
    t.revision = b.value("revision", 1u);
    return built;
  }

  if (kind == "alarms_summary") {
    // the server's alarms summary as cadenced relays it; nextInS (seconds from now) stands in for nextUnixMs so a
    // script's timers run from when it is sent
    out->which_body = cadence_screen_v1_Envelope_alarms_summary_tag;
    auto& a = out->body.alarms_summary;
    const auto now = std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::system_clock::now().time_since_epoch()).count();
    const auto when = [now](const Json& o) -> std::int64_t {
      if (o.contains("nextInS")) return now + o.value("nextInS", std::int64_t{0}) * 1000;
      return o.value("nextUnixMs", std::int64_t{0});
    };
    a.revision = b.value("revision", 1u);
    const auto devices = b.value("devices", Json::array());
    if (!devices.empty()) {
      a.devices = out.keepArray<cadence_v2_AlarmsDevice>(devices.size());
      a.devices_count = static_cast<pb_size_t>(devices.size());
    }
    for (std::size_t i = 0; i < devices.size(); ++i) {
      const auto& d = devices[i];
      auto& x = a.devices[i];
      ipc::copyField(x.device_id, d.value("deviceId", ""));
      setStr(out, x.name, d.value("name", ""));
      setStr(out, x.room_name, d.value("roomName", ""));
      x.here = d.value("here", false);
      x.same_room = d.value("sameRoom", false);
      x.snooze_minutes = d.value("snoozeMinutes", 9u);
      const auto items = d.value("items", Json::array());
      if (items.empty()) continue;
      x.items = out.keepArray<cadence_v2_AlarmItem>(items.size());
      x.items_count = static_cast<pb_size_t>(items.size());
      for (std::size_t k = 0; k < items.size(); ++k) {
        const auto& it = items[k];
        auto& y = x.items[k];
        ipc::copyField(y.kind, it.value("kind", "alarm"));
        ipc::copyField(y.id, it.value("id", ""));
        setStr(out, y.label, it.value("label", ""));
        ipc::copyField(y.time, it.value("time", ""));
        for (const auto& day : it.value("days", Json::array())) {
          if (y.days_count < 7 && day.is_string()) ipc::copyField(y.days[y.days_count++], day.get<std::string>());
        }
        y.next_unix_ms = when(it);
        y.seconds = it.value("seconds", 0u);
        y.snoozed = it.value("snoozed", false);
      }
    }
    for (const auto& r : b.value("ringing", Json::array())) {
      if (a.ringing_count >= 4) break;
      auto& x = a.ringing[a.ringing_count++];
      ipc::copyField(x.device_id, r.value("deviceId", ""));
      setStr(out, x.device_name, r.value("deviceName", ""));
      setStr(out, x.room_name, r.value("roomName", ""));
      ipc::copyField(x.kind, r.value("kind", "alarm"));
      ipc::copyField(x.id, r.value("id", ""));
      setStr(out, x.label, r.value("label", ""));
      ipc::copyField(x.time, r.value("time", ""));
      x.seconds = r.value("seconds", 0u);
      x.started_unix_ms = now;
      x.own = r.value("own", false);
      x.snooze_minutes = r.value("snoozeMinutes", 9u);
    }
    return built;
  }

  if (kind == "cards") {
    out->which_body = cadence_screen_v1_Envelope_cards_tag;
    auto& c = out->body.cards;
    setStr(out, c.context_id, b.value("contextId", "sim"));
    if (b.contains("cards")) {
      std::vector<cards::CardPtr> owned;
      if (!loadCards(b["cards"], cardDir, owned, error)) return std::nullopt;
      if (!setCards(out, c.cards, owned, built.cards)) return std::nullopt;
      c.cards_count = static_cast<pb_size_t>(owned.size());
    }
    return built;
  }

  if (kind == "media_preparing") {
    // the server resolves a link it will play here (media.video/prepare)
    out->which_body = cadence_screen_v1_Envelope_media_preparing_tag;
    auto& p = out->body.media_preparing;
    p.state = b.value("state", "resolving") == "failed" ? cadence_screen_v1_PrepareState_PREPARE_STATE_FAILED
                                                         : cadence_screen_v1_PrepareState_PREPARE_STATE_RESOLVING;
    setStr(out, p.title, b.value("title", ""));
    setStr(out, p.subtitle, b.value("subtitle", ""));
    setStr(out, p.artwork_url, b.value("artworkUrl", ""));
    ipc::copyField(p.code, b.value("code", ""));
    return built;
  }

  if (kind == "media_play") {
    // a server video's play as cadenced sends it (media.video/play): its metadata, renditions, view and audio choices
    out->which_body = cadence_screen_v1_Envelope_media_play_tag;
    auto& m = out->body.media_play;
    ipc::copyField(m.session_id, b.value("sessionId", "video-1"));
    m.purpose = cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT;
    m.url = out.keep(b.value("url", "http://fake.invalid/play/yt"));
    m.playable = cadence_v2_PlayableKind_PLAYABLE_KIND_STREAM;
    m.has_metadata = true;
    const auto& md = b.value("metadata", Json::object());
    setStr(out, m.metadata.title, md.value("title", ""));
    setStr(out, m.metadata.artist, md.value("artist", ""));
    setStr(out, m.metadata.album, md.value("album", ""));
    m.metadata.kind = md.value("kind", "video") == "track" ? cadence_v2_MediaKind_MEDIA_KIND_TRACK : cadence_v2_MediaKind_MEDIA_KIND_VIDEO;
    // an episode: its show, its number and name, the season; the art as the server's image proxy names it
    setStr(out, m.metadata.series, md.value("series", ""));
    ipc::copyField(m.metadata.episode, md.value("episode", ""));
    setStr(out, m.metadata.episode_name, md.value("episodeName", ""));
    m.metadata.season = md.value("season", 0u);
    setStr(out, m.metadata.artwork_url, md.value("artworkUrl", ""));
    if (md.contains("episode")) m.metadata.kind = cadence_v2_MediaKind_MEDIA_KIND_EPISODE;
    if (md.contains("channel")) {
      m.metadata.has_channel = true;
      setStr(out, m.metadata.channel.name, md.value("channel", ""));
    }
    m.metadata.has_provider = true;
    ipc::copyField(m.metadata.provider.id, md.value("provider", "youtube"));
    setStr(out, m.metadata.provider.name, md.value("providerName", ""));
    setStr(out, m.metadata.provider.source, md.value("source", ""));
    const auto viewOf = [](const std::string& v) {
      return v == "audio"        ? cadence_screen_v1_MediaView_MEDIA_VIEW_AUDIO
             : v == "visualizer" ? cadence_screen_v1_MediaView_MEDIA_VIEW_VISUALIZER
             : v == "video"      ? cadence_screen_v1_MediaView_MEDIA_VIEW_VIDEO
                                 : cadence_screen_v1_MediaView_MEDIA_VIEW_UNSPECIFIED;
    };
    m.view = viewOf(b.value("view", ""));
    for (const auto& v : b.value("views", Json::array())) {
      if (m.views_count < 4 && v.is_string()) m.views[m.views_count++] = viewOf(v.get<std::string>());
    }
    for (const auto& r : b.value("renditions", Json::array())) {
      if (m.renditions_count >= 16) break;
      auto& x = m.renditions[m.renditions_count++];
      ipc::copyField(x.name, r.value("name", ""));
      x.height = r.value("height", 0u);
      x.selected = r.value("selected", false);
      ipc::copyField(x.hdr, r.value("hdr", ""));
    }
    for (const auto& a : b.value("audioChoices", Json::array())) {
      if (m.audio_choices_count >= 24) break;
      auto& x = m.audio_choices[m.audio_choices_count++];
      ipc::copyField(x.id, a.value("id", ""));
      ipc::copyField(x.language, a.value("language", ""));
      ipc::copyField(x.label, a.value("label", ""));
      x.original = a.value("original", false);
      x.dubbed = a.value("dubbed", false);
      x.selected = a.value("selected", false);
    }
    return built;
  }

  if (kind == "media_session_state") {
    // the server-owned transport and queue, as cadenced relays them beside a media_play
    out->which_body = cadence_screen_v1_Envelope_media_session_state_tag;
    auto& s = out->body.media_session_state;
    ipc::copyField(s.session_id, b.value("sessionId", ""));
    for (const auto& a : b.value("actions", Json::array())) {
      if (s.actions_count >= 8 || !a.is_string()) continue;
      ipc::copyField(s.actions[s.actions_count++], a.get<std::string>());
    }
    const auto next = b.value("upNext", Json::array());
    if (!next.empty()) {
      auto* items = out.keepArray<cadence_v2_MediaQueueItem>(std::min<std::size_t>(next.size(), 20));
      for (const auto& item : next) {
        if (!item.is_object()) continue;
        auto& q = items[s.up_next_count++];
        ipc::copyField(q.ref, item.value("ref", ""));
        setStr(out, q.title, item.value("title", ""));
        setStr(out, q.subtitle, item.value("subtitle", ""));
        q.duration_ms = item.value("durationMs", 0u);
        ipc::copyField(q.artwork_sha256, item.value("artworkSha256", ""));
      }
      s.up_next = items;
    }
    return built;
  }

  if (kind == "now_playing") {
    // the session audiod renders as cadenced names it (an airplay cast, spotify connect); {} or no sessionId: none.
    // a relative artworkPath is the working directory's; positionAtMs defaults to now, so the position runs
    out->which_body = cadence_screen_v1_Envelope_now_playing_tag;
    auto& n = out->body.now_playing;
    ipc::copyField(n.session_id, b.value("sessionId", ""));
    const auto source = b.value("source", "airplay");
    n.source = source == "soloist" || source == "spotify" ? cadence_v2_MediaSource_MEDIA_SOURCE_SOLOIST
               : source == "url"                         ? cadence_v2_MediaSource_MEDIA_SOURCE_URL
                                                         : cadence_v2_MediaSource_MEDIA_SOURCE_AIRPLAY;
    const auto state = b.value("state", n.session_id[0] != '\0' ? "playing" : "idle");
    n.state = state == "playing"  ? cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING
              : state == "paused" ? cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED
                                  : cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE;
    n.position_ms = b.value("positionMs", 0ull);
    n.position_at_ms = b.value("positionAtMs", static_cast<std::int64_t>(std::chrono::duration_cast<std::chrono::milliseconds>(
                                                    std::chrono::system_clock::now().time_since_epoch()).count()));
    n.duration_ms = b.value("durationMs", 0ull);
    setStr(out, n.title, b.value("title", ""));
    setStr(out, n.artist, b.value("artist", ""));
    setStr(out, n.album, b.value("album", ""));
    setStr(out, n.sender, b.value("sender", ""));
    for (const auto& a : b.value("actions", Json::array({"pause", "resume", "stop"}))) {
      if (n.actions_count >= 8 || !a.is_string()) continue;
      const auto v = a.get<std::string>();
      const auto action = v == "pause"    ? cadence_screen_v1_MediaAction_MEDIA_ACTION_PAUSE
                          : v == "resume" ? cadence_screen_v1_MediaAction_MEDIA_ACTION_RESUME
                          : v == "stop"   ? cadence_screen_v1_MediaAction_MEDIA_ACTION_STOP
                          : v == "seek"   ? cadence_screen_v1_MediaAction_MEDIA_ACTION_SEEK
                          : v == "next"   ? cadence_screen_v1_MediaAction_MEDIA_ACTION_NEXT
                          : v == "previous" ? cadence_screen_v1_MediaAction_MEDIA_ACTION_PREVIOUS
                                            : cadence_screen_v1_MediaAction_MEDIA_ACTION_UNSPECIFIED;
      if (action != cadence_screen_v1_MediaAction_MEDIA_ACTION_UNSPECIFIED) n.actions[n.actions_count++] = action;
    }
    std::string art = b.value("artworkPath", "");
    if (!art.empty()) {
      std::error_code ec;
      const auto abs = std::filesystem::absolute(art, ec);
      if (!ec) art = abs.string();
    }
    ipc::copyField(n.artwork_path, art);
    ipc::copyField(n.artwork_sha256, b.value("artworkSha256", ""));
    n.color_a = b.value("colorA", 0u);
    n.color_b = b.value("colorB", 0u);
    return built;
  }

  if (error != nullptr) *error = "unknown message " + kind;
  return std::nullopt;
}

}  // namespace cadence::screen::sim
