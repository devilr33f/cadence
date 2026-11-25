#include "platform/keys.h"

#include <catch2/catch_test_macros.hpp>

#include <SDL3/SDL_keycode.h>

using namespace cadence::screen::platform;

TEST_CASE("the remote's keys map to screen/v1 keys") {
  CHECK(keyOf(SDLK_UP) == cadence_screen_v1_Key_KEY_UP);
  CHECK(keyOf(SDLK_RETURN) == cadence_screen_v1_Key_KEY_OK);
  CHECK(keyOf(SDLK_AC_BACK) == cadence_screen_v1_Key_KEY_BACK);
  CHECK(keyOf(SDLK_ESCAPE) == cadence_screen_v1_Key_KEY_BACK);
  CHECK(keyOf(SDLK_G) == cadence_screen_v1_Key_KEY_ASSISTANT);
  CHECK(keyOf(SDLK_VOLUMEUP) == cadence_screen_v1_Key_KEY_VOLUME_UP);
  CHECK_FALSE(keyOf(SDLK_Q));
}

TEST_CASE("a key goes down, repeats, turns long and comes up") {
  KeyTracker t(500);
  auto d = t.down(cadence_screen_v1_Key_KEY_RIGHT, false, 0);
  REQUIRE(d);
  CHECK(d->action == cadence_screen_v1_KeyAction_KEY_ACTION_DOWN);
  auto r = t.down(cadence_screen_v1_Key_KEY_RIGHT, true, 100);
  REQUIRE(r);
  CHECK(r->action == cadence_screen_v1_KeyAction_KEY_ACTION_REPEAT);
  CHECK(t.nextDueMs(100) == 400);
  CHECK_FALSE(t.poll(499));
  auto l = t.poll(500);
  REQUIRE(l);
  CHECK(l->action == cadence_screen_v1_KeyAction_KEY_ACTION_LONG);
  CHECK_FALSE(t.poll(600));
  auto u = t.up(cadence_screen_v1_Key_KEY_RIGHT, 700);
  REQUIRE(u);
  CHECK(u->action == cadence_screen_v1_KeyAction_KEY_ACTION_UP);
  CHECK(t.nextDueMs(700) == -1);
}

TEST_CASE("a short back is a back press at its up") {
  KeyTracker t(500);
  CHECK_FALSE(t.down(cadence_screen_v1_Key_KEY_BACK, false, 0));
  auto u = t.up(cadence_screen_v1_Key_KEY_BACK, 200);
  REQUIRE(u);
  CHECK(u->key == cadence_screen_v1_Key_KEY_BACK);
  CHECK(u->action == cadence_screen_v1_KeyAction_KEY_ACTION_PRESS);
}

TEST_CASE("a long back is the assistant, and its up is swallowed") {
  KeyTracker t(500);
  CHECK_FALSE(t.down(cadence_screen_v1_Key_KEY_BACK, false, 0));
  CHECK_FALSE(t.down(cadence_screen_v1_Key_KEY_BACK, true, 300));
  auto l = t.poll(500);
  REQUIRE(l);
  CHECK(l->key == cadence_screen_v1_Key_KEY_ASSISTANT);
  CHECK(l->action == cadence_screen_v1_KeyAction_KEY_ACTION_LONG);
  CHECK_FALSE(t.up(cadence_screen_v1_Key_KEY_BACK, 900));
}

TEST_CASE("a back held past 250 ms says so once before it turns into the assistant") {
  KeyTracker t(500);
  CHECK_FALSE(t.down(cadence_screen_v1_Key_KEY_BACK, false, 0));
  CHECK(t.nextDueMs(0) == 250);
  CHECK_FALSE(t.poll(200));
  auto h = t.poll(260);
  REQUIRE(h);
  CHECK(h->key == cadence_screen_v1_Key_KEY_BACK);
  CHECK(h->action == cadence_screen_v1_KeyAction_KEY_ACTION_DOWN);
  CHECK_FALSE(t.poll(300));
  CHECK(t.nextDueMs(300) == 200);
  auto l = t.poll(500);
  REQUIRE(l);
  CHECK(l->key == cadence_screen_v1_Key_KEY_ASSISTANT);
}

TEST_CASE("a long back released before the poll is still the assistant") {
  KeyTracker t(500);
  t.down(cadence_screen_v1_Key_KEY_BACK, false, 0);
  auto u = t.up(cadence_screen_v1_Key_KEY_BACK, 800);
  REQUIRE(u);
  CHECK(u->key == cadence_screen_v1_Key_KEY_ASSISTANT);
}
