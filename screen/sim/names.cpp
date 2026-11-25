#include "sim/names.h"

namespace cadence::screen::sim {

std::string routeName(cadence_v2_ScreenRoute route) {
  switch (route) {
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_HOME:
      return "home";
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_SEARCH:
      return "search";
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_TITLE:
      return "title";
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_EPISODES:
      return "episodes";
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_PICKER:
      return "picker";
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_RESOLVING:
      return "resolving";
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_PLAYER:
      return "player";
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_AUDIO:
      return "audio";
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_PANEL:
      return "panel";
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_QUEUE:
      return "queue";
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_TASKS:
      return "tasks";
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_TASK:
      return "task";
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_SETTINGS:
      return "settings";
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_AMBIENT:
      return "ambient";
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_ENROLL:
      return "enroll";
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_AIRPLAY:
      return "airplay";
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_DEVICES:
      return "devices";
    case cadence_v2_ScreenRoute_SCREEN_ROUTE_ALARMS:
      return "alarms";
    default:
      return "unspecified";
  }
}

std::optional<cadence_v2_ScreenRoute> routeOf(const std::string& name) {
  for (int r = cadence_v2_ScreenRoute_SCREEN_ROUTE_UNSPECIFIED; r <= _cadence_v2_ScreenRoute_MAX;
       r++) {
    const auto route = static_cast<cadence_v2_ScreenRoute>(r);
    if (routeName(route) == name) return route;
  }
  return std::nullopt;
}

std::string keyName(cadence_screen_v1_Key key) {
  switch (key) {
    case cadence_screen_v1_Key_KEY_UP:
      return "UP";
    case cadence_screen_v1_Key_KEY_DOWN:
      return "DOWN";
    case cadence_screen_v1_Key_KEY_LEFT:
      return "LEFT";
    case cadence_screen_v1_Key_KEY_RIGHT:
      return "RIGHT";
    case cadence_screen_v1_Key_KEY_OK:
      return "OK";
    case cadence_screen_v1_Key_KEY_BACK:
      return "BACK";
    case cadence_screen_v1_Key_KEY_HOME:
      return "HOME";
    case cadence_screen_v1_Key_KEY_MENU:
      return "MENU";
    case cadence_screen_v1_Key_KEY_ASSISTANT:
      return "G";
    case cadence_screen_v1_Key_KEY_PLAY_PAUSE:
      return "PLAY_PAUSE";
    case cadence_screen_v1_Key_KEY_PLAY:
      return "PLAY";
    case cadence_screen_v1_Key_KEY_PAUSE:
      return "PAUSE";
    case cadence_screen_v1_Key_KEY_STOP:
      return "STOP";
    case cadence_screen_v1_Key_KEY_NEXT:
      return "NEXT";
    case cadence_screen_v1_Key_KEY_PREVIOUS:
      return "PREVIOUS";
    case cadence_screen_v1_Key_KEY_FAST_FORWARD:
      return "FAST_FORWARD";
    case cadence_screen_v1_Key_KEY_REWIND:
      return "REWIND";
    case cadence_screen_v1_Key_KEY_VOLUME_UP:
      return "VOLUME_UP";
    case cadence_screen_v1_Key_KEY_VOLUME_DOWN:
      return "VOLUME_DOWN";
    case cadence_screen_v1_Key_KEY_VOLUME_MUTE:
      return "VOLUME_MUTE";
    case cadence_screen_v1_Key_KEY_POWER:
      return "POWER";
    default:
      return "";
  }
}

std::optional<cadence_screen_v1_Key> keyOf(const std::string& name) {
  for (int k = cadence_screen_v1_Key_KEY_UNSPECIFIED; k <= cadence_screen_v1_Key_KEY_POWER; k++) {
    const auto key = static_cast<cadence_screen_v1_Key>(k);
    if (keyName(key) == name) return key;
  }
  return std::nullopt;
}

}  // namespace cadence::screen::sim
