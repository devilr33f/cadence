#include "player/audio-control.h"
#include "player/audio-route.h"
#include "player/eq-filter.h"

#include <catch2/catch_approx.hpp>
#include <catch2/catch_test_macros.hpp>

#include <cmath>

using namespace cadence::screen::player;
using Catch::Approx;

TEST_CASE("the gain is 0 until the gate opens and a level arrives (rule 7)") {
  AudioControl a;
  CHECK(a.volume(0) == 0);
  a.setLevel(40, 0, 0);
  CHECK(a.volume(0) == 0);
  a.setGate(true);
  CHECK(a.volume(0) == Approx(40));
  a.setGate(false);
  CHECK(a.volume(0) == 0);
}

TEST_CASE("an open gate without a level stays silent") {
  AudioControl a;
  a.setGate(true);
  CHECK_FALSE(a.hasLevel());
  CHECK(a.volume(1000) == 0);
}

TEST_CASE("the first level applies at once, later ones ramp") {
  AudioControl a;
  a.setGate(true);
  a.setLevel(50, 1000, 0);
  CHECK(a.volume(0) == Approx(50));
  a.setLevel(10, 1000, 100);
  CHECK(a.ramping(600));
  CHECK(a.volume(600) == Approx(30));
  CHECK(a.volume(1100) == Approx(10));
  CHECK_FALSE(a.ramping(1100));
}

TEST_CASE("a duck lowers, never raises, and ends back at the level") {
  AudioControl a;
  a.setGate(true);
  a.setLevel(80, 0, 0);
  a.setDuck(true, 0.25f, 200, 0);
  CHECK(a.volume(100) == Approx(80 * 0.625));
  CHECK(a.volume(200) == Approx(20));
  // a duck above 1 is clamped: it keeps the level, never raises it
  a.setDuck(true, 3.0f, 0, 300);
  CHECK(a.volume(300) == Approx(80));
  a.setDuck(true, NAN, 0, 400);
  CHECK(a.volume(400) == 0);
  a.setDuck(false, 0, 0, 500);
  CHECK(a.volume(500) == Approx(80));
}

TEST_CASE("mute silences without forgetting the level") {
  AudioControl a;
  a.setGate(true);
  a.setLevel(30, 0, 0);
  a.setMute(true);
  CHECK(a.volume(0) == 0);
  a.setMute(false);
  CHECK(a.volume(0) == Approx(30));
}

TEST_CASE("routes: content on the stock ao is DIRECT with the player's controls") {
  const auto r = audioRouteFor(AoMode::Stock, cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT);
  CHECK(r.route == cadence_v2_AudioRoute_AUDIO_ROUTE_DIRECT);
  CHECK(r.playerControls);
  CHECK_FALSE(r.silent);
  CHECK(r.controls.size() == 4);
}

TEST_CASE("routes: content through ao_cadence is MIXER with every control, audiod's") {
  const auto r = audioRouteFor(AoMode::Cadence, cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_CONTENT);
  CHECK(r.route == cadence_v2_AudioRoute_AUDIO_ROUTE_MIXER);
  CHECK_FALSE(r.playerControls);
  CHECK(r.controls.size() == 6);
}

TEST_CASE("routes: ambient is NONE and silent on either ao") {
  for (auto mode : {AoMode::Stock, AoMode::Cadence}) {
    const auto r = audioRouteFor(mode, cadence_screen_v1_MediaPurpose_MEDIA_PURPOSE_AMBIENT);
    CHECK(r.route == cadence_v2_AudioRoute_AUDIO_ROUTE_NONE);
    CHECK(r.silent);
    CHECK(r.controls.empty());
  }
}

TEST_CASE("eq: bands become a labelled lavfi chain") {
  EqRequest eq;
  eq.preampDb = -3;
  eq.bands = {{cadence_screen_v1_EqType_EQ_TYPE_PEAKING, 1000, 2.5f, 1.0f},
              {cadence_screen_v1_EqType_EQ_TYPE_LOW_SHELF, 100, -4, 0},
              {cadence_screen_v1_EqType_EQ_TYPE_HIGH_PASS, 40, 0, 0}};
  const auto f = eqFilter(eq);
  REQUIRE(f);
  CHECK(*f ==
        "@cadence_eq:lavfi=[volume=volume=-3.000dB,equalizer=f=1000.000:t=q:w=1.000:g=2.500,"
        "lowshelf=f=100.000:t=q:w=0.707:g=-4.000,highpass=f=40.000:t=q:w=0.707]");
}

TEST_CASE("eq: flat and empty clear it, bad bands and unknown presets are refused") {
  CHECK(eqFilter(EqRequest{"flat", 0, {}}) == std::string());
  CHECK(eqFilter(EqRequest{}) == std::string());
  CHECK_FALSE(eqFilter(EqRequest{"rock", 0, {}}));
  CHECK_FALSE(eqFilter(EqRequest{"", 0, {{cadence_screen_v1_EqType_EQ_TYPE_PEAKING, 5, 1, 1}}}));
  CHECK_FALSE(eqFilter(EqRequest{"", 0, {{cadence_screen_v1_EqType_EQ_TYPE_PEAKING, 1000, 40, 1}}}));
  CHECK_FALSE(eqFilter(EqRequest{"", 0, {{cadence_screen_v1_EqType_EQ_TYPE_UNSPECIFIED, 1000, 1, 1}}}));
  CHECK_FALSE(eqFilter(EqRequest{"", NAN, {}}));
}
