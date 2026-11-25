#include "audiod/soloist-wire.h"

#include "text.h"

#include <nlohmann/json.hpp>

#include <algorithm>
#include <cmath>
#include <initializer_list>

namespace cadence::audiod::soloist {

namespace {

using Json = nlohmann::json;

// serde_json's limit, which v1 decoded with: soloist's own events nest seven levels deep. deeper text is skipped as
// it is parsed, so it never takes memory
constexpr int kMaxDepth = 128;
// v1's bound on a length or a position: 2^31 - 1 seconds
constexpr std::uint64_t kMaxMs = 2'147'483'647'000;
constexpr unsigned kMaxLevel = 100;

// the value at path through nested objects; null when a step is missing or not an object
const Json* at(const Json& value, std::initializer_list<std::string_view> path) {
  const Json* current = &value;
  for (const auto key : path) {
    if (!current->is_object())
      return nullptr;
    const auto found = current->find(key);
    if (found == current->end())
      return nullptr;
    current = &*found;
  }
  return current;
}

const std::string* textOf(const Json* value) {
  return value != nullptr && value->is_string() ? value->get_ptr<const std::string*>() : nullptr;
}

std::optional<bool> boolean(const Json& frame, std::string_view key) {
  const auto* value = at(frame, {key});
  if (value == nullptr || !value->is_boolean())
    return std::nullopt;
  return value->get<bool>();
}

std::optional<std::uint64_t> milliseconds(const Json* value) {
  if (value == nullptr)
    return std::nullopt;
  if (value->is_number_unsigned()) {
    const auto ms = value->get<std::uint64_t>();
    return ms <= kMaxMs ? std::optional(ms) : std::nullopt;
  }
  if (value->is_number_float()) {
    const auto ms = value->get<double>();
    if (!std::isfinite(ms) || ms < 0 || ms > static_cast<double>(kMaxMs))
      return std::nullopt;
    return static_cast<std::uint64_t>(ms);
  }
  return std::nullopt;
}

std::string bounded(std::string_view text) { return text::boundedUtf8(text, kMetadataTextBytes); }

bool hex(char c) { return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F'); }

// v1's check of an image url, within MediaState's bound: https with a host, printable ascii a uri allows, each % with
// two hex digits. one over the bound is not cut into a broken url
bool imageUrl(std::string_view url) {
  constexpr std::string_view kScheme = "https://";
  if (url.size() > kArtworkUrlBytes || !url.starts_with(kScheme) || url.size() == kScheme.size())
    return false;
  const char host = url[kScheme.size()];
  if (host == '/' || host == '?' || host == '#')
    return false;
  for (std::size_t i = kScheme.size(); i < url.size();) {
    const char c = url[i];
    if (c == '%') {
      if (i + 2 >= url.size() || !hex(url[i + 1]) || !hex(url[i + 2]))
        return false;
      i += 3;
    } else if (c > ' ' && c <= '~' && c != '"' && c != '\\') {
      ++i;
    } else {
      return false;
    }
  }
  return true;
}

std::string artists(const Json& item) {
  const auto* creators = at(item, {"decorations", "creators"});
  if (creators == nullptr || !creators->is_array())
    return {};
  std::string joined;
  bool found = false;
  for (const auto& creator : *creators) {
    const auto* name = textOf(at(creator, {"entity", "decorations", "identity", "name"}));
    if (name == nullptr)
      continue;
    if (found)
      joined += ", ";
    joined += *name;
    found = true;
    if (joined.size() > kMetadataTextBytes)
      break;
  }
  return bounded(joined);
}

std::string artwork(const Json& item) {
  const auto* covers = at(item, {"decorations", "visual_identity", "cover"});
  if (covers == nullptr || !covers->is_array())
    return {};
  for (const auto& cover : *covers)
    if (const auto* url = textOf(at(cover, {"url"})); url != nullptr && imageUrl(*url))
      return *url;
  return {};
}

// an item that is no object is no track: an empty one
Track track(const Json* item) {
  Track out;
  if (item == nullptr || !item->is_object())
    return out;
  if (const auto* title = textOf(at(*item, {"decorations", "identity", "name"})))
    out.metadata.title = bounded(*title);
  out.metadata.artist = artists(*item);
  if (const auto* album = textOf(at(*item, {"decorations", "parent", "entity", "decorations", "identity", "name"})))
    out.metadata.album = bounded(*album);
  out.metadata.artworkUrl = artwork(*item);
  out.durationMs = milliseconds(at(*item, {"decorations", "playback", "duration_ms"})).value_or(0);
  return out;
}

std::optional<PlayState> playState(const Json& frame) {
  const auto* status = textOf(at(frame, {"status"}));
  if (status == nullptr)
    return std::nullopt;
  if (*status == "playing")
    return PlayState::Playing;
  if (*status == "paused")
    return PlayState::Paused;
  if (*status == "idle")
    return PlayState::Idle;
  if (*status == "buffering")
    return PlayState::Buffering;
  return std::nullopt;
}

// discarded when it is no json, or nests deeper than kMaxDepth
Json parse(std::string_view text) {
  bool tooDeep = false;
  auto parsed = Json::parse(
      text,
      [&tooDeep](int depth, Json::parse_event_t event, Json&) {
        if ((event == Json::parse_event_t::object_start || event == Json::parse_event_t::array_start) &&
            depth >= kMaxDepth)
          tooDeep = true;
        return !tooDeep;
      },
      false);
  return tooDeep ? Json(Json::value_t::discarded) : parsed;
}

}  // namespace

Event decode(std::string_view text) {
  const auto frame = parse(text);
  if (!frame.is_object())
    return Other{};
  const auto* type = textOf(at(frame, {"type"}));
  if (type == nullptr)
    return Other{};
  if (*type == "auth_state") {
    const auto loggedIn = boolean(frame, "logged_in");
    const auto active = boolean(frame, "is_active");
    if (!loggedIn || !active)
      return Other{};
    return Auth{.loggedIn = *loggedIn, .active = *active};
  }
  if (*type == "device_changed") {
    const auto active = boolean(frame, "is_active");
    if (!active)
      return Other{};
    return Device{.active = *active};
  }
  if (*type == "playback_state" || *type == "playback_changed") {
    const auto state = playState(frame);
    if (!state)
      return Other{};
    if (*type == "playback_changed")
      return Status{.state = *state, .track = std::nullopt, .positionMs = std::nullopt};
    return Status{.state = *state,
                  .track = track(at(frame, {"item"})),
                  .positionMs = milliseconds(at(frame, {"position", "position_ms"}))};
  }
  if (*type == "volume_changed") {
    const auto* volume = at(frame, {"volume"});
    if (volume == nullptr || !volume->is_number_unsigned() || volume->get<std::uint64_t>() > kMaxLevel)
      return Other{};
    return Volume{.level = static_cast<std::uint8_t>(volume->get<std::uint64_t>())};
  }
  if (*type == "error")
    return Error{};
  if (*type == "command_result") {
    const auto* name = textOf(at(frame, {"command"}));
    if (name == nullptr)
      return Answer{};
    for (const auto command : {Command::Pause, Command::Play, Command::SkipNext, Command::SkipPrev})
      if (*name == commandName(command))
        return Answer{.name = *name, .control = std::nullopt};
    return *name == kSetVolumeName ? Answer{.name = *name, .control = std::nullopt} : Answer{};
  }
  if (*type == "track_changed")
    return track(at(frame, {"item"}));
  if (*type == "position_sync") {
    const auto ms = milliseconds(at(frame, {"position", "position_ms"}));
    if (!ms)
      return Other{};
    return Position{.ms = *ms};
  }
  return Other{};
}

std::string_view commandName(Command command) {
  switch (command) {
    case Command::Pause:
      return "pause";
    case Command::Play:
      return "play";
    case Command::SkipNext:
      return "skip_next";
    case Command::SkipPrev:
      return "skip_prev";
  }
  return "pause";
}

std::string commandText(Command command) {
  std::string text = R"({"type":"command","command":")";
  text += commandName(command);
  text += "\"}";
  return text;
}

std::string setVolumeText(unsigned level) {
  return R"({"type":"command","command":")" + std::string(kSetVolumeName) + R"(","volume":)" +
         std::to_string(std::min(level, kMaxLevel)) + "}";
}

}  // namespace cadence::audiod::soloist
