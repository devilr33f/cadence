#include "app/routes/playback/context.h"

#include <RmlUi/Core/ElementDocument.h>
#include <nlohmann/json.hpp>

#include <algorithm>
#include <cstdio>

namespace cadence::screen::routes {

std::uint64_t PlaybackContext::position() const {
  const auto now = host != nullptr ? host->epochMs() : 0;
  // the item's bar; anything else the player plays (ambient's clip) by its own state, run on while it plays
  if (session && (session->device || state.sessionId.empty() || state.sessionId == session->sessionId)) {
    return pos.at(now, state.durationMs);
  }
  std::uint64_t p = state.positionMs;
  if (playing() && state.positionAtMs > 0) {
    const auto ahead = now - state.positionAtMs;
    if (ahead > 0 && ahead < PositionModel::kRunOnMs) {
      p += static_cast<std::uint64_t>(static_cast<double>(ahead) * std::max(0.0f, state.rate));
    }
  }
  if (state.durationMs > 0) p = std::min(p, state.durationMs);
  return p;
}

namespace {

// a seek's target within the item: never past its last second
std::uint64_t clampTarget(std::int64_t target, std::uint64_t durationMs) {
  if (durationMs > 1000) target = std::min<std::int64_t>(target, static_cast<std::int64_t>(durationMs) - 1000);
  return static_cast<std::uint64_t>(std::max<std::int64_t>(0, target));
}

// the player's seek a command became: none from a player that counts none (its readings then count by time and
// distance, as cadenced's echoes do)
std::optional<std::uint32_t> seqOf(const app::PlayerControl& p) {
  const auto seq = p.state().seekSeq;
  return seq > 0 ? std::optional<std::uint32_t>(seq) : std::nullopt;
}

}  // namespace

std::string PlaybackContext::seekBy(std::int64_t deltaMs) const {
  const auto now = host != nullptr ? host->epochMs() : 0;
  const auto base = pos.at(now, state.durationMs);
  const auto target = clampTarget(static_cast<std::int64_t>(base) + deltaMs, state.durationMs);
  if (session && (session->device || session->follow)) {
    // cadenced seeks a session it renders to a position alone; a follower's seek is the group's
    const auto r = deviceCommand(cadence_screen_v1_MediaAction_MEDIA_ACTION_SEEK, target);
    if (r.empty()) pos.seek(target, now, std::nullopt);
    return r;
  }
  auto* p = player();
  if (p == nullptr) return "unsupported";
  player::Command c;
  c.action = player::CommandAction::Seek;
  c.relative = true;
  c.deltaMs = static_cast<std::int64_t>(target) - static_cast<std::int64_t>(base);
  const auto r = p->command(c);
  if (r.empty()) pos.seek(target, now, seqOf(*p));
  return r;
}

Rml::Element* PlaybackContext::el(const std::string& id) const {
  auto* page = host != nullptr ? host->ui().page() : nullptr;
  return page != nullptr ? page->GetElementById(id) : nullptr;
}

std::string PlaybackContext::command(player::CommandAction action, std::uint64_t positionMs, const std::string& track,
                                     cadence_v2_MediaTrackKind kind) const {
  // a follower's transport is the group's, but its stop: the user leaves the item, the group plays on
  if (session && (session->device || (session->follow && action != player::CommandAction::Stop &&
                                       action != player::CommandAction::SelectTrack))) {
    switch (action) {
      case player::CommandAction::Pause: return deviceCommand(cadence_screen_v1_MediaAction_MEDIA_ACTION_PAUSE);
      case player::CommandAction::Resume: return deviceCommand(cadence_screen_v1_MediaAction_MEDIA_ACTION_RESUME);
      case player::CommandAction::Stop: return deviceCommand(cadence_screen_v1_MediaAction_MEDIA_ACTION_STOP);
      case player::CommandAction::Seek: {
        const auto target = clampTarget(static_cast<std::int64_t>(positionMs), state.durationMs);
        const auto r = deviceCommand(cadence_screen_v1_MediaAction_MEDIA_ACTION_SEEK, target);
        if (r.empty()) pos.seek(target, host->epochMs(), std::nullopt);
        return r;
      }
      default: return "unsupported";
    }
  }
  auto* p = player();
  if (p == nullptr) return "unsupported";
  player::Command c;
  c.action = action;
  c.positionMs = positionMs;
  c.trackId = track;
  c.trackKind = kind;
  const auto r = p->command(c);
  // the bar holds a seek's target until the player reads that seek's position
  if (action == player::CommandAction::Seek && r.empty() && host != nullptr) {
    pos.seek(clampTarget(static_cast<std::int64_t>(positionMs), state.durationMs), host->epochMs(), seqOf(*p));
  }
  return r;
}

std::string PlaybackContext::deviceCommand(cadence_screen_v1_MediaAction action, std::uint64_t positionMs) const {
  if (!session || !(session->device || session->follow) || host == nullptr) return "unsupported";
  // a follower's goes to the group's session, once its NowPlaying named one
  const auto& id = session->device ? session->sessionId : session->groupSession;
  if (id.empty()) return "unavailable";
  cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
  e.which_body = cadence_screen_v1_Envelope_now_playing_command_tag;
  auto& c = e.body.now_playing_command;
  std::snprintf(c.session_id, sizeof c.session_id, "%s", id.c_str());
  c.action = action;
  c.position_ms = positionMs;
  return host->send(e) ? "" : "unavailable";
}

void PlaybackContext::toast(const std::string& rml, int ms) const {
  ui::ToastSpec t;
  t.id = "playback";
  t.text = rml;
  t.durationMs = ms;
  host->ui().toast(t);
}

void PlaybackContext::itemAction(const std::string& actionId, const std::function<void()>& changed) {
  if (!session) return;
  auto& s = *session;
  const ItemAction* action = nullptr;
  for (const auto& a : s.itemActions)
    if (a.id == actionId) action = &a;
  if (action == nullptr || !action->enabled) return;
  const auto kind = action->kind;
  // optimistic: like and dislike are a pair, choosing one clears the other in the same step (design §14)
  const auto before = s.itemActions;
  bool on = false;
  for (auto& a : s.itemActions) {
    if (a.id == actionId) {
      a.toggled = !a.toggled;
      on = a.toggled;
    } else if ((kind == ActionKind::Like && a.kind == ActionKind::Dislike) || (kind == ActionKind::Dislike && a.kind == ActionKind::Like)) {
      a.toggled = false;
    }
  }
  if (changed) changed();
  const std::string sid = s.sessionId;
  const bool server = s.serverFields;
  nlohmann::json args = server ? nlohmann::json{{"sessionId", sid}, {"actionId", actionId}}
                               : nlohmann::json{{"ref", s.itemRef}, {"action", actionId}, {"on", on}};
  rpc.call(server ? "media.item_action" : "catalog.act", args.dump(), [this, before, sid, server, changed](const RpcAnswer& a) {
    if (!session || session->sessionId != sid) return;
    if (a.ok()) {
      // the server's toggled state comes with its next media_session_state; the catalog answers it here
      if (!server) applyActions(*session, a.json);
    } else {
      // refused: both of the pair back as they were (section 9)
      session->itemActions = before;
      toast("[[common.unavailable_now]]");
    }
    if (changed) changed();
  }, nowMs);
}

void setText(Rml::Element* e, const std::string& rml) {
  if (e != nullptr && e->GetInnerRML() != rml) e->SetInnerRML(rml);
}

void setStyle(Rml::Element* e, const char* property, const std::string& value) {
  if (e != nullptr) e->SetProperty(property, value);
}

std::string dp(double v) {
  char buf[32];
  std::snprintf(buf, sizeof buf, "%.1fdp", v);
  return buf;
}

std::string pct(double f) {
  char buf[32];
  std::snprintf(buf, sizeof buf, "%.3f%%", std::clamp(f, 0.0, 1.0) * 100);
  return buf;
}

bool isPress(const platform::KeyEvent& e) {
  return e.action == cadence_screen_v1_KeyAction_KEY_ACTION_UP || e.action == cadence_screen_v1_KeyAction_KEY_ACTION_PRESS;
}

bool isDownOrRepeat(const platform::KeyEvent& e) {
  return e.action == cadence_screen_v1_KeyAction_KEY_ACTION_DOWN || e.action == cadence_screen_v1_KeyAction_KEY_ACTION_REPEAT ||
         e.action == cadence_screen_v1_KeyAction_KEY_ACTION_PRESS;
}

}  // namespace cadence::screen::routes
