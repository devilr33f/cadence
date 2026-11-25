#include "player/mpv-tracks.h"

#include <cctype>
#include <cstring>

namespace cadence::screen::player {

namespace {

const mpv_node* find(const mpv_node& map, const char* key) {
  if (map.format != MPV_FORMAT_NODE_MAP || map.u.list == nullptr) return nullptr;
  for (int i = 0; i < map.u.list->num; i++) {
    if (std::strcmp(map.u.list->keys[i], key) == 0) return &map.u.list->values[i];
  }
  return nullptr;
}

std::string str(const mpv_node& map, const char* key) {
  const mpv_node* n = find(map, key);
  return n != nullptr && n->format == MPV_FORMAT_STRING && n->u.string != nullptr ? n->u.string : "";
}

bool flag(const mpv_node& map, const char* key) {
  const mpv_node* n = find(map, key);
  return n != nullptr && n->format == MPV_FORMAT_FLAG && n->u.flag != 0;
}

std::int64_t integer(const mpv_node& map, const char* key) {
  const mpv_node* n = find(map, key);
  return n != nullptr && n->format == MPV_FORMAT_INT64 ? n->u.int64 : 0;
}

}  // namespace

std::vector<MpvTrack> parseTrackList(const mpv_node& node) {
  std::vector<MpvTrack> out;
  if (node.format != MPV_FORMAT_NODE_ARRAY || node.u.list == nullptr) return out;
  for (int i = 0; i < node.u.list->num; i++) {
    const mpv_node& t = node.u.list->values[i];
    if (t.format != MPV_FORMAT_NODE_MAP) continue;
    out.push_back(MpvTrack{integer(t, "id"), str(t, "type"), str(t, "lang"), str(t, "title"), flag(t, "selected"),
                           flag(t, "external"), flag(t, "albumart"), str(t, "codec")});
  }
  return out;
}

std::optional<VideoContent> parseVideoParams(const mpv_node& node) {
  const auto dw = integer(node, "dw");
  const auto dh = integer(node, "dh");
  if (dw <= 0 || dh <= 0 || dw > (1 << 20) || dh > (1 << 20)) return std::nullopt;
  return VideoContent{static_cast<int>(dw), static_cast<int>(dh)};
}

MpvAudioParams parseAudioParams(const mpv_node& node) {
  MpvAudioParams out;
  if (node.format != MPV_FORMAT_NODE_MAP) return out;
  out.rate = integer(node, "samplerate");
  out.channels = integer(node, "channel-count");
  out.format = str(node, "format");
  return out;
}

std::optional<std::int64_t> audioTrackToRetry(const std::vector<MpvTrack>& tracks) {
  std::optional<std::int64_t> first;
  for (const auto& t : tracks) {
    if (t.type != "audio") continue;
    if (t.external) return t.id;
    if (!first) first = t.id;
  }
  return first;
}

std::vector<Track> toTracks(const std::vector<MpvTrack>& tracks) {
  std::vector<Track> out;
  for (const auto& t : tracks) {
    Track track;
    if (t.type == "video") {
      if (t.albumart) continue;
      track.kind = cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_VIDEO;
      track.id = "v";
    } else if (t.type == "audio") {
      track.kind = cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_AUDIO;
      track.id = "a";
    } else if (t.type == "sub") {
      track.kind = cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_SUBTITLE;
      track.id = "s";
      track.external = t.external;
    } else {
      continue;
    }
    track.id += std::to_string(t.id);
    track.language = bcp47(t.lang);
    track.label = t.title;
    track.selected = t.selected;
    track.codec = t.codec;
    out.push_back(std::move(track));
  }
  return out;
}

std::optional<TrackRef> trackSelection(const std::string& trackId, cadence_v2_MediaTrackKind kind) {
  const auto propertyFor = [](char letter) -> const char* {
    switch (letter) {
      case 'a': return "aid";
      case 's': return "sid";
      case 'v': return "vid";
      default: return nullptr;
    }
  };
  if (trackId.empty()) {
    switch (kind) {
      case cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_SUBTITLE: return TrackRef{"sid", "no"};
      case cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_AUDIO: return TrackRef{"aid", "no"};
      case cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_VIDEO: return TrackRef{"vid", "no"};
      default: return std::nullopt;
    }
  }
  const char* property = propertyFor(trackId[0]);
  if (property == nullptr || trackId.size() < 2 || trackId.size() > 10) return std::nullopt;
  for (std::size_t i = 1; i < trackId.size(); i++) {
    if (std::isdigit(static_cast<unsigned char>(trackId[i])) == 0) return std::nullopt;
  }
  if (trackId[1] == '0') return std::nullopt;
  return TrackRef{property, trackId.substr(1)};
}

std::string bcp47(const std::string& language) {
  static const struct {
    const char* from;
    const char* to;
  } kMap[] = {
      {"eng", "en"}, {"rus", "ru"}, {"jpn", "ja"}, {"ukr", "uk"}, {"deu", "de"}, {"ger", "de"}, {"fra", "fr"},
      {"fre", "fr"}, {"spa", "es"}, {"ita", "it"}, {"por", "pt"}, {"chi", "zh"}, {"zho", "zh"}, {"kor", "ko"},
      {"pol", "pl"}, {"tur", "tr"}, {"ara", "ar"}, {"hin", "hi"}, {"nld", "nl"}, {"dut", "nl"}, {"swe", "sv"},
      {"fin", "fi"}, {"nor", "no"}, {"dan", "da"}, {"ces", "cs"}, {"cze", "cs"}, {"bel", "be"}, {"kaz", "kk"},
  };
  std::string lower;
  for (char c : language) lower.push_back(static_cast<char>(std::tolower(static_cast<unsigned char>(c))));
  for (const auto& m : kMap) {
    if (lower == m.from) return m.to;
  }
  // mpv's lang is free text from the container: keep a short token only (it is shown, never logged)
  if (lower.size() > 35) return "";
  return lower;
}

}  // namespace cadence::screen::player
