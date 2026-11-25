#include "app/routes/core/context.h"

#include "ipc/codec.h"

#include <algorithm>

namespace cadence::screen::routes {

void ContextPublisher::set(const ScreenContextData& data, std::int64_t nowMs) {
  if (haveCurrent_ && data == current_) return;
  current_ = data;
  haveCurrent_ = true;
  // trailing: a burst of focus moves sends one context, half a second after the last; a page that keeps changing
  // (results streaming in) still sends one every 2 s
  if (dueMs_ < 0) firstMs_ = nowMs;
  dueMs_ = std::min(nowMs + kContextDebounceMs, firstMs_ + kContextMaxWaitMs);
}

void ContextPublisher::flush() {
  if (!haveCurrent_) return;
  send();
}

void ContextPublisher::update(std::int64_t nowMs) {
  if (dueMs_ >= 0 && nowMs >= dueMs_) send();
}

bool ContextPublisher::send() {
  dueMs_ = -1;
  ipc::OutEnvelope out(cadence_screen_v1_Envelope_screen_context_tag);
  auto& c = out->body.screen_context;
  c.revision = ++revision_;
  c.route = current_.route;
  ipc::copyField(c.page_ref, current_.pageRef);
  ipc::copyField(c.focused_ref, current_.focusedRef);
  for (const auto& r : current_.visibleRefs) {
    if (c.visible_refs_count >= kMaxVisibleRefs) break;
    ipc::copyField(c.visible_refs[c.visible_refs_count++], r);
  }
  const bool selection = !current_.source.empty() || !current_.voiceover.empty() || !current_.quality.empty() ||
                         !current_.episode.empty();
  c.has_selection = selection;
  if (selection) {
    ipc::copyField(c.selection.source, current_.source);
    ipc::copyField(c.selection.voiceover, current_.voiceover);
    ipc::copyField(c.selection.quality, current_.quality);
    ipc::copyField(c.selection.episode, current_.episode);
  }
  if (!current_.query.empty()) c.query = out.keep(current_.query);
  c.drawer_open = current_.drawerOpen;
  if (current_.hasPlayer) {
    c.has_player = true;
    ipc::copyField(c.player.item_ref, current_.playerItemRef);
    c.player.position_ms = current_.positionMs;
    auto* tracks = current_.tracks.empty() ? nullptr : out.keepArray<cadence_v2_MediaTrack>(current_.tracks.size());
    for (std::size_t i = 0; i < current_.tracks.size(); ++i) {
      const auto& t = current_.tracks[i];
      cadence_v2_MediaTrack& mt = tracks[i];
      mt = cadence_v2_MediaTrack_init_zero;
      ipc::copyField(mt.id, t.id);
      mt.kind = t.kind;
      ipc::copyField(mt.language, t.language);
      mt.label = out.keep(t.label);
      mt.selected = t.selected;
      mt.external = t.external;
    }
    c.player.tracks_count = static_cast<pb_size_t>(current_.tracks.size());
    c.player.tracks = tracks;
  }
  if (!send_ || !send_(out.get())) return false;
  sent_++;
  return true;
}

}  // namespace cadence::screen::routes
