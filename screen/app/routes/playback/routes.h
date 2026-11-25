#pragma once

#include "app/routes/ambient/ambient.h"
#include "app/routes/audio/audio.h"
#include "app/routes/core/router.h"
#include "app/routes/panels/side-sheet.h"
#include "app/routes/player/osd.h"
#include "app/routes/playback/context.h"

#include <nlohmann/json.hpp>

#include <functional>
#include <optional>
#include <memory>
#include <string>

namespace cadence::screen::routes {

class PageRoute;

// stream i2's playback (design §12-15, §19, plan items 9, 15, 16) on i1's router: the player's osd, audio mode and
// ambient are routes (PLAYER, AUDIO, AMBIENT) over one shared item, with panels, the queue and the voiceover
// switcher as their side sheets. a service beside the stack keeps what outlives a page: the item media.video/play
// started (from cadenced, or the ui's own play that resolving sent), idle's timer that opens ambient, whose first key
// only wakes, the remote's keys the osd reads before the focus manager, and the tap's feed for the visualizers
class PlaybackRoutes final : public Service, public app::VizFeed, private PlaybackHost {
 public:
  struct Options {
    // the full-screen visualizer: the host on its thread by default
    std::unique_ptr<VizView> viz;
    // ambient's images
    std::unique_ptr<ImageSource> images;
    // the device clock (unix ms); the system's by default
    std::function<std::int64_t()> epochMs;
  };
  explicit PlaybackRoutes(Options options);
  PlaybackRoutes();
  ~PlaybackRoutes() override;

  // PLAYER, AUDIO and AMBIENT in the router's registry, this service beside them (the router owns it)
  static PlaybackRoutes& install(Router& router, Options options = {});

  // Service
  void attach(Router& router, ui::Ui& ui, app::Outbox& outbox) override;
  void detach() override;
  void abandon() override {
    ui_ = nullptr;
    audio_.abandonGraphics();
  }
  bool key(const platform::KeyEvent& event) override;
  void message(const cadence_screen_v1_Envelope& m) override;
  bool route(const cadence_screen_v1_Route& r) override;
  void playerState(const player::PlayerState& state) override;
  void subtitle(const std::string& text) override;
  void update(std::int64_t nowMs) override;
  app::VizFeed* vizFeed() override { return this; }
  void setWake(std::function<void()> wake) override {
    audio_.setWake(wake);
    if (auto* v = audio_.viz()) v->setWake(std::move(wake));
  }
  // audio or video with no playback page on top (BACK keeps it behind home, whose chip or hero brings it back)
  [[nodiscard]] std::optional<BackgroundPlay> background() const override;
  bool foreground() override;

  // the tap's thread
  void pcm(std::span<const float> interleaved, std::uint32_t channels, std::uint32_t rate, std::int64_t monoNs) override;
  void bands(const std::array<std::uint8_t, 16>& bands) override;

  // a session as media.video/play starts one: the player (or audio mode for a track) over what shows, unless
  // resolving is on top, which hands itself over to the player once the play starts
  void start(Session session);
  void go(cadence_v2_ScreenRoute route);
  void openPanel(const std::string& panelId, Rml::Element* opener);
  void openQueue(Rml::Element* opener);
  // display.screen's config (the json cadenced validated)
  void configure(const std::string& json);
  // the gallery between its pages: no item, nothing pending, the settings' defaults
  void reset();

  [[nodiscard]] PlaybackContext& context() { return c_; }
  [[nodiscard]] PlayerPage& playerPage() { return player_; }
  [[nodiscard]] AudioPage& audioPage() { return audio_; }
  [[nodiscard]] AmbientPage& ambientPage() { return ambient_; }
  [[nodiscard]] SideSheet& sheet() { return sheet_; }
  // the playback page on top of the stack, if any
  [[nodiscard]] Page* current() const;
  // what shows: the top page's route, or its sheet's (queue, panel, picker)
  [[nodiscard]] cadence_v2_ScreenRoute shownRoute() const;
  [[nodiscard]] bool active() const { return current() != nullptr; }

 private:
  // the screen's own player's live reading into the bar's position (PlaybackContext::pos)
  void takePosition();
  friend class PageRoute;

  // PlaybackHost
  ui::Ui& ui() override { return *ui_; }
  bool send(const cadence_screen_v1_Envelope& envelope) override { return outbox_ != nullptr && outbox_->send(envelope); }
  app::PlayerControl* player() override { return router_ != nullptr ? router_->player() : nullptr; }
  void leave() override;
  [[nodiscard]] std::int64_t epochMs() const override { return epochMs_(); }

  void wake();
  void openPage(cadence_v2_ScreenRoute id);
  void loadDetail();
  void checkUpNext();
  // the server's fields of a session (media_session_state): merged into the playing one, or held for its play
  void serverFields(const cadence_v2_MediaSessionState& state);
  static void takeServerFields(Session& to, const Session& from);
  // media.control for the playing session; done (may be null) gets whether it was ok
  void transport(const std::string& action, const std::string& ref, std::function<void(bool)> done);
  // media.play after the playing session (replaces); done gets the answer
  void play(nlohmann::json args, std::function<void(const RpcAnswer&)> done);
  // an item the queue or up next names: the server's jump for its own, else media.play with reason
  void playItem(const QueueItem& item, bool sameChoices, const char* reason);
  // the switcher's choice (voiceover, source, quality) at the position kept
  // the session audiod renders (NowPlaying): a new one displaces what the screen played and opens audio mode; its
  // changes update the page; none ends it (back to what was under the page)
  void nowPlaying(const cadence_screen_v1_NowPlaying& now);
  // the server resolves an item it will play here (media.video/prepare): resolving shows it, then its failure
  void preparing(const cadence_screen_v1_MediaPreparing& p);
  void switchTo(const SwitchChoice& choice, const std::string& choicesJson);
  void tickClock();
  Page* pageOf(cadence_v2_ScreenRoute id);
  // «now playing» for a session that began, whoever started it: a corner notice a moment later, unless the full
  // player shows the same already (its osd up, or the audio page in front with no drawer over it)
  void announce();
  // a timer's callback runs only while this lives (the ui's timers outlive it)
  std::shared_ptr<char> alive_ = std::make_shared<char>();

  PlaybackContext c_;
  SideSheet sheet_;
  PlayerPage player_;
  AudioPage audio_;
  AmbientPage ambient_;
  Router* router_ = nullptr;
  ui::Ui* ui_ = nullptr;
  app::Outbox* outbox_ = nullptr;
  std::function<std::int64_t()> epochMs_;
  // the route that holds each page now (a replaced one's teardown must not leave its successor's)
  std::map<Page*, PageRoute*> owners_;
  IdleTimer idle_;
  // the key that woke ambient: its up is swallowed too
  cadence_screen_v1_Key wakeKey_ = cadence_screen_v1_Key_KEY_UNSPECIFIED;
  // catalog.options for the playing episode: what auto-continue and the switcher keep
  std::string options_;
  std::string voiceoverId_;
  // the group's session (NowPlaying of source GROUP) while this device plays a group's stream: a follower's play that
  // starts later sends its transport there (multiroom P4)
  std::string groupSession_;
  // the server's fields for a session whose play has not come yet
  std::optional<Session> serverState_;
  std::int64_t clockMinute_ = -1;
  bool clockStarted_ = false;
  bool inKey_ = false;
};

}  // namespace cadence::screen::routes
