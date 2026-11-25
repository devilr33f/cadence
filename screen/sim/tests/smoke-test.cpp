// the sim's smoke test: runs a walkthrough headless as a child process, then checks the screenshots it left — the
// right size, and pixels that vary (a drawn ui, not a clear). a plain main, no catch2
#include "sim/png.h"

#include <unistd.h>

#include <cstdio>
#include <cstdlib>
#include <string>

namespace {

using cadence::screen::render::Image;
using cadence::screen::sim::readPng;

int fail(const std::string& what) {
  std::fprintf(stderr, "sim-smoke: fail: %s\n", what.c_str());
  return 1;
}

// the spread of sampled luminance, so a uniform frame (a clear, a black screen) says so
int variance(const Image& image) {
  int min = 255;
  int max = 0;
  const std::size_t n = image.rgba.size() / 4;
  for (std::size_t i = 0; i < n; i += 97) {
    const int lum = (static_cast<int>(image.rgba[i * 4]) + static_cast<int>(image.rgba[i * 4 + 1]) +
                     static_cast<int>(image.rgba[i * 4 + 2])) /
                    3;
    if (lum < min) min = lum;
    if (lum > max) max = lum;
  }
  return max - min;
}

// pixels of the ember token (#FF9A3C) in the band at the right edge where the volume overlay sits, midway down
int emberInVolumeBand(const Image& image) {
  int count = 0;
  for (int y = 150; y < 570; ++y) {
    for (int x = 1150; x < 1240; ++x) {
      const std::size_t i = (static_cast<std::size_t>(y) * static_cast<std::size_t>(image.width) +
                             static_cast<std::size_t>(x)) * 4;
      const int r = image.rgba[i];
      const int g = image.rgba[i + 1];
      const int b = image.rgba[i + 2];
      if (r > 220 && g > 120 && g < 185 && b < 100) ++count;
    }
  }
  return count;
}

}  // namespace

int main() {
  const std::string dir = "/tmp/cadence-screen-sim-smoke." + std::to_string(::getpid());
  const std::string scriptPath = dir + "/walk.txt";
  std::string mkdir = "mkdir -p " + dir;
  if (std::system(mkdir.c_str()) != 0) return fail("mkdir");

  // the walkthrough the docs promise, with this run's own paths: home, a title, the player, the assistant
  const std::string script = "start --headless\n"
                             "wait-for route:home 15000\n"
                             "screenshot " + dir + "/home.png\n" +
                             // the fake cadenced steps its level and sends it back: the overlay shows, then goes
                             // (before the title, whose picker wants the walk's own timing)
                             "press VOLUME_UP\n"
                             "wait 500\n"
                             "screenshot " + dir + "/volume.png\n" +
                             "wait 2600\n"
                             "screenshot " + dir + "/volume-gone.png\n" +
                             "send {\"route\": {\"route\": \"title\", \"ref\": \"ref-tg-1\"}}\n"
                             "wait-for route:title 15000\n"
                             "wait 800\n"
                             "screenshot " + dir + "/title.png\n" +
                             "press OK\n"
                             "wait-for route:picker 10000\n"
                             "press OK\n"
                             "wait-for route:resolving 10000\n"
                             "wait-for route:player 20000\n"
                             "wait-for media:playing 10000\n"
                             "screenshot " + dir + "/player.png\n" +
                             "press PLAY_PAUSE\n"
                             "wait 300\n"
                             "hold BACK 800\n"
                             "wait-for drawer 8000\n"
                             "screenshot " + dir + "/assistant.png\n" +
                             "quit\n";
  FILE* out = std::fopen(scriptPath.c_str(), "w");
  if (out == nullptr) return fail("cannot write " + scriptPath);
  std::fputs(script.c_str(), out);
  std::fclose(out);

  const std::string command = std::string(CADENCE_SIM_BIN) + " --headless run " + scriptPath;
  const int code = std::system(command.c_str());
  if (code != 0) return fail("the run exited " + std::to_string(code));

  const char* names[] = {"home.png", "title.png", "player.png", "assistant.png"};
  for (const char* name : names) {
    Image image;
    if (!readPng(dir + "/" + name, image)) return fail(std::string("no screenshot ") + name);
    if (image.width != 1280 || image.height != 720) {
      return fail(std::string(name) + " is " + std::to_string(image.width) + "x" + std::to_string(image.height) +
                  ", not 1280x720");
    }
    const int spread = variance(image);
    if (spread < 24) return fail(std::string(name) + " looks uniform (spread " + std::to_string(spread) + ")");
  }
  Image shown;
  Image gone;
  if (!readPng(dir + "/volume.png", shown) || !readPng(dir + "/volume-gone.png", gone))
    return fail("no volume screenshots");
  if (emberInVolumeBand(shown) < 20) return fail("no volume overlay after a volume key");
  if (emberInVolumeBand(gone) != 0) return fail("the volume overlay did not hide");
  std::printf("sim-smoke: ok\n");
  return 0;
}
