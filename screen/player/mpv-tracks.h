#pragma once

#include "player/player.h"
#include "player/video-fit.h"

#include <mpv/client.h>

#include <cstdint>
#include <optional>
#include <string>
#include <vector>

namespace cadence::screen::player {

// one entry of mpv's track-list
struct MpvTrack {
  std::int64_t id = 0;
  // video, audio, sub
  std::string type;
  std::string lang;
  std::string title;
  bool selected = false;
  bool external = false;
  bool albumart = false;
  // mpv's codec name (h264, hevc, aac, ...)
  std::string codec;
};

std::vector<MpvTrack> parseTrackList(const mpv_node& node);

// audio-params or audio-out-params: the rate, the channels and mpv's format name (floatp, s16, ...); zero and empty
// when mpv has none
struct MpvAudioParams {
  std::int64_t rate = 0;
  std::int64_t channels = 0;
  std::string format;
  bool operator==(const MpvAudioParams&) const = default;
};
MpvAudioParams parseAudioParams(const mpv_node& node);

// the audio track to select again once ao_cadence can open (one that failed before its ring came is deselected):
// an external one first (a split stream's audio), else the first; nullopt for none
std::optional<std::int64_t> audioTrackToRetry(const std::vector<MpvTrack>& tracks);
// the MediaState tracks: cover art is no video track, ids are the kind's letter and mpv's id
std::vector<Track> toTracks(const std::vector<MpvTrack>& tracks);

struct TrackRef {
  // aid, sid, vid
  const char* property = nullptr;
  // mpv's id, or "no"
  std::string value;
};
// select_track as an mpv property: an id names its kind ("a2"); an empty id turns kind off; nullopt when invalid
std::optional<TrackRef> trackSelection(const std::string& trackId, cadence_v2_MediaTrackKind kind);

// mpv's video-params as the picture's shape: dw and dh, its display size (the sample aspect applied). the rotation is
// left out: vo=mediacodec_embed never turns the picture, so the plane takes it as decoded. nullopt without a size
std::optional<VideoContent> parseVideoParams(const mpv_node& node);

// iso 639-2 (mkv's eng, rus) as bcp 47's two letters where one exists; anything else as it is, lowercased
std::string bcp47(const std::string& language);

}  // namespace cadence::screen::player
