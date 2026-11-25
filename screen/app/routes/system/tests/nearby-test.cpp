#include "app/routes/system/nearby.h"

#include <catch2/catch_test_macros.hpp>

using namespace cadence::screen::routes;

TEST_CASE("nearby: an answer's items in the server's order, every field read") {
  const auto items = nearby::parse(R"({"items": [
    {"id": "s-1", "deviceId": "kitchen", "name": "Кухня", "room": "Кухня", "sameRoom": true, "source": "url",
     "state": "playing", "title": "Северное сияние", "artist": "Полярные огни", "art": "img:abc123",
     "positionMs": 61000, "positionAtMs": 1794652800000, "durationMs": 215000.0, "here": false},
    {"id": "g-1", "deviceId": "pickle", "name": "Гостиная и кухня", "sameRoom": false, "source": "spotify",
     "state": "paused", "here": true}
  ]})");
  REQUIRE(items);
  REQUIRE(items->size() == 2);
  const auto& a = (*items)[0];
  CHECK(a.id == "s-1");
  CHECK(a.deviceId == "kitchen");
  CHECK(a.name == "Кухня");
  CHECK(a.room == "Кухня");
  CHECK(a.sameRoom);
  CHECK(a.source == "url");
  CHECK_FALSE(a.paused);
  CHECK(a.title == "Северное сияние");
  CHECK(a.artist == "Полярные огни");
  CHECK(a.art == "img:abc123");
  CHECK(a.positionMs == 61000);
  CHECK(a.positionAtMs == 1794652800000);
  CHECK(a.durationMs == 215000);
  CHECK_FALSE(a.here);
  const auto& b = (*items)[1];
  CHECK(b.paused);
  CHECK(b.here);
  CHECK_FALSE(b.sameRoom);
  CHECK(b.title.empty());
  CHECK_FALSE(b.positionMs);
}

TEST_CASE("nearby: unknown fields are ignored, bad items skipped, wrong types read as absent") {
  const auto items = nearby::parse(R"({"version": 3, "items": [
    "not an item", 42, null,
    {"deviceId": "no-id", "name": "x"},
    {"id": "no-device", "name": "x"},
    {"id": "no-name", "deviceId": "d"},
    {"id": 7, "deviceId": "d", "name": "numeric id"},
    {"id": "ok", "deviceId": "d", "name": "Спальня", "future": {"nested": true}, "sameRoom": "yes", "here": 1,
     "title": 5, "positionMs": "10", "durationMs": 1e300, "art": "file:///etc/passwd"},
    {"id": "art", "deviceId": "e", "name": "Кабинет", "art": "img:"}
  ]})");
  REQUIRE(items);
  REQUIRE(items->size() == 2);
  const auto& ok = (*items)[0];
  CHECK(ok.id == "ok");
  CHECK_FALSE(ok.sameRoom);
  CHECK_FALSE(ok.here);
  CHECK(ok.title.empty());
  CHECK_FALSE(ok.positionMs);
  CHECK_FALSE(ok.durationMs);
  // art that is not the server's image proxy is never drawn
  CHECK(ok.art.empty());
  CHECK((*items)[1].art.empty());
}

TEST_CASE("nearby: no items is an empty list; a broken answer is none") {
  CHECK(nearby::parse(R"({"items": []})")->empty());
  CHECK(nearby::parse(R"({})")->empty());
  CHECK(nearby::parse(R"({"items": null})")->empty());
  CHECK_FALSE(nearby::parse(R"({"items": {}})"));
  CHECK_FALSE(nearby::parse(R"([])"));
  CHECK_FALSE(nearby::parse("not json"));
  CHECK_FALSE(nearby::parse(""));
}

TEST_CASE("nearby: the strip's own room, and a signature that ignores the moving position") {
  auto items = *nearby::parse(R"({"items": [
    {"id": "a", "deviceId": "a", "name": "A", "sameRoom": true, "positionMs": 1000},
    {"id": "b", "deviceId": "b", "name": "B", "sameRoom": false},
    {"id": "c", "deviceId": "c", "name": "C", "sameRoom": true}
  ]})");
  const auto room = nearby::sameRoom(items);
  REQUIRE(room.size() == 2);
  CHECK(room[0].id == "a");
  CHECK(room[1].id == "c");
  const auto sig = nearby::signature(items);
  items[0].positionMs = 5000;
  CHECK(nearby::signature(items) == sig);
  items[0].paused = true;
  CHECK(nearby::signature(items) != sig);
}

TEST_CASE("nearby: a panel card names its device by its whole id or one of its parts") {
  CHECK(nearby::cardOfDevice("kitchen-1", "kitchen-1"));
  CHECK(nearby::cardOfDevice("devices:kitchen-1", "kitchen-1"));
  CHECK(nearby::cardOfDevice("media.kitchen-1.card", "kitchen-1"));
  CHECK(nearby::cardOfDevice("remote-0b1c2d3e-aaaa-bbbb-cccc-0123456789ab", "0b1c2d3e-aaaa-bbbb-cccc-0123456789ab"));
  CHECK_FALSE(nearby::cardOfDevice("remote-0b1c2d3e-aaaa", "0b1c2d3e"));
  CHECK_FALSE(nearby::cardOfDevice("devices:kitchen-10", "kitchen-1"));
  CHECK_FALSE(nearby::cardOfDevice("devices:kitchen", "kitchen-1"));
  CHECK_FALSE(nearby::cardOfDevice("devices:kitchen-1", ""));
}
