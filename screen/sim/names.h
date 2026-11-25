#pragma once

#include "cadence/screen/v1/ui.pb.h"
#include "cadence/v2/screen.pb.h"

#include <optional>
#include <string>

namespace cadence::screen::sim {

// the names a script uses, the wire's tokens in lower case without their prefix (home, player, ...)
std::string routeName(cadence_v2_ScreenRoute route);
std::optional<cadence_v2_ScreenRoute> routeOf(const std::string& name);

// the remote's keys as a script presses them (UP, DOWN, ..., G for the assistant)
std::string keyName(cadence_screen_v1_Key key);
std::optional<cadence_screen_v1_Key> keyOf(const std::string& name);

}  // namespace cadence::screen::sim
