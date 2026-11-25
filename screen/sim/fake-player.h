#pragma once

#include "player/player.h"
#include "render/render-backend.h"

#include <chrono>
#include <cstdint>
#include <functional>
#include <optional>
#include <vector>

namespace cadence::screen::sim {

// the sim's player: no mpv, no network. every play starts a synthetic session on a clock (paused behind the gate as
// the real one waits), and the video plane is a gradient the sim paints, so the osd, the routes and the screenshots
// all have something real to show. the main thread only, as the player's contract says
class FakePlayer final : public player::Player {
 public:
  explicit FakePlayer(render::RenderBackend& backend);

  player::Result play(const player::PlayRequest& request) override;
  player::Result command(const player::Command& command) override;
  void setGate(bool open) override;
  player::Result setLevel(std::uint32_t level, std::uint32_t rampMs) override;
  player::Result setDuck(bool active, float level, std::uint32_t rampMs) override;
  player::Result setMute(bool muted) override;
  player::Result setEq(const player::EqRequest& eq) override;
  void pump(std::int64_t nowMs) override;
  [[nodiscard]] bool frameDue() override;
  bool renderFrame() override;
  [[nodiscard]] std::optional<render::Texture> videoTexture() const override;
  [[nodiscard]] bool videoInGl() const override;
  [[nodiscard]] bool firstFrameShown() const override;
  void reportSwap() override {}
  [[nodiscard]] const player::PlayerState& state() const override;
  // the cache's first fill, as mpv's would read: a short wait with a rising percentage and a speed, so resolving
  // and the osd show what they show on a real stream
  [[nodiscard]] std::optional<int> bufferingPercent() const override;
  [[nodiscard]] bool waiting() const override;
  [[nodiscard]] std::optional<std::uint64_t> cacheSpeed() const override;
  void onStateChanged(std::function<void(const player::PlayerState&)> listener) override;

 private:
  void changed();
  void stop(const char* error);
  [[nodiscard]] player::Track track(const char* id, cadence_v2_MediaTrackKind kind, const char* language,
                                    const char* label, bool selected) const;
  static std::int64_t unixMs();

  render::RenderBackend& backend_;
  std::function<void(const player::PlayerState&)> listener_;
  player::PlayerState state_;
  // the session's first pump (the fill runs from it) and the last pump's time
  std::int64_t fillFromMs_ = -1;
  std::int64_t nowMs_ = 0;
  // a seek under way: its target (s) and when it lands
  bool seekPending_ = false;
  double seekTarget_ = 0;
  std::int64_t seekLandsAtMs_ = 0;
  render::Texture texture_;
  std::vector<std::uint8_t> pixels_;
  // seconds into the item, advanced by pump while playing
  double position_ = 0;
  std::int64_t lastPumpMs_ = 0;
  std::int64_t lastFrameMs_ = 0;
  std::uint64_t frame_ = 0;
  bool gate_ = false;
  bool notify_ = false;
  bool frameDue_ = false;
  bool firstFrame_ = false;
  std::uint32_t level_ = 40;
};

}  // namespace cadence::screen::sim
