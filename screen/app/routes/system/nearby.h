#pragma once

#include <cstdint>
#include <optional>
#include <string>
#include <string_view>
#include <vector>

// what the household's other devices play (rpc devices.media, pkg/rpc's DevicesMedia), as home's strip and the top
// bar's indicator read it. plain c++ over nlohmann: no ui, so it is tested anywhere. names, titles and artists are
// shown, never logged (rule 8)
namespace cadence::screen::routes::nearby {

struct Item {
  // the server's id of the item (a session's or a group's); the panel opens on deviceId
  std::string id;
  std::string deviceId;
  std::string name;
  std::string room;
  bool sameRoom = false;
  std::string source;
  // playing or paused (anything else the server adds reads as playing)
  bool paused = false;
  std::string title;
  std::string artist;
  // "img:<id>" alone (the server's image proxy); any other art is dropped
  std::string art;
  std::optional<std::int64_t> positionMs;
  std::optional<std::int64_t> positionAtMs;
  std::optional<std::int64_t> durationMs;
  // this screen plays it too (it is in the item's group)
  bool here = false;
};

// the items of one answer or event ({items: [...]}): unknown fields ignored, an item without its ids or name
// skipped, a field of the wrong type read as absent; nullopt when the json is no object with an items array (the
// last list stands)
std::optional<std::vector<Item>> parse(std::string_view json);

// the items in the caller's room, in the server's order
std::vector<Item> sameRoom(const std::vector<Item>& items);

// the panel card of a device: its id is the device's, or names it as one of its ':' or '.' separated parts
// ("devices:<id>", "media.<id>.card"), so the screen focuses it whatever prefix the server gives its cards
bool cardOfDevice(std::string_view cardId, std::string_view deviceId);

// what the strip draws of a list (equal lists draw the same, so an equal event redraws nothing)
std::string signature(const std::vector<Item>& items);

}  // namespace cadence::screen::routes::nearby
