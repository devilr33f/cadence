#pragma once

#include "app/routes/core/router.h"
#include "app/routes/playback/routes.h"
#include "ui/images/image-cache.h"

#include <memory>
#include <optional>
#include <string>
#include <vector>

// i2's routes off the device: the router with a blank home under them, a player that records what it is asked, an
// outbox that records the calls and contexts, a clock fixed at 21:40
namespace cadence::screen::routes::playback {

struct SentCall {
  std::string id;
  std::string method;
  std::string json;
};

class FakePlayer final : public app::PlayerControl {
 public:
  player::Result play(const player::PlayRequest& request) override {
    // as mpv's player: the new session is its state at once
    plays.push_back(request);
    playerState.sessionId = request.sessionId;
    playerState.purpose = request.purpose;
    return "";
  }
  player::Result command(const player::Command& command) override {
    commands.push_back(command);
    return "";
  }
  [[nodiscard]] const player::PlayerState& state() const override { return playerState; }
  [[nodiscard]] std::optional<int> bufferingPercent() const override { return buffering; }
  [[nodiscard]] bool waiting() const override { return waits; }
  [[nodiscard]] std::optional<std::uint64_t> cacheSpeed() const override { return speed; }

  player::PlayerState playerState;
  std::optional<int> buffering;
  bool waits = false;
  std::optional<std::uint64_t> speed;
  std::vector<player::PlayRequest> plays;
  std::vector<player::Command> commands;
};

class FakeOutbox final : public app::Outbox {
 public:
  bool send(const cadence_screen_v1_Envelope& e) override;
  std::vector<SentCall> calls;
  // the transport the audio page asked of a cast (NowPlayingCommand)
  std::vector<cadence_screen_v1_NowPlayingCommand> castCommands;
  // each context's route, page ref, position and tracks as text
  struct Context {
    std::uint64_t revision = 0;
    cadence_v2_ScreenRoute route = cadence_v2_ScreenRoute_SCREEN_ROUTE_UNSPECIFIED;
    std::string pageRef;
    bool hasPlayer = false;
    std::string itemRef;
    std::uint64_t positionMs = 0;
    std::string tracks;
  };
  std::vector<Context> contexts;
};

// 2026-11-14 21:40 utc (the test image's zone)
inline constexpr std::int64_t kEpochMs = 1794692400000;

struct Rig {
  // prepare: what the router takes before it attaches (a test's layer)
  explicit Rig(ui::Ui& ui, std::function<void(Router&)> prepare = {});
  ~Rig();
  Rig(const Rig&) = delete;
  Rig& operator=(const Rig&) = delete;

  ui::Ui& ui;
  FakeOutbox outbox;
  FakePlayer fakePlayer;
  std::unique_ptr<ui::images::ImageCache> images;
  std::unique_ptr<Router> router;
  PlaybackRoutes* playback = nullptr;
  std::shared_ptr<bool> alive;

  // the player's report as the routes take it, at the rig's clock
  void state(player::PlayerState s);
  // the answer to the newest call of a method (the fake server)
  bool answer(const std::string& method, const std::string& json, const std::string& status = "ok");
  // a card page (proto/fixtures/cards/v1) answering the newest call of a method
  bool answerPage(const std::string& method, const std::string& fixture);
  [[nodiscard]] bool called(const std::string& method) const;
  [[nodiscard]] Json lastArgs(const std::string& method) const;
  // as cadenced or the ui's own play would: media.video/play for the session
  void play(const Session& s);
  // as cadenced would: the session audiod renders (an airplay cast from "iPhone Маши" with the gallery's cover, at
  // positionMs of 2:03); an empty id is none
  void cast(const std::string& sessionId, cadence_v2_PlaybackState state, std::uint64_t positionMs,
            const std::string& title = "worms", cadence_v2_MediaSource source = cadence_v2_MediaSource_MEDIA_SOURCE_AIRPLAY);
};

// a fresh rig on ui (the previous one goes first)
Rig& start(ui::Ui& ui, std::function<void(Router&)> prepare = {});
// the rig goes, while its ui is still there
void stop();

}  // namespace cadence::screen::routes::playback
