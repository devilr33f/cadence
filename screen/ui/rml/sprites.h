#pragma once

#include <cstdint>
#include <optional>
#include <string_view>
#include <vector>

namespace cadence::screen::ui {

// rgba8, premultiplied, top row first: what rmlui's GenerateTexture takes
struct Pixels {
  int width = 0;
  int height = 0;
  std::vector<std::uint8_t> rgba;
};

// the textures the ui draws itself instead of shipping images (design r2 sections 7, 10, 17 and 25):
//   gen:spinner:<0-7>        a 270 degree arc started at 45 degree steps (the ui turns frame 0 itself)
//   gen:arc:<0-7>            the long-press hold arc, an eighth more of a circle each frame
//   gen:qr:<hex payload>     a qr code, ink modules on paper with its quiet zone
//   gen:art:<seed>           the gallery's stand-in artwork (no image ever comes from the network in stream b)
//   gen:white                one white pixel
//   gen:glow, gen:glow:<r>   the focus glow around a ring of radius 24 dp (cards), or r dp (pills, circles)
std::optional<Pixels> generateSprite(std::string_view source);

// hex of a payload's bytes, as gen:qr wants it
std::string hexOf(std::string_view bytes);

}  // namespace cadence::screen::ui
