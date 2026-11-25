#include "app/config-json.h"
#include "app/options.h"

#include <catch2/catch_test_macros.hpp>

using namespace cadence::screen::app;

TEST_CASE("options: defaults, the env's socket, and flags") {
  std::string error;
  auto o = parseOptions({}, nullptr, &error);
  REQUIRE(o);
  CHECK(o->socket == "/run/cadence/screen.sock");
  CHECK(o->platform.vsync);

  o = parseOptions({"--driver", "offscreen", "--size", "1280x720", "--no-vsync", "--ao", "null", "--exit-after-ms", "500"},
                   "@cadence-screen", &error);
  REQUIRE(o);
  CHECK(o->socket == "@cadence-screen");
  CHECK(o->platform.videoDriver == "offscreen");
  CHECK(o->platform.width == 1280);
  CHECK(o->platform.height == 720);
  CHECK_FALSE(o->platform.vsync);
  CHECK(o->ao == "null");
  CHECK(o->exitAfterMs == 500);

  o = parseOptions({"--hwdec", "v4l2m2m", "--mpv-profile", "fast", "--max-display-fps", "60"}, nullptr, &error);
  REQUIRE(o);
  CHECK(o->hwdec == "v4l2m2m");
  CHECK(o->mpvProfile == "fast");
  CHECK(o->maxDisplayFps == 60);

  o = parseOptions({"--socket", "/tmp/s.sock"}, "@env", &error);
  REQUIRE(o);
  CHECK(o->socket == "/tmp/s.sock");
}

TEST_CASE("options: bad arguments are named") {
  std::string error;
  CHECK_FALSE(parseOptions({"--size", "big"}, nullptr, &error));
  CHECK(error == "--size");
  CHECK_FALSE(parseOptions({"--socket"}, nullptr, &error));
  CHECK(error == "--socket");
  CHECK_FALSE(parseOptions({"--frobnicate"}, nullptr, &error));
  CHECK(error == "--frobnicate");
  CHECK_FALSE(parseOptions({"--exit-after-ms", "-1"}, nullptr, &error));
  CHECK_FALSE(parseOptions({"--mpv-profile"}, nullptr, &error));
  CHECK(error == "--mpv-profile");
}

TEST_CASE("display.screen's overscan from its config json") {
  CHECK(topLevelNumber(R"({"overscan":2.5})", "overscan") == 2.5);
  CHECK(topLevelNumber(R"({"theme":"afterglow", "overscan" : 5, "x":1})", "overscan") == 5);
  CHECK_FALSE(topLevelNumber(R"({"ambient":{"overscan":5}})", "overscan"));
  CHECK_FALSE(topLevelNumber(R"({"note":"overscan","x":1})", "overscan"));
  CHECK_FALSE(topLevelNumber(R"({"overscan":"5"})", "overscan"));
  CHECK_FALSE(topLevelNumber(R"({})", "overscan"));
  CHECK(topLevelNumber(R"({"ambient":{"a":[1,{"b":2}]},"overscan":0})", "overscan") == 0);
}

TEST_CASE("cadenced's config.json: the server's url, nothing else read") {
  const char* config =
      R"({"board":"android-tv","serverUrl":"https:\/\/cadence.example\/v2","deviceId":"d-1","deviceToken":"t"})";
  CHECK(topLevelString(config, "serverUrl") == "https://cadence.example/v2");
  CHECK(topLevelString(R"({"serverUrl" : "http://10.0.0.2:8080"})", "serverUrl") == "http://10.0.0.2:8080");
  CHECK_FALSE(topLevelString(R"({"board":"android-tv"})", "serverUrl"));
  CHECK_FALSE(topLevelString(R"({"serverUrl":5})", "serverUrl"));
  CHECK_FALSE(topLevelString(R"({"x":{"serverUrl":"https://a"}})", "serverUrl"));
  CHECK_FALSE(topLevelString(R"({"serverUrl":"https://a\u0041"})", "serverUrl"));
  CHECK_FALSE(topLevelString(R"({"serverUrl":"https://a)", "serverUrl"));
}
