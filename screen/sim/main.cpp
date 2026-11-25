// cadence-screen-sim: the tv's ui as a native macos (or linux) process, driven by a script. the real screen — the
// app's loop, the routed ui, the screen/v1 link — against a fake cadenced in-process; see screen/README.md
#include "sim/sim.h"

#include <cstdio>
#include <cstdlib>
#include <iostream>
#include <string>
#include <vector>

namespace {

const char* usage() {
  return "usage: cadence-screen-sim [--size WxH] [--lang ru|en] [--windowed] [--fixtures DIR] [--cards DIR]\n"
         "                      [run SCRIPT | -]\n"
         "                      (no script: commands on stdin, one a line)\n";
}

}  // namespace

int main(int argc, char** argv) {
  cadence::screen::sim::Sim::Options options;
#ifdef CADENCE_SIM_CARDS
  options.cardDir = CADENCE_SIM_CARDS;
#endif
  std::string script;
  std::vector<std::string> args(argv + 1, argv + argc);
  for (std::size_t i = 0; i < args.size(); i++) {
    const std::string& a = args[i];
    auto value = [&]() -> std::string { return i + 1 < args.size() ? args[++i] : ""; };
    if (a == "--help" || a == "-h") {
      std::fputs(usage(), stdout);
      return 0;
    }
    if (a == "--headless") {
      options.headless = true;
    } else if (a == "--windowed") {
      options.headless = false;
    } else if (a == "--lang") {
      options.language = value();
    } else if (a == "--size") {
      const std::string size = value();
      const auto x = size.find('x');
      options.width = x == std::string::npos ? 0 : std::atoi(size.c_str());
      options.height = x == std::string::npos ? 0 : std::atoi(size.c_str() + x + 1);
      if (options.width <= 0 || options.height <= 0) {
        std::fprintf(stderr, "cadence-screen-sim: bad --size %s\n%s", size.c_str(), usage());
        return 2;
      }
    } else if (a == "--fixtures") {
      options.fixturesDir = value();
    } else if (a == "--cards") {
      options.cardDir = value();
    } else if (a == "run") {
      script = value();
      if (script.empty()) {
        std::fprintf(stderr, "cadence-screen-sim: run needs a script path (or -)\n%s", usage());
        return 2;
      }
    } else {
      std::fprintf(stderr, "cadence-screen-sim: bad argument %s\n%s", a.c_str(), usage());
      return 2;
    }
  }
  return cadence::screen::sim::runSim(options, script);
}
