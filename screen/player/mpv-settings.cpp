#include "player/mpv-settings.h"

#include <cctype>
#include <cmath>
#include <cstdlib>
#include <initializer_list>
#include <map>

namespace cadence::screen::player {

namespace {

bool space(char c) { return c == ' ' || c == '\n' || c == '\t' || c == '\r'; }

// one member of the config's top-level object: its key (owned: the object is scanned in one pass) and the raw text
// of its value, a view into the json itself (a string with its quotes)
struct Member {
  std::string key;
  std::string_view raw;
};

// the offset just past a json string value that starts at i (its opening quote), or npos
std::size_t endOfString(std::string_view json, std::size_t i) {
  for (std::size_t j = i + 1; j < json.size(); j++) {
    if (json[j] == '\\') {
      j++;
      continue;
    }
    if (json[j] == '"') return j + 1;
  }
  return std::string_view::npos;
}

// walks the members of the one top-level object, skipping nested values whole. false when the text is not a json
// object a config could be
bool scanObject(std::string_view json, std::vector<Member>& out) {
  std::size_t i = 0;
  const std::size_t n = json.size();
  while (i < n && space(json[i])) i++;
  if (i >= n || json[i] != '{') return false;
  i++;
  int depth = 1;
  bool expectKey = true;
  while (i < n) {
    const char c = json[i];
    if (space(c)) {
      i++;
      continue;
    }
    if (c == ',') {
      expectKey = depth == 1;
      i++;
      continue;
    }
    if (c == '}' || c == ']') {
      depth--;
      i++;
      if (depth == 0) return c == '}';
      continue;
    }
    if (c == '{' || c == '[') {
      depth++;
      i++;
      continue;
    }
    if (c != '"') return false;
    // a string: a key at depth 1, a value elsewhere
    if (depth != 1 || !expectKey) {
      const auto end = endOfString(json, i);
      if (end == std::string_view::npos) return false;
      i = end;
      continue;
    }
    const auto q = endOfString(json, i);
    if (q == std::string_view::npos) return false;
    std::string key;
    if (q > i + 1) key.assign(json.substr(i + 1, q - i - 2));
    std::size_t k = q;
    while (k < n && space(json[k])) k++;
    if (k >= n || json[k] != ':') return false;
    k++;
    while (k < n && space(json[k])) k++;
    if (k >= n) return false;
    std::size_t end = n;
    if (json[k] == '"') {
      end = endOfString(json, k);
    } else if (json[k] == '{' || json[k] == '[') {
      int d = 0;
      std::size_t j = k;
      for (; j < n; j++) {
        if (json[j] == '"') {
          const auto e = endOfString(json, j);
          if (e == std::string_view::npos) return false;
          j = e - 1;
          continue;
        }
        if (json[j] == '{' || json[j] == '[') d++;
        if (json[j] == '}' || json[j] == ']') d--;
        if (d == 0) {
          j++;
          break;
        }
      }
      end = j;
    } else {
      std::size_t j = k;
      while (j < n && json[j] != ',' && json[j] != '}' && json[j] != ']') j++;
      while (j > k && space(json[j - 1])) j--;
      end = j;
    }
    if (end == std::string_view::npos || end > n) return false;
    out.push_back(Member{std::move(key), json.substr(k, end - k)});
    i = end;
    expectKey = false;
  }
  return false;
}

// the text of a string value without its quotes; false when it holds an escape (no value the schema takes has one)
bool stringValue(std::string_view raw, std::string& out) {
  if (raw.size() < 2 || raw.front() != '"' || raw.back() != '"') return false;
  const std::string_view text = raw.substr(1, raw.size() - 2);
  for (const char c : text) {
    if (c == '\\') return false;
  }
  out.assign(text);
  return true;
}

bool boolValue(std::string_view raw, bool& out) {
  if (raw == "true") {
    out = true;
    return true;
  }
  if (raw == "false") {
    out = false;
    return true;
  }
  return false;
}

// a json number the whole raw parses as
bool numberValue(std::string_view raw, double& out) {
  const std::string text(raw);
  char* end = nullptr;
  const double v = std::strtod(text.c_str(), &end);
  if (end == text.c_str() || *end != '\0' || !std::isfinite(v)) return false;
  out = v;
  return true;
}

bool inEnum(std::string_view v, std::initializer_list<std::string_view> values) {
  for (const auto w : values) {
    if (v == w) return true;
  }
  return false;
}

// mpv's yes/no
const char* yesNo(bool v) { return v ? "yes" : "no"; }

// a short, fixed rendering: an integral number with no decimal point, any other with %.6g
std::string formatNumber(double v) {
  if (std::fabs(v - static_cast<double>(std::llround(v))) < 1e-9) return std::to_string(std::llround(v));
  char buf[32];
  std::snprintf(buf, sizeof buf, "%.6g", v);
  return buf;
}

// one bcp 47 short tag: two or three letters, then a dash and one to eight letters or digits ("ru", "pt-BR")
bool languageTag(std::string_view v) {
  std::size_t i = 0;
  while (i < v.size() && std::isalpha(static_cast<unsigned char>(v[i])) != 0) i++;
  if (i < 2 || i > 3) return false;
  if (i == v.size()) return true;
  if (v[i] != '-') return false;
  const std::size_t sub = ++i;
  while (i < v.size() && std::isalnum(static_cast<unsigned char>(v[i])) != 0) i++;
  return i - sub >= 1 && i - sub <= 8 && i == v.size();
}

// one to eight comma-separated tags ("rus,ru,jpn,eng"), as the schema takes them
bool languageList(std::string_view v) {
  std::size_t tags = 0;
  std::size_t start = 0;
  for (std::size_t i = 0; i <= v.size(); i++) {
    if (i != v.size() && v[i] != ',') continue;
    if (!languageTag(v.substr(start, i - start))) return false;
    tags++;
    start = i + 1;
  }
  return tags >= 1 && tags <= 8;
}

// printable ascii, no quote or backslash (mpvQuote carries the value whole either way; this keeps it dull)
bool dullString(std::string_view v) {
  if (v.empty() || v.size() > 256) return false;
  for (const char c : v) {
    const auto u = static_cast<unsigned char>(c);
    if (u < 0x20 || u > 0x7e || c == '"' || c == '\\') return false;
  }
  return true;
}

constexpr std::initializer_list<std::string_view> kHwdec = {"auto", "auto-safe", "no", "mediacodec", "vaapi", "vdpau",
                                                            "nvdec", "v4l2m2m", "v4l2m2m-copy", "drm", "drm-copy"};
constexpr std::initializer_list<std::string_view> kVideoSync = {"audio", "display-resample",
                                                                "display-resample-desync", "display-vdrop",
                                                                "display-adrop", "desync"};
constexpr std::initializer_list<std::string_view> kTscale = {"oversample", "catmull_rom", "mitchell", "linear",
                                                             "sphinx"};
constexpr std::initializer_list<std::string_view> kScalers = {
    "bilinear", "bicubic", "catmull_rom", "mitchell", "lanczos", "spline16", "spline36",
    "ewa_lanczos", "ewa_lanczossharp", "ewa_robidoux", "ewa_robidouxsharp", "oversample"};

// the preset's own options: mpv's built-in fast and high-quality, as our fixed strings (the render api path's
// scalers; the surface-embed path has none to tune, so only decoding remains)
void presetOptions(std::string_view profile, VideoPath path, std::vector<MpvSettingOption>& out) {
  if (profile == "fast") {
    out.push_back({"vd-lavc-skiploopfilter", "nonref", true});
    if (path == VideoPath::RenderApi) {
      out.push_back({"scale", "bilinear", false});
      out.push_back({"cscale", "bilinear", false});
      out.push_back({"dscale", "bilinear", false});
      out.push_back({"correct-downscaling", "no", false});
      out.push_back({"sigmoid-upscaling", "no", false});
    }
    return;
  }
  if (profile == "high-quality" && path == VideoPath::RenderApi) {
    out.push_back({"scale", "spline36", false});
    out.push_back({"cscale", "spline36", false});
    out.push_back({"dscale", "mitchell", false});
    out.push_back({"correct-downscaling", "yes", false});
    out.push_back({"linear-downscaling", "yes", false});
    out.push_back({"sigmoid-upscaling", "yes", false});
  }
}

// one member to its options; false when the key is unknown or its value fails the device's own check
bool mapMember(const Member& m, VideoPath path, std::vector<MpvSettingOption>& out) {
  std::string s;
  double v = 0;
  bool b = false;
  const auto text = [&]() {
    std::string t;
    return stringValue(m.raw, t) ? t : std::string();
  };
  if (m.key == "profile") {
    const std::string t = text();
    if (!inEnum(t, {"fast", "default", "high-quality"})) return false;
    presetOptions(t, path, out);
    return true;
  }
  if (m.key == "hwdec") {
    const std::string t = text();
    if (!inEnum(t, kHwdec)) return false;
    out.push_back({"hwdec", t, true});
    return true;
  }
  if (m.key == "cacheEnabled") {
    if (!boolValue(m.raw, b)) return false;
    out.push_back({"cache", yesNo(b), true});
    return true;
  }
  if (m.key == "demuxerMaxBytes" || m.key == "demuxerMaxBackBytes") {
    if (!numberValue(m.raw, v) || v < 1048576 || v > 2147483647 || v != static_cast<double>(std::llround(v))) return false;
    out.push_back({m.key == "demuxerMaxBytes" ? "demuxer-max-bytes" : "demuxer-max-back-bytes",
                   std::to_string(static_cast<long long>(v)), true});
    return true;
  }
  if (m.key == "cacheSecs" || m.key == "readaheadS" || m.key == "networkTimeoutS") {
    const double max = m.key == "networkTimeoutS" ? 300 : 600;
    if (!numberValue(m.raw, v) || v < 1 || v > max) return false;
    out.push_back({m.key == "cacheSecs" ? "cache-secs"
                   : m.key == "readaheadS" ? "demuxer-readahead-secs"
                                           : "network-timeout",
                   formatNumber(v), true});
    return true;
  }
  if (m.key == "streamBufferBytes") {
    if (!numberValue(m.raw, v) || v < 4096 || v > 10485760 || v != static_cast<double>(std::llround(v))) return false;
    out.push_back({"stream-buffer-size", std::to_string(static_cast<long long>(v)), true});
    return true;
  }
  if (m.key == "userAgent") {
    if (!stringValue(m.raw, s) || !dullString(s)) return false;
    out.push_back({"user-agent", s, true});
    return true;
  }
  if (m.key == "videoSync") {
    const std::string t = text();
    if (!inEnum(t, kVideoSync)) return false;
    out.push_back({"video-sync", t, true});
    return true;
  }
  if (m.key == "interpolation") {
    if (!boolValue(m.raw, b)) return false;
    out.push_back({"interpolation", yesNo(b), false});
    return true;
  }
  if (m.key == "tscale") {
    const std::string t = text();
    if (!inEnum(t, kTscale)) return false;
    out.push_back({"tscale", t, false});
    return true;
  }
  if (m.key == "scale" || m.key == "cscale" || m.key == "dscale") {
    const std::string t = text();
    if (!inEnum(t, kScalers)) return false;
    out.push_back({std::string(m.key), t, false});
    return true;
  }
  if (m.key == "deband") {
    if (!boolValue(m.raw, b)) return false;
    out.push_back({"deband", yesNo(b), false});
    return true;
  }
  if (m.key == "sharpen") {
    if (!numberValue(m.raw, v) || v < 0 || v > 20) return false;
    out.push_back({"sharpen", formatNumber(v), false});
    return true;
  }
  if (m.key == "audioLanguages" || m.key == "subtitleLanguages") {
    if (!stringValue(m.raw, s) || !languageList(s)) return false;
    out.push_back({m.key == "audioLanguages" ? "alang" : "slang", s, true});
    return true;
  }
  if (m.key == "audioDelayMs") {
    if (!numberValue(m.raw, v) || v < -10000 || v > 10000 || v != static_cast<double>(std::llround(v))) return false;
    out.push_back({"audio-delay", formatNumber(v / 1000.0), false});
    return true;
  }
  return false;
}

}  // namespace

MpvSettings parseMpvSettings(std::string_view json, VideoPath path) {
  MpvSettings out;
  std::vector<Member> members;
  if (!scanObject(json, members)) {
    out.refused.emplace_back("config");
    return out;
  }
  for (const auto& m : members) {
    if (m.key.empty() || m.key.size() > 32) continue;
    std::vector<MpvSettingOption> options;
    if (!mapMember(m, path, options)) {
      out.refused.emplace_back(m.key);
      continue;
    }
    out.entries.push_back(MpvSetting{std::string(m.key), std::move(options)});
  }
  return out;
}

MpvOptions nextLoadOptions(const MpvSettings& settings) {
  std::map<std::string, std::string> out;
  const auto add = [&out](const MpvSetting& e) {
    for (const auto& o : e.options) {
      if (o.nextLoad) out[o.option] = o.value;
    }
  };
  // an explicit key's value wins over the preset's same option, whatever their order in the config
  for (const auto& e : settings.entries) {
    if (e.key != "profile") add(e);
  }
  for (const auto& e : settings.entries) {
    if (e.key == "profile") add(e);
  }
  return {out.begin(), out.end()};
}

}  // namespace cadence::screen::player
