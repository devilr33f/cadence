#include "player/mpv-tracks.h"
#include "player/video-fit.h"

#include <catch2/catch_test_macros.hpp>

#include <vector>

using namespace cadence::screen;
using player::VideoContent;

namespace {
const render::Rect k1080{0, 0, 1920, 1080};
}

TEST_CASE("16:9 fills a 16:9 screen") {
  CHECK(player::fitRect({1920, 1080}, k1080) == k1080);
  CHECK(player::fitRect({1280, 720}, k1080) == k1080);
  CHECK(player::fitRect({3840, 2160}, k1080) == k1080);
}

TEST_CASE("an ipad's mirror is pillarboxed, never stretched") {
  // airplay_mirror_state width=1568 height=1080 on the stick's 1080p
  CHECK(player::fitRect({1568, 1080}, k1080) == render::Rect{176, 0, 1568, 1080});
  // the same ipad turned: a portrait stream
  CHECK(player::fitRect({1080, 1568}, k1080) == render::Rect{588, 0, 744, 1080});
}

TEST_CASE("a portrait phone's mirror is a centred pillar") {
  CHECK(player::fitRect({886, 1920}, k1080) == render::Rect{711, 0, 498, 1080});
}

TEST_CASE("4:3 is pillarboxed and 21:9 letterboxed") {
  CHECK(player::fitRect({640, 480}, k1080) == render::Rect{240, 0, 1440, 1080});
  CHECK(player::fitRect({2560, 1080}, k1080) == render::Rect{0, 135, 1920, 810});
  // 1.85:1 on a 4k screen
  CHECK(player::fitRect({1998, 1080}, {0, 0, 3840, 2160}) == render::Rect{0, 42, 3840, 2076});
}

TEST_CASE("the sample aspect widens or heightens the picture") {
  // pal dvd: 720x576 at 16:15 is 4:3, at 64:45 is 16:9
  CHECK(player::displaySize({720, 576, 16, 15}) == render::Size{768, 576});
  CHECK(player::fitRect({720, 576, 16, 15}, k1080) == render::Rect{240, 0, 1440, 1080});
  CHECK(player::fitRect({720, 576, 64, 45}, k1080) == k1080);
  // a sample taller than wide grows the height
  CHECK(player::displaySize({1440, 1080, 3, 4}) == render::Size{1440, 1440});
  CHECK(player::fitRect({1440, 1080, 3, 4}, k1080) == render::Rect{420, 0, 1080, 1080});
  // a missing or nonsense ratio is square
  CHECK(player::displaySize({640, 480, 0, 0}) == render::Size{640, 480});
  CHECK(player::displaySize({640, 480, -1, 2}) == render::Size{640, 480});
}

TEST_CASE("a quarter turn swaps the shape") {
  CHECK(player::displaySize({1920, 1080, 1, 1, 90}) == render::Size{1080, 1920});
  CHECK(player::displaySize({1920, 1080, 1, 1, 270}) == render::Size{1080, 1920});
  CHECK(player::displaySize({1920, 1080, 1, 1, 180}) == render::Size{1920, 1080});
  CHECK(player::displaySize({1920, 1080, 1, 1, -90}) == render::Size{1080, 1920});
  CHECK(player::fitRect({1920, 1080, 1, 1, 90}, k1080) == render::Rect{656, 0, 608, 1080});
  // the sample aspect is the picture's own, applied before the turn
  CHECK(player::displaySize({720, 576, 64, 45, 90}) == render::Size{576, 1024});
}

TEST_CASE("a shape within a percent fills, so no hairline bars") {
  // a coded 1088 without its crop
  CHECK(player::fitRect({1920, 1088}, k1080) == k1080);
  // 1.85:1 is not that close
  CHECK(player::fitRect({1998, 1080}, k1080).height == 1038);
}

TEST_CASE("the overscan's safe area is what the video fits in") {
  const render::Rect safe{96, 54, 1728, 972};
  CHECK(player::fitRect({1920, 1080}, safe) == safe);
  CHECK(player::fitRect({640, 480}, safe) == render::Rect{312, 54, 1296, 972});
}

TEST_CASE("no size fills the area") {
  CHECK(player::fitRect({}, k1080) == k1080);
  CHECK(player::fitRect({0, 1080}, k1080) == k1080);
  CHECK(player::displaySize({}) == render::Size{});
  CHECK(player::fitRect({1920, 1080}, {}) == render::Rect{});
}

TEST_CASE("the layout places the plane for what shows") {
  player::VideoLayout layout;
  std::vector<render::Rect> sent;
  layout.setSink([&](const render::Rect& r) { sent.push_back(r); });
  using S = player::VideoLayout::Source;

  // nothing before the display is known
  layout.setContent(S::Player, VideoContent{640, 480});
  CHECK(sent.empty());
  CHECK(layout.rect() == render::Rect{});

  layout.setArea({1920, 1080}, k1080);
  REQUIRE(sent.size() == 1);
  CHECK(sent.back() == render::Rect{240, 0, 1440, 1080});

  // the mirror wins while it shows; the same rect is not sent again
  layout.setContent(S::Mirror, VideoContent{1568, 1080});
  CHECK(sent.back() == render::Rect{176, 0, 1568, 1080});
  layout.setContent(S::Mirror, VideoContent{1568, 1080});
  CHECK(sent.size() == 2);
  layout.setContent(S::Player, std::nullopt);
  CHECK(sent.size() == 2);

  // the ipad turns
  layout.setContent(S::Mirror, VideoContent{1080, 1568});
  CHECK(sent.back() == render::Rect{588, 0, 744, 1080});

  // the overscan shrinks the area
  layout.setArea({1920, 1080}, {96, 54, 1728, 972});
  CHECK(sent.back() == render::Rect{625, 54, 669, 972});

  // nothing shows: the whole display again
  layout.setContent(S::Mirror, std::nullopt);
  CHECK(sent.back() == k1080);
  CHECK(sent.size() == 5);
  CHECK_FALSE(layout.content(S::Mirror));
}

TEST_CASE("a sink set late hears the rect now") {
  player::VideoLayout layout;
  layout.setArea({1920, 1080}, {});
  layout.setContent(player::VideoLayout::Source::Player, VideoContent{2560, 1080});
  std::vector<render::Rect> sent;
  layout.setSink([&](const render::Rect& r) { sent.push_back(r); });
  REQUIRE(sent.size() == 1);
  CHECK(sent.back() == render::Rect{0, 135, 1920, 810});
}

TEST_CASE("mpv's video-params give the display size") {
  char kW[] = "w", kDw[] = "dw", kDh[] = "dh", kRotate[] = "rotate";
  char* keys[] = {kW, kDw, kDh, kRotate};
  mpv_node values[4]{};
  for (auto& v : values) v.format = MPV_FORMAT_INT64;
  // anamorphic pal: 720 wide, shown 1024 x 576, turned (mediacodec_embed does not turn it, nor does the plane)
  values[0].u.int64 = 720;
  values[1].u.int64 = 1024;
  values[2].u.int64 = 576;
  values[3].u.int64 = 90;
  mpv_node_list list{4, values, keys};
  mpv_node map{};
  map.format = MPV_FORMAT_NODE_MAP;
  map.u.list = &list;
  CHECK(player::parseVideoParams(map) == VideoContent{1024, 576});

  values[2].u.int64 = 0;
  CHECK_FALSE(player::parseVideoParams(map));
  mpv_node none{};
  CHECK_FALSE(player::parseVideoParams(none));
}
