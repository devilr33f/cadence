#pragma once

#include "player/audio-route.h"
#include "player/player.h"

#include <cstdint>
#include <string>
#include <utility>
#include <vector>

namespace cadence::screen::player {

// how video reaches the screen (s1): linux draws mpv's frames through the render api into our texture; android
// gives mpv its own SurfaceView below the ui (vo=mediacodec_embed, hwdec=mediacodec), as the gl path cannot hold
// 1080p on the stick
enum class VideoPath { RenderApi, SurfaceEmbed };

struct MpvConfig {
  VideoPath videoPath = VideoPath::RenderApi;
  AoMode ao = AoMode::Stock;
  // forces mpv's ao (tests: null); empty = the mode's own
  std::string aoOverride;
  // empty = the path's default: auto-safe through the render api (v4l2m2m-copy or drm-copy on the pi), mediacodec
  // on the surface
  std::string hwdec;
  // one of mpv's built-in profiles applied first (the pi's "fast": its default scalers and dither held a
  // 1080p60 clip at ~19 fps of the v3d's time, fast at the panel's 60); empty = none
  std::string profile;
  // 0 = the panel's own rate; a board whose flip path sustains less tells the player this instead, and
  // 60 fps content drops 2:1 at the vo instead of stalling the pipeline
  int displayFpsCap = 0;
  // ao_cadence's ring and eventfd (AoMode::Cadence)
  int ringFd = -1;
  int eventFd = -1;
  // mpv's log level forwarded (warn and error only ever ship, scrubbed)
  std::string logLevel = "warn";
};

using MpvOptions = std::vector<std::pair<std::string, std::string>>;

// the gl functions mpv is never handed. its libmpv path fences every frame (ra_gl_ctx_submit_frame) and only a swap
// of its own, which libmpv never makes, ever waits on or deletes those fences: one leaked GLsync a frame, which mesa's
// v3d backs with a sync_file fd, so 60 fps video filled the pi's 1024 fds in ~16 s and every dma-buf import after it
// failed. gles 3 requires GL_ARB_sync, so mpv is handed a glFenceSync that makes no fence (it skips a null one, and
// glDeleteSync(0) does nothing); buffer storage is withheld, as its host-mapped upload buffers are the one user that
// needs a real fence (they fall back to plain uploads, as on gles without the extension)
bool mpvWithheldGlFunction(const char* name);
// the glFenceSync mpv gets instead: true for that name
bool mpvStubbedGlFunction(const char* name);

// the options set before mpv_initialize
MpvOptions mpvInitOptions(const MpvConfig& config);

// ao_cadence's options for a ring audiod offered: its fds, and a buffer of three quarters of the ring. the reader
// holds a flushed ring until it is half full, so ao_cadence's own default (40 ms, 1920 frames) never got there and
// the session was silent (s5's refill against an 8192-frame ring on the stick)
MpvOptions ringOptions(int ringFd, int eventFd, std::uint32_t capacityFrames);

// the loadfile command's arguments for a request: paused while the gate holds it, no audio track when the route is
// silent, the start position, and media.mpv's next-load options. every option value is length-quoted (%n%), so
// headers with commas pass whole
std::vector<std::string> loadfileCommand(const PlayRequest& request, bool paused, const AudioRouteInfo& route,
                                         const MpvOptions& settings = {});

// "%n%value": mpv's quoting for a value inside a key=value list
std::string mpvQuote(const std::string& value);

// http-header-fields' value: "Key: Value" items, commas inside escaped
std::string headerFields(const std::map<std::string, std::string>& headers);

}  // namespace cadence::screen::player
