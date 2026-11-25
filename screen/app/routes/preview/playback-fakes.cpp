#include "app/routes/preview/playback-fakes.h"

#include "app/routes/preview/fake-catalog.h"
#include "ui/cards/model.h"

#include <cstdio>
#include <cstring>
#include <fstream>
#include <iterator>

namespace cadence::screen::routes::playback {

namespace {

// the root the playback routes open over (home is i1's; here a blank page)
class Blank final : public Route {
 public:
  Blank(Services& s, const RouteArgs&) : Route(s) {}
  [[nodiscard]] cadence_v2_ScreenRoute id() const override { return cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME; }
  std::string body() override { return "<div class=\"safe\"/>"; }
};

// generated images for ambient's ken burns: dusk over hills, 960 x 540, no file and no network
class GeneratedImages final : public ImageSource {
 public:
  void request(const std::string&, std::function<void(std::optional<DecodedImage>)> done) override {
    pending_.push_back(std::move(done));
  }
  void poll() override {
    auto p = std::move(pending_);
    pending_.clear();
    for (auto& done : p) done(image());
  }

 private:
  static DecodedImage image() {
    DecodedImage img;
    img.width = 960;
    img.height = 540;
    img.rgba.resize(960 * 540 * 4);
    for (int y = 0; y < 540; ++y) {
      for (int x = 0; x < 960; ++x) {
        const double t = y / 540.0;
        double r = 40 + 170 * (1 - t) * (1 - t), g = 30 + 90 * (1 - t), b = 70 + 120 * t;
        const double hill = 380 + 50 * std::sin(x / 140.0) + 30 * std::sin(x / 53.0 + 1);
        if (y > hill) {
          r = 22 + 10 * std::sin(x / 31.0);
          g = 30;
          b = 38;
        }
        if (std::hypot(x - 640.0, y - 300.0) < 60 && y <= hill) {
          r = 255;
          g = 190;
          b = 120;
        }
        const auto o = static_cast<std::size_t>((y * 960 + x) * 4);
        img.rgba[o] = static_cast<std::uint8_t>(std::clamp(r, 0.0, 255.0));
        img.rgba[o + 1] = static_cast<std::uint8_t>(std::clamp(g, 0.0, 255.0));
        img.rgba[o + 2] = static_cast<std::uint8_t>(std::clamp(b, 0.0, 255.0));
        img.rgba[o + 3] = 255;
      }
    }
    return img;
  }
  std::vector<std::function<void(std::optional<DecodedImage>)>> pending_;
};

std::unique_ptr<Rig>& current() {
  static std::unique_ptr<Rig> r;
  return r;
}

void tick(Rig* r, std::shared_ptr<bool> alive) {
  r->ui.after(16, [r, alive] {
    if (!*alive) return;
    r->router->update(r->ui.nowMs());
    tick(r, alive);
  });
}

}  // namespace

bool FakeOutbox::send(const cadence_screen_v1_Envelope& e) {
  if (e.which_body == cadence_screen_v1_Envelope_rpc_request_tag) {
    const auto& r = e.body.rpc_request;
    calls.push_back({r.id, r.method, r.json != nullptr ? r.json : ""});
  }
  if (e.which_body == cadence_screen_v1_Envelope_now_playing_command_tag) castCommands.push_back(e.body.now_playing_command);
  if (e.which_body == cadence_screen_v1_Envelope_screen_context_tag) {
    const auto& c = e.body.screen_context;
    Context x;
    x.revision = c.revision;
    x.route = c.route;
    x.pageRef = c.page_ref;
    x.hasPlayer = c.has_player;
    x.itemRef = c.player.item_ref;
    x.positionMs = c.player.position_ms;
    for (pb_size_t i = 0; i < c.player.tracks_count; ++i)
      x.tracks += std::string(c.player.tracks[i].id) + (c.player.tracks[i].selected ? "*" : "") + " ";
    contexts.push_back(x);
  }
  return true;
}

Rig::Rig(ui::Ui& u, std::function<void(Router&)> prepare) : ui(u), alive(std::make_shared<bool>(true)) {
  ui::images::CacheOptions o;
  o.inlineDecode = true;
  images = std::make_unique<ui::images::ImageCache>(std::make_shared<fake::FakeImages>(), o);
  Router::Options ro;
  ro.images = images.get();
  ro.openRoot = false;
  router = std::make_unique<Router>(ro);
  router->registerRoute(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME,
                        [](Services& s, const RouteArgs& a) -> std::unique_ptr<Route> { return std::make_unique<Blank>(s, a); });
  PlaybackRoutes::Options po;
  po.viz = makeSyncVizView(30);
  po.images = std::make_unique<GeneratedImages>();
  po.epochMs = [] { return kEpochMs; };
  playback = &PlaybackRoutes::install(*router, std::move(po));
  if (prepare) prepare(*router);
  router->attachPlayer(&fakePlayer);
  router->attach(u, outbox);
  ui.onActivate([this](const ui::Activation& a) { router->activate(a); });
  ui.onBack([this] { return router->back(); });
  ui.onPageLoaded([this](Rml::ElementDocument& doc) { router->loaded(doc); });
  router->open(cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME);
  tick(this, alive);
}

Rig::~Rig() {
  *alive = false;
  if (!images->bound()) router->abandon();
  router.reset();
  images.reset();
}

void Rig::state(player::PlayerState s) {
  s.positionAtMs = kEpochMs;
  fakePlayer.playerState = s;
  router->playerState(s);
}

bool Rig::answer(const std::string& method, const std::string& json, const std::string& status) {
  for (auto it = outbox.calls.rbegin(); it != outbox.calls.rend(); ++it) {
    if (it->method != method) continue;
    cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
    e.which_body = cadence_screen_v1_Envelope_rpc_response_tag;
    std::strncpy(e.body.rpc_response.id, it->id.c_str(), sizeof e.body.rpc_response.id - 1);
    std::strncpy(e.body.rpc_response.status, status.c_str(), sizeof e.body.rpc_response.status - 1);
    std::string body = json;
    e.body.rpc_response.json = body.data();
    outbox.calls.erase(std::next(it).base());
    router->message(e);
    return true;
  }
  return false;
}

bool Rig::answerPage(const std::string& method, const std::string& fixture) {
  std::ifstream in(std::string(CADENCE_SCREEN_PLAYBACK_CARDS) + "/" + fixture, std::ios::binary);
  const std::string bytes{std::istreambuf_iterator<char>(in), {}};
  auto page = ui::cards::decodePage({reinterpret_cast<const std::uint8_t*>(bytes.data()), bytes.size()});
  if (!page) return false;
  for (auto it = outbox.calls.rbegin(); it != outbox.calls.rend(); ++it) {
    if (it->method != method) continue;
    cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
    e.which_body = cadence_screen_v1_Envelope_rpc_response_tag;
    std::strncpy(e.body.rpc_response.id, it->id.c_str(), sizeof e.body.rpc_response.id - 1);
    std::strncpy(e.body.rpc_response.status, "ok", sizeof e.body.rpc_response.status - 1);
    e.body.rpc_response.page = page.get();
    outbox.calls.erase(std::next(it).base());
    router->message(e);
    return true;
  }
  return false;
}

bool Rig::called(const std::string& method) const {
  for (const auto& c : outbox.calls)
    if (c.method == method) return true;
  return false;
}

Json Rig::lastArgs(const std::string& method) const {
  for (auto it = outbox.calls.rbegin(); it != outbox.calls.rend(); ++it)
    if (it->method == method) return Json::parse(it->json);
  return {};
}

void Rig::play(const Session& s) { playback->start(s); }

void stop() { current().reset(); }

Rig& start(ui::Ui& ui, std::function<void(Router&)> prepare) {
  current().reset();
  current() = std::make_unique<Rig>(ui, std::move(prepare));
  return *current();
}

}  // namespace cadence::screen::routes::playback

namespace cadence::screen::routes::playback {

void Rig::cast(const std::string& sessionId, cadence_v2_PlaybackState state, std::uint64_t positionMs,
               const std::string& title, cadence_v2_MediaSource source) {
  cadence_screen_v1_Envelope e = cadence_screen_v1_Envelope_init_zero;
  e.which_body = cadence_screen_v1_Envelope_now_playing_tag;
  auto& n = e.body.now_playing;
  std::snprintf(n.session_id, sizeof n.session_id, "%s", sessionId.c_str());
  n.source = source;
  n.state = sessionId.empty() ? cadence_v2_PlaybackState_PLAYBACK_STATE_IDLE : state;
  n.position_ms = positionMs;
  n.position_at_ms = kEpochMs;
  n.duration_ms = 123000;
  std::string t = title, artist = "guardin", album = "worms", sender = "iPhone Маши";
  n.title = t.data();
  n.artist = artist.data();
  n.album = album.data();
  if (source == cadence_v2_MediaSource_MEDIA_SOURCE_AIRPLAY) n.sender = sender.data();
  n.actions_count = 3;
  n.actions[0] = cadence_screen_v1_MediaAction_MEDIA_ACTION_PAUSE;
  n.actions[1] = cadence_screen_v1_MediaAction_MEDIA_ACTION_RESUME;
  n.actions[2] = cadence_screen_v1_MediaAction_MEDIA_ACTION_STOP;
  if (source == cadence_v2_MediaSource_MEDIA_SOURCE_SOLOIST) {
    n.actions[n.actions_count++] = cadence_screen_v1_MediaAction_MEDIA_ACTION_NEXT;
    n.actions[n.actions_count++] = cadence_screen_v1_MediaAction_MEDIA_ACTION_PREVIOUS;
  }
  std::snprintf(n.artwork_path, sizeof n.artwork_path, "%s", CADENCE_SCREEN_PLAYBACK_GOLDEN "/../../../../sim/examples/cover-dusk.jpg");
  std::snprintf(n.artwork_sha256, sizeof n.artwork_sha256, "%s", "8c73c45694719f0b7718292a66db96f639dcde4a5122325ff24c39a16cb56637");
  n.color_a = 0x94525f;
  n.color_b = 0x643a4c;
  playback->message(e);
}

}  // namespace cadence::screen::routes::playback
