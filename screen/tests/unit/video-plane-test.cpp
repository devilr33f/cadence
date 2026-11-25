#include "player/video-plane.h"

#include <catch2/catch_test_macros.hpp>

using cadence::screen::player::videoPlaneShows;

TEST_CASE("the frame stays clear for a video plane below it") {
  // android's mpv on the SurfaceView: playing a film must not be covered (it was: only the ui showed)
  CHECK(videoPlaneShows(false, true, false));
  CHECK(videoPlaneShows(true, false, false));
  // mpv drawing into gl (linux) draws its own texture; nothing playing keeps the opaque background
  CHECK_FALSE(videoPlaneShows(false, true, true));
  CHECK_FALSE(videoPlaneShows(false, false, false));
}
