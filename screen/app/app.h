#pragma once

#include "app/convert.h"
#include "app/options.h"
#include "app/ui-layer.h"
#include "ipc/client.h"
#include "platform/keys.h"
#include "platform/platform.h"
#include "player/mirror-presenter.h"
#include "player/mpv-settings.h"
#include "player/player.h"
#include "player/tap-client.h"
#include "render/capture.h"
#include "render/frame-scheduler.h"
#include "render/gles-backend.h"
#include "render/perf-hud.h"
#include "render/perf.h"

#if defined(CADENCE_SCREEN_HAS_MPV)
#include "player/mpv-player.h"
#endif

#include <atomic>
#include <cstdint>
#include <functional>
#include <memory>
#include <mutex>
#include <optional>
#include <string>

namespace cadence::screen::app {

// what builds the player when the build has no mpv (the sim's placeholder): called on the main thread with the gl
// context current; null makes start fail with "player"
using PlayerFactory = std::function<std::unique_ptr<player::Player>(render::RenderBackend&)>;

// cadence-screen's main loop: sdl events, the link's messages and mpv's wakes on one thread, which renders when
// something changed (render-when-dirty) and owns the gl context
class App final : private Outbox, private PlayerControl {
 public:
  App(AppOptions options, std::unique_ptr<UiLayer> ui, PlayerFactory playerFactory = nullptr);
  ~App() override;
  App(const App&) = delete;
  App& operator=(const App&) = delete;

  // sdl, gl, the backend, the player, the ui and the link; false and a token in error when one fails
  bool start(std::string* error);
  // one turn of the loop, waiting at most maxWaitMs for something to do; false once it should end
  bool step(int maxWaitMs);
  // start, then steps until quit or exitAfterMs; the process's exit code
  int run();
  // in reverse order of start: the link, the ui, the player (render context, then mpv), the backend, gl, sdl
  void shutdown();

#if defined(CADENCE_SCREEN_HAS_MPV)
  // named apart from the player and platform namespaces, which a member of that name would hide in here
  // the player as mpv's, only when startGl made one: a player factory's (the sim's placeholder) is not an MpvPlayer
  [[nodiscard]] player::MpvPlayer* mpv() const { return mpv_; }
#endif
  [[nodiscard]] platform::Platform* sdl() const { return platform_.get(); }
  [[nodiscard]] render::RenderBackend* backend() const { return backend_.get(); }
  [[nodiscard]] std::uint64_t presented() const { return presented_; }
  [[nodiscard]] bool gateOpen() const { return gateOpen_; }
  // one frame drawn as present would (without the swap) and read back; the sim and debugging only, it stalls the gpu
  [[nodiscard]] std::optional<render::Image> capture();
  // the event wait the last step computed (ms): an idle screen sleeps, it never polls
  [[nodiscard]] int lastWaitMs() const { return lastWaitMs_; }
  // the frame profile since the last reset (the sim's perf command)
  [[nodiscard]] render::perf::Summary perfSummary() const;
  [[nodiscard]] const render::perf::Recorder& perfRecorder() const { return perf_; }
  void resetPerf();

 private:
  bool send(const cadence_screen_v1_Envelope& envelope) override;
  // the ui's way to the player (PlayerControl)
  player::Result play(const player::PlayRequest& request) override;
  player::Result command(const player::Command& command) override;
  [[nodiscard]] const player::PlayerState& state() const override;
  [[nodiscard]] std::optional<int> bufferingPercent() const override;
  [[nodiscard]] bool waiting() const override;
  [[nodiscard]] std::optional<std::uint64_t> cacheSpeed() const override;
  [[nodiscard]] bool pictureReady() const override;
  [[nodiscard]] player::PositionSample positionSample() const override;
  // ring offers from audiod's tap into mpv's ao_cadence
  void adoptRings();
  // the tap's newest group clock since the last turn (multiroom P4)
  std::optional<player::FollowClock> takeGroupClock();
  // how long the loop waits at most while a follower's picture is steered
  static constexpr int kFollowStepMs = 16;
  // what lives on the gl context: the backend, the player (its render context) and the ui's gl objects
  bool startGl(std::string* error);
  void stopGl();
  // one frame onto what is bound (the video plane, then the ui); present and capture's shared body
  void draw();
  void handleSdl(const SDL_Event& event, std::int64_t nowMs);
  void handleKey(const platform::KeyEvent& event, std::int64_t nowMs, bool injected);
  void handleLink(ipc::ScreenClient::Event& event);
  void handleMessage(const cadence_screen_v1_Envelope& envelope);
  // the video surface takes one producer: a mirror session stops the local player, and a local play the mirror
  void startMirror(const cadence_screen_v1_MirrorStart& start);
  void reply(const char* requestId, const std::string& code);
  void setGate(bool open);
  void sendScreenStatus(bool force);
  void sendMediaState();
  // media.mpv: the config cadenced sent, and its state back; kept for a player a context loss rebuilds
  void applyMpvConfigJson(const char* json);
  void sendMpvState(const player::MpvKeyState& state);
  void applyGeometry();
  void present(render::perf::Turn& turn);
  void logPerf(std::int64_t nowMs);
  // on the main thread; the link's thread reads the copy under helloMu_
  void refreshHello();
  [[nodiscard]] std::int64_t monotonicMs() const;

  AppOptions options_;
  PlayerFactory playerFactory_;
  std::unique_ptr<UiLayer> ui_;
  std::unique_ptr<platform::Platform> platform_;
  std::unique_ptr<render::GlesBackend> backend_;
  std::unique_ptr<player::Player> player_;
  player::MpvPlayer* mpv_ = nullptr;
  std::unique_ptr<ipc::ScreenClient> client_;
  // airplay mirroring (the tap's frames onto the video surface); null where the platform has no decoder
  std::unique_ptr<player::MirrorPresenter> mirror_;
  std::unique_ptr<player::TapClient> tap_;
  // the ui's feed for the tap's thread, read there without the ui
  std::atomic<VizFeed*> vizFeed_{nullptr};
  // the tap thread's newest group clock, taken by the main thread
  std::mutex groupClockMu_;
  std::optional<player::FollowClock> groupClock_;
  render::FrameScheduler scheduler_;
  render::perf::Recorder perf_;
  // the screen_perf line's own window, reset at each line
  render::perf::Recorder perfLog_;
  std::unique_ptr<render::perf::Hud> hud_;
  platform::KeyTracker keys_;

  std::mutex helloMu_;
  HelloInfo hello_;
  UiGeometry geometry_;
  double overscanPercent_ = 0;
  // media.mpv's config as cadenced last sent it ("" once none has)
  std::string mpvConfigJson_;
  bool gateOpen_ = false;
  bool displayOn_ = true;
  bool backgrounded_ = false;
  bool quit_ = false;
  bool exposed_ = true;
  bool linkUp_ = false;
  std::optional<UiStatus> sentUi_;
  cadence_screen_v1_ScreenPower sentPower_ = cadence_screen_v1_ScreenPower_SCREEN_POWER_UNSPECIFIED;
  bool sentPictureReady_ = false;
  std::uint64_t presented_ = 0;
  // the last turn presented a change of the ui: the next looks again without sleeping
  bool animating_ = false;
  int lastWaitMs_ = -1;
  std::int64_t startedMs_ = 0;
  std::int64_t statsLoggedMs_ = 0;
};

}  // namespace cadence::screen::app
