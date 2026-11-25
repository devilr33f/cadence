#include "app/options.h"

#include <cstdlib>
#include <string_view>

namespace cadence::screen::app {

AppOptions defaultOptions() {
  AppOptions o;
#if defined(__ANDROID__)
  o.socket = "@cadence-screen";
  o.platform.videoDriver = "android";
  o.platform.translucent = true;
#else
  o.socket = "/run/cadence/screen.sock";
#endif
  return o;
}

void applyPerfEnv(AppOptions& o, const char* perf, const char* hud) {
  if (perf != nullptr && perf[0] != '\0' && std::string_view(perf) != "0") {
    o.perfLogMs = 5000;
    o.perfGpu = std::string_view(perf) == "gpu";
  }
  if (hud != nullptr && hud[0] != '\0' && std::string_view(hud) != "0") o.hud = true;
}

const char* usage() {
  return "usage: cadence-screen [--socket PATH|@NAME] [--driver kmsdrm|wayland|offscreen] [--size WxH] [--no-vsync]\n"
         "                      [--ao NAME] [--hwdec NAME] [--mpv-profile NAME] [--max-display-fps N] [--exit-after-ms N]\n";
}

namespace {

bool parseSize(const std::string& s, int* w, int* h) {
  const auto x = s.find('x');
  if (x == std::string::npos) return false;
  char* end = nullptr;
  const long width = std::strtol(s.c_str(), &end, 10);
  if (end != s.c_str() + x) return false;
  const long height = std::strtol(s.c_str() + x + 1, &end, 10);
  if (*end != '\0' || width <= 0 || height <= 0 || width > 16384 || height > 16384) return false;
  *w = static_cast<int>(width);
  *h = static_cast<int>(height);
  return true;
}

}  // namespace

std::optional<AppOptions> parseOptions(const std::vector<std::string>& args, const char* socketEnv,
                                       std::string* error) {
  AppOptions o = defaultOptions();
  if (socketEnv != nullptr && *socketEnv != '\0') o.socket = socketEnv;
  for (std::size_t i = 0; i < args.size(); i++) {
    const std::string& a = args[i];
    const auto value = [&]() -> std::optional<std::string> {
      if (i + 1 >= args.size()) return std::nullopt;
      return args[++i];
    };
    if (a == "--help" || a == "-h") {
      o.help = true;
    } else if (a == "--no-vsync") {
      o.platform.vsync = false;
    } else if (a == "--socket" || a == "--driver" || a == "--size" || a == "--ao" || a == "--hwdec" ||
               a == "--mpv-profile" || a == "--max-display-fps" || a == "--exit-after-ms") {
      const auto v = value();
      if (!v || v->empty()) {
        if (error != nullptr) *error = a;
        return std::nullopt;
      }
      if (a == "--socket") {
        o.socket = *v;
      } else if (a == "--driver") {
        o.platform.videoDriver = *v;
      } else if (a == "--size") {
        if (!parseSize(*v, &o.platform.width, &o.platform.height)) {
          if (error != nullptr) *error = a;
          return std::nullopt;
        }
      } else if (a == "--ao") {
        o.ao = *v;
      } else if (a == "--hwdec") {
        o.hwdec = *v;
      } else if (a == "--mpv-profile") {
        o.mpvProfile = *v;
      } else if (a == "--max-display-fps") {
        char* end = nullptr;
        const long fps = std::strtol(v->c_str(), &end, 10);
        if (*end != '\0' || fps < 1 || fps > 240) {
          if (error != nullptr) *error = a;
          return std::nullopt;
        }
        o.maxDisplayFps = static_cast<int>(fps);
      } else {
        char* end = nullptr;
        const long long ms = std::strtoll(v->c_str(), &end, 10);
        if (*end != '\0' || ms < 0) {
          if (error != nullptr) *error = a;
          return std::nullopt;
        }
        o.exitAfterMs = ms;
      }
    } else {
      if (error != nullptr) *error = a;
      return std::nullopt;
    }
  }
  return o;
}

}  // namespace cadence::screen::app
