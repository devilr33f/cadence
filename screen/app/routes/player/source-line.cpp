#include "app/routes/player/source-line.h"

#include <algorithm>
#include <cctype>

namespace cadence::screen::routes {

std::string codecName(std::string_view c) {
  struct Known {
    std::string_view mpv;
    std::string_view shown;
  };
  static constexpr Known kKnown[] = {
      {"h264", "H.264"}, {"hevc", "HEVC"}, {"h265", "HEVC"},  {"av1", "AV1"},     {"vp9", "VP9"},       {"vp8", "VP8"},
      {"mpeg2video", "MPEG-2"}, {"aac", "AAC"}, {"opus", "Opus"}, {"vorbis", "Vorbis"}, {"flac", "FLAC"}, {"mp3", "MP3"},
      {"ac3", "AC-3"}, {"eac3", "E-AC-3"}, {"dts", "DTS"}, {"truehd", "TrueHD"},
  };
  for (const auto& k : kKnown)
    if (k.mpv == c) return std::string(k.shown);
  std::string out;
  for (const char ch : c) {
    // a decoder's name is a token; anything else is not shown
    if (std::isalnum(static_cast<unsigned char>(ch)) == 0 && ch != '-' && ch != '_' && ch != '.') return {};
    out.push_back(static_cast<char>(std::toupper(static_cast<unsigned char>(ch))));
  }
  return out.size() <= 12 ? out : std::string();
}

std::string codecLine(const std::vector<player::Track>& tracks) {
  std::string video, audio;
  for (const auto& t : tracks) {
    if (!t.selected || t.codec.empty()) continue;
    if (t.kind == cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_VIDEO && video.empty()) video = codecName(t.codec);
    if (t.kind == cadence_v2_MediaTrackKind_MEDIA_TRACK_KIND_AUDIO && audio.empty()) audio = codecName(t.codec);
  }
  if (video.empty()) return audio;
  if (audio.empty()) return video;
  return video + " \xc2\xb7 " + audio;
}

std::string providerLabel(const Session& s) {
  if (s.torrplayer() && !s.providerSource.empty()) return s.providerSource;
  return s.providerName;
}

}  // namespace cadence::screen::routes
