#include "app/routes/playback/routes.h"
#include "app/routes/playback/server-choices.h"
#include "app/routes/resolving/resolving.h"

#include "app/convert.h"
#include "app/routes/panels/panel.h"
#include "app/routes/player/switcher.h"
#include "app/routes/queue/queue.h"
#include "i18n/locale.h"
#include "ipc/log.h"
#include "ui/core/rml.h"

#include <nlohmann/json.hpp>

#include <cctype>
#include <chrono>
#include <cstring>
#include <ctime>

namespace cadence::screen::routes {

// one playback page as a route of the stack: its body, its activations, its part of the screen context
class PageRoute final : public Route {
 public:
  PageRoute(Services& s, PlaybackRoutes& p, Page& page) : Route(s), p_(p), page_(page) {
    // the page's clock from the ui's, before the service's next frame
    p_.c_.nowMs = s.ui.nowMs();
    p_.owners_[&page_] = this;
    page_.enter();
  }
  ~PageRoute() override {
    // a route replaced by another of the same page leaves nothing behind it
    if (p_.owners_[&page_] == this) {
      p_.owners_.erase(&page_);
      // a router going down with its ui (detach) leaves nothing to touch
      if (p_.ui_ != nullptr) page_.leave();
    }
  }
  [[nodiscard]] cadence_v2_ScreenRoute id() const override { return page_.route(); }
  std::string body() override { return page_.body(); }
  [[nodiscard]] std::string bodyClasses() const override { return page_.bodyClasses(); }
  void loaded(Rml::ElementDocument& doc, bool restoring) override {
    (void)doc;
    page_.loaded(restoring);
  }
  bool activate(const ui::Activation& a) override { return page_.activate(a); }
  void context(ScreenContextData& c) const override {
    // refs, the position and the tracks; never a title
    c.route = p_.shownRoute();
    const auto& pc = p_.c_;
    if (!pc.session || &page_ == &p_.ambient_) return;
    c.pageRef = pc.session->itemRef;
    c.episode = pc.session->episode;
    c.voiceover = p_.voiceoverId_;
    c.hasPlayer = true;
    c.playerItemRef = pc.session->itemRef;
    c.positionMs = pc.position();
    for (const auto& t : pc.state.tracks) c.tracks.push_back({t.id, t.kind, t.language, t.label, t.selected, t.external});
  }
  [[nodiscard]] Page& page() const { return page_; }

 private:
  PlaybackRoutes& p_;
  Page& page_;
};

namespace {

// «now playing» shows this long after a session began, once the page it opened is in front
constexpr int kAnnounceAfterMs = 700;

std::int64_t systemEpochMs() {
  return std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::system_clock::now().time_since_epoch()).count();
}

}  // namespace

PlaybackRoutes::PlaybackRoutes() : PlaybackRoutes(Options{}) {}

PlaybackRoutes::PlaybackRoutes(Options o)
    : sheet_(c_),
      player_(c_, sheet_),
      audio_(c_, sheet_, o.viz ? std::move(o.viz) : makeHostVizView()),
      ambient_(c_, o.images ? std::move(o.images) : makeNoImages()),
      epochMs_(o.epochMs ? std::move(o.epochMs) : std::function<std::int64_t()>(systemEpochMs)) {
  c_.host = this;
  c_.go = [this](cadence_v2_ScreenRoute r) { go(r); };
  c_.contextChanged = [this] {
    if (router_ != nullptr) router_->contextChanged();
  };
  c_.transport = [this](const std::string& action, const std::string& ref, std::function<void(bool)> done) {
    transport(action, ref, std::move(done));
  };
  player_.playNext = [this](const QueueItem& item, bool same) { playItem(item, same, "continue"); };
  player_.switched = [this](const SwitchChoice& ch, const std::string& choices) { switchTo(ch, choices); };
  player_.openPanel = [this](const std::string& id, Rml::Element* opener) { openPanel(id, opener); };
  player_.openQueue = [this](Rml::Element* opener) { openQueue(opener); };
  audio_.openPanel = player_.openPanel;
  audio_.openQueue = player_.openQueue;
  sheet_.onClosed = [this] {
    if (current() == &player_) player_.show(OsdState::Details, -1, 3000);
    c_.contextChanged();
  };
}

PlaybackRoutes::~PlaybackRoutes() = default;

PlaybackRoutes& PlaybackRoutes::install(Router& router, Options options) {
  auto service = std::make_unique<PlaybackRoutes>(std::move(options));
  auto* p = service.get();
  for (const auto id : {cadence_v2_ScreenRoute_SCREEN_ROUTE_PLAYER, cadence_v2_ScreenRoute_SCREEN_ROUTE_AUDIO,
                        cadence_v2_ScreenRoute_SCREEN_ROUTE_AMBIENT}) {
    router.registerRoute(id, [p, id](Services& s, const RouteArgs&) -> std::unique_ptr<Route> {
      // the player and audio mode show an item: none, nothing to open (resolving steps back instead)
      if (id != cadence_v2_ScreenRoute_SCREEN_ROUTE_AMBIENT && !p->c_.session) return nullptr;
      // resolving hands a track over as the player: it shows on the audio page
      auto* page = p->pageOf(id);
      if (id == cadence_v2_ScreenRoute_SCREEN_ROUTE_PLAYER && p->c_.session->mode == Mode::Audio) page = &p->audio_;
      return std::make_unique<PageRoute>(s, *p, *page);
    });
  }
  router.addService(std::move(service));
  return *p;
}

void registerPlaybackRoutes(Router& router) { PlaybackRoutes::install(router); }

Page* PlaybackRoutes::pageOf(cadence_v2_ScreenRoute id) {
  switch (id) {
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_AUDIO:
      return &audio_;
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_AMBIENT:
      return &ambient_;
    default:
      return &player_;
  }
}

Page* PlaybackRoutes::current() const {
  auto* r = router_ != nullptr ? dynamic_cast<PageRoute*>(router_->top()) : nullptr;
  return r != nullptr ? &r->page() : nullptr;
}

cadence_v2_ScreenRoute PlaybackRoutes::shownRoute() const {
  auto* p = current();
  if (p == nullptr) return cadence_v2_ScreenRoute_SCREEN_ROUTE_UNSPECIFIED;
  if (sheet_.isOpen() && sheet_.content() != nullptr) return sheet_.content()->route();
  return p->route();
}

void PlaybackRoutes::attach(Router& router, ui::Ui& ui, app::Outbox& outbox) {
  router_ = &router;
  ui_ = &ui;
  outbox_ = &outbox;
  c_.nowMs = ui.nowMs();
  c_.rpc = PlaybackRpc([this](const cadence_screen_v1_Envelope& e) { return send(e); });
  idle_.configure(c_.settings.ambientSource == "off" ? 0 : c_.settings.idleAfterS, c_.settings.sleepAfterS,
                  c_.settings.pausedAfterS);
  clockStarted_ = false;
  // the visualizer's texture and ken burns, under the ui's layer
  ui.setUnderlay([this](render::RenderBackend& b, render::Rect r) {
    if (auto* p = current()) p->renderUnder(b, r);
  });
}

void PlaybackRoutes::detach() {
  if (ui_ != nullptr) ui_->setUnderlay(nullptr);
  // gl objects go with the context's owner when the ui is gone
  audio_.releaseGraphics();
  ambient_.releaseGraphics();
  c_.rpc.cancelAll();
  router_ = nullptr;
  ui_ = nullptr;
  outbox_ = nullptr;
}

void PlaybackRoutes::reset() {
  sheet_.close();
  c_.session.reset();
  c_.state = {};
  c_.subtitle.clear();
  c_.rpc.cancelAll();
  options_.clear();
  voiceoverId_.clear();
  serverState_.reset();
  wakeKey_ = cadence_screen_v1_Key_KEY_UNSPECIFIED;
  c_.settings = PlaybackSettings{};
  idle_.configure(c_.settings.idleAfterS, c_.settings.sleepAfterS);
  idle_.touch(c_.nowMs);
}

void PlaybackRoutes::leave() {
  // off the screen: every playback route on top goes (ambient over the player, the player itself)
  if (router_ == nullptr) return;
  while (current() != nullptr && router_->pop()) {
  }
}

void PlaybackRoutes::openPage(cadence_v2_ScreenRoute id) {
  if (router_ == nullptr) return;
  // a playback page replaces another (audio after a film), anything else stays under it
  if (current() != nullptr && current() != &ambient_) {
    router_->replace(id);
  } else {
    router_->open(id);
  }
}

void PlaybackRoutes::go(cadence_v2_ScreenRoute r) {
  switch (r) {
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_PLAYER:
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_AUDIO:
      if (c_.session) openPage(r);
      return;
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_QUEUE:
      if (c_.session) {
        if (current() != &player_ && current() != &audio_) openPage(c_.session->mode == Mode::Audio ? cadence_v2_ScreenRoute_SCREEN_ROUTE_AUDIO
                                                                                                   : cadence_v2_ScreenRoute_SCREEN_ROUTE_PLAYER);
        openQueue(nullptr);
      }
      return;
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_AMBIENT:
      if (current() != &ambient_ && router_ != nullptr) router_->open(r);
      return;
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME:
      // home itself, whatever led to the player (title, picker): a film going on plays behind it
      if (router_ != nullptr && router_->popTo(r)) return;
      leave();
      return;
    default:
      leave();
      return;
  }
}

std::optional<BackgroundPlay> PlaybackRoutes::background() const {
  // audio and video stay available after BACK: home offers their page back and the media keys reach their transport
  if (!c_.session || current() != nullptr) return std::nullopt;
  const auto st = c_.state.state;
  // a session whose player went idle, ended or failed plays nothing behind the page
  if (st == cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE || st == cadence_v2_PlaybackState_PLAYBACK_STATE_UNSPECIFIED ||
      c_.state.ended || !c_.state.errorCode.empty())
    return std::nullopt;
  const auto& s = *c_.session;
  const auto& name = !s.series.empty() ? s.series : !s.title.empty() ? s.title : s.providerName;
  return BackgroundPlay{s.sessionId, s.itemRef, ui::rml::esc(name), ui::rml::esc(s.artist), s.art,
                        s.mode == Mode::Audio, st == cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED};
}

bool PlaybackRoutes::foreground() {
  if (!background()) return false;
  idle_.touch(c_.nowMs);
  openPage(cadence_v2_ScreenRoute_SCREEN_ROUTE_PLAYER);
  // a track or a cast comes back as audio mode
  return current() == &player_ || current() == &audio_;
}

void PlaybackRoutes::wake() {
  // idle's first key only wakes: back to what it covered, with its focus as it was
  idle_.touch(c_.nowMs);
  if (current() == &ambient_ && router_ != nullptr) router_->pop();
}

void PlaybackRoutes::start(Session s) {
  const bool sameItem = c_.session && c_.session->sessionId == s.sessionId && c_.session->itemRef == s.itemRef;
  // the server's fields of this session, whether they came before its play or with the one it replaces
  if (serverState_ && serverState_->sessionId == s.sessionId) {
    takeServerFields(s, *serverState_);
  } else if (c_.session && c_.session->sessionId == s.sessionId && c_.session->serverFields) {
    takeServerFields(s, *c_.session);
  }
  c_.session = std::move(s);
  c_.subtitle.clear();
  // a new item's bar starts from its own readings; the same item played again keeps a seek it holds
  if (!sameItem) c_.pos.reset();
  takePosition();
  // a server item's audio view: the cover alone, or with the visualizer (the owner's own when one is on)
  if (c_.session->view == View::Audio) c_.settings.visualizer = VizFamily::Off;
  if (c_.session->view == View::Visualizer && c_.settings.visualizer == VizFamily::Off) c_.settings.visualizer = VizFamily::Drive;
  if (!sameItem) {
    options_.clear();
    voiceoverId_.clear();
  }
  idle_.touch(c_.nowMs);
  if (current() == &ambient_) wake();
  // resolving hands itself over to the player once the play starts (i1's resolving route)
  const bool resolving = router_ != nullptr && router_->top() != nullptr &&
                         router_->top()->id() == cadence_v2_ScreenRoute_SCREEN_ROUTE_RESOLVING;
  if (!resolving) openPage(c_.session->mode == Mode::Audio ? cadence_v2_ScreenRoute_SCREEN_ROUTE_AUDIO : cadence_v2_ScreenRoute_SCREEN_ROUTE_PLAYER);
  loadDetail();
  c_.contextChanged();
  if (!sameItem) announce();
}

void PlaybackRoutes::announce() {
  if (ui_ == nullptr || !c_.session) return;
  const std::string id = c_.session->sessionId + "\x1f" + c_.session->title + "\x1f" + c_.session->episode;
  // a moment later, once the page it opened is in front
  ui_->after(kAnnounceAfterMs, [this, id, alive = std::weak_ptr<char>(alive_)] {
    if (alive.expired() || ui_ == nullptr || !c_.session) return;
    const auto& s = *c_.session;
    if (s.sessionId + "\x1f" + s.title + "\x1f" + s.episode != id) return;
    const bool drawer = router_ != nullptr && router_->status().drawerOpen;
    if (!drawer && current() == &player_ && player_.osdShown()) return;
    if (!drawer && current() == &audio_ && audio_.state() != AudioPage::State::Ambient) return;
    ui::NowPlayingSpec n;
    n.art = s.art;
    const std::string show = !s.series.empty() ? s.series : s.title;
    n.title = ui::rml::esc(show.empty() ? s.providerName : show);
    std::string line;
    if (!s.episode.empty()) {
      line = s.season > 0 ? "[[episodes.season_episode|season=" + std::to_string(s.season) + "|n=" + ui::rml::esc(s.episode) + "]]"
                          : "[[episodes.episode|n=" + ui::rml::esc(s.episode) + "]]";
      const std::string name = !s.episodeName.empty() ? s.episodeName : (s.series.empty() || s.title == s.series ? "" : s.title);
      if (!name.empty() && name != s.episode) line += " \xc2\xb7 " + ui::rml::esc(name);
    } else if (!s.artist.empty()) {
      line = ui::rml::esc(s.album.empty() ? s.artist : s.artist + " \xc2\xb7 " + s.album);
    } else if (!s.channelName.empty()) {
      line = ui::rml::esc(s.channelName);
    }
    n.line = line;
    n.source = ui::rml::esc(s.providerSource.empty() ? s.providerName : s.providerName + " \xc2\xb7 " + s.providerSource);
    ui_->nowPlaying(n);
  });
}

bool PlaybackRoutes::key(const platform::KeyEvent& e) {
  idle_.touch(c_.nowMs);
  // a page passes keys on through Ui::key, which asks this filter again: the second time it is the ui's
  if (inKey_) return false;
  // the drawer (i3's layer) or a focused toast reads the remote while it is open
  if (router_ != nullptr && (router_->status().drawerOpen || ui_->toastFocused())) return false;
  auto* page = current();
  if (page == &ambient_) {
    // the first key only wakes; its up (or repeats) never reach the page behind
    if (e.action == cadence_screen_v1_KeyAction_KEY_ACTION_DOWN || e.action == cadence_screen_v1_KeyAction_KEY_ACTION_PRESS) {
      wakeKey_ = e.key;
      wake();
    }
    return true;
  }
  if (wakeKey_ != cadence_screen_v1_Key_KEY_UNSPECIFIED && e.key == wakeKey_) {
    if (e.action == cadence_screen_v1_KeyAction_KEY_ACTION_UP || e.action == cadence_screen_v1_KeyAction_KEY_ACTION_PRESS ||
        e.action == cadence_screen_v1_KeyAction_KEY_ACTION_LONG)
      wakeKey_ = cadence_screen_v1_Key_KEY_UNSPECIFIED;
    return true;
  }
  if (page == nullptr) {
    // background audio and video use the same transport as their page; the key never changes the route
    if (e.key != cadence_screen_v1_Key_KEY_PLAY_PAUSE && e.key != cadence_screen_v1_Key_KEY_PLAY &&
        e.key != cadence_screen_v1_Key_KEY_PAUSE)
      return false;
    if (!background()) return false;
    if (e.action == cadence_screen_v1_KeyAction_KEY_ACTION_DOWN || e.action == cadence_screen_v1_KeyAction_KEY_ACTION_PRESS) {
      const bool paused = c_.state.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED;
      c_.command(e.key == cadence_screen_v1_Key_KEY_PAUSE ||
                         (e.key == cadence_screen_v1_Key_KEY_PLAY_PAUSE && !paused)
                     ? player::CommandAction::Pause
                     : player::CommandAction::Resume);
    }
    return true;
  }
  inKey_ = true;
  const bool used = page->key(e);
  inKey_ = false;
  c_.contextChanged();
  return used;
}

bool PlaybackRoutes::route(const cadence_screen_v1_Route& r) {
  idle_.touch(c_.nowMs);
  if (r.back && current() == &ambient_) {
    wake();
    return true;
  }
  if (r.route == cadence_v2_ScreenRoute_SCREEN_ROUTE_QUEUE && !r.back) {
    if (!c_.session) return false;
    go(r.route);
    return true;
  }
  return false;
}

void PlaybackRoutes::message(const cadence_screen_v1_Envelope& m) {
  switch (m.which_body) {
    case cadence_screen_v1_Envelope_rpc_response_tag:
      if (!c_.rpc.take(m)) sheet_.message(m);
      return;
    case cadence_screen_v1_Envelope_media_play_tag: {
      const auto& p = m.body.media_play;
      if (p.purpose != cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT) return;
      auto s = sessionFrom(p, app::playRequestOf(p));
      if (s.follow) s.groupSession = groupSession_;
      start(std::move(s));
      return;
    }
    case cadence_screen_v1_Envelope_capability_config_tag: {
      const auto& cc = m.body.capability_config;
      if (std::strcmp(cc.capability, "display.screen") == 0 && cc.json != nullptr) configure(cc.json);
      return;
    }
    case cadence_screen_v1_Envelope_media_session_state_tag:
      serverFields(m.body.media_session_state);
      return;
    case cadence_screen_v1_Envelope_media_preparing_tag:
      preparing(m.body.media_preparing);
      return;
    case cadence_screen_v1_Envelope_now_playing_tag:
      nowPlaying(m.body.now_playing);
      return;
    // idle yields at once to a turn, a toast, a cast or a pin
    case cadence_screen_v1_Envelope_voice_state_tag:
    case cadence_screen_v1_Envelope_toast_tag:
    case cadence_screen_v1_Envelope_assistant_turn_tag:
    case cadence_screen_v1_Envelope_mirror_start_tag:
    case cadence_screen_v1_Envelope_airplay_pin_tag:
      idle_.touch(c_.nowMs);
      if (current() == &ambient_) wake();
      return;
    case cadence_screen_v1_Envelope_alarms_summary_tag:
      // and to a ring (its overlay holds the remote): a summary of alarms alone leaves idle be
      if (m.body.alarms_summary.ringing_count == 0) return;
      idle_.touch(c_.nowMs);
      if (current() == &ambient_) wake();
      return;
    default:
      return;
  }
}

void PlaybackRoutes::playerState(const player::PlayerState& s) {
  const bool ambientClip = !ambient_.clipSession().empty() && s.sessionId == ambient_.clipSession();
  // a session audiod renders is the page's: the screen's own player (displaced, paused) says nothing of it
  if (!ambientClip && c_.session && c_.session->device) return;
  c_.state = s;
  auto* page = current();
  if (ambientClip) {
    if (page == &ambient_) ambient_.playerChanged();
    return;
  }
  if (!c_.session || (s.sessionId != c_.session->sessionId && !s.sessionId.empty())) return;
  takePosition();
  if (c_.playing()) idle_.touch(c_.nowMs);
  if (page != nullptr && page != &ambient_) page->playerChanged();
  if (s.ended && s.errorCode.empty() && !c_.session->upNext && page == nullptr) c_.session.reset();
}

void PlaybackRoutes::takePosition() {
  // the screen's own player: its live reading (mpv's time-pos as it came), which the held seek filters
  if (!c_.session || c_.session->device || c_.host == nullptr) return;
  auto* p = player();
  if (p == nullptr || p->state().sessionId != c_.session->sessionId) return;
  c_.pos.reading(p->positionSample(), c_.host->epochMs());
}

void PlaybackRoutes::subtitle(const std::string& text) {
  c_.subtitle = text;
  if (current() == &player_) player_.subtitleChanged();
}

void PlaybackRoutes::update(std::int64_t nowMs) {
  c_.nowMs = nowMs;
  if (router_ == nullptr) return;
  if (!clockStarted_) {
    // idle counts from the first frame, not from the clock's zero
    clockStarted_ = true;
    idle_.touch(nowMs);
  }
  tickClock();
  c_.rpc.tick(nowMs);
  // the bar from the player's live reading each turn, not the debounced state alone
  takePosition();
  auto* page = current();
  // the drawer over the film: the osd steps aside (design §16, one translucent layer at a time)
  const bool drawer = router_->status().drawerOpen;
  if (drawer && page == &player_ && player_.osd().shown) player_.hide();
  if (drawer) idle_.touch(nowMs);
  if (page != nullptr) {
    page->tick(nowMs);
    if (page->dirty()) ui_->markDirty();
  }
  // idle: nothing playing, no key, nothing open over the page
  const bool paused = c_.session.has_value() && c_.state.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PAUSED;
  if (page != &ambient_ && idle_.idle(nowMs, c_.playing(), paused) && !ui_->levelOpen() && !sheet_.isOpen()) {
    CADENCE_LOG(Info, "ambient_idle");
    go(cadence_v2_ScreenRoute_SCREEN_ROUTE_AMBIENT);
  }
  if (current() == &ambient_ && idle_.sleep(nowMs)) ambient_.setAsleep(true);
}

void PlaybackRoutes::configure(const std::string& raw) {
  const auto j = nlohmann::json::parse(raw, nullptr, false);
  if (!j.is_object()) return;
  auto& s = c_.settings;
  if (const auto a = j.find("ambient"); a != j.end() && a->is_object()) {
    s.ambientSource = a->value("source", s.ambientSource);
    s.idleAfterS = a->value("idleAfterS", s.idleAfterS);
    s.sleepAfterS = a->value("sleepAfterS", s.sleepAfterS);
    s.pausedAfterS = a->value("pausedAfterS", s.pausedAfterS);
    s.showClock = a->value("showClock", s.showClock);
    s.showWeather = a->value("showWeather", s.showWeather);
    s.showCaptions = a->value("showCaptions", s.showCaptions);
  }
  if (const auto v = j.find("visualizer"); v != j.end() && v->is_string()) {
    const auto f = v->get<std::string>();
    s.visualizer = f == "off"        ? VizFamily::Off
                   : f == "classic"  ? VizFamily::Classic
                   : f == "scenes"   ? VizFamily::Scenes
                   : f == "milkdrop" ? VizFamily::Milkdrop
                                     : VizFamily::Drive;
  }
  if (const auto v = j.find("vizScene"); v != j.end() && v->is_string()) s.vizScene = v->get<std::string>();
  if (const auto v = j.find("vizShape"); v != j.end() && v->is_string()) s.vizShape = v->get<std::string>();
  if (const auto v = j.find("vizQuality"); v != j.end() && v->is_string()) s.vizQuality = v->get<std::string>();
  if (const auto v = j.find("vizOffsetMs"); v != j.end() && v->is_number_integer()) s.vizOffsetMs = v->get<int>();
  // display.screen's own keys (what settings.set writes), then the older player block
  if (const auto a = j.find("autoSkipOpenings"); a != j.end() && a->is_boolean()) s.autoSkip = a->get<bool>();
  if (const auto sub = j.find("subtitles"); sub != j.end() && sub->is_object()) {
    const auto size = sub->value("size", std::string());
    if (size == "s" || size == "m" || size == "l") s.subtitleSize = static_cast<char>(std::toupper(size[0]));
  }
  if (const auto p = j.find("player"); p != j.end() && p->is_object()) {
    s.autoSkip = p->value("autoSkip", s.autoSkip);
    const auto size = p->value("subtitleSize", std::string(1, s.subtitleSize));
    if (!size.empty() && (size[0] == 'S' || size[0] == 'M' || size[0] == 'L')) s.subtitleSize = size[0];
  }
  // ambient off: idle never starts
  idle_.configure(s.ambientSource == "off" ? 0 : s.idleAfterS, s.sleepAfterS, s.pausedAfterS);
  audio_.settingsChanged();
}

void PlaybackRoutes::loadDetail() {
  if (!c_.session || c_.session->itemRef.empty() || c_.host == nullptr) return;
  const std::string sid = c_.session->sessionId;
  const std::string ref = c_.session->itemRef;
  nlohmann::json args = {{"ref", ref}};
  c_.rpc.call("catalog.item", args.dump(), [this, sid](const RpcAnswer& a) {
    if (!c_.session || c_.session->sessionId != sid || !a.ok()) return;
    applyDetail(*c_.session, a.json);
    if (auto* p = current()) p->sessionChanged();
    checkUpNext();
  }, c_.nowMs);
  if (!c_.session->torrplayer()) return;
  // the voiceover playing now: the chip's name, and what auto-continue keeps
  nlohmann::json opt = {{"ref", ref}};
  if (!c_.session->episode.empty()) opt["episode"] = c_.session->episode;
  c_.rpc.call("catalog.options", opt.dump(), [this, sid](const RpcAnswer& a) {
    if (!c_.session || c_.session->sessionId != sid || !a.ok()) return;
    options_ = a.json;
    for (const auto& ch : switchChoices(a.json)) {
      if (ch.group == "voiceover" && ch.current) {
        c_.session->voiceover = ch.label;
        voiceoverId_ = ch.id;
      }
    }
    if (auto* p = current()) p->sessionChanged();
    checkUpNext();
  }, c_.nowMs);
}

void PlaybackRoutes::checkUpNext() {
  // does this voiceover have the next episode? the up-next card says so before its countdown (design §12)
  if (!c_.session || !c_.session->upNext || options_.empty() || voiceoverId_.empty() || c_.session->upNext->episode.empty()) return;
  const std::string sid = c_.session->sessionId;
  nlohmann::json args = {{"ref", c_.session->itemRef}, {"episode", c_.session->upNext->episode},
                         {"choices", nlohmann::json::parse(selectionJson(options_, "voiceover", voiceoverId_))}};
  c_.rpc.call("catalog.options", args.dump(), [this, sid](const RpcAnswer& a) {
    if (!c_.session || c_.session->sessionId != sid || !a.ok()) return;
    bool has = false;
    for (const auto& ch : switchChoices(a.json))
      if (ch.group == "voiceover" && ch.id == voiceoverId_) has = ch.hasEpisode;
    player_.setUpNextMissing(!has);
  }, c_.nowMs);
}

void PlaybackRoutes::takeServerFields(Session& to, const Session& from) {
  to.serverFields = from.serverFields;
  to.actions = from.actions;
  to.itemActions = from.itemActions;
  to.serverUpNext = from.serverUpNext;
  to.upNext = from.upNext;
  to.queuePosition = from.queuePosition;
  to.panels = from.panels;
}

void PlaybackRoutes::serverFields(const cadence_v2_MediaSessionState& st) {
  // the server's fields of a session it owns: merged into the playing one in place, or kept for its play to come
  if (!c_.session || c_.session->sessionId != st.session_id) {
    Session held;
    held.sessionId = st.session_id;
    applyServerState(held, st);
    serverState_ = std::move(held);
    return;
  }
  serverState_.reset();
  auto& s = *c_.session;
  const auto before = s.upNext ? s.upNext->ref : std::string();
  applyServerState(s, st);
  // the page keeps its focus (sessionChanged rebuilds rows in place); a new up next starts its countdown afresh
  if (auto* p = current()) {
    p->sessionChanged();
    if (p == &audio_) audio_.queueChanged();
  }
  if ((s.upNext ? s.upNext->ref : std::string()) != before) player_.setUpNextMissing(false);
  c_.contextChanged();
}

void PlaybackRoutes::transport(const std::string& action, const std::string& ref, std::function<void(bool)> done) {
  if (!c_.session) {
    if (done) done(false);
    return;
  }
  nlohmann::json args = {{"sessionId", c_.session->sessionId}, {"action", action}};
  if (!ref.empty()) args["ref"] = ref;
  const bool sent = c_.rpc.call("media.control", args.dump(), [this, done](const RpcAnswer& a) {
    if (!a.ok()) c_.toast("[[common.unavailable_now]]");
    if (done) done(a.ok());
  }, c_.nowMs);
  if (!sent && done) done(false);
}

void PlaybackRoutes::play(nlohmann::json args, std::function<void(const RpcAnswer&)> done) {
  // the server resolves it and sends media.video/play, so the session is cadenced's (media.play, pkg/rpc); the play
  // itself comes back as a MediaPlay, which start() takes
  if (!c_.session) return;
  args["replaces"] = c_.session->sessionId;
  const bool sent = c_.rpc.call("media.play", args.dump(), [this, done](const RpcAnswer& a) {
    if (!a.ok()) c_.toast("[[resolving.play_failed]]");
    if (done) done(a);
  }, c_.nowMs, 45000);
  if (!sent) {
    c_.toast("[[common.unavailable_now]]");
    if (done) done(RpcAnswer{"unavailable", "{}", nullptr});
  }
}

void PlaybackRoutes::playItem(const QueueItem& item, bool sameChoices, const char* reason) {
  if (!c_.session) return;
  // one of the server's own items: its jump plays it
  if (item.server) {
    transport("jump", item.ref, [this](bool ok) {
      if (ok) sheet_.close();
    });
    return;
  }
  // the next episode with the same voiceover and source (auto-continue): the server plays it after this session
  nlohmann::json args = {{"ref", c_.session->itemRef}, {"from", "start"}, {"reason", reason}};
  if (!item.episode.empty()) args["episode"] = item.episode;
  if (sameChoices && !options_.empty()) {
    const auto o = nlohmann::json::parse(options_, nullptr, false);
    if (o.is_object() && o.contains("selected") && o["selected"].is_object() && !o["selected"].empty()) args["choices"] = o["selected"];
  }
  if (item.episode.empty() && !item.ref.empty()) args["ref"] = item.ref;
  play(std::move(args), [this](const RpcAnswer& a) {
    if (a.ok()) sheet_.close();
  });
}

void PlaybackRoutes::switchTo(const SwitchChoice& choice, const std::string& choicesJson) {
  if (!c_.session) return;
  // a video the server plays from a link has no catalog item: the server switches its quality itself
  if (choice.group == "quality" && c_.session->serverVideo()) {
    choices::switchQuality(c_, choice.id);
    return;
  }
  // the switch keeps the position when the episode is there, else it starts over
  const std::uint64_t at = choice.hasEpisode ? c_.position() : 0;
  nlohmann::json args = {{"ref", c_.session->itemRef}, {"reason", "switch"}};
  if (const auto ch = nlohmann::json::parse(choicesJson, nullptr, false); ch.is_object() && !ch.empty()) args["choices"] = ch;
  if (!c_.session->episode.empty()) args["episode"] = c_.session->episode;
  if (at > 0) {
    args["startMs"] = at;
  } else {
    args["from"] = "start";
  }
  play(std::move(args), [this, choice](const RpcAnswer& a) {
    if (!a.ok()) {
      if (auto* sw = dynamic_cast<SwitcherContent*>(sheet_.content()); sw != nullptr && sheet_.isOpen()) sw->failed();
      return;
    }
    sheet_.close();
    // the new session's voiceover is the one chosen (catalog.options of the new play says it again)
    if (choice.group == "voiceover") {
      voiceoverId_ = choice.id;
      if (c_.session) c_.session->voiceover = choice.label;
      if (auto* p = current()) p->sessionChanged();
    }
    if (choice.group == "voiceover" || choice.group == "source")
      c_.toast("[[player.switcher.done|name=" + ui::rml::esc(choice.label) + "]]");
  });
}

void PlaybackRoutes::nowPlaying(const cadence_screen_v1_NowPlaying& n) {
  const bool none = n.session_id[0] == '\0' || n.state == cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE ||
                    n.state == cadence_v2_PlaybackState_PLAYBACK_STATE_UNSPECIFIED;
  // a follower's sound is the group's session: its NowPlaying names where the follower's transport goes, and never
  // displaces the picture (multiroom P4)
  const bool group = !none && n.source == cadence_v2_MediaSource_MEDIA_SOURCE_GROUP;
  groupSession_ = group ? std::string(n.session_id) : std::string();
  if (c_.session && c_.session->follow && (none || group)) {
    c_.session->groupSession = groupSession_;
    return;
  }
  if (none) {
    // the cast ended (or another plane holds the speaker): its page goes, nothing resumes (audiod's arbitration)
    if (c_.session && c_.session->device) {
      const bool shown = current() == &audio_;
      sheet_.close();
      c_.session.reset();
      c_.state = {};
      if (shown) leave();
      c_.contextChanged();
    }
    return;
  }
  auto s = sessionFrom(n);
  if (!s.artPath.empty() && ui_ != nullptr && ui_->images() != nullptr) ui_->images()->addLocal(s.art.substr(4), s.artPath);
  player::PlayerState st;
  st.sessionId = s.sessionId;
  st.purpose = cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT;
  st.state = n.state;
  st.positionMs = n.position_ms;
  st.positionAtMs = n.position_at_ms;
  st.durationMs = n.duration_ms;
  st.rate = n.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING ? 1.0f : 0.0f;
  st.audioRoute = cadence_v2_AudioRoute_AUDIO_ROUTE_MIXER;
  const bool same = c_.session && c_.session->device && c_.session->sessionId == s.sessionId;
  // cadenced's echo of where the cast is: a seek the screen asked for holds the bar until an echo of it comes
  const player::PositionSample echo{n.position_ms, n.position_at_ms, st.rate, 0, false};
  if (!same) {
    // a new cast displaced what the screen played: audiod's arbitration paused the screen's player already; it is
    // paused here too, so nothing plays on under the cast whatever became of that pause on its way
    if (auto* p = player(); p != nullptr) {
      const auto& ps = p->state();
      if (!ps.sessionId.empty() && ps.sessionId != ambient_.clipSession() &&
          (ps.state == cadence_v2_PlaybackState_PLAYBACK_STATE_PLAYING ||
           ps.state == cadence_v2_PlaybackState_PLAYBACK_STATE_BUFFERING)) {
        player::Command pause;
        pause.action = player::CommandAction::Pause;
        p->command(pause);
        CADENCE_LOG(Info, "player_displaced_by_cast");
      }
    }
    sheet_.close();
    c_.state = st;
    start(std::move(s));
    if (c_.host != nullptr) c_.pos.reading(echo, c_.host->epochMs());
    return;
  }
  auto& cur = *c_.session;
  // what the page draws once (the art, the type, the chips): a new track builds it again
  const bool look = cur.title != s.title || cur.artist != s.artist || cur.album != s.album || cur.art != s.art ||
                    cur.sender != s.sender || cur.colorA != s.colorA || cur.colorB != s.colorB;
  const bool controls = cur.actions != s.actions;
  cur = std::move(s);
  c_.state = st;
  if (c_.host != nullptr) c_.pos.reading(echo, c_.host->epochMs());
  auto* page = current();
  if (look) announce();
  if (page == &audio_) {
    if (controls) audio_.sessionChanged();
    if (look) audio_.trackChanged();
    // state is independent of the look: a pause that arrives with new metadata still settles the visualizer
    audio_.playerChanged();
    if (look && router_ != nullptr) router_->refresh();
  }
  if (c_.playing()) idle_.touch(c_.nowMs);
  c_.contextChanged();
}

void PlaybackRoutes::preparing(const cadence_screen_v1_MediaPreparing& p) {
  if (router_ == nullptr) return;
  auto* r = dynamic_cast<ResolvingRoute*>(router_->top());
  const bool ours = r != nullptr && r->external();
  if (p.state == cadence_screen_v1_PrepareState_PREPARE_STATE_FAILED) {
    if (ours) r->prepareFailed(p.code);
    return;
  }
  if (p.state != cadence_screen_v1_PrepareState_PREPARE_STATE_RESOLVING) return;
  // the image proxy's id in the art's url (<origin>/v2/images/<id>)
  std::string art;
  const std::string url = p.artwork_url != nullptr ? p.artwork_url : "";
  if (const auto at = url.rfind("/v2/images/"); at != std::string::npos && url.starts_with("http")) art = url.substr(at + 11);
  const std::string title = p.title != nullptr ? p.title : "";
  const std::string sub = p.subtitle != nullptr ? p.subtitle : "";
  if (ours) {
    r->prepared(title, sub, art);
  } else {
    idle_.touch(c_.nowMs);
    if (current() == &ambient_) wake();
    RouteArgs args;
    args.data = {{"external", true}, {"title", title}, {"label", sub}, {"art", art}};
    router_->open(cadence_v2_ScreenRoute_SCREEN_ROUTE_RESOLVING, args);
    r = dynamic_cast<ResolvingRoute*>(router_->top());
  }
  if (r != nullptr && p.stage != nullptr && *p.stage != '\0') {
    Json ev{{"stage", p.stage}, {"elapsedMs", p.elapsed_ms}, {"remainingMs", p.remaining_ms},
            {"height", p.height}, {"vcodec", p.vcodec != nullptr ? p.vcodec : ""},
            {"acodec", p.acodec != nullptr ? p.acodec : ""}, {"audioTracks", p.audio_tracks},
            {"done", p.done}, {"failed", p.failed}};
    r->event(ev);
  }
}

void PlaybackRoutes::openPanel(const std::string& panelId, Rml::Element* opener) {
  if (!c_.session) return;
  for (const auto& p : c_.session->panels) {
    if (p.id == panelId) {
      sheet_.open(std::make_unique<PanelContent>(p), opener);
      c_.contextChanged();
      return;
    }
  }
}

void PlaybackRoutes::openQueue(Rml::Element* opener) {
  sheet_.open(std::make_unique<QueueContent>([this](const QueueItem& item) {
    playItem(item, true, "user");
  }), opener);
  c_.contextChanged();
}

void PlaybackRoutes::tickClock() {
  if (c_.host == nullptr) return;
  // the device clock as the host keeps it (a gallery's is fixed)
  const auto now = static_cast<std::time_t>(c_.host->epochMs() / 1000);
  const auto minute = static_cast<std::int64_t>(now / 60);
  if (minute == clockMinute_) return;
  clockMinute_ = minute;
  std::tm local{};
  localtime_r(&now, &local);
  c_.clock = i18n::clock(c_.ui().translator().language(), local.tm_hour, local.tm_min);
}

void PlaybackRoutes::pcm(std::span<const float> s, std::uint32_t ch, std::uint32_t rate, std::int64_t monoNs) {
  audio_.pcm(s, ch, rate, monoNs);
}

void PlaybackRoutes::bands(const std::array<std::uint8_t, 16>& b) { audio_.bands(b); }

}  // namespace cadence::screen::routes
