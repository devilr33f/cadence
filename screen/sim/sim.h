#pragma once

#include "sim/fake-daemon.h"
#include "sim/script.h"

#include <atomic>
#include <cstdint>
#include <functional>
#include <memory>
#include <string>
#include <thread>
#include <vector>

namespace cadence::screen::app {
class App;
class UiLayer;
}  // namespace cadence::screen::app

namespace cadence::screen::routes {
class Router;
}

namespace cadence::screen::ui::images {
class ImageCache;
}  // namespace cadence::screen::ui::images

namespace cadence::screen::sim {

// the simulator itself: the real screen (app::App with the routed ui and the placeholder player) against the fake
// cadenced, driven one command at a time from a script file or stdin. the runner and the ui share the main thread, so
// between commands the app steps exactly as its own loop would
class Sim {
 public:
  struct Options {
    int width = 1280;
    int height = 720;
    std::string language = "ru";
    bool headless = true;
    // the swap waits for vsync (a window's real pacing; headless swaps never wait)
    bool vsync = false;
    // images decode on the cache's workers, as on the device (the default decodes inline: screenshots never race)
    bool asyncImages = false;
    bool hud = false;
    std::string fixturesDir;
    std::string cardDir;
  };

  explicit Sim(Options options);
  ~Sim();
  Sim(const Sim&) = delete;
  Sim& operator=(const Sim&) = delete;

  // boots the screen and the fake, waits for the ui's first presented frame; false and a token in error
  bool start(std::string* error);
  void shutdown();

  // runs one command; false (with a line on stderr) when it fails. `quit` is a command like any other
  bool run(const Command& command);

  // steps the app for ms of wall time, in the loop's own slices
  void pump(int ms);
  // steps until pred holds or timeoutMs passed
  bool pumpFor(const std::function<bool()>& pred, int timeoutMs);

  [[nodiscard]] const Options& options() const { return options_; }

 private:
  bool startScreen(std::string* error);
  bool selectorHolds(const std::string& selector, std::string* error);
  void printContext();
  bool sendKey(const std::string& name, bool down);
  bool press(const Command& c);
  bool hold(const Command& c);
  bool screenshot(const Command& c);
  bool bands(const Command& c);
  bool pcm(const Command& c);
  bool perf(const Command& c);

  Options options_;
  FakeDaemon fake_;
  std::unique_ptr<ui::images::ImageCache> images_;
  std::unique_ptr<app::App> app_;
  app::UiLayer* ui_ = nullptr;
  routes::Router* router_ = nullptr;
  std::uint64_t pcmFrame_ = 0;
  // pcm-file: a track streamed into the visualizer feed in real time, as audiod's tap would
  bool pcmFile(const Command& c);
  void stopPcmFile();
  std::thread pcmThread_;
  std::atomic<bool> pcmRunning_{false};
  bool started_ = false;
};

// the process entry: runs the commands from the script file (or stdin when empty), start to quit
int runSim(const Sim::Options& options, const std::string& scriptPath);

}  // namespace cadence::screen::sim
