#include "app/app.h"

#include "app/config-json.h"
#include "ipc/log.h"
#include "platform/lifecycle.h"
#include "platform/overscan.h"
#include "player/video-fit.h"
#include "player/video-plane.h"
#include "render/gl.h"
#include "render/perf.h"

#include <algorithm>
#include <chrono>
#include <cstring>
#include <utility>

#ifndef CADENCE_SCREEN_VERSION
#define CADENCE_SCREEN_VERSION "dev"
#endif

namespace cadence::screen::app {

namespace {

std::int64_t nowNs() {
  return std::chrono::duration_cast<std::chrono::nanoseconds>(std::chrono::steady_clock::now().time_since_epoch())
      .count();
}

std::int64_t unixMs() {
  return std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::system_clock::now().time_since_epoch())
      .count();
}

}  // namespace

App::App(AppOptions options, std::unique_ptr<UiLayer> ui, PlayerFactory playerFactory)
    : options_(std::move(options)), playerFactory_(std::move(playerFactory)),
      ui_(ui ? std::move(ui) : std::make_unique<NullUi>()) {}

App::~App() { shutdown(); }

std::int64_t App::monotonicMs() const { return nowNs() / 1000000; }

bool App::start(std::string* error) {
  startedMs_ = monotonicMs();
  statsLoggedMs_ = startedMs_;
  platform_ = platform::Platform::create(options_.platform, error);
  if (!platform_) return false;
  if (!startGl(error)) return false;

  platform::Platform* p = platform_.get();
  ipc::Log::get().setSink([this](const ipc::LogRecord& r) {
    if (!client_) return;
    auto line = logLineEnvelope(r);
    client_->send(line.get());
  });
  ipc::ClientConfig link;
  link.path = options_.socket;
  mirror_ = player::MirrorPresenter::create();
  refreshHello();
  link.hello = [this] {
    std::lock_guard lock(helloMu_);
    return helloEnvelope(hello_);
  };
  client_ = std::make_unique<ipc::ScreenClient>(std::move(link), [p] { p->wake(); });
  client_->start();
  // audiod's tap: the visualizers' pcm and bands straight to the ui's feed (the tap's thread), ring offers to mpv
  player::TapSinks sinks;
  sinks.pcm = [this](std::span<const float> pcm, std::uint32_t channels, std::uint32_t rate, std::int64_t monoNs) {
    if (auto* f = vizFeed_.load()) f->pcm(pcm, channels, rate, monoNs);
  };
  sinks.bands = [this](const std::array<std::uint8_t, 16>& bands) {
    if (auto* f = vizFeed_.load()) f->bands(bands);
  };
  sinks.ringReady = [p] { p->wake(); };
  // the group's media clock, for a follower's picture: kept for the main thread's next turn
  sinks.groupClock = [this](const player::FollowClock& clock) {
    std::lock_guard lock(groupClockMu_);
    groupClock_ = clock;
  };
  if (mirror_) {
    sinks.mirror = [this](const player::TapSinks::MirrorFrame& f) {
      mirror_->frame(f.sessionId, f.ptsNs, f.config, f.keyframe, f.data);
    };
    sinks.clock = [this](std::int64_t mediaNs, std::int64_t monoNs, std::int64_t latencyNs) {
      mirror_->clock(mediaNs, monoNs, latencyNs);
    };
  }
  tap_ = std::make_unique<player::TapClient>(std::move(sinks));
  return true;
}

player::Result App::play(const player::PlayRequest& request) {
  if (!player_) return "unsupported";
  if (mirror_ && mirror_->active()) {
    // the video surface is the mirror's: idle's clips wait for it to end, a play the user asked for ends it (as
    // cadenced's MediaPlay does)
    if (request.purpose == cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_AMBIENT) return "busy";
    CADENCE_LOG(Info, "mirror_displaced_by_play");
    mirror_->stop("");
  }
  return player_->play(request);
}

player::Result App::command(const player::Command& command) {
  return player_ ? player_->command(command) : "unsupported";
}

const player::PlayerState& App::state() const {
  static const player::PlayerState none;
  return player_ ? player_->state() : none;
}

std::optional<int> App::bufferingPercent() const { return player_ ? player_->bufferingPercent() : std::nullopt; }
bool App::waiting() const { return player_ && player_->waiting(); }
std::optional<std::uint64_t> App::cacheSpeed() const { return player_ ? player_->cacheSpeed() : std::nullopt; }
player::PositionSample App::positionSample() const {
  return player_ ? player_->positionSample() : player::PositionSample{};
}
bool App::pictureReady() const {
  if (!player_ || player_->state().sessionId.empty()) return false;
  return player_->videoInGl() ? player_->firstFrameShown() : !player_->waiting();
}

std::optional<player::FollowClock> App::takeGroupClock() {
  std::lock_guard lock(groupClockMu_);
  return std::exchange(groupClock_, std::nullopt);
}

void App::adoptRings() {
  if (!tap_) return;
  for (auto& offer : tap_->takeRings()) {
    // a ring for a session this player is not playing still serves the next one audiod pairs with it. the ring is
    // audiod's own (a sealed memfd and an eventfd): only an mpv player takes one, and only where audiod runs
#if defined(CADENCE_SCREEN_HAS_MPV)
    if (!mpv() || !mpv()->useRing(std::move(offer.ring))) CADENCE_LOG(Info, "tap_ring_unused");
#else
    (void)offer;
#endif
  }
}

bool App::startGl(std::string* error) {
  backend_ = std::make_unique<render::GlesBackend>();
  if (!backend_->ok()) {
    if (error != nullptr) *error = "backend";
    CADENCE_LOG(Error, "render_backend_failed");
    return false;
  }
  CADENCE_LOG(Info, "gl_ready", {{"es3", platform_->glVersion().rfind("OpenGL ES 3", 0) == 0}});

  // whoever runs the app may bring its own player (the sim's placeholder, which plays its made-up urls even where
  // mpv is installed for make sim)
  if (playerFactory_) player_ = playerFactory_(*backend_);
#if defined(CADENCE_SCREEN_HAS_MPV)
  if (!player_) {
    player::MpvPlayerConfig config;
    config.mpv.ao = player::buildAoMode();
    // --ao cadence names the compile-time cadence path itself, never an override: an override makes useRing
    // refuse the offered ring, so the first one never reaches the ao and split audio stays silent
    config.mpv.aoOverride = options_.ao == "cadence" ? std::string{} : options_.ao;
    config.mpv.hwdec = options_.hwdec;
    config.mpv.profile = options_.mpvProfile;
    config.mpv.displayFpsCap = options_.maxDisplayFps;
    platform::Platform* p = platform_.get();
#if defined(__ANDROID__)
    config.mpv.videoPath = player::VideoPath::SurfaceEmbed;
    config.output = std::make_unique<player::SurfaceEmbedOutput>();
#else
    config.mpv.videoPath = player::VideoPath::RenderApi;
    config.output = std::make_unique<player::RenderApiOutput>(*backend_, platform_->drawableSize(),
                                                              &platform::Platform::getProcAddress, [p] { p->wake(); });
#endif
    config.wake = [p] { p->wake(); };
    config.unixNowMs = unixMs;
    auto made = player::MpvPlayer::create(std::move(config));
    mpv_ = made.get();
    player_ = std::move(made);
  }
  if (!player_) {
    if (error != nullptr) *error = "player";
    return false;
  }
  // media.mpv: a player this fresh applies the config in force again (a context loss rebuilds it)
  if (auto* m = mpv()) {
    m->onMpvState([this](const player::MpvKeyState& st) { sendMpvState(st); });
    if (!mpvConfigJson_.empty()) m->applyMpvConfig(mpvConfigJson_);
  }
#else
  // a build without mpv: whoever runs the app brought its own player
  if (!player_) {
    if (error != nullptr) *error = "player";
    return false;
  }
#endif
  player_->setGate(gateOpen_);
  player_->onStateChanged([this](const player::PlayerState& s) {
    ui_->playerState(s);
    sendMediaState();
  });
  player_->onSubtitle([this](const std::string& text) { ui_->subtitle(text); });
  player_->onStats([this](const player::PlayerStats& st) {
    auto out = playerStatsEnvelope(st);
    send(out.get());
  });

  if (options_.hud && !hud_) hud_ = std::make_unique<render::perf::Hud>();
  if (const auto hz = platform_->display().refreshMhz; hz > 0) {
    perf_.setRefreshHz(hz / 1000.0);
    perfLog_.setRefreshHz(hz / 1000.0);
  }
  geometry_ = UiGeometry{};
  applyGeometry();
  ui_->attach(*backend_, geometry_, *this);
  ui_->attachPlayer(this);
  vizFeed_ = ui_->vizFeed();
  if (auto* f = vizFeed_.load()) {
    platform::Platform* p = platform_.get();
    f->setWake([p] { p->wake(); });
  }
  exposed_ = true;
  return true;
}

void App::stopGl() {
  if (ui_) ui_->attachPlayer(nullptr);
  if (ui_ && backend_) ui_->detach();
  if (hud_ && backend_) hud_->release(*backend_);
  // the render context first (gl current), then mpv, then our gl objects
  mpv_ = nullptr;
  player_.reset();
  backend_.reset();
}

void App::refreshHello() {
  HelloInfo info;
  info.version = CADENCE_SCREEN_VERSION;
#if defined(__ANDROID__)
  info.platform = cadence_screen_v1_Platform_PLATFORM_ANDROID;
  info.hwdec = {"mediacodec"};
#else
  info.platform = cadence_screen_v1_Platform_PLATFORM_LINUX;
  info.hwdec = {options_.hwdec.empty() ? "auto-safe" : options_.hwdec};
#endif
  info.display = platform_->display();
  info.maxVideoHeight = static_cast<std::uint32_t>(info.display.pixels.height > 0 ? info.display.pixels.height : 0);
  info.cardVersions = ui_->cardVersions();
  info.cardFeatures = ui_->cardFeatures();
  info.mirroring = mirror_ != nullptr;
  std::lock_guard lock(helloMu_);
  hello_ = std::move(info);
}

void App::applyGeometry() {
  UiGeometry g;
  g.screen = platform_->drawableSize();
  g.safeArea = platform::safeArea(g.screen, overscanPercent_);
  g.dpRatio = platform::dpRatio(g.screen);
  if (g == geometry_) return;
  geometry_ = g;
  // android's video plane fits the safe area the ui lays out in (linux's mpv letterboxes inside its full-screen quad)
  player::videoLayout().setArea(g.screen, g.safeArea);
  ui_->resize(geometry_);
  exposed_ = true;
}

void App::shutdown() {
  // the tap's thread feeds the ui: it stops before anything it reaches
  if (tap_) tap_->stop();
  tap_.reset();
  mirror_.reset();
  if (client_) client_->stop();
  ipc::Log::get().setSink(nullptr);
  client_.reset();
  stopGl();
  platform_.reset();
}

bool App::send(const cadence_screen_v1_Envelope& envelope) {
  // the ui's plays go through the server (rpc media.play): every MediaPlay the player takes is cadenced's
  if (envelope.which_body == cadence_screen_v1_Envelope_media_play_tag) return false;
  return client_ && client_->send(envelope);
}

void App::reply(const char* requestId, const std::string& code) {
  if (requestId == nullptr || requestId[0] == '\0') return;
  auto out = commandResultEnvelope(requestId, code);
  send(out.get());
}

void App::setGate(bool open) {
  gateOpen_ = open;
  if (player_) player_->setGate(open);
}

void App::sendMediaState() {
  if (!player_) return;
  auto out = mediaStateEnvelope(player_->state());
  sentPictureReady_ = pictureReady();
  out->body.media_state.picture_ready = sentPictureReady_;
  send(out.get());
}

// media.mpv's config, as the server set it and cadenced checked it: the player checks it again, applies each key
// live where mpv takes a runtime set and with the next file otherwise, and reports where each key landed. without an
// engine to tune (the sim's placeholder player) every key is refused, so the owner sees it
void App::applyMpvConfigJson(const char* json) {
  mpvConfigJson_ = json != nullptr ? json : "";
#if defined(CADENCE_SCREEN_HAS_MPV)
  if (auto* p = mpv()) {
    p->applyMpvConfig(mpvConfigJson_);
    return;
  }
#endif
  const auto settings = player::parseMpvSettings(mpvConfigJson_, player::VideoPath::RenderApi);
  player::MpvKeyState st;
  for (const auto& e : settings.entries) st.refused.push_back(e.key);
  for (const auto& k : settings.refused) st.refused.push_back(k);
  sendMpvState(st);
}

// the state's json list: the config's keys, identifiers our parser bounded (never escaped text)
std::string keyList(const std::vector<std::string>& keys) {
  std::string out;
  for (const auto& k : keys) {
    if (!out.empty()) out += ',';
    out += '"' + k + '"';
  }
  return out;
}

void App::sendMpvState(const player::MpvKeyState& state) {
  const std::string json = "{\"screen\":true,\"applied\":[" + keyList(state.applied) + "],\"pending\":[" +
                           keyList(state.pending) + "],\"refused\":[" + keyList(state.refused) + "]}";
  auto out = capabilityStateEnvelope("media.mpv", json);
  send(out.get());
}

void App::sendScreenStatus(bool force) {
  const UiStatus ui = ui_->status();
  cadence_screen_v1_ScreenPower power = cadence_screen_v1_ScreenPower_SCREEN_POWER_ON;
  if (!displayOn_) power = cadence_screen_v1_ScreenPower_SCREEN_POWER_SLEEP;
  if (!force && sentUi_ && *sentUi_ == ui && sentPower_ == power) return;
  auto out = screenStatusEnvelope(power, ui, platform_->display());
  if (send(out.get())) {
    sentUi_ = ui;
    sentPower_ = power;
  }
}

void App::handleKey(const platform::KeyEvent& event, std::int64_t nowMs, bool injected) {
  (void)nowMs;
  const bool used = displayOn_ && ui_->key(event);
  if (injected || used) return;
  // what the ui leaves (volume, the assistant, power) goes to cadenced
  auto out = inputEventEnvelope(event, unixMs());
  send(out.get());
}

void App::handleSdl(const SDL_Event& e, std::int64_t nowMs) {
  if (e.type == platform_->wakeEventType()) {
    platform_->woken();
    return;
  }
  if (e.type == SDL_EVENT_KEY_DOWN || e.type == SDL_EVENT_KEY_UP) {
    const auto key = platform::keyOf(e.key.key);
    if (!key) return;
    const auto event = e.type == SDL_EVENT_KEY_DOWN ? keys_.down(*key, e.key.repeat, nowMs) : keys_.up(*key, nowMs);
    if (event) handleKey(*event, nowMs, false);
    return;
  }
  switch (platform::lifecycleOf(e)) {
    case platform::Lifecycle::Quit:
      quit_ = true;
      break;
    case platform::Lifecycle::Pause:
      backgrounded_ = true;
      CADENCE_LOG(Info, "app_background");
      break;
    case platform::Lifecycle::Resume:
      backgrounded_ = false;
      exposed_ = true;
      if (!platform_->makeCurrent()) {
        // the context went with the background: everything holding gl starts again on a new one
        CADENCE_LOG(Warn, "gl_context_lost");
        stopGl();
        std::string error;
        quit_ = !platform_->recreateContext(&error) || !startGl(&error);
        if (quit_) break;
        sendMediaState();
      }
      CADENCE_LOG(Info, "app_foreground");
      break;
    case platform::Lifecycle::Resize:
      applyGeometry();
      refreshHello();
      sendScreenStatus(true);
      break;
    case platform::Lifecycle::Expose:
      exposed_ = true;
      break;
    case platform::Lifecycle::LowMemory:
      CADENCE_LOG(Warn, "app_low_memory");
      break;
    case platform::Lifecycle::None:
      break;
  }
}

void App::handleLink(ipc::ScreenClient::Event& event) {
  switch (event.kind) {
    case ipc::ScreenClient::Event::Kind::Connected:
      linkUp_ = true;
      sendMediaState();
      sendScreenStatus(true);
      return;
    case ipc::ScreenClient::Event::Kind::Disconnected:
      linkUp_ = false;
      // nobody says the level holds any more (rule 7): shut until a new Ready or PlaybackGate opens it
      setGate(false);
      return;
    case ipc::ScreenClient::Event::Kind::Message:
      if (event.envelope) handleMessage(*event.envelope);
      return;
  }
}

void App::handleMessage(const cadence_screen_v1_Envelope& m) {
  switch (m.which_body) {
    case cadence_screen_v1_Envelope_ready_tag:
      setGate(m.body.ready.gate_open);
      if (tap_) tap_->connect(m.body.ready.tap_path);
      ui_->message(m);
      break;
    case cadence_screen_v1_Envelope_playback_gate_tag:
      setGate(m.body.playback_gate.open);
      break;
    case cadence_screen_v1_Envelope_media_play_tag:
      if (mirror_ && mirror_->active()) {
        CADENCE_LOG(Info, "mirror_displaced_by_play");
        mirror_->stop("");
      }
      reply(m.body.media_play.request_id, player_->play(playRequestOf(m.body.media_play)));
      ui_->message(m);
      break;
    case cadence_screen_v1_Envelope_media_command_tag: {
      const auto c = commandOf(m.body.media_command);
      reply(m.body.media_command.request_id, c ? player_->command(*c) : "unsupported");
      break;
    }
    case cadence_screen_v1_Envelope_set_level_tag:
      reply(m.body.set_level.request_id, player_->setLevel(m.body.set_level.level, m.body.set_level.ramp_ms));
      break;
    case cadence_screen_v1_Envelope_set_eq_tag:
      reply(m.body.set_eq.request_id, player_->setEq(eqOf(m.body.set_eq)));
      break;
    case cadence_screen_v1_Envelope_mute_tag:
      reply(m.body.mute.request_id, player_->setMute(m.body.mute.muted));
      break;
    case cadence_screen_v1_Envelope_duck_tag: {
      const auto& d = m.body.duck;
      const auto& session = player_->state().sessionId;
      if (d.session_id[0] == '\0' || session == d.session_id) player_->setDuck(d.active, d.level, d.ramp_ms);
      ui_->message(m);
      break;
    }
    case cadence_screen_v1_Envelope_display_power_tag:
      displayOn_ = m.body.display_power.on;
      exposed_ = true;
      CADENCE_LOG(Info, "display_power", {{"on", displayOn_}});
      reply(m.body.display_power.request_id, "");
      sendScreenStatus(false);
      ui_->message(m);
      break;
    case cadence_screen_v1_Envelope_alarms_summary_tag: {
      // this screen's own ring wakes the display as display_power on would (owner, 2026-11-16); another device's never
      // does: the ui shows that one only while the display is already on
      const auto& a = m.body.alarms_summary;
      bool own = false;
      for (pb_size_t i = 0; i < a.ringing_count; ++i) own = own || a.ringing[i].own;
      if (own && !displayOn_) {
        displayOn_ = true;
        exposed_ = true;
        CADENCE_LOG(Info, "display_woken", {{"reason", "alarm_ring"}});
        sendScreenStatus(false);
      }
      ui_->message(m);
      break;
    }
    case cadence_screen_v1_Envelope_capability_config_tag: {
      const auto& c = m.body.capability_config;
      if (std::strcmp(c.capability, "media.mpv") == 0) {
        applyMpvConfigJson(c.json);
      } else if (std::strcmp(c.capability, "display.screen") == 0 && c.json != nullptr) {
        overscanPercent_ = topLevelNumber(c.json, "overscan").value_or(0);
        applyGeometry();
      }
      ui_->message(m);
      break;
    }
    case cadence_screen_v1_Envelope_input_event_tag: {
      const auto& i = m.body.input_event;
      platform::KeyEvent e{i.key, i.action, i.source};
      handleKey(e, monotonicMs(), true);
      break;
    }
    case cadence_screen_v1_Envelope_mirror_start_tag:
      startMirror(m.body.mirror_start);
      ui_->message(m);
      break;
    case cadence_screen_v1_Envelope_mirror_stop_tag:
      if (mirror_) mirror_->stop(m.body.mirror_stop.session_id);
      ui_->message(m);
      break;
    default:
      ui_->message(m);
      break;
  }
}

void App::startMirror(const cadence_screen_v1_MirrorStart& start) {
  if (!mirror_) return;
  if (player_ && player_->state().state != cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE) {
    // airplay displaced the session already (audiod's arbitration); a paused player still holds the surface
    player::Command stop;
    stop.action = player::CommandAction::Stop;
    player_->command(stop);
  }
  mirror_->start(start.session_id, static_cast<int>(start.codec), static_cast<int>(start.width),
                 static_cast<int>(start.height));
}

void App::draw() {
  const auto size = platform_->drawableSize();
  backend_->beginFrame(size);
  // a mirror, and android's mpv, decode onto the video SurfaceView under this surface: the frame stays clear for it
  const bool playing = player_ && player_->state().state != cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE;
  if (player::videoPlaneShows(mirror_ && mirror_->active(), playing, player_ && player_->videoInGl()))
    backend_->clear(0, 0, 0, 0);
  if (displayOn_) {
    const auto video = player_->videoTexture();
    if (video) {
      render::QuadParams q;
      q.dst = render::Rect{0, 0, size.width, size.height};
      q.blend = render::Blend::None;
      backend_->drawTexturedQuad(*video, q);
    }
    ui_->setVideoUnder(video.has_value());
    ui_->render(*backend_);
    if (hud_) hud_->draw(*backend_, size, perf_, perf_.summary(nowNs()));
  }
}

void App::present(render::perf::Turn& turn) {
  auto t = render::perf::nowNs();
  draw();
  auto next = render::perf::nowNs();
  turn.ns[static_cast<std::size_t>(render::perf::Phase::Draw)] += next - t;
  t = next;
  if (options_.perfGpu) {
    // the gpu's own time: the swap then waits for vsync alone
    glFinish();
    next = render::perf::nowNs();
    turn.ns[static_cast<std::size_t>(render::perf::Phase::Gpu)] = next - t;
    t = next;
  }
  platform_->swap();
  player_->reportSwap();
  next = render::perf::nowNs();
  turn.ns[static_cast<std::size_t>(render::perf::Phase::Swap)] = next - t;
  scheduler_.presented();
  presented_++;
  turn.presented = true;
}

std::optional<render::Image> App::capture() {
  if (!platform_ || !backend_ || !player_) return std::nullopt;
  draw();
  return render::readPixels(platform_->drawableSize());
}

render::perf::Summary App::perfSummary() const { return perf_.summary(nowNs()); }

void App::resetPerf() {
  perf_.reset(nowNs());
  render::perf::take();
  render::perf::takeCounters();
}

void App::logPerf(std::int64_t now) {
  if (now - statsLoggedMs_ < options_.perfLogMs) return;
  statsLoggedMs_ = now;
  const auto s = perfLog_.summary(nowNs());
  perfLog_.reset(nowNs());
  // an idle window says only that it was idle: its turns a second (the loop's wakeups) are the idle cost
  const auto phase = [&](render::perf::Phase p) { return s.phaseMs[static_cast<std::size_t>(p)]; };
  const auto span = [&](render::perf::Span x) { return static_cast<std::size_t>(x); };
  using render::perf::Phase;
  using render::perf::Span;
  if (s.frames == 0) {
    CADENCE_LOG(Info, "screen_perf", {{"window_s", s.seconds}, {"turns_per_s", s.turnsPerS}, {"frames", s.frames}});
    return;
  }
  CADENCE_LOG(Info, "screen_perf",
              {{"window_s", s.seconds},
               {"frames", s.frames},
               {"fps", s.fps},
               {"turns_per_s", s.turnsPerS},
               {"anim_frames", s.chained},
               {"anim_fps", s.chainedFps},
               {"late", s.late},
               {"cpu_p50_ms", s.cpuMs.p50},
               {"cpu_p95_ms", s.cpuMs.p95},
               {"cpu_max_ms", s.cpuMs.max},
               {"frame_p95_ms", s.frameMs.p95},
               {"update_p95_ms", phase(Phase::Update).p95},
               {"rml_update_p95_ms", s.spanMs[span(Span::RmlUpdate)].p95},
               {"draw_p95_ms", phase(Phase::Draw).p95},
               {"rml_render_p95_ms", s.spanMs[span(Span::RmlRender)].p95},
               {"gpu_p95_ms", phase(Phase::Gpu).p95},
               {"swap_p50_ms", phase(Phase::Swap).p50},
               {"swap_p95_ms", phase(Phase::Swap).p95},
               {"link_p95_ms", phase(Phase::Link).p95},
               {"draws_p95", s.counters[static_cast<std::size_t>(render::perf::Counter::Draws)].p95},
               {"clip_masks_p95", s.counters[static_cast<std::size_t>(render::perf::Counter::ClipMasks)].p95},
               {"layers_p95", s.counters[static_cast<std::size_t>(render::perf::Counter::Layers)].p95},
               {"fill_mpx_p95", s.counters[static_cast<std::size_t>(render::perf::Counter::FillKpx)].p95 / 1000.0},
               {"gradients_p95", s.counters[static_cast<std::size_t>(render::perf::Counter::Gradients)].p95},
               {"gradient_mpx_p95", s.counters[static_cast<std::size_t>(render::perf::Counter::GradientKpx)].p95 / 1000.0},
               {"stencil_clear_mpx_p95",
                s.counters[static_cast<std::size_t>(render::perf::Counter::StencilClearKpx)].p95 / 1000.0},
               {"uploads", s.spanCount[span(Span::Upload)]},
               {"upload_ms", s.spanTotalMs[span(Span::Upload)]},
               {"image_scan_ms", s.spanTotalMs[span(Span::ImageScan)]},
               {"idle_turns", s.idleTurns}});
}

bool App::step(int maxWaitMs) {
  if (quit_ || !platform_) return false;
  std::int64_t now = monotonicMs();
  // this turn follows an animation's frame at once (its stats say whether it kept the display's rate)
  const bool chained = animating_;
  render::WaitInputs w;
  // a follower's picture is steered every turn, so the loop turns at a frame's pace while one plays (multiroom P4)
  w.maxWaitMs = player_ && player_->following() ? std::min(maxWaitMs, kFollowStepMs) : maxWaitMs;
  w.keysDueMs = keys_.nextDueMs(now);
  w.uiDueMs = ui_->nextUpdateMs(now);
  w.frameOwed = exposed_ || ui_->dirty();
  w.animating = animating_;
  w.hidden = backgrounded_ || !displayOn_;
  w.holding = scheduler_.holding();
  const int wait = render::loopWaitMs(w);
  lastWaitMs_ = wait;
  render::perf::Turn turn;
  turn.chained = chained;
  using render::perf::Phase;
  const auto mark = [&turn](Phase p, std::int64_t& t) {
    const auto n = render::perf::nowNs();
    turn.ns[static_cast<std::size_t>(p)] += n - t;
    t = n;
  };
  SDL_Event e;
  const bool woke = platform_->waitEvent(&e, wait);
  std::int64_t t = render::perf::nowNs();
  if (woke) {
    now = monotonicMs();
    handleSdl(e, now);
    while (!quit_ && SDL_PollEvent(&e)) handleSdl(e, now);
  }
  mark(Phase::Events, t);
  if (quit_ || !player_) return !quit_;
  now = monotonicMs();
  for (auto& event : client_->drain()) handleLink(event);
  adoptRings();
  if (const auto clock = takeGroupClock()) player_->groupClock(*clock);
  player_->pump(unixMs());
  if (auto l = keys_.poll(now)) handleKey(*l, now, false);
  mark(Phase::Link, t);
  ui_->update(now);
  sendScreenStatus(false);
  mark(Phase::Update, t);

  animating_ = false;
  if (!backgrounded_) {
    render::FrameInputs in;
    in.nowMs = now;
    in.videoInGl = player_->videoInGl();
    in.videoFrameDue = player_->frameDue();
    in.firstVideoFrame = player_->firstFrameShown();
    // the display off draws no ui, so its changes owe no frame (one black frame went at display_power's expose)
    in.uiDirty = displayOn_ && ui_->dirty();
    in.uiReady = ui_->ready();
    in.exposed = exposed_;
    exposed_ = false;
    turn.uiDirty = in.uiDirty;
    const auto decision = scheduler_.decide(in);
    if (decision.renderVideo) player_->renderFrame();
    if (pictureReady() != sentPictureReady_) sendMediaState();
    mark(Phase::Draw, t);
    if (decision.present) present(turn);
    // a frame of a ui change: the next one may be due a vsync later (a transition, ken burns, the visualizer), so
    // the next turn looks at once rather than sleeping; one turn that finds nothing new goes back to sleeping
    animating_ = decision.present && in.uiDirty;
  }
  turn.endNs = render::perf::nowNs();
  turn.spans = render::perf::take();
  turn.counters = render::perf::takeCounters();
  perf_.add(turn);
  perfLog_.add(turn);
  logPerf(now);
  if (options_.exitAfterMs > 0 && now - startedMs_ >= options_.exitAfterMs) quit_ = true;
  return !quit_;
}

int App::run() {
  std::string error;
  if (!start(&error)) {
    CADENCE_LOG(Error, "start_failed", {{"code", ipc::Token(error)}});
    shutdown();
    return 1;
  }
  while (step(250)) {
  }
  CADENCE_LOG(Info, "exit", {{"presented", presented_}});
  shutdown();
  return 0;
}

}  // namespace cadence::screen::app
