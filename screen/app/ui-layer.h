#pragma once

#include "cadence/screen/v1/screen.pb.h"
#include "platform/keys.h"
#include "player/player.h"
#include "render/render-backend.h"

#include <array>
#include <cstdint>
#include <functional>
#include <optional>
#include <span>
#include <string>
#include <vector>

namespace cadence::screen::app {

// the ui's way out to cadenced (rpc requests, screen context, enroll codes, ...): false while there is no link
class Outbox {
 public:
  virtual ~Outbox() = default;
  virtual bool send(const cadence_screen_v1_Envelope& envelope) = 0;
};

// what the ui does with the local player itself: the transport the user drives on the osd (pause, seek, tracks), a
// voiceover switch or an auto-continue that resolved its own playable, and idle's muted aerial clips. every call is
// the player's own (async to mpv); cadenced hears of the result through MediaState, as with its own commands
class PlayerControl {
 public:
  virtual ~PlayerControl() = default;
  virtual player::Result play(const player::PlayRequest& request) = 0;
  virtual player::Result command(const player::Command& command) = 0;
  [[nodiscard]] virtual const player::PlayerState& state() const = 0;
  // mpv's buffering percentage while it waits for its cache
  [[nodiscard]] virtual std::optional<int> bufferingPercent() const { return std::nullopt; }
  // the session waits for its source (loading, seeking, paused for the cache), whatever the user's pause
  [[nodiscard]] virtual bool waiting() const { return false; }
  // what the cache reads now, bytes/s; nullopt when the player does not say
  [[nodiscard]] virtual std::optional<std::uint64_t> cacheSpeed() const { return std::nullopt; }
  // the session's picture is up: its first frame drawn where the player draws through our gl, else (a plane of its
  // own, android's surface, or no video) the session no longer waits
  [[nodiscard]] virtual bool pictureReady() const { return !waiting(); }
  // where playback is now (player::Player::positionSample)
  [[nodiscard]] virtual player::PositionSample positionSample() const {
    const auto& s = state();
    return player::PositionSample{s.positionMs, s.positionAtMs, s.rate, s.seekSeq, s.seekPending};
  }
};

// the tap's feed for the visualizers (player/tap-client.h): called on the tap's own thread, so it never waits
class VizFeed {
 public:
  virtual ~VizFeed() = default;
  virtual void pcm(std::span<const float> interleaved, std::uint32_t channels, std::uint32_t rate,
                   std::int64_t monoNs) = 0;
  virtual void bands(const std::array<std::uint8_t, 16>& bands) = 0;
  // any thread: wakes the main loop when a visualizer drawing on a thread of its own finished a frame; set once, at
  // start, before any frame
  virtual void setWake(std::function<void()> wake) { (void)wake; }
};

struct UiStatus {
  cadence_v2_ScreenRoute route = cadence_v2_ScreenRoute_SCREEN_ROUTE_UNSPECIFIED;
  bool drawerOpen = false;

  bool operator==(const UiStatus&) const = default;
};

struct UiGeometry {
  render::Size screen;
  // inside the overscan: where the ui lays out
  render::Rect safeArea;
  double dpRatio = 1.0;

  bool operator==(const UiGeometry&) const = default;
};

// the ui over the video (stream b's rmlui layer implements it). main thread only, gl current in attach, detach and
// render. render draws premultiplied over whatever is already in the frame (the video), every presented frame: a ui
// that redraws only when dirty keeps its own layer (RenderBackend::createRenderTarget) and composites it
class UiLayer {
 public:
  virtual ~UiLayer() = default;

  // the backend lives until detach; after a context loss the app detaches, makes a new backend and attaches again
  virtual void attach(render::RenderBackend& backend, const UiGeometry& geometry, Outbox& outbox) = 0;
  virtual void detach() = 0;
  virtual void resize(const UiGeometry& geometry) = 0;

  // a key from the remote, or one cadenced injected (source api): true when the ui used it
  virtual bool key(const platform::KeyEvent& event) = 0;
  // a message for the ui: route, toast, voice_state, link_status, claim_state, cards, assistant_turn, rpc_*, ready,
  // capability_config, airplay_pin, mirror_*, text_input, ...
  virtual void message(const cadence_screen_v1_Envelope& envelope) = 0;
  virtual void playerState(const player::PlayerState& state) = 0;

  // advances animations to nowMs
  virtual void update(std::int64_t nowMs) = 0;
  // something changed since the last render
  [[nodiscard]] virtual bool dirty() const = 0;
  // ms from nowMs until update has something due (0: now), -1 when nothing is until an input: the main loop's sleep
  [[nodiscard]] virtual std::int64_t nextUpdateMs(std::int64_t nowMs) const {
    (void)nowMs;
    return -1;
  }
  // the first page is laid out (until then the app shows nothing: s1's startup fix)
  [[nodiscard]] virtual bool ready() const = 0;
  virtual void render(render::RenderBackend& backend) = 0;
  // before render: whether a video frame drew under the ui through gl. without one, every frame the loop presents
  // is a ui change, so the ui may draw straight into the frame rather than keep a layer of its own
  virtual void setVideoUnder(bool video) { (void)video; }

  [[nodiscard]] virtual UiStatus status() const = 0;
  // cadence.cards.v1 versions it renders, for Hello
  [[nodiscard]] virtual std::vector<std::uint32_t> cardVersions() const = 0;
  // the card features it draws (primitives and optional fields, as a card names them), for Hello; empty claims
  // every feature of its versions
  [[nodiscard]] virtual std::vector<std::string> cardFeatures() const { return {}; }

  // the local player, from attach until detach (null without one)
  virtual void attachPlayer(PlayerControl* player) { (void)player; }
  // the subtitle line now playing (empty between lines): the ui draws subtitles itself
  virtual void subtitle(const std::string& text) { (void)text; }
  // where the tap's pcm and bands go; it lives as long as the layer
  virtual VizFeed* vizFeed() { return nullptr; }
};

// no ui: wave 1's stand-in until stream b lands, and the tests'
class NullUi final : public UiLayer {
 public:
  void attach(render::RenderBackend&, const UiGeometry&, Outbox&) override {}
  void detach() override {}
  void resize(const UiGeometry&) override {}
  bool key(const platform::KeyEvent&) override { return false; }
  void message(const cadence_screen_v1_Envelope&) override {}
  void playerState(const player::PlayerState&) override {}
  void update(std::int64_t) override {}
  [[nodiscard]] bool dirty() const override { return false; }
  [[nodiscard]] bool ready() const override { return true; }
  void render(render::RenderBackend&) override {}
  [[nodiscard]] UiStatus status() const override { return {}; }
  [[nodiscard]] std::vector<std::uint32_t> cardVersions() const override { return {}; }
};

}  // namespace cadence::screen::app
