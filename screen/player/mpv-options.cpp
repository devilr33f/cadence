#include "player/mpv-options.h"

#include <cstdio>
#include <string_view>

namespace cadence::screen::player {

bool mpvWithheldGlFunction(const char* name) {
  if (name == nullptr) return false;
  const std::string_view n(name);
  return n == "glBufferStorage" || n == "glBufferStorageEXT";
}

bool mpvStubbedGlFunction(const char* name) { return name != nullptr && std::string_view(name) == "glFenceSync"; }

MpvOptions mpvInitOptions(const MpvConfig& c) {
  MpvOptions o;
  // first, so the options below (and the dashboard's mpv settings, set later) win over the profile's
  if (!c.profile.empty()) o.emplace_back("profile", c.profile);
  o.insert(o.end(), {
      // nothing of mpv's own ui, input, config or scripts: the screen is the ui
      {"config", "no"},
      {"terminal", "no"},
      {"input-default-bindings", "no"},
      {"input-vo-keyboard", "no"},
      {"osc", "no"},
      {"load-scripts", "no"},
      {"osd-level", "0"},
      // urls are resolved server-side (yt-dlp runs there), never by mpv
      {"ytdl", "no"},
      {"sub-auto", "no"},
      // the screen draws subtitles itself from sub-text (mediacodec_embed draws none): one look on every path
      {"sub-visibility", "no"},
      {"audio-display", "no"},
      {"idle", "yes"},
      {"keep-open", "no"},
      {"cache", "yes"},
  });
  if (c.videoPath == VideoPath::RenderApi) {
    o.emplace_back("vo", "libmpv");
    o.emplace_back("hwdec", c.hwdec.empty() ? "auto-safe" : c.hwdec);
  } else {
    o.emplace_back("vo", "mediacodec_embed");
    o.emplace_back("hwdec", c.hwdec.empty() ? "mediacodec" : c.hwdec);
    // mpv draws nothing until the app hands it its surface (wid)
    o.emplace_back("force-window", "no");
  }
  if (!c.aoOverride.empty()) {
    o.emplace_back("ao", c.aoOverride);
  } else if (c.ao == AoMode::Cadence) {
    o.emplace_back("ao", "cadence");
    o.emplace_back("ao-cadence-ring-fd", std::to_string(c.ringFd));
    o.emplace_back("ao-cadence-event-fd", std::to_string(c.eventFd));
  }
  // the stock ao is levelled through mpv's volume, which starts at 0: nothing is audible until cadenced's level
  if (c.ao == AoMode::Stock) o.emplace_back("volume", "0");
  return o;
}

MpvOptions ringOptions(int ringFd, int eventFd, std::uint32_t capacityFrames) {
  // ao_cadence's --ao-cadence-buffer range: 5 to 1000 ms; the ring is 48 kHz
  const std::uint64_t ms = static_cast<std::uint64_t>(capacityFrames) * 3 / 4 * 1000 / 48000;
  const std::uint64_t buffer = ms < 5 ? 5 : ms > 1000 ? 1000 : ms;
  return {{"ao-cadence-ring-fd", std::to_string(ringFd)},
          {"ao-cadence-event-fd", std::to_string(eventFd)},
          {"ao-cadence-buffer", std::to_string(buffer)}};
}

std::string mpvQuote(const std::string& value) { return "%" + std::to_string(value.size()) + "%" + value; }

std::string headerFields(const std::map<std::string, std::string>& headers) {
  std::string out;
  for (const auto& [key, value] : headers) {
    if (!out.empty()) out += ',';
    for (char ch : key + ": " + value) {
      if (ch == ',' || ch == '\\') out += '\\';
      out += ch;
    }
  }
  return out;
}

std::vector<std::string> loadfileCommand(const PlayRequest& r, bool paused, const AudioRouteInfo& route,
                                         const MpvOptions& settings) {
  std::string options = "pause=" + std::string(paused ? "yes" : "no");
  if (r.startMs > 0) {
    char buf[32];
    std::snprintf(buf, sizeof buf, "%.3f", static_cast<double>(r.startMs) / 1000.0);
    options += ",start=" + mpvQuote(buf);
  } else {
    options += ",start=" + mpvQuote("none");
  }
  if (route.silent) {
    options += ",aid=no,mute=yes";
  } else {
    options += ",aid=auto,mute=no";
  }
  options += ",http-header-fields=" + mpvQuote(headerFields(r.headers));
  // media.mpv's next-load options, before the live stream's own cache bound, which wins
  for (const auto& [key, value] : settings) {
    options += "," + key + "=" + mpvQuote(value);
  }
  // a live stream is never cached far ahead
  if (r.live) options += ",cache-secs=" + mpvQuote("10");
  return {"loadfile", r.url, "replace", "-1", options};
}

}  // namespace cadence::screen::player
