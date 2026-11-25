#include "audiod/soloist-wire.h"

#include <catch2/catch_test_macros.hpp>

#include <string>
#include <variant>

using namespace cadence::audiod;
using namespace cadence::audiod::soloist;

namespace {

// the examples of soloist's websocket reference, as v1's wire.rs tested them
constexpr std::string_view kAuth =
    R"({"type":"auth_state","logged_in":true,"is_active":true,"device_name":"Kitchen speaker"})";
constexpr std::string_view kPlaybackState = R"({
    "type": "playback_state",
    "status": "playing",
    "item": {
        "uri": "spotify:track:2JRo0gjbX4GrCqBYdRohoo",
        "entity_type": "track",
        "decorations": {
            "identity": { "name": "My Song" },
            "visual_identity": { "cover": [ { "url": "https://i.scdn.co/image/ab67616d00001e02...", "size": "large" } ] },
            "parent": { "entity": { "uri": "spotify:album:4aawyAB9vmqN3uQ7FjRGTy", "entity_type": "album", "decorations": { "identity": { "name": "Album Name" } } } },
            "creators": [ { "entity": { "uri": "spotify:artist:...", "decorations": { "identity": { "name": "Artist Name" } } } } ],
            "playback": { "duration_ms": 210000, "content_ratings": [] }
        }
    },
    "context": {
        "uri": "spotify:playlist:37i9dQZF1DXcBWIGoYBM5M",
        "entity_type": "playlist",
        "decorations": { "identity": { "name": "Today's Top Hits" } }
    },
    "position": { "position_ms": 45000, "timestamp_ms": 1747654321000, "speed": 1.0 },
    "volume": 65,
    "is_active": true,
    "options": { "shuffle": false, "repeat": "off", "playback_speed": 1.0, "modes": { "context_enhancement": "NONE" } },
    "available_actions": { "pause": {}, "skip_next": {}, "seek_forward": { "step_ms": 15000 }, "seek_backward": { "step_ms": 15000 } }
})";

Track documentedTrack() {
  return Track{.metadata = {.title = "My Song",
                            .artist = "Artist Name",
                            .album = "Album Name",
                            .artworkUrl = "https://i.scdn.co/image/ab67616d00001e02...",
                            .artworkId = {},
                            .sender = {}},
               .durationMs = 210000};
}

bool isOther(std::string_view text) { return std::holds_alternative<Other>(decode(text)); }

// a playback_state whose item and position are the given json
std::string playbackWith(const std::string& item, const std::string& position = "null") {
  return R"({"type":"playback_state","status":"playing","item":)" + item + R"(,"position":)" + position + "}";
}

Track trackOf(const std::string& item) {
  const auto event = decode(playbackWith(item));
  const auto* status = std::get_if<Status>(&event);
  REQUIRE(status != nullptr);
  REQUIRE(status->track.has_value());
  return *status->track;
}

}  // namespace

TEST_CASE("EachDocumentedEventDecodes") {
  CHECK(decode(kAuth) == Event{Auth{.loggedIn = true, .active = true}});
  CHECK(decode(R"({"type":"auth_state","logged_in":false,"is_active":false,"device_name":"x"})") ==
        Event{Auth{.loggedIn = false, .active = false}});
  // the snapshot's volume of 65 is not a slider move: it is not read
  CHECK(decode(kPlaybackState) ==
        Event{Status{.state = PlayState::Playing, .track = documentedTrack(), .positionMs = 45000}});
  CHECK(decode(R"({ "type": "playback_changed", "status": "paused" })") ==
        Event{Status{.state = PlayState::Paused, .track = std::nullopt, .positionMs = std::nullopt}});
  for (const auto& [name, state] : {std::pair{"playing", PlayState::Playing}, std::pair{"paused", PlayState::Paused},
                                    std::pair{"idle", PlayState::Idle}, std::pair{"buffering", PlayState::Buffering}}) {
    const auto event = decode(std::string(R"({"type":"playback_changed","status":")") + name + "\"}");
    CHECK(event == Event{Status{.state = state, .track = std::nullopt, .positionMs = std::nullopt}});
  }
  CHECK(decode(R"({ "type": "volume_changed", "volume": 42 })") == Event{Volume{.level = 42}});
  CHECK(decode(R"({ "type": "device_changed", "is_active": true, "device_name": "Kitchen speaker" })") ==
        Event{Device{.active = true}});
  CHECK(decode(R"({ "type": "error", "message": "command requires authentication" })") == Event{Error{}});
  // soloist answers each command in order: a command_result is the latest one's success
  CHECK(decode(R"({ "type": "command_result", "command": "pause" })") == Event{Answer{.name = "pause"}});
  CHECK(decode(R"({ "type": "command_result", "command": "set_volume" })") == Event{Answer{.name = "set_volume"}});
  CHECK(decode(R"({ "type": "command_result" })") == Event{Answer{}});
  CHECK(decode(R"({ "type": "command_result", "command": 7 })") == Event{Answer{}});
  // a name no command has is none
  CHECK(decode(R"({ "type": "command_result", "command": ")" + std::string(40, 'x') + R"(" })") == Event{Answer{}});
  CHECK(decode(R"({"type":"track_changed","item":{"decorations":{"identity":{"name":"Replacement"}}}})") ==
        Event{Track{.metadata = {.title = "Replacement", .artist = {}, .album = {}, .artworkUrl = {}, .artworkId = {}, .sender = {}}, .durationMs = 0}});
  CHECK(decode(R"({"type":"position_sync","position":{"position_ms":46000,"timestamp_ms":1747654321000,"speed":1.0}})") ==
        Event{Position{.ms = 46000}});
  for (const auto ignored : {
           R"({"type":"context_changed","context":{"uri":"spotify:playlist:37i9dQZF1DXcBWIGoYBM5M","entity_type":"playlist"}})",
           R"({"type":"options_changed","options":{"shuffle":true,"repeat":"off","playback_speed":1.0}})",
           R"({"type":"queue_changed","previous":[],"upcoming":[{"uid":"spotify:track:upcoming","source":"context","item":{"uri":"spotify:track:upcoming","entity_type":"track","decorations":{"identity":{"name":"Next Song"}}}}]})",
       }) {
    INFO(ignored);
    CHECK(isOther(ignored));
  }
}

TEST_CASE("APlaybackStateWithoutAnItemSaysThereIsNone") {
  // unlike playback_changed, a snapshot without an item clears what was shown
  for (const auto& item : {std::string("null"), std::string("[]"), std::string("\"x\"")}) {
    INFO(item);
    CHECK(trackOf(item) == Track{});
  }
  const auto event = decode(R"({"type":"playback_state","status":"idle"})");
  CHECK(event == Event{Status{.state = PlayState::Idle, .track = Track{}, .positionMs = std::nullopt}});
  // a track_changed without one too
  CHECK(decode(R"({"type":"track_changed"})") == Event{Track{}});
  CHECK(decode(R"({"type":"track_changed","item":7})") == Event{Track{}});
}

TEST_CASE("MetadataIsBoundedAndPicksTheFirstHttpsCover") {
  std::string longTitle;
  for (int i = 0; i < 100; ++i)
    longTitle += "\xF0\x9F\x8E\xB5";
  const auto track = trackOf(R"({"decorations":{
      "identity":{"name":")" + longTitle + R"("},
      "creators":[
        {"entity":{"decorations":{"identity":{"name":"First"}}}},
        {"entity":{"decorations":{"identity":{"name":7}}}},
        {"entity":{}},
        {"entity":{"decorations":{"identity":{"name":"Second"}}}}],
      "visual_identity":{"cover":[
        {"url":"http://insecure.invalid/art"},
        {"url":7},
        {"url":"https:///no-host"},
        {"url":"https://spaced out.invalid/art"},
        {"url":"https://images.invalid/art.png"},
        {"url":"https://images.invalid/second.png"}]},
      "playback":{"duration_ms":9007199254740991}}})");
  // cut on a character boundary within the bound of MediaState's text
  CHECK(track.metadata.title.size() <= kMetadataTextBytes);
  CHECK(track.metadata.title.size() % 4 == 0);
  CHECK(longTitle.starts_with(track.metadata.title));
  CHECK(track.metadata.artist == "First, Second");
  CHECK(track.metadata.artworkUrl == "https://images.invalid/art.png");
  CHECK(track.durationMs == 0);
}

TEST_CASE("ManyArtistsAreJoinedWithinTheBound") {
  std::string creators;
  for (int i = 0; i < 100; ++i)
    creators += std::string(i == 0 ? "" : ",") + R"({"entity":{"decorations":{"identity":{"name":"Artist )" +
                std::to_string(i) + R"("}}}})";
  const auto track = trackOf(R"({"decorations":{"creators":[)" + creators + "]}}");
  CHECK(track.metadata.artist.size() <= kMetadataTextBytes);
  CHECK(track.metadata.artist.starts_with("Artist 0, Artist 1, Artist 2"));
}

TEST_CASE("ArtworkAndMillisecondsUseTheirExactBounds") {
  const std::string prefix = "https://images.invalid/";
  const std::string longest = prefix + std::string(kArtworkUrlBytes - prefix.size(), 'a');
  const std::string oversized = longest + "a";
  const auto exact = trackOf(R"({"decorations":{"visual_identity":{"cover":[{"url":")" + oversized + R"("},{"url":")" +
                             longest + R"("}]},"playback":{"duration_ms":2147483647000}}})");
  // a cover over the bound is skipped, never cut into a broken url
  CHECK(exact.metadata.artworkUrl == longest);
  CHECK(exact.durationMs == 2147483647000);
  CHECK(trackOf(R"({"decorations":{"playback":{"duration_ms":2147483647001}}})").durationMs == 0);
  CHECK(trackOf(R"({"decorations":{"playback":{"duration_ms":-1}}})").durationMs == 0);
  CHECK(trackOf(R"({"decorations":{"playback":{"duration_ms":"210000"}}})").durationMs == 0);
  CHECK(trackOf(R"({"decorations":{"playback":{"duration_ms":210000.9}}})").durationMs == 210000);

  const auto positionOf = [](const std::string& position) {
    const auto event = decode(playbackWith("null", position));
    return std::get<Status>(event).positionMs;
  };
  CHECK(positionOf(R"({"position_ms":2147483647000})") == 2147483647000);
  CHECK(positionOf(R"({"position_ms":0})") == 0);
  CHECK_FALSE(positionOf(R"({"position_ms":2147483647001})").has_value());
  CHECK_FALSE(positionOf(R"({"position_ms":-1})").has_value());
  CHECK_FALSE(positionOf(R"({"position_ms":null})").has_value());
  CHECK_FALSE(positionOf("45000").has_value());
  CHECK(isOther(R"({"type":"position_sync","position":{"position_ms":-1}})"));
  CHECK(isOther(R"({"type":"position_sync","position":{}})"));
  CHECK(isOther(R"({"type":"position_sync"})"));
}

TEST_CASE("UnknownFieldsAreIgnoredAndWrongTypesAreOther") {
  CHECK(decode(R"({"type":"volume_changed","volume":42,"new_field":{"a":[1,2]}})") == Event{Volume{.level = 42}});
  CHECK(decode(R"({"type":"error","message":"x","code":7})") == Event{Error{}});
  CHECK(decode(R"({"type":"error"})") == Event{Error{}});
  CHECK(decode(R"({"type":"playback_changed","status":"paused","extra":null})") ==
        Event{Status{.state = PlayState::Paused, .track = std::nullopt, .positionMs = std::nullopt}});
  for (const auto other : {
           R"({"type":"something_new","volume":42})",
           R"({"type":7})",
           R"({"volume":42})",
           R"({"type":"auth_state","is_active":true})",
           R"({"type":"auth_state","logged_in":"yes","is_active":true})",
           R"({"type":"auth_state","logged_in":1,"is_active":true})",
           R"({"type":"playback_changed","status":"stopped"})",
           R"({"type":"playback_changed","status":"PLAYING"})",
           R"({"type":"playback_changed"})",
           R"({"type":"playback_state","item":{"decorations":{"identity":{"name":"x"}}}})",
           R"({"type":"device_changed"})",
           R"({"type":"device_changed","is_active":null})",
           "{}",
           "[]",
           "null",
           R"("volume_changed")",
           "not json",
           "",
           R"({"type":"volume_changed","volume":42)",
           R"({"type":"volume_changed","volume":42} trailing)",
           "{\"type\":\"error\",\"message\":\"\xFF\"}",
       }) {
    INFO(other);
    CHECK(isOther(other));
  }
}

TEST_CASE("AVolumeOver100IsOther") {
  CHECK(decode(R"({"type":"volume_changed","volume":100})") == Event{Volume{.level = 100}});
  CHECK(decode(R"({"type":"volume_changed","volume":0})") == Event{Volume{.level = 0}});
  for (const auto bad : {"101", "255", "256", "300", "-1", "42.5", "42.0", "\"42\"", "null", "true"}) {
    INFO(bad);
    CHECK(isOther(std::string(R"({"type":"volume_changed","volume":)") + bad + "}"));
  }
  CHECK(isOther(R"({"type":"volume_changed"})"));
}

TEST_CASE("HostileTextDecodesToOther") {
  const std::string deep = std::string(10000, '[') + std::string(10000, ']');
  const std::string widest = std::string(500000, '[') + std::string(500000, ']');
  for (const auto& hostile : {
           deep,
           widest,
           R"({"type":"volume_changed","volume":1,"x":)" + deep + "}",
           R"({"type":)" + deep + "}",
           R"({"type":")" + std::string(1 << 20, 'a') + "\"}",
           std::string(1 << 20, 'x'),
           std::string(R"({"type":"volume_changed","volume":1e400})"),
           std::string(R"({"type":"volume_changed","volume":-1e400})"),
           std::string(R"({"type":"volume_changed","volume":99999999999999999999999999})"),
           std::string("[1,2,3]"),
           std::string("42"),
           std::string("-1.5e3"),
       }) {
    INFO(hostile.substr(0, 40));
    CHECK(isOther(hostile));
  }
}

TEST_CASE("NestingUpToTheBoundIsRead") {
  // well past soloist's own nesting, which reaches seven levels in an item
  std::string nested = "1";
  for (int i = 0; i < 100; ++i)
    nested = "[" + nested + "]";
  CHECK(decode(R"({"type":"volume_changed","volume":1,"x":)" + nested + "}") == Event{Volume{.level = 1}});
}

TEST_CASE("CommandsAreExactJson") {
  CHECK(commandText(Command::Pause) == R"({"type":"command","command":"pause"})");
  CHECK(commandText(Command::Play) == R"({"type":"command","command":"play"})");
  CHECK(commandText(Command::SkipNext) == R"({"type":"command","command":"skip_next"})");
  CHECK(commandText(Command::SkipPrev) == R"({"type":"command","command":"skip_prev"})");
  CHECK(setVolumeText(50) == R"({"type":"command","command":"set_volume","volume":50})");
  CHECK(setVolumeText(0) == R"({"type":"command","command":"set_volume","volume":0})");
  CHECK(setVolumeText(100) == R"({"type":"command","command":"set_volume","volume":100})");
  CHECK(setVolumeText(250) == R"({"type":"command","command":"set_volume","volume":100})");
  CHECK(commandName(Command::Pause) == "pause");
  CHECK(commandName(Command::SkipPrev) == "skip_prev");
}
