// the sim's alarms walk: home's strip, a ring of another device's over everything (BACK leaves it, «Стоп» ends it
// through alarms.act), the alarms page, and the display rule (another device's ring never wakes a sleeping screen,
// shows once it is on; this screen's own wakes it). runs headless as a child process and checks the screenshots by
// their pixels: the ring is the ink layer over the whole frame with the ember «Стоп» under the time. a plain main
#include "sim/png.h"

#include <unistd.h>

#include <cstdio>
#include <cstdlib>
#include <string>

namespace {

using cadence::screen::render::Image;
using cadence::screen::sim::readPng;

int fail(const std::string& what) {
  std::fprintf(stderr, "sim-alarms: fail: %s\n", what.c_str());
  return 1;
}

const unsigned char* px(const Image& image, int x, int y) {
  return &image.rgba[(static_cast<std::size_t>(y) * static_cast<std::size_t>(image.width) + static_cast<std::size_t>(x)) * 4];
}

// the share of sampled pixels of the ink token (#0C0E13): the ring's layer covers nearly all of the frame
double inkShare(const Image& image) {
  int n = 0;
  int ink = 0;
  for (int y = 0; y < image.height; y += 7) {
    for (int x = 0; x < image.width; x += 7) {
      const auto* p = px(image, x, y);
      ++n;
      if (std::abs(p[0] - 12) <= 4 && std::abs(p[1] - 14) <= 4 && std::abs(p[2] - 19) <= 4) ++ink;
    }
  }
  return n > 0 ? static_cast<double>(ink) / n : 0;
}

// ember (#FF9A3C) pixels where the ring's buttons sit, under the time (1280x720)
int emberInButtons(const Image& image) {
  int count = 0;
  for (int y = 380; y < 520; ++y) {
    for (int x = 380; x < 900; ++x) {
      const auto* p = px(image, x, y);
      if (p[0] > 220 && p[1] > 120 && p[1] < 185 && p[2] < 100) ++count;
    }
  }
  return count;
}

}  // namespace

int main() {
  const std::string dir = "/tmp/cadence-screen-sim-alarms." + std::to_string(::getpid());
  const std::string scriptPath = dir + "/alarms.txt";
  const std::string mkdir = "mkdir -p " + dir;
  if (std::system(mkdir.c_str()) != 0) return fail("mkdir");
  const std::string ex = CADENCE_SIM_EXAMPLES;
  const std::string script = "start --headless\n"
                             "wait-for route:home 15000\n"
                             "send-json " + ex + "/alarms.json\n"
                             "wait 1200\n"
                             "screenshot " + dir + "/home.png\n"
                             "send-json " + ex + "/alarms-ring.json\n"
                             "wait 800\n"
                             "screenshot " + dir + "/ring.png\n"
                             "press BACK\n"
                             "wait 400\n"
                             "screenshot " + dir + "/back.png\n"
                             "press OK\n"
                             "wait 800\n"
                             "rpc-calls alarms.act\n"
                             "screenshot " + dir + "/stopped.png\n"
                             "send {\"route\": {\"route\": \"alarms\", \"ref\": \"kitchen-midi\"}}\n"
                             "wait-for route:alarms 3000\n"
                             "wait 600\n"
                             "screenshot " + dir + "/page.png\n"
                             "press BACK\n"
                             "wait-for route:home 3000\n"
                             "send {\"display_power\": {\"on\": false}}\n"
                             "wait 300\n"
                             "send-json " + ex + "/alarms-ring-asleep.json\n"
                             "wait 800\n"
                             "screenshot " + dir + "/asleep.png\n"
                             "send {\"display_power\": {\"on\": true}}\n"
                             "wait 800\n"
                             "screenshot " + dir + "/woke.png\n"
                             "press OK\n"
                             "wait 800\n"
                             "send {\"display_power\": {\"on\": false}}\n"
                             "wait 300\n"
                             "send-json " + ex + "/alarms-ring-own.json\n"
                             "wait 800\n"
                             "screenshot " + dir + "/own.png\n"
                             "quit\n";
  FILE* out = std::fopen(scriptPath.c_str(), "w");
  if (out == nullptr) return fail("cannot write " + scriptPath);
  std::fputs(script.c_str(), out);
  std::fclose(out);

  const std::string command = std::string(CADENCE_SIM_BIN) + " --headless run " + scriptPath + " 2>/dev/null";
  FILE* run = ::popen(command.c_str(), "r");
  if (run == nullptr) return fail("cannot run the sim");
  std::string printed;
  char buf[512];
  while (std::fgets(buf, sizeof buf, run) != nullptr) printed += buf;
  const int code = ::pclose(run);
  if (code != 0) return fail("the run exited " + std::to_string(code));
  if (printed.find("rpc-calls alarms.act 1 ") == std::string::npos || printed.find("\"action\":\"stop\"") == std::string::npos)
    return fail("«Стоп» sent no alarms.act stop");

  const auto load = [&](const char* name, Image& image) { return readPng(dir + "/" + name, image) && image.width == 1280; };
  Image home, ring, back, stopped, page, asleep, woke, own;
  if (!load("home.png", home) || !load("ring.png", ring) || !load("back.png", back) || !load("stopped.png", stopped) ||
      !load("page.png", page) || !load("asleep.png", asleep) || !load("woke.png", woke) || !load("own.png", own))
    return fail("a screenshot is missing or not 1280x720");
  if (inkShare(home) > 0.6) return fail("home looks covered before any ring");
  if (inkShare(ring) < 0.9) return fail("no ring overlay for another device's ring on an awake screen");
  if (emberInButtons(ring) < 200) return fail("the ring's «Стоп» is not focused");
  if (inkShare(back) < 0.9) return fail("BACK took the ring overlay away");
  if (inkShare(stopped) > 0.6) return fail("the ring overlay stayed after «Стоп»");
  if (inkShare(page) > 0.9 || inkShare(page) < 0.3) return fail("the alarms page did not draw");
  if (inkShare(asleep) > 0.05) return fail("another device's ring woke the sleeping display");
  if (inkShare(woke) < 0.9) return fail("the ring still ringing did not show once the display was on");
  if (inkShare(own) < 0.9) return fail("this screen's own ring did not wake the display");
  std::printf("sim-alarms: ok\n");
  return 0;
}
