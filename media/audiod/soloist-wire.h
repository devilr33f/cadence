#pragma once

#include "audiod/media-source.h"

#include <cstdint>
#include <optional>
#include <string>
#include <string_view>
#include <variant>

namespace cadence::audiod::soloist {

// soloist's websocket api (reference §4.2, §4.3, §8.4): the events it sends, decoded leniently into the few fields
// used, and the commands it takes. what an event carries of a track lives in memory only and is never logged (rule 8)

enum class Command : std::uint8_t { Pause, Play, SkipNext, SkipPrev };

enum class PlayState : std::uint8_t { Playing, Paused, Idle, Buffering };

// a track as soloist describes it, cut to MediaState's bounds: track_changed's event (an empty one when it has no
// item), and the item of a playback_state
struct Track {
  // title, the artists joined with ", ", album, and the first https cover within kArtworkUrlBytes; empty when absent
  MediaMetadata metadata;
  // 0 when it gave none
  std::uint64_t durationMs = 0;

  bool operator==(const Track&) const = default;
};

// auth_state
struct Auth {
  bool loggedIn = false;
  bool active = false;
  bool operator==(const Auth&) const = default;
};

// device_changed
struct Device {
  bool active = false;
  bool operator==(const Device&) const = default;
};

// playback_state or playback_changed. a playback_state is a snapshot: its item (an empty track when it has none) and
// its position; a playback_changed says nothing of either. the snapshot's own volume is never read: it is not a
// slider move
struct Status {
  PlayState state = PlayState::Idle;
  std::optional<Track> track;
  std::optional<std::uint64_t> positionMs;
  bool operator==(const Status&) const = default;
};

// volume_changed, 0-100
struct Volume {
  std::uint8_t level = 0;
  bool operator==(const Volume&) const = default;
};

// error, whose text is never read. decode leaves unsupported empty; a connection sets it to the control command the
// error answers
struct Error {
  std::optional<Command> unsupported;
  bool operator==(const Error&) const = default;
};

// command_result: the success of the command it names (pause, play, skip_next, skip_prev or set_volume; empty for
// anything else), its other fields never read. decode leaves control empty; a connection sets it to the control
// command it answers
struct Answer {
  std::string name{};
  std::optional<Command> control{};
  bool operator==(const Answer&) const = default;
};

// what set_volume is called in soloist's api, also in a command_result
inline constexpr std::string_view kSetVolumeName = "set_volume";

// position_sync
struct Position {
  std::uint64_t ms = 0;
  bool operator==(const Position&) const = default;
};

// any other type, and any text that is no event of a known type with the right field types
struct Other {
  bool operator==(const Other&) const = default;
};

using Event = std::variant<Auth, Device, Status, Volume, Error, Answer, Track, Position, Other>;

// unknown types and fields are ignored; a missing or mistyped field of a known type, invalid json or nesting over 128
// levels make it Other
Event decode(std::string_view text);

// the command's name in soloist's api, also its log token
std::string_view commandName(Command command);
// {"type":"command","command":"<name>"}
std::string commandText(Command command);
// {"type":"command","command":"set_volume","volume":<min(level, 100)>}
std::string setVolumeText(unsigned level);

}  // namespace cadence::audiod::soloist
