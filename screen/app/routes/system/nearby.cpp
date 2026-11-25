#include "app/routes/system/nearby.h"

#include <nlohmann/json.hpp>

namespace cadence::screen::routes::nearby {

namespace {

using Json = nlohmann::json;

std::string str(const Json& j, const char* key) {
  const auto it = j.find(key);
  return it != j.end() && it->is_string() ? it->get<std::string>() : std::string();
}

bool flag(const Json& j, const char* key) {
  const auto it = j.find(key);
  return it != j.end() && it->is_boolean() && it->get<bool>();
}

std::optional<std::int64_t> number(const Json& j, const char* key) {
  const auto it = j.find(key);
  if (it == j.end() || !it->is_number()) return std::nullopt;
  if (it->is_number_integer()) return it->get<std::int64_t>();
  const double d = it->get<double>();
  // a ms value well past any session's (and NaN) is no value
  if (!(d > -1e15 && d < 1e15)) return std::nullopt;
  return static_cast<std::int64_t>(d);
}

}  // namespace

std::optional<std::vector<Item>> parse(std::string_view json) {
  const auto j = Json::parse(json.begin(), json.end(), nullptr, false);
  if (j.is_discarded() || !j.is_object()) return std::nullopt;
  const auto items = j.find("items");
  // no items at all is an empty list (nothing plays); a non-array is no answer
  if (items == j.end() || items->is_null()) return std::vector<Item>{};
  if (!items->is_array()) return std::nullopt;
  std::vector<Item> out;
  for (const auto& e : *items) {
    if (!e.is_object()) continue;
    Item it;
    it.id = str(e, "id");
    it.deviceId = str(e, "deviceId");
    it.name = str(e, "name");
    if (it.id.empty() || it.deviceId.empty() || it.name.empty()) continue;
    it.room = str(e, "room");
    it.sameRoom = flag(e, "sameRoom");
    it.source = str(e, "source");
    it.paused = str(e, "state") == "paused";
    it.title = str(e, "title");
    it.artist = str(e, "artist");
    it.art = str(e, "art");
    if (!it.art.starts_with("img:") || it.art.size() == 4) it.art.clear();
    it.positionMs = number(e, "positionMs");
    it.positionAtMs = number(e, "positionAtMs");
    it.durationMs = number(e, "durationMs");
    it.here = flag(e, "here");
    out.push_back(std::move(it));
  }
  return out;
}

std::vector<Item> sameRoom(const std::vector<Item>& items) {
  std::vector<Item> out;
  for (const auto& it : items) {
    if (it.sameRoom) out.push_back(it);
  }
  return out;
}

bool cardOfDevice(std::string_view cardId, std::string_view deviceId) {
  if (deviceId.empty()) return false;
  if (cardId == deviceId) return true;
  // the server's panel names a card remote-<id> (and puts the focus card first, which the caller falls back to)
  if (cardId.size() == deviceId.size() + 7 && cardId.substr(0, 7) == "remote-" && cardId.substr(7) == deviceId) return true;
  std::size_t start = 0;
  while (start <= cardId.size()) {
    std::size_t end = cardId.find_first_of(":.", start);
    if (end == std::string_view::npos) end = cardId.size();
    if (cardId.substr(start, end - start) == deviceId) return true;
    start = end + 1;
  }
  return false;
}

std::string signature(const std::vector<Item>& items) {
  std::string s;
  for (const auto& it : items) {
    // the position moves on every event: the strip draws no bar, so it is not part of what it shows
    s += it.id + '\x1f' + it.deviceId + '\x1f' + it.name + '\x1f' + it.title + '\x1f' + it.artist + '\x1f' + it.art +
         '\x1f' + (it.paused ? "p" : "r") + (it.here ? "h" : "") + (it.sameRoom ? "s" : "") + '\x1e';
  }
  return s;
}

}  // namespace cadence::screen::routes::nearby
