#pragma once

#include "platform/platform.h"
#include "player/mpv-options.h"

#include <cstdint>
#include <optional>
#include <string>
#include <vector>

namespace cadence::screen::app {

struct AppOptions {
  // cadenced's screen socket; @name is abstract (android)
  std::string socket;
  platform::PlatformOptions platform;
  // mpv's ao forced (null: no sound at all, for smoke runs); empty = the build's route
  std::string ao;
  std::string hwdec;
  // mpv's built-in profile applied first (MpvConfig::profile), empty = none
  std::string mpvProfile;
  // caps what the player is told the display shows (0: the panel's own rate): a board whose flip path
  // sustains less than the panel's refresh paces video there, and mpv drops cleanly at the vo
  int maxDisplayFps = 0;
  // ends the run after this long (smoke runs), 0 = never
  std::int64_t exitAfterMs = 0;
  bool help = false;
  // the frame profile (CADENCE_SCREEN_PERF): the screen_perf line's period (60 s by default, 5 s with the env set)
  // and a glFinish before each swap, so the line tells the gpu's time from the vsync wait (CADENCE_SCREEN_PERF=gpu)
  std::int64_t perfLogMs = 60000;
  bool perfGpu = false;
  // the on-screen frame graph (CADENCE_SCREEN_HUD=1)
  bool hud = false;
};

// CADENCE_SCREEN_PERF and CADENCE_SCREEN_HUD into the options (unset leaves them)
void applyPerfEnv(AppOptions& options, const char* perfEnv, const char* hudEnv);

// the defaults for this platform: /run/cadence/screen.sock on linux, @cadence-screen on android
AppOptions defaultOptions();

// --socket PATH --driver NAME --size WxH --no-vsync --ao NAME --hwdec NAME --mpv-profile NAME --max-display-fps N --exit-after-ms N --help; the env's
// CADENCE_SCREEN_SOCKET seeds --socket. nullopt and the bad argument in error
std::optional<AppOptions> parseOptions(const std::vector<std::string>& args, const char* socketEnv,
                                       std::string* error);

const char* usage();

}  // namespace cadence::screen::app
