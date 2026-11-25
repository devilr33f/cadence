#include "app/app.h"
#include "app/options.h"
#include "app/routes/core/router.h"

#include <cstdio>
#include <cstdlib>
#include <string>
#include <vector>

int main(int argc, char** argv) {
  std::vector<std::string> args(argv + 1, argv + argc);
  std::string error;
  auto options = cadence::screen::app::parseOptions(args, std::getenv("CADENCE_SCREEN_SOCKET"), &error);
  if (!options) {
    std::fprintf(stderr, "cadence-screen: bad argument %s\n%s", error.c_str(), cadence::screen::app::usage());
    return 2;
  }
  if (options->help) {
    std::fputs(cadence::screen::app::usage(), stdout);
    return 0;
  }
  cadence::screen::app::applyPerfEnv(*options, std::getenv("CADENCE_SCREEN_PERF"), std::getenv("CADENCE_SCREEN_HUD"));
  cadence::screen::app::App app(std::move(*options), cadence::screen::routes::makeRoutedUi());
  return app.run();
}
