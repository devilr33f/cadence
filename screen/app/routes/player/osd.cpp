#include "app/routes/player/osd.h"

#include "app/routes/core/failure-card.h"
#include "app/routes/core/failure.h"
#include "app/routes/panels/chapters.h"
#include "app/routes/player/cache-info.h"
#include "app/routes/player/source-line.h"
#include "app/routes/player/switcher.h"
#include "app/routes/playback/server-choices.h"
#include "ui/core/rml.h"

#include <RmlUi/Core/ElementDocument.h>
#include <nlohmann/json.hpp>

#include <algorithm>
#include <cstdlib>
#include <ctime>

namespace cadence::screen::routes {

namespace {

using ui::rml::esc;

constexpr std::int64_t kAutoHideMs = 5000;
constexpr std::int64_t kCompactMs = 2000;
constexpr std::int64_t kOkShowMs = 3000;
constexpr std::int64_t kBufferingAfterMs = 1000;
constexpr std::int64_t kUpNextLeadMs = 30000;
constexpr std::int64_t kUpNextCountMs = 10000;
constexpr std::int64_t kUndoSkipMs = 3000;
// previous within the first seconds goes to the item before, later to this one's start
constexpr std::uint64_t kPreviousRestartMs = 3000;

// the builder's markup with an id on its first element
std::string withId(std::string html, const std::string& id) {
  const auto at = html.find(' ');
  if (at != std::string::npos) html.insert(at, " id=\"" + id + "\"");
  return html;
}

std::string iconFor(ActionKind k, bool toggled) {
  switch (k) {
    case ActionKind::Like:
      return "like";
    case ActionKind::Dislike:
      return "dislike";
    case ActionKind::Save:
      return "heart";
    case ActionKind::Subscribe:
      return "subscribe";
    case ActionKind::Queue:
      return "queue_add";
    default:
      // a toggled chip shows its check already
      return toggled ? "" : "more";
  }
}

std::string trackLabel(const player::Track& t) {
  if (!t.label.empty()) return esc(t.label);
  if (!t.language.empty()) return esc(t.language);
  return esc(t.id);
}

std::string clockOf(std::int64_t epochMs) {
  const auto secs = static_cast<std::time_t>(epochMs / 1000);
  std::tm local{};
  localtime_r(&secs, &local);
  char buf[16];
  std::snprintf(buf, sizeof buf, "%d:%02d", local.tm_hour, local.tm_min);
  return buf;
}

}  // namespace

PlayerPage::PlayerPage(PlaybackContext& c, SideSheet& sheet) : Page(c), sheet_(sheet) {}

std::string PlayerPage::rowB() const {
  const auto& s = *c_.session;
  std::string b;
  if (s.has("previous")) b += withId(ui::rml::iconButton("previous", "[[player.osd.previous]]", "osd:previous"), "o-prev");
  b += withId(ui::rml::iconButton("back10", "−10", "osd:back10"), "o-back10");
  const bool paused = c_.state.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED;
  b += withId(ui::rml::iconButton(paused ? "play" : "pause", paused ? "[[player.osd.play]]" : "[[player.osd.pause]]",
                                  "osd:play", true),
              "o-play");
  b += withId(ui::rml::iconButton("forward10", "+10", "osd:fwd10"), "o-fwd10");
  // a follower's next is the server's alone (its leader's, multiroom P4): never an episode it would play itself
  if (s.has("next") || (s.upNext && !s.follow)) b += withId(ui::rml::iconButton("next", "[[player.osd.next]]", "osd:next"), "o-next");
  b += "<div class=\"chips\">";
  // the chips show their current values: voiceover (torrplayer), audio, subtitles, quality
  if (s.torrplayer()) {
    b += withId(ui::rml::chip("[[player.osd.voiceover|name=" + esc(s.voiceover.empty() ? s.providerSource : s.voiceover) + "]]",
                              "audio", std::nullopt, "osd:voiceover"),
                "o-vo");
  }
  std::string audio, subs = "[[player.osd.subtitles_off]]";
  int audios = 0, subtitles = 0;
  for (const auto& t : c_.state.tracks) {
    if (t.kind == cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_AUDIO) {
      ++audios;
      if (t.selected) audio = trackLabel(t);
    }
    if (t.kind == cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_SUBTITLE) {
      ++subtitles;
      if (t.selected) subs = trackLabel(t);
    }
  }
  if (audios > 1) b += withId(ui::rml::chip(audio.empty() ? "[[player.sheet.audio]]" : audio, "audio", std::nullopt, "osd:audio"), "o-audio");
  // a server video's own audio languages and views (rpc media.video)
  if (const auto a = choices::currentAudio(s); !a.empty()) b += withId(ui::rml::chip(a, "audio", std::nullopt, "osd:saudio"), "o-saudio");
  if (const auto v = choices::currentView(s); !v.empty()) b += withId(ui::rml::chip(v, "film", std::nullopt, "osd:view"), "o-view");
  if (subtitles > 0) b += withId(ui::rml::chip(subs, "subtitles", std::nullopt, "osd:subs"), "o-subs");
  if (s.renditions.size() > 1) {
    std::string q;
    for (const auto& r : s.renditions)
      if (r.selected) q = esc(r.name);
    b += withId(ui::rml::chip(q.empty() ? esc(s.renditions.front().name) : q, "quality", std::nullopt, "osd:quality"), "o-quality");
  }
  return b + "</div>";
}

std::string PlayerPage::rowC() const {
  const auto& s = *c_.session;
  std::string r;
  if (!s.channelName.empty()) {
    std::string channelPanel;
    for (const auto& p : s.panels)
      if (p.kind == "channel") channelPanel = p.id;
    r += withId(ui::rml::chip(esc(s.channelName), "user", std::nullopt, channelPanel.empty() ? "" : "osd:panel:" + channelPanel),
                "o-channel");
  }
  for (std::size_t i = 0; i < s.itemActions.size(); ++i) {
    const auto& a = s.itemActions[i];
    std::string chip = ui::rml::chip(esc(a.label), iconFor(a.kind, a.toggled), a.toggled, "osd:act:" + std::to_string(i));
    if (!a.enabled) chip.replace(chip.find("class=\"chip"), 11, "class=\"chip unavailable");
    r += withId(std::move(chip), "o-act-" + std::to_string(i));
  }
  for (std::size_t i = 0; i < s.panels.size(); ++i) {
    const auto& p = s.panels[i];
    if (p.kind == "channel" && !s.channelName.empty()) continue;
    const std::string label = esc(p.title) + (p.countLabel.empty() ? "" : " · " + esc(p.countLabel));
    r += withId(ui::rml::chip(label, {}, std::nullopt, "osd:panel:" + p.id), "o-panel-" + std::to_string(i));
  }
  if (!s.chapters.empty()) r += withId(ui::rml::chip("[[player.osd.chapters]]", "playlist", std::nullopt, "osd:chapters"), "o-chapters");
  if (s.upNext || !s.episodes.empty()) r += withId(ui::rml::chip("[[audio.queue]]", "playlist", std::nullopt, "osd:queue"), "o-queue");
  return r;
}

std::string PlayerPage::badges() const {
  const auto& s = *c_.session;
  std::string b;
  if (!s.providerName.empty()) {
    // torrplayer's badge names the source it played from (anime365); another provider's source is a chip beside
    b += ui::rml::badge(ui::rml::BadgeKind::Provider, esc(providerLabel(s)), esc(monogram(s.providerId)));
    if (!s.providerSource.empty() && !s.torrplayer()) b += ui::rml::sourceChip(esc(s.providerSource));
  }
  // what the player decodes: «HEVC · AAC», once mpv names the tracks
  if (const auto codecs = codecLine(c_.state.tracks); !codecs.empty()) b += ui::rml::sourceChip(esc(codecs));
  for (const auto& q : s.badges) b += ui::rml::badge(ui::rml::BadgeKind::Quality, esc(q));
  if (s.live) b += ui::rml::badge(ui::rml::BadgeKind::Live, {});
  return b;
}

std::string PlayerPage::subLine() const {
  const auto& s = *c_.session;
  std::string sub;
  if (s.season > 0 && !s.episode.empty()) {
    sub = "[[episodes.season_episode|season=" + std::to_string(s.season) + "|n=" + esc(s.episode) + "]]";
  } else if (!s.episode.empty()) {
    sub = "[[episodes.episode|n=" + esc(s.episode) + "]]";
  }
  if (!s.series.empty() && !sub.empty()) sub = esc(s.series) + " · " + sub;
  if (sub.empty()) sub = esc(!s.channelName.empty() ? s.channelName : s.artist);
  if (!s.statsLabel.empty()) sub += (sub.empty() ? "" : " · ") + esc(s.statsLabel);
  return sub;
}

std::string PlayerPage::page() const {
  const auto& s = *c_.session;
  std::string top = "<div class=\"osd-top\"><div><div class=\"badges\" id=\"o-badges\">" + badges() + "</div>";
  // design §19: a quiet line for a play that is not a plain stream
  if (s.playable == cadence_v2_PlayableKind_PLAYABLE_KIND_MATCH) {
    top += "<div class=\"meta\">[[audio.match|provider=" + esc(s.matchedFrom.empty() ? "YouTube Music" : s.matchedFrom) + "]]</div>";
  }
  top += "</div><div class=\"when\"><div class=\"now\" id=\"o-now\"/><div class=\"ends\" id=\"o-ends\"/></div></div>";
  const std::string sub = subLine();

  std::string gaps;
  const auto dur = c_.state.durationMs;
  if (dur > 0) {
    for (std::size_t i = 1; i < s.chapters.size(); ++i)
      gaps += "<div class=\"gapmark\" style=\"left: " + pct(static_cast<double>(s.chapters[i].startMs) / static_cast<double>(dur)) + ";\"/>";
  }
  const std::string scrub = "<div class=\"bar\"><div class=\"buffered\" id=\"o-buf\"/><div class=\"played\" id=\"o-played\"/>" + gaps +
                            "</div><div class=\"thumb\" id=\"o-thumb\"/><div class=\"chapter\" id=\"o-chapter\"/>"
                            "<div class=\"times\"><span id=\"o-pos\"/><span id=\"o-dur\"/></div>"
                            "<div class=\"cache-info hidden\" id=\"o-cache\"/>";

  std::string p = "<div id=\"player\"><div class=\"subs size-" + std::string(1, static_cast<char>(std::tolower(c_.settings.subtitleSize))) +
                  "\" id=\"subs\"/>";
  p += "<div class=\"osd gone off\" id=\"osd\"><div class=\"scrim-top\"/><div class=\"scrim-bottom\"/>" + top +
       "<div class=\"osd-body\" id=\"o-body\"><div class=\"osd-title\" id=\"o-title\">" + esc(s.title) +
       "</div><div class=\"osd-sub\" id=\"o-sub\">" + sub +
       // the osd's spot for a segment's action (skip the opening or the ending): right-aligned on the title's lines,
       // above the scrubber, clear of the transport and the chips; empty until something fills it
       "</div><div class=\"osd-slot\" id=\"o-slot\"/><div class=\"osd-row-a\"><div class=\"scrub f\" id=\"o-scrub\" data-action=\"osd:scrub\">" + scrub +
       "</div></div><div class=\"osd-row-b\" id=\"o-row-b\">" + rowB() + "</div><div class=\"osd-row-c\" id=\"o-row-c\">" +
       rowC() + "</div></div></div>";
  // the compact bar: scrubber and times, no focus
  p += "<div class=\"compact gone\" id=\"compact\"><div class=\"scrub\" id=\"c-scrub\"><div class=\"bar\"><div class=\"buffered\" "
       "id=\"c-buf\"/><div class=\"played\" id=\"c-played\"/></div><div class=\"times\"><span id=\"c-pos\"/><span "
       "id=\"c-dur\"/></div></div></div>";
  p += "<div class=\"skip f hidden\" id=\"skip\" data-action=\"osd:skip\"><span id=\"skip-text\"/><div class=\"left\" id=\"skip-left\"/></div>";
  p += "<div class=\"upnext hidden\" id=\"upnext\"><div class=\"uhead\"><div class=\"uart\" id=\"un-art\"/><div class=\"utext\"><div class=\"utitle line1\" "
       "id=\"un-title\"/><div class=\"umeta\" id=\"un-meta\"/></div></div><div class=\"uline\" id=\"un-line\"/><div class=\"ubuttons\">" +
       withId(ui::rml::button({"[[title.watch]]", "play", "upnext:watch", true, false, false, {}, {}}), "un-watch") +
       withId(ui::rml::button({"[[player.up_next.other_voiceover]]", "audio", "upnext:switch", false, false, false, {}, "hidden"}), "un-switch") +
       withId(ui::rml::button({"[[common.cancel]]", {}, "upnext:cancel", false, false, false, {}, {}}), "un-cancel") + "</div></div>";
  p += "<div class=\"buffering hidden\" id=\"buffering\">" + ui::rml::spinner() + "<span id=\"buf-text\"/><span class=\"buf-speed\" id=\"buf-speed\"/></div>";
  // the player's failure as it came (failure.h): the source, the player's own code, the call; filled when it fails
  ui::rml::ErrorCard fail;
  fail.title = "[[resolving.play_failed]]";
  fail.source = "<span id=\"fail-source\"/>";
  fail.plain = "<span id=\"fail-plain\"/>";
  fail.hint = "<span id=\"fail-hint\"/>";
  fail.reason = "<span id=\"fail-reason\"/>";
  fail.detail = "<span id=\"fail-detail\"/>";
  fail.actions.push_back({"[[common.retry]]", "refresh", "fail:retry", true, false, false, "f-retry", {}});
  if (s.torrplayer()) fail.actions.push_back({"[[picker.other_source]]", "audio", "fail:switch", false, false, false, "f-switch", {}});
  fail.actions.push_back({"[[common.back]]", "back", "fail:back", false, false, false, "f-back", {}});
  p += "<div class=\"failure hidden\" id=\"failure\"><div class=\"fbody\">" + ui::rml::errorCard(fail) + "</div></div>";
  p += "<div id=\"side-host\"/></div>";
  return p;
}

void PlayerPage::enter() {
  osd_ = OsdState{};
  okDown_ = false;
  skippedAtMs_ = -1;
  upNextAtMs_ = -1;
  upNextCancelled_ = false;
  upNextMissing_ = false;
  bufferingSinceMs_ = -1;
  lastSubs_.clear();
  firstLoad_ = true;
}

std::string PlayerPage::body() { return c_.session ? page() : std::string("<div/>"); }

void PlayerPage::loaded(bool restoring) {
  (void)restoring;
  lastSubs_.clear();
  refresh();
  subtitleChanged();
  // the film starts with the osd shown for 3 s, focus on play/pause (design §18.7); back from a route above, the
  // osd shows again where it was
  if (firstLoad_) {
    firstLoad_ = false;
    show(OsdState::Transport, -1, kOkShowMs);
  } else if (osd_.shown) {
    show(osd_.row, -1, kOkShowMs);
  }
}

void PlayerPage::leave() { sheet_.close(); }

void PlayerPage::sessionChanged() {
  // a new item or its details (actions, panels, up next): the rows again, focus kept where it can be
  if (!c_.session) return;
  // the focused control by its id: a control added before it (a next that the server's up next brings) leaves the
  // focus where it was, not on whatever took its column
  auto* focused = c_.ui().focus().current();
  const std::string focusId = focused != nullptr ? focused->GetId() : std::string();
  // the item's details may name what its play left out: the title, the line under it, the provider's badge
  if (auto* t = c_.el("o-title")) t->SetInnerRML(esc(c_.session->title));
  if (auto* t = c_.el("o-sub")) t->SetInnerRML(subLine());
  if (auto* t = c_.el("o-badges")) t->SetInnerRML(badges());
  if (auto* b = c_.el("o-row-b")) b->SetInnerRML(rowB());
  if (auto* r = c_.el("o-row-c")) r->SetInnerRML(rowC());
  if (osd_.shown) {
    bool kept = false;
    if (!focusId.empty()) {
      for (const auto row : {OsdState::Transport, OsdState::Details}) {
        const auto all = items(row);
        for (std::size_t i = 0; i < all.size(); ++i) {
          if (all[i]->GetId() != focusId || osd_.row != row) continue;
          osd_.column[static_cast<std::size_t>(row)] = static_cast<int>(i);
          kept = true;
        }
      }
    }
    if (kept || osd_.row == OsdState::Scrubber) {
      if (auto* f = c_.el(focusId); f != nullptr && c_.ui().focus().current() != f) c_.ui().focus().focus(f);
      c_.ui().markDirty();
    } else {
      place();
    }
  }
  refresh();
}

std::vector<Rml::Element*> PlayerPage::items(OsdState::Row row) const {
  std::vector<Rml::Element*> out;
  const char* id = row == OsdState::Scrubber ? nullptr : row == OsdState::Transport ? "o-row-b" : "o-row-c";
  if (id == nullptr) {
    if (auto* s = c_.el("o-scrub")) out.push_back(s);
    return out;
  }
  auto* r = c_.el(id);
  if (r == nullptr) return out;
  Rml::ElementList list;
  r->QuerySelectorAll(list, ".f");
  for (auto* e : list) out.push_back(e);
  return out;
}

std::string PlayerPage::focusedId() const {
  auto* f = c_.ui().focus().current();
  return f != nullptr ? f->GetId() : std::string();
}

void PlayerPage::placeSkip() {
  auto* pill = c_.el("skip");
  auto* slot = c_.el("o-slot");
  auto* home = c_.el("compact");
  if (pill == nullptr || slot == nullptr || home == nullptr || home->GetParentNode() == nullptr) return;
  auto* parent = pill->GetParentNode();
  if (osd_.shown && parent != slot) {
    auto moved = parent->RemoveChild(pill);
    pill = slot->AppendChild(std::move(moved));
    pill->SetClass("inslot", true);
  } else if (!osd_.shown && parent == slot) {
    slotFocused_ = false;
    auto moved = slot->RemoveChild(pill);
    pill = home->GetParentNode()->InsertBefore(std::move(moved), home->GetNextSibling());
    pill->SetClass("inslot", false);
  }
}

void PlayerPage::place() {
  slotFocused_ = false;
  auto all = items(osd_.row);
  if (all.empty()) {
    // a row with nothing (no item actions yet): transport
    osd_.row = OsdState::Transport;
    all = items(osd_.row);
    if (all.empty()) return;
  }
  auto& col = osd_.column[static_cast<std::size_t>(osd_.row)];
  col = std::clamp(col, 0, static_cast<int>(all.size()) - 1);
  c_.ui().focus().focus(all[static_cast<std::size_t>(col)]);
  if (auto* body = c_.el("o-body")) body->SetClass("lifted", osd_.row == OsdState::Details);
  c_.ui().markDirty();
}

void PlayerPage::show(OsdState::Row row, int column, std::int64_t forMs) {
  osd_.shown = true;
  placeSkip();
  osd_.row = row;
  if (column >= 0) osd_.column[static_cast<std::size_t>(row)] = column;
  if (row == OsdState::Transport && column < 0) {
    // play/pause: after previous and −10
    const auto all = items(OsdState::Transport);
    for (std::size_t i = 0; i < all.size(); ++i)
      if (all[i]->GetId() == "o-play") osd_.column[1] = static_cast<int>(i);
  }
  // a timed show hides after forMs, else after the usual 5 s
  osd_.lastKeyMs = c_.nowMs - (forMs > 0 ? kAutoHideMs - forMs : 0);
  if (auto* o = c_.el("osd")) {
    o->SetClass("off", false);
    o->SetClass("gone", false);
  }
  if (auto* compact = c_.el("compact")) compact->SetClass("gone", true);
  if (auto* subs = c_.el("subs")) subs->SetClass("raised", true);
  place();
  refresh();
  c_.contextChanged();
}

void PlayerPage::hide() {
  osd_.shown = false;
  placeSkip();
  if (auto* o = c_.el("osd")) o->SetClass("gone", true);
  // once faded, out of the focus manager's reach too (visibility cuts the fade, so it waits for it)
  if (auto* osd = c_.el("osd")) {
    // the element's own pointer: neither this page nor its document need outlive the wait
    c_.ui().after(240, [o = osd->GetObserverPtr()] {
      if (auto* e = o.get(); e != nullptr && e->IsClassSet("gone")) e->SetClass("off", true);
    });
  }
  if (auto* subs = c_.el("subs")) subs->SetClass("raised", false);
  if (auto* body = c_.el("o-body")) body->SetClass("lifted", false);
  // the skip pill keeps focus while its segment plays; otherwise nothing on the page holds it
  if (auto* skip = c_.el("skip"); skip != nullptr && !skip->IsClassSet("hidden")) {
    c_.ui().focus().focus(skip);
  } else if (auto* f = c_.ui().focus().current()) {
    f->Blur();
  }
  c_.ui().markDirty();
}

void PlayerPage::seek(int direction, bool held) {
  // held: accelerating, 10 s steps, then 30 s, then 60 s
  osd_.seekRepeats = held ? osd_.seekRepeats + 1 : 0;
  const std::int64_t step = osd_.seekRepeats > 12 ? 60000 : osd_.seekRepeats > 4 ? 30000 : 10000;
  // a relative seek from where the bar is (a seek under way: its target, so rapid presses add up); the bar holds the
  // target at once and until the player reads it, so no reading from before the seek moves it back
  c_.seekBy(direction * step);
  refresh();
}

bool PlayerPage::okHidden(const platform::KeyEvent& e) {
  // OK with the osd hidden: during a skippable segment the pill skips, else play/pause and the osd for 3 s
  if (e.action == cadence_screen_v1_KeyAction_KEY_ACTION_DOWN) {
    okDown_ = true;
    return true;
  }
  if (!isPress(e) || (!okDown_ && e.action != cadence_screen_v1_KeyAction_KEY_ACTION_PRESS)) return true;
  okDown_ = false;
  if (skippedAtMs_ >= 0 && c_.nowMs - skippedAtMs_ < kUndoSkipMs) {
    // «ОК — вернуть»: back to where the auto-skip jumped from
    c_.command(player::CommandAction::Seek, skippedFromMs_);
    skippedAtMs_ = -1;
    c_.ui().clearToasts();
    return true;
  }
  if (skip_ != nullptr) {
    activate(ui::Activation{c_.el("skip"), "osd:skip", false, std::nullopt, std::nullopt});
    return true;
  }
  const bool paused = c_.state.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED;
  c_.command(paused ? player::CommandAction::Resume : player::CommandAction::Pause);
  show(OsdState::Transport, -1, kOkShowMs);
  return true;
}

void PlayerPage::syncFromFocus() {
  // focus may have moved without a key of ours (a repair, the gallery's walk): rows and columns follow it
  auto* f = c_.ui().focus().current();
  if (f == nullptr || !osd_.shown) return;
  for (int row = 0; row < 3; ++row) {
    const auto all = items(static_cast<OsdState::Row>(row));
    const auto it = std::find(all.begin(), all.end(), f);
    if (it == all.end()) continue;
    osd_.row = static_cast<OsdState::Row>(row);
    osd_.column[static_cast<std::size_t>(row)] = static_cast<int>(it - all.begin());
    if (auto* body = c_.el("o-body")) body->SetClass("lifted", osd_.row == OsdState::Details);
    return;
  }
}

bool PlayerPage::key(const platform::KeyEvent& e) {
  if (!c_.session) return false;
  syncFromFocus();
  if (sheet_.isOpen()) return sheet_.key(e);
  if (c_.ui().levelOpen()) {
    const bool used = c_.ui().key(e);
    osd_.lastKeyMs = c_.nowMs;
    return used;
  }
  const auto k = e.key;
  // the error block and up next hold the focus on their own buttons
  auto* failure = c_.el("failure");
  auto* upnext = c_.el("upnext");
  const bool failing = failure != nullptr && !failure->IsClassSet("hidden");
  const bool upNext = upnext != nullptr && !upnext->IsClassSet("hidden");
  if (failing || (upNext && !osd_.shown)) {
    if (k == cadence_screen_v1_Key_KEY_BACK) {
      if (!isPress(e)) return true;
      if (failing) {
        c_.go(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
      } else {
        // BACK cancels up next and keeps the credits playing
        activate(ui::Activation{nullptr, "upnext:cancel", false, std::nullopt, std::nullopt});
      }
      return true;
    }
    if (k == cadence_screen_v1_Key_KEY_UP || k == cadence_screen_v1_Key_KEY_DOWN) return true;
    return c_.ui().key(e);
  }
  const bool press = isPress(e);
  const bool downish = isDownOrRepeat(e);
  const bool held = e.action == cadence_screen_v1_KeyAction_KEY_ACTION_REPEAT;

  if (!osd_.shown) {
    switch (k) {
      case cadence_screen_v1_Key_KEY_OK:
        return okHidden(e);
      case cadence_screen_v1_Key_KEY_LEFT:
      case cadence_screen_v1_Key_KEY_RIGHT:
        if (!downish) return true;
        seek(k == cadence_screen_v1_Key_KEY_RIGHT ? 1 : -1, held);
        osd_.compactUntilMs = c_.nowMs + kCompactMs;
        if (auto* compact = c_.el("compact")) compact->SetClass("gone", false);
        c_.ui().markDirty();
        return true;
      case cadence_screen_v1_Key_KEY_UP:
        if (downish) show(OsdState::Transport);
        return true;
      case cadence_screen_v1_Key_KEY_DOWN:
        if (downish) show(OsdState::Details);
        return true;
      case cadence_screen_v1_Key_KEY_BACK:
        // one level up, the film playing on behind home (its picture under the page); home's «Продолжить» or BACK
        // brings it back (PlaybackRoutes::foreground)
        if (press) c_.go(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
        return true;
      case cadence_screen_v1_Key_KEY_PLAY_PAUSE:
      case cadence_screen_v1_Key_KEY_PLAY:
      case cadence_screen_v1_Key_KEY_PAUSE:
        if (e.action == cadence_screen_v1_KeyAction_KEY_ACTION_DOWN || e.action == cadence_screen_v1_KeyAction_KEY_ACTION_PRESS) {
          const bool paused = c_.state.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED;
          c_.command(k == cadence_screen_v1_Key_KEY_PAUSE || (k == cadence_screen_v1_Key_KEY_PLAY_PAUSE && !paused)
                         ? player::CommandAction::Pause
                         : player::CommandAction::Resume);
          show(OsdState::Transport, -1, kOkShowMs);
        }
        return true;
      default:
        return false;
    }
  }

  osd_.lastKeyMs = c_.nowMs;
  switch (k) {
    case cadence_screen_v1_Key_KEY_BACK:
      if (press) hide();
      return true;
    case cadence_screen_v1_Key_KEY_OK:
      return c_.ui().key(e);
    case cadence_screen_v1_Key_KEY_LEFT:
    case cadence_screen_v1_Key_KEY_RIGHT: {
      if (!downish) return true;
      const int dir = k == cadence_screen_v1_Key_KEY_RIGHT ? 1 : -1;
      // the skip pill alone in its slot: nothing beside it
      if (slotFocused_) return true;
      if (osd_.row == OsdState::Scrubber) {
        seek(dir, held);
        return true;
      }
      const auto all = items(osd_.row);
      auto& col = osd_.column[static_cast<std::size_t>(osd_.row)];
      // rows never wrap: a nudge at the end (the focus manager's)
      if (col + dir >= 0 && col + dir < static_cast<int>(all.size())) {
        col += dir;
        place();
      } else {
        c_.ui().focus().move(dir > 0 ? ui::Dir::Right : ui::Dir::Left);
      }
      return true;
    }
    case cadence_screen_v1_Key_KEY_UP:
    case cadence_screen_v1_Key_KEY_DOWN: {
      if (!downish) return true;
      // the slot sits above the scrubber: ↑ from it reaches the skip pill there, ↓ from the pill the scrubber again
      if (slotFocused_) {
        if (k == cadence_screen_v1_Key_KEY_DOWN) {
          osd_.row = OsdState::Scrubber;
          place();
        }
        return true;
      }
      if (k == cadence_screen_v1_Key_KEY_UP && osd_.row == OsdState::Scrubber) {
        auto* pill = c_.el("skip");
        if (pill != nullptr && pill->IsClassSet("inslot") && !pill->IsClassSet("hidden")) {
          c_.ui().focus().focus(pill);
          slotFocused_ = true;
          c_.ui().markDirty();
        }
        return true;
      }
      int row = static_cast<int>(osd_.row) + (k == cadence_screen_v1_Key_KEY_DOWN ? 1 : -1);
      while (row >= 0 && row <= 2 && items(static_cast<OsdState::Row>(row)).empty()) row += k == cadence_screen_v1_Key_KEY_DOWN ? 1 : -1;
      if (row < 0 || row > 2) return true;
      osd_.row = static_cast<OsdState::Row>(row);
      osd_.seekRepeats = 0;
      place();
      return true;
    }
    default:
      return false;
  }
}

void PlayerPage::openTracks(cadence_v2_MediaTrackKind kind, Rml::Element* opener) {
  std::string body = "<div class=\"list\">";
  const bool subs = kind == cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_SUBTITLE;
  if (subs) {
    bool any = false;
    for (const auto& t : c_.state.tracks)
      if (t.kind == kind && t.selected) any = true;
    body += ui::rml::chip("[[player.osd.subtitles_off]]", {}, !any, "track:off");
  }
  for (const auto& t : c_.state.tracks) {
    if (t.kind != kind) continue;
    body += ui::rml::chip(trackLabel(t), subs ? "subtitles" : "audio", t.selected, "track:" + esc(t.id));
  }
  body += "</div>";
  if (subs) {
    body += "<div class=\"sgroup\">[[settings.subtitles.size]]</div><div class=\"hstack\" style=\"gap: 16dp;\">";
    for (const char sz : {'S', 'M', 'L'}) body += ui::rml::chip(std::string(1, sz), {}, c_.settings.subtitleSize == sz, std::string("subsize:") + sz);
    body += "</div>";
  }
  (void)opener;
  c_.ui().sheet(subs ? "[[player.sheet.subtitles]]" : "[[player.sheet.audio]]", body);
}

void PlayerPage::openQuality(Rml::Element* opener) {
  (void)opener;
  std::string body = "<div class=\"list\">";
  for (const auto& r : c_.session->renditions)
    body += ui::rml::chip(esc(r.name) + (r.hdr.empty() ? "" : " · " + esc(r.hdr)), "quality", r.selected, "quality:" + esc(r.name));
  c_.ui().sheet("[[player.sheet.quality]]", body + "</div>");
}

void PlayerPage::toggleAction(const ItemAction& action, Rml::Element* e) {
  const std::string focusId = e != nullptr ? e->GetId() : std::string();
  c_.itemAction(action.id, [this, focusId] {
    sessionChanged();
    if (auto* f = c_.el(focusId)) c_.ui().focus().focus(f);
  });
}

bool PlayerPage::activate(const ui::Activation& a) {
  if (!c_.session) return false;
  if (sheet_.isOpen() && sheet_.activate(a)) return true;
  osd_.lastKeyMs = c_.nowMs;
  const auto& act = a.action;
  if (choices::activate(c_, act)) return true;
  if (act == "osd:saudio") {
    choices::openAudio(c_);
    return true;
  }
  if (act == "osd:view") {
    choices::openViews(c_);
    return true;
  }
  if (act.starts_with("track:")) {
    const auto id = act.substr(6);
    if (id == "off") {
      c_.command(player::CommandAction::SelectTrack, 0, {}, cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_SUBTITLE);
    } else {
      c_.command(player::CommandAction::SelectTrack, 0, id);
    }
    c_.ui().closeLevel();
    return true;
  }
  if (act.starts_with("subsize:")) {
    c_.settings.subtitleSize = act.back();
    if (auto* subs = c_.el("subs")) {
      for (const char* sz : {"size-s", "size-m", "size-l"}) subs->SetClass(sz, false);
      subs->SetClass(std::string("size-") + static_cast<char>(std::tolower(act.back())), true);
    }
    c_.ui().closeLevel();
    return true;
  }
  if (act.starts_with("quality:")) {
    // a quality is a choice like a voiceover: the server resolves it, the film carries on from here
    const auto name = act.substr(8);
    c_.ui().closeLevel();
    if (switched) switched(SwitchChoice{"quality", name, name, {}, false, true}, nlohmann::json{{"quality", name}}.dump());
    return true;
  }
  if (act == "osd:play") {
    const bool paused = c_.state.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED;
    c_.command(paused ? player::CommandAction::Resume : player::CommandAction::Pause);
    return true;
  }
  if (act == "osd:back10" || act == "osd:fwd10") {
    seek(act == "osd:fwd10" ? 1 : -1, false);
    return true;
  }
  if (act == "osd:next") {
    // the server's next for a session it owns, else the episode after this one
    if (c_.session->has("next") && c_.transport) {
      c_.transport("next", {}, nullptr);
    } else if (c_.session->upNext && playNext && !c_.session->follow) {
      playNext(*c_.session->upNext, true);
    }
    return true;
  }
  if (act == "osd:previous") {
    // to the start first, as every player does, the item before after that (the server's previous)
    if (c_.session->has("previous") && c_.transport && c_.position() < kPreviousRestartMs) {
      c_.transport("previous", {}, nullptr);
    } else {
      c_.command(player::CommandAction::Seek, 0);
    }
    return true;
  }
  if (act == "osd:scrub") return true;
  if (act == "osd:audio") {
    openTracks(cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_AUDIO, a.element);
    return true;
  }
  if (act == "osd:subs") {
    openTracks(cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_SUBTITLE, a.element);
    return true;
  }
  if (act == "osd:quality") {
    openQuality(a.element);
    return true;
  }
  if (act == "osd:voiceover" || act == "fail:switch" || act == "upnext:switch") {
    if (auto* f = c_.el("failure")) f->SetClass("hidden", true);
    if (auto* u = c_.el("upnext")) u->SetClass("hidden", true);
    hide();
    sheet_.open(std::make_unique<SwitcherContent>([this](const SwitchChoice& ch, const std::string& choices) {
      if (switched) switched(ch, choices);
    }), a.element != nullptr ? a.element : c_.el("o-vo"));
    return true;
  }
  if (act.starts_with("osd:panel:")) {
    hide();
    if (openPanel) openPanel(act.substr(10), a.element);
    return true;
  }
  if (act == "osd:queue") {
    hide();
    if (openQueue) openQueue(a.element);
    return true;
  }
  if (act == "osd:chapters") {
    hide();
    sheet_.open(std::make_unique<ChaptersContent>(), a.element);
    return true;
  }
  if (act.starts_with("osd:act:")) {
    const auto i = static_cast<std::size_t>(std::atoi(act.c_str() + 8));
    if (i < c_.session->itemActions.size()) toggleAction(c_.session->itemActions[i], a.element);
    return true;
  }
  if (act == "osd:skip") {
    if (skip_ != nullptr) c_.command(player::CommandAction::Seek, skip_->endMs);
    skip_ = nullptr;
    if (auto* s = c_.el("skip")) s->SetClass("hidden", true);
    if (auto* f = c_.ui().focus().current()) f->Blur();
    return true;
  }
  if (act == "upnext:watch") {
    if (c_.session->upNext && playNext) playNext(*c_.session->upNext, true);
    return true;
  }
  if (act == "upnext:cancel") {
    upNextCancelled_ = true;
    upNextAtMs_ = -1;
    if (auto* u = c_.el("upnext")) u->SetClass("hidden", true);
    if (auto* f = c_.ui().focus().current()) f->Blur();
    c_.ui().markDirty();
    return true;
  }
  if (act == "fail:retry") {
    // the failure stays, «Повторить» spinning, until the player plays again (playerChanged) or fails again
    if (auto* b = c_.el("f-retry")) b->SetClass("loading", true);
    if (auto* p = c_.player()) {
      auto r = c_.session->request;
      r.startMs = c_.position();
      p->play(r);
    }
    return true;
  }
  if (act == "fail:back") {
    c_.go(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
    return true;
  }
  return false;
}

void PlayerPage::startUpNext(std::int64_t nowMs) {
  auto* u = c_.el("upnext");
  if (u == nullptr || !c_.session || !c_.session->upNext) return;
  upNextAtMs_ = nowMs;
  const auto& n = *c_.session->upNext;
  setText(c_.el("un-title"), esc(n.title.empty() ? c_.session->title : n.title));
  if (auto* art = c_.el("un-art"); art != nullptr && !n.art.empty()) art->SetInnerRML(ui::rml::artImage(n.art, "uimg"));
  u->SetClass("hidden", false);
  if (auto* w = c_.el("un-watch")) {
    w->SetClass("hidden", upNextMissing_);
    if (!upNextMissing_) c_.ui().focus().focus(w);
  }
  if (auto* sw = c_.el("un-switch")) {
    sw->SetClass("hidden", !upNextMissing_);
    if (upNextMissing_) c_.ui().focus().focus(sw);
  }
  refreshUpNext();
  c_.ui().markDirty();
}

void PlayerPage::refreshUpNext() {
  if (upNextAtMs_ < 0 || !c_.session || !c_.session->upNext) return;
  const auto& n = *c_.session->upNext;
  const std::string vo = esc(c_.session->voiceover);
  if (upNextMissing_) {
    setText(c_.el("un-meta"), "[[player.up_next.missing|voiceover=" + vo + "|label=" + esc(n.episode) + "]]");
    setStyle(c_.el("un-line"), "transform", "scaleX(0)");
    return;
  }
  // a tenth of a second at a time: the line moves in steps the eye cannot tell, and a frame is the same each run
  const auto left = std::max<std::int64_t>(0, (kUpNextCountMs - (c_.nowMs - upNextAtMs_)) / 100 * 100);
  // «Серия 6 · AniLibria» and the countdown, which the shrinking line shows too
  std::string meta = "[[player.up_next.in|seconds=" + std::to_string((left + 999) / 1000) + "]]";
  if (!n.episode.empty()) {
    meta += " · [[episodes.episode|n=" + esc(n.episode) + "]]";
    if (!vo.empty()) meta += " · " + vo;
  }
  setText(c_.el("un-meta"), meta);
  setStyle(c_.el("un-line"), "transform", "scaleX(" + std::to_string(static_cast<double>(left) / kUpNextCountMs) + ")");
  if (left == 0 && playNext) {
    upNextAtMs_ = -1;
    playNext(n, true);
  }
}

void PlayerPage::refreshSkip() {
  auto* pill = c_.el("skip");
  if (pill == nullptr || !c_.session) return;
  const auto pos = c_.position();
  const Skip* now = skipAt(c_.session->skips, pos);
  // up next covers the ending: its card, not «Пропустить эндинг»
  if (now != nullptr && now->kind == SkipKind::Ending && upNextAtMs_ >= 0) now = nullptr;
  if (now != nullptr && c_.settings.autoSkip && (skippedAtMs_ < 0 || skippedEndMs_ != now->endMs)) {
    // auto-skip: a 3 s toast replaces the pill, OK goes back
    skippedAtMs_ = c_.nowMs;
    skippedFromMs_ = pos;
    skippedEndMs_ = now->endMs;
    c_.command(player::CommandAction::Seek, now->endMs);
    c_.toast(std::string("[[player.osd.skipped|kind=") + skipKey(now->kind) + "]]", static_cast<int>(kUndoSkipMs));
    now = nullptr;
  }
  if (now != skip_) {
    skip_ = now;
    pill->SetClass("hidden", now == nullptr);
    if (now == nullptr && slotFocused_) {
      // the segment ended under focus: back to the scrubber
      osd_.row = OsdState::Scrubber;
      place();
    }
    if (now != nullptr) {
      setText(c_.el("skip-text"), std::string("[[player.osd.skip|kind=") + skipKey(now->kind) + "]]");
      // focusable with the osd hidden: the first OK skips
      if (!osd_.shown) c_.ui().focus().focus(pill);
    } else if (c_.ui().focus().current() == pill) {
      pill->Blur();
    }
  }
  if (now != nullptr) {
    const double left = 1.0 - static_cast<double>(pos - now->startMs) / static_cast<double>(now->endMs - now->startMs);
    setStyle(c_.el("skip-left"), "transform", "scaleX(" + std::to_string(std::clamp(left, 0.0, 1.0)) + ")");
  }
}

void PlayerPage::refresh() {
  if (!c_.session) return;
  const auto pos = c_.position();
  const auto dur = c_.state.durationMs;
  const double played = dur > 0 ? static_cast<double>(pos) / static_cast<double>(dur) : 0;
  const double buffered = dur > 0 ? static_cast<double>(pos + c_.state.bufferedMs) / static_cast<double>(dur) : 0;
  for (const char* prefix : {"o-", "c-"}) {
    const std::string p = prefix;
    setStyle(c_.el(p + "played"), "width", pct(played));
    setStyle(c_.el(p + "buf"), "width", pct(buffered));
    setText(c_.el(p + "pos"), clockTime(pos));
    setText(c_.el(p + "dur"), c_.session->live ? "[[audio.live]]" : (dur > 0 ? clockTime(dur) : ""));
  }
  setStyle(c_.el("o-thumb"), "left", pct(played));
  const int ch = chapterAt(c_.session->chapters, pos);
  auto* chapter = c_.el("o-chapter");
  setText(chapter, ch >= 0 ? esc(c_.session->chapters[static_cast<std::size_t>(ch)].title) : "");
  setStyle(chapter, "left", pct(played));
  const auto now = c_.host->epochMs();
  setText(c_.el("o-now"), c_.clock.empty() ? clockOf(now) : c_.clock);
  if (dur > pos && !c_.session->live) {
    setText(c_.el("o-ends"), "[[player.osd.ends_at|time=" + clockOf(now + static_cast<std::int64_t>(dur - pos)) + "]]");
  }
  // play/pause shows what OK will do
  if (auto* play = c_.el("o-play")) {
    const bool paused = c_.state.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED;
    // held by the playback gate (rule 7): the button says why it waits, whatever its focus, instead of offering a
    // play that would be refused
    const bool gated = c_.state.gated;
    if (play->IsClassSet("gated") != gated) play->SetClass("gated", gated);
    setText(play->QuerySelector(".icon"), ui::rml::iconChar(gated ? "volume" : paused ? "play" : "pause"));
    setText(play->QuerySelector(".word"), gated ? "[[player.osd.gated]]" : paused ? "[[player.osd.play]]" : "[[player.osd.pause]]");
  }
  // the cache line under the scrubber and the speed under the spinner, while the player says what its cache reads
  const auto* pc = c_.player();
  const auto speed = pc != nullptr ? pc->cacheSpeed() : std::nullopt;
  const auto lang = c_.ui().translator().language();
  if (auto* line = c_.el("o-cache")) {
    const auto text = speed ? cacheLine(lang, c_.state.bufferedMs, speed) : std::string();
    if (line->IsClassSet("hidden") != text.empty()) line->SetClass("hidden", text.empty());
    setText(line, text);
  }
  // buffering: nothing under 1 s, then a spinner and the percentage; also a seek or a load while paused, and the wait
  // for the first frame (the player's waiting)
  const bool buffering = c_.state.state == cadence_v2_PlaybackState_PLAYBACK_STATE_BUFFERING || (pc != nullptr && pc->waiting());
  if (buffering && bufferingSinceMs_ < 0) bufferingSinceMs_ = c_.nowMs;
  if (!buffering) bufferingSinceMs_ = -1;
  auto* buf = c_.el("buffering");
  const bool showBuf = bufferingSinceMs_ >= 0 && c_.nowMs - bufferingSinceMs_ >= kBufferingAfterMs;
  if (buf != nullptr && buf->IsClassSet("hidden") == showBuf) buf->SetClass("hidden", !showBuf);
  if (showBuf) {
    const auto p = c_.player() != nullptr ? c_.player()->bufferingPercent() : std::nullopt;
    setText(c_.el("buf-text"), p ? "[[player.osd.buffering_percent|percent=" + std::to_string(*p) + "]]" : "[[player.osd.buffering]]");
    setText(c_.el("buf-speed"), speed ? speedText(lang, *speed) : std::string());
  }
  refreshSkip();
  refreshUpNext();
}

void PlayerPage::playerChanged() {
  if (!c_.session) return;
  // the session's last state: a failure shows its block; the end with nothing next goes home
  if (c_.state.ended && c_.state.sessionId == c_.session->sessionId) {
    if (!c_.state.errorCode.empty()) {
      if (auto* f = c_.el("failure")) {
        f->SetClass("hidden", false);
        // the player's own code with the session's source, as they are
        failure::Call why;
        why.method = "player";
        why.code = "ended";
        why.failure = failure::token(c_.state.errorCode);
        why.provider = failure::token(c_.session->providerId);
        why.source = failure::token(c_.session->providerSource);
        setText(c_.el("fail-source"), ui::rml::esc(failure::sourceLine(why)));
        const auto words = failure::wordsFor(why);
        setText(c_.el("fail-plain"), words.reason);
        setText(c_.el("fail-hint"), words.hint);
        setText(c_.el("fail-reason"), ui::rml::esc(failure::reasonLine(why)));
        setText(c_.el("fail-detail"), ui::rml::esc(failure::detailLine(why)));
        if (auto* retry = c_.el("f-retry")) retry->SetClass("loading", false);
        if (auto* retry = c_.el("f-retry")) c_.ui().focus().focus(retry);
        hide();
        if (auto* retry = c_.el("f-retry")) c_.ui().focus().focus(retry);
      }
    } else if (!c_.session->upNext) {
      c_.go(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
      return;
    }
  }
  // a retry that plays: the failure gives way
  if (!c_.state.ended && (c_.state.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING ||
                          c_.state.state == cadence_v2_PlaybackState_PLAYBACK_STATE_BUFFERING)) {
    if (auto* f = c_.el("failure"); f != nullptr && !f->IsClassSet("hidden")) {
      f->SetClass("hidden", true);
      if (auto* b = c_.el("f-retry")) b->SetClass("loading", false);
    }
  }
  // the codecs the badges name, once mpv knows them
  if (const auto codecs = codecLine(c_.state.tracks); codecs != shownCodecs_) {
    shownCodecs_ = codecs;
    if (auto* t = c_.el("o-badges")) t->SetInnerRML(badges());
  }
  // the tracks the chips list changed
  if (auto* b = c_.el("o-row-b")) {
    const auto before = focusedId();
    b->SetInnerRML(rowB());
    if (osd_.shown) {
      if (auto* f = c_.el(before)) c_.ui().focus().focus(f);
      else place();
    }
  }
  refresh();
}

void PlayerPage::subtitleChanged() {
  auto* subs = c_.el("subs");
  if (subs == nullptr || c_.subtitle == lastSubs_) return;
  lastSubs_ = c_.subtitle;
  // one ink box per line; third-party text, escaped
  std::string rml;
  std::size_t start = 0;
  while (start <= c_.subtitle.size() && !c_.subtitle.empty()) {
    const auto end = c_.subtitle.find('\n', start);
    const auto line = c_.subtitle.substr(start, end == std::string::npos ? std::string::npos : end - start);
    if (!line.empty()) rml += (rml.empty() ? "" : "<br/>") + std::string("<span>") + esc(line) + "</span>";
    if (end == std::string::npos) break;
    start = end + 1;
  }
  subs->SetInnerRML(rml);
  c_.ui().markDirty();
}

void PlayerPage::tick(std::int64_t nowMs) {
  if (!c_.session) return;
  sheet_.tick(nowMs);
  // auto-hide: 5 s after the last key while playing; never paused, with a sheet or level open, or while seeking
  const bool hold = !c_.playing() || sheet_.isOpen() || c_.ui().levelOpen() ||
                    (osd_.row == OsdState::Scrubber && osd_.seekRepeats > 0 && nowMs - osd_.lastKeyMs < 1000);
  if (osd_.shown && !hold && nowMs - osd_.lastKeyMs >= kAutoHideMs) hide();
  if (!osd_.shown && osd_.compactUntilMs > 0 && nowMs >= osd_.compactUntilMs) {
    osd_.compactUntilMs = 0;
    if (auto* compact = c_.el("compact")) compact->SetClass("gone", true);
    c_.ui().markDirty();
  }
  // up next: from the ending segment's start (server-marked), else the last 30 s. a follower counts none down: its
  // leader's own countdown moves the group, whose next item the follower then gets (multiroom P4)
  const auto dur = c_.state.durationMs;
  const auto pos = c_.position();
  if (upNextAtMs_ < 0 && !upNextCancelled_ && c_.session->upNext && !c_.session->follow && dur > 0 && !sheet_.isOpen()) {
    std::uint64_t from = dur > kUpNextLeadMs ? dur - kUpNextLeadMs : 0;
    for (const auto& s : c_.session->skips)
      if (s.kind == SkipKind::Ending) from = std::min(from, s.startMs);
    if (pos >= from && pos > 0) startUpNext(nowMs);
  }
  // the clock, the bar and the countdown move once a second
  if (nowMs / 250 != lastRefresh_) {
    lastRefresh_ = nowMs / 250;
    refresh();
  }
}

}  // namespace cadence::screen::routes
