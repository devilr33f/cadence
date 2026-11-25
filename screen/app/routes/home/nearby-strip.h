#pragma once

#include "app/routes/system/nearby.h"

#include <string>
#include <vector>

namespace cadence::screen::routes::home_nearby {

// at most this many items on the strip; the quick panel shows them all
inline constexpr std::size_t kMaxItems = 4;

// home's strip of what plays in this room on other devices (devices.media's sameRoom items): a label and a row of
// compact cover-and-type entries in the now-playing chip's shape, each opening the quick panel on its device; ""
// with none
std::string strip(const std::vector<nearby::Item>& sameRoom);

}  // namespace cadence::screen::routes::home_nearby
