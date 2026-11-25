// media.mpv: the config's keys to mpv options, the device's own check of each value, and the allowlist itself (no
// option name or filter string ever comes from the config)
#include "player/mpv-settings.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <string>

using namespace cadence::screen::player;

namespace {

std::string optionOf(const MpvOptions& o, const std::string& key) {
  const auto it = std::find_if(o.begin(), o.end(), [&](const auto& kv) { return kv.first == key; });
  return it == o.end() ? "<unset>" : it->second;
}

const MpvSetting* entry(const MpvSettings& s, const std::string& key) {
  const auto it = std::find_if(s.entries.begin(), s.entries.end(), [&](const auto& e) { return e.key == key; });
  return it == s.entries.end() ? nullptr : &*it;
}

std::size_t refusedCount(const MpvSettings& s) { return s.refused.size(); }

}  // namespace

TEST_CASE("an empty config maps nothing, and nothing is refused") {
  const auto s = parseMpvSettings("{}", VideoPath::RenderApi);
  CHECK(s.entries.empty());
  CHECK(s.refused.empty());
  CHECK(nextLoadOptions(s).empty());
  // absent is mpv's own default: the config cadenced sends before the owner sets anything changes nothing
  const auto nothing = parseMpvSettings("", VideoPath::RenderApi);
  CHECK(nothing.entries.empty());
  CHECK(nothing.refused.size() == 1);
}

TEST_CASE("every key maps to its mpv option, live or with the next file") {
  const auto s = parseMpvSettings(
      "{\"profile\":\"default\",\"hwdec\":\"v4l2m2m-copy\",\"cacheEnabled\":false,\"demuxerMaxBytes\":1048576,"
      "\"demuxerMaxBackBytes\":268435456,\"cacheSecs\":30.5,\"readaheadS\":60,\"networkTimeoutS\":15,"
      "\"streamBufferBytes\":524288,\"userAgent\":\"Cadence/1.0 (Linux; Android 9)\","
      "\"videoSync\":\"display-resample\",\"interpolation\":true,\"tscale\":\"oversample\",\"scale\":\"spline36\","
      "\"cscale\":\"spline36\",\"dscale\":\"mitchell\",\"deband\":true,\"sharpen\":1.5,"
      "\"audioLanguages\":\"rus,ru,jpn,eng\",\"subtitleLanguages\":\"ru\",\"audioDelayMs\":-250}",
      VideoPath::RenderApi);
  CHECK(s.refused.empty());
  struct Want {
    const char* key;
    const char* option;
    const char* value;
    bool nextLoad;
  };
  // live: what mpv takes as a runtime set; next file: cache, network, decode, track choice and sync
  const Want wants[] = {
      {"hwdec", "hwdec", "v4l2m2m-copy", true},
      {"cacheEnabled", "cache", "no", true},
      {"demuxerMaxBytes", "demuxer-max-bytes", "1048576", true},
      {"demuxerMaxBackBytes", "demuxer-max-back-bytes", "268435456", true},
      {"cacheSecs", "cache-secs", "30.5", true},
      {"readaheadS", "demuxer-readahead-secs", "60", true},
      {"networkTimeoutS", "network-timeout", "15", true},
      {"streamBufferBytes", "stream-buffer-size", "524288", true},
      {"userAgent", "user-agent", "Cadence/1.0 (Linux; Android 9)", true},
      {"videoSync", "video-sync", "display-resample", true},
      {"interpolation", "interpolation", "yes", false},
      {"tscale", "tscale", "oversample", false},
      {"scale", "scale", "spline36", false},
      {"cscale", "cscale", "spline36", false},
      {"dscale", "dscale", "mitchell", false},
      {"deband", "deband", "yes", false},
      {"sharpen", "sharpen", "1.5", false},
      {"audioLanguages", "alang", "rus,ru,jpn,eng", true},
      {"subtitleLanguages", "slang", "ru", true},
      {"audioDelayMs", "audio-delay", "-0.25", false},
  };
  for (const auto& w : wants) {
    const auto* e = entry(s, w.key);
    INFO(w.key);
    REQUIRE(e != nullptr);
    REQUIRE(e->options.size() == 1);
    CHECK(e->options[0].option == w.option);
    CHECK(e->options[0].value == w.value);
    CHECK(e->options[0].nextLoad == w.nextLoad);
  }
  // the preset with no work of its own maps to nothing and applies
  const auto* profile = entry(s, "profile");
  REQUIRE(profile != nullptr);
  CHECK(profile->options.empty());
  // the next-file options, sorted by name
  const auto load = nextLoadOptions(s);
  CHECK(optionOf(load, "alang") == "rus,ru,jpn,eng");
  CHECK(optionOf(load, "cache") == "no");
  CHECK(optionOf(load, "hwdec") == "v4l2m2m-copy");
  CHECK(optionOf(load, "user-agent") == "Cadence/1.0 (Linux; Android 9)");
  CHECK(optionOf(load, "scale") == "<unset>");
  CHECK(optionOf(load, "deband") == "<unset>");
}

TEST_CASE("the presets: fixed options per path") {
  // the render api (desktop, the pi): fast drops the scalers, high-quality raises them
  const auto fast = parseMpvSettings("{\"profile\":\"fast\"}", VideoPath::RenderApi);
  const auto* e = entry(fast, "profile");
  REQUIRE(e != nullptr);
  REQUIRE(e->options.size() == 6);
  CHECK(e->options[0].option == "vd-lavc-skiploopfilter");
  CHECK(e->options[0].value == "nonref");
  CHECK(e->options[0].nextLoad);
  const auto hq = parseMpvSettings("{\"profile\":\"high-quality\"}", VideoPath::RenderApi);
  const auto* h = entry(hq, "profile");
  REQUIRE(h != nullptr);
  REQUIRE(h->options.size() == 6);
  CHECK(h->options[0].option == "scale");
  CHECK(h->options[0].value == "spline36");
  CHECK(h->options[5].option == "sigmoid-upscaling");
  CHECK(h->options[5].value == "yes");
  // the surface-embed path (the stick): no scalers to tune, only decoding
  const auto embedFast = parseMpvSettings("{\"profile\":\"fast\"}", VideoPath::SurfaceEmbed);
  REQUIRE(entry(embedFast, "profile")->options.size() == 1);
  const auto embedHq = parseMpvSettings("{\"profile\":\"high-quality\"}", VideoPath::SurfaceEmbed);
  CHECK(entry(embedHq, "profile")->options.empty());
}

TEST_CASE("an explicit key wins over the preset's same option, in any order") {
  for (const auto* json : {"{\"profile\":\"high-quality\",\"scale\":\"bilinear\"}",
                           "{\"scale\":\"bilinear\",\"profile\":\"high-quality\"}"}) {
    const auto s = parseMpvSettings(json, VideoPath::RenderApi);
    const auto load = nextLoadOptions(s);
    const auto* scale = entry(s, "scale");
    REQUIRE(scale != nullptr);
    CHECK(scale->options[0].value == "bilinear");
    // the preset keeps its other options
    const auto* profile = entry(s, "profile");
    REQUIRE(profile != nullptr);
    REQUIRE(profile->options.size() == 6);
    CHECK(profile->options[0].value == "spline36");
  }
}

TEST_CASE("the device's own check: unknown keys, wrong types, out-of-range values") {
  const auto s = parseMpvSettings(
      "{\"vo\":\"x11\",\"volume\":100,\"vf\":\"gradfun\",\"demuxer-lavf-o\":\"x\",\"cacheSecs\":0.5,"
      "\"readaheadS\":601,\"networkTimeoutS\":0,\"streamBufferBytes\":4095,\"demuxerMaxBytes\":1048575,"
      "\"demuxerMaxBackBytes\":2147483648,\"sharpen\":20.5,\"audioDelayMs\":10001,\"hwdec\":\"auto-copy-safe\","
      "\"profile\":\"ultra\",\"videoSync\":\"display-resample-soft\",\"tscale\":\"lanczos\",\"scale\":\"haasnsoft\","
      "\"cacheEnabled\":\"yes\",\"deband\":1,\"audioLanguages\":\"rus, ru\",\"subtitleLanguages\":\"rus,russian\","
      "\"userAgent\":\"Mozilla\\\"5.0\"}",
      VideoPath::RenderApi);
  CHECK(s.entries.empty());
  CHECK(s.refused.size() == 22);
  for (const auto& key : {"vo", "volume", "vf", "demuxer-lavf-o", "cacheSecs", "readaheadS", "networkTimeoutS",
                          "streamBufferBytes", "demuxerMaxBytes", "demuxerMaxBackBytes", "sharpen", "audioDelayMs",
                          "hwdec", "profile", "videoSync", "tscale", "scale", "cacheEnabled", "deband",
                          "audioLanguages", "subtitleLanguages", "userAgent"}) {
    INFO(key);
    CHECK(std::find(s.refused.begin(), s.refused.end(), key) != s.refused.end());
  }
  // a value outside the schema's range never reaches mpv, neither live nor with the next file
  CHECK(nextLoadOptions(s).empty());
}

TEST_CASE("not a config: refused whole") {
  CHECK(refusedCount(parseMpvSettings("[1,2]", VideoPath::RenderApi)) == 1);
  CHECK(refusedCount(parseMpvSettings("\"x\"", VideoPath::RenderApi)) == 1);
  CHECK(refusedCount(parseMpvSettings("{\"scale\":", VideoPath::RenderApi)) == 1);
  CHECK(refusedCount(parseMpvSettings("{\"a\":{\"b\":1}}", VideoPath::RenderApi)) == 1);
  // whitespace and member order are free
  const auto s = parseMpvSettings("  { \"deband\" : true , \"scale\" : \"lanczos\" }  ", VideoPath::RenderApi);
  CHECK(s.refused.empty());
  REQUIRE(entry(s, "deband") != nullptr);
  REQUIRE(entry(s, "scale") != nullptr);
}

TEST_CASE("language lists: one to eight tags, each two to eight letters, digits or a dash") {
  CHECK(entry(parseMpvSettings("{\"audioLanguages\":\"eng\"}", VideoPath::RenderApi), "audioLanguages") != nullptr);
  CHECK(entry(parseMpvSettings("{\"audioLanguages\":\"ru-RU,eng\"}", VideoPath::RenderApi), "audioLanguages") !=
        nullptr);
  CHECK(entry(parseMpvSettings("{\"audioLanguages\":\"rus,ru,jpn,eng\"}", VideoPath::RenderApi),
              "audioLanguages") != nullptr);
  // nine tags, a one-letter tag, spaces, an empty tag, a tag with a comma inside quotes
  for (const auto* json : {"{\"audioLanguages\":\"a,b,c,d,e,f,g,h,i\"}", "{\"audioLanguages\":\"r\"}",
                           "{\"audioLanguages\":\"rus, ru\"}", "{\"audioLanguages\":\"rus,\",}"}) {
    const auto s = parseMpvSettings(json, VideoPath::RenderApi);
    CHECK(entry(s, "audioLanguages") == nullptr);
    CHECK(s.refused.size() == 1);
  }
}
