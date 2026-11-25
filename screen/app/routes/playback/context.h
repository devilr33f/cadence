#pragma once

#include "app/routes/playback/position.h"
#include "app/routes/playback/rpc.h"
#include "app/routes/playback/session.h"
#include "platform/keys.h"
#include "player/player.h"
#include "app/ui-layer.h"
#include "render/render-backend.h"
#include "ui/core/ui.h"

#include <RmlUi/Core/Element.h>

#include <cstdint>
#include <functional>
#include <optional>
#include <string>

namespace cadence::screen::routes {

// what the playback pages reach (PlaybackRoutes over the router, or a gallery's fake)
class PlaybackHost {
 public:
  virtual ~PlaybackHost() = default;
  virtual ui::Ui& ui() = 0;
  // to cadenced: rpc requests; false while there is no link
  virtual bool send(const cadence_screen_v1_Envelope& envelope) = 0;
  // the local player; null before the app attached one
  virtual app::PlayerControl* player() = 0;
  // the playback pages step off the screen (back to what was under them)
  virtual void leave() = 0;
  // unix ms on the device clock
  [[nodiscard]] virtual std::int64_t epochMs() const = 0;
};

// the audio page's visualizer: none, the classic bars under the page, or one of the host's families (the drive
// scenes, the feedback scenes, milkdrop through projectM where the build has it)
enum class VizFamily { Off, Classic, Scenes, Drive, Milkdrop };
// the host's families draw full screen under the layer
inline bool hostFamily(VizFamily f) { return f == VizFamily::Scenes || f == VizFamily::Drive || f == VizFamily::Milkdrop; }

// display.screen's config as the playback routes read it (cadenced validated it against its schema)
struct PlaybackSettings {
  // ambient (plan item 15)
  std::string ambientSource = "aerial";
  int idleAfterS = 600;
  int sleepAfterS = 3600;
  // idle over a paused video; -1 is idleAfterS, 0 never
  int pausedAfterS = -1;
  bool showClock = true;
  bool showWeather = true;
  bool showCaptions = true;
  // the player
  bool autoSkip = false;
  // S, M, L
  char subtitleSize = 'M';
  VizFamily visualizer = VizFamily::Drive;
  // the drive family's pinned scene (cover, warp, tunnel) and shape (triangle, ...); auto follows the track
  std::string vizScene = "auto";
  std::string vizShape = "auto";
  // the visualizer's render size: low, medium, high, max, native (the output's own)
  std::string vizQuality = "medium";
  // how far ahead of the sound the visualizer's picture looks, ms (its render and the composite's frames)
  int vizOffsetMs = 60;
};

// what the playback routes share: the shell, the calls, the item and the player as last reported
struct PlaybackContext {
  PlaybackHost* host = nullptr;
  PlaybackRpc rpc{nullptr};
  std::optional<Session> session;
  player::PlayerState state;
  std::string subtitle;
  PlaybackSettings settings;
  std::int64_t nowMs = 0;
  // the clock and weather the overlays show, already formatted
  std::string clock = "21:40";
  std::string weather;
  // a page asks the routes to show another (set by PlaybackRoutes)
  std::function<void(cadence_v2_ScreenRoute)> go;
  // the screen context changed (debounced by the routes)
  std::function<void()> contextChanged;
  // the server's transport of the session (rpc media.control {sessionId, action, ref?}: next, previous, jump,
  // play_next, remove); done gets the answer (the new state comes with the next media_session_state)
  std::function<void(const std::string& action, const std::string& ref, std::function<void(bool ok)> done)> transport;

  [[nodiscard]] ui::Ui& ui() const { return host->ui(); }
  [[nodiscard]] app::PlayerControl* player() const { return host->player(); }
  // where the bar shows playback (PositionModel): the player's live reading (cadenced's echo for a session it
  // renders), run on while it plays, and a seek's target until a reading of that seek arrives. mutable: a seek is
  // held by command, which the pages reach through a const context too
  mutable PositionModel pos;
  // the position now
  [[nodiscard]] std::uint64_t position() const;
  // a seek by deltaMs from the position the bar shows (the held target while a seek is under way, so rapid presses
  // add up into one target); "" when sent
  std::string seekBy(std::int64_t deltaMs) const;
  [[nodiscard]] bool playing() const { return state.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING; }
  // an element of the current page by id
  [[nodiscard]] Rml::Element* el(const std::string& id) const;
  // the player's command, or cadenced's for a session audiod renders (Session::device); "" when sent
  std::string command(player::CommandAction action, std::uint64_t positionMs = 0, const std::string& track = {},
                      cadence_v2_MediaTrackKind kind = cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_UNSPECIFIED) const;
  // a transport action on the device's session (NowPlayingCommand to cadenced); "" when sent
  std::string deviceCommand(cadence_screen_v1_MediaAction action, std::uint64_t positionMs = 0) const;
  void toast(const std::string& rml, int ms = 3000) const;
  // an item action (like, save, ...) flipped at once, like and dislike as a pair: media.item_action for a session
  // the server sends the fields of (its next media_session_state says the toggled state), else catalog.act; a
  // refusal puts the actions back. changed runs after each change of the session's actions
  void itemAction(const std::string& actionId, const std::function<void()>& changed);
};

// one of the routes' pages: the player's osd, audio mode, ambient. the router loads its body; it keeps it current by
// touching elements (never a reload while it shows)
class Page {
 public:
  explicit Page(PlaybackContext& c) : c_(c) {}
  virtual ~Page() = default;
  // opened: its state from the start (before the first body)
  virtual void enter() = 0;
  virtual std::string body() = 0;
  [[nodiscard]] virtual std::string bodyClasses() const { return "page over-video"; }
  // after each load of its body; restoring: back from a route above it
  virtual void loaded(bool restoring) { (void)restoring; }
  virtual void leave() {}
  virtual bool key(const platform::KeyEvent& event) = 0;
  virtual bool activate(const ui::Activation& a) {
    (void)a;
    return false;
  }
  virtual void tick(std::int64_t nowMs) { (void)nowMs; }
  virtual void playerChanged() {}
  virtual void subtitleChanged() {}
  virtual void sessionChanged() {}
  virtual void renderUnder(render::RenderBackend& backend, render::Rect screen) {
    (void)backend;
    (void)screen;
  }
  [[nodiscard]] virtual bool dirty() const { return false; }
  [[nodiscard]] virtual cadence_v2_ScreenRoute route() const = 0;

 protected:
  PlaybackContext& c_;
};

// small rml helpers
void setText(Rml::Element* e, const std::string& rml);
void setStyle(Rml::Element* e, const char* property, const std::string& value);
std::string dp(double v);
std::string pct(double fraction);
bool isPress(const platform::KeyEvent& e);
bool isDownOrRepeat(const platform::KeyEvent& e);

}  // namespace cadence::screen::routes
