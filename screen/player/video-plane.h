#pragma once

namespace cadence::screen::player {

// whether this frame must stay clear (alpha 0) so the video plane under the gl surface shows: android's mpv
// (vo=mediacodec_embed) and the airplay mirror decode onto the activity's SurfaceView below the translucent sdl
// surface, so an opaque clear hid every film, clip and mirror. a player drawing into gl (linux) needs no hole
inline bool videoPlaneShows(bool mirrorActive, bool playerActive, bool videoInGl) {
  return mirrorActive || (playerActive && !videoInGl);
}

}  // namespace cadence::screen::player
