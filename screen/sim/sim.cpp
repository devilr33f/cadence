#include "sim/sim.h"

#include "app/app.h"
#include "app/options.h"
#include "app/routes/core/router.h"
#include "app/routes/preview/fake-catalog.h"
#include "app/ui-layer.h"
#include "sim/fake-player.h"
#include "sim/names.h"
#include "sim/png.h"
#include "sim/send.h"
#include "ui/core/shell.h"
#include "ui/images/image-cache.h"

#include <SDL3/SDL.h>

#include <unistd.h>

#include <array>
#include <chrono>
#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <filesystem>
#include <fstream>
#include <iostream>
#include <iterator>

namespace cadence::screen::sim {

namespace {

std::int64_t steadyMs() {
  return std::chrono::duration_cast<std::chrono::milliseconds>(std::chrono::steady_clock::now().time_since_epoch())
      .count();
}

std::int64_t steadyNs() {
  return std::chrono::duration_cast<std::chrono::nanoseconds>(std::chrono::steady_clock::now().time_since_epoch()).count();
}

// the keyboard stands for the remote, as the linux tests' do: the same sdl keycodes platform/keys maps to screen/v1
// keys, so the app's own KeyTracker makes the press, the long press and the assistant of a held BACK
std::optional<SDL_Keycode> sdlKey(const std::string& name) {
  const auto key = keyOf(name);
  if (!key) return std::nullopt;
  switch (*key) {
    case cadence_screen_v1_Key_KEY_UP:
      return SDLK_UP;
    case cadence_screen_v1_Key_KEY_DOWN:
      return SDLK_DOWN;
    case cadence_screen_v1_Key_KEY_LEFT:
      return SDLK_LEFT;
    case cadence_screen_v1_Key_KEY_RIGHT:
      return SDLK_RIGHT;
    case cadence_screen_v1_Key_KEY_OK:
      return SDLK_RETURN;
    case cadence_screen_v1_Key_KEY_BACK:
      return SDLK_ESCAPE;
    case cadence_screen_v1_Key_KEY_HOME:
      return SDLK_AC_HOME;
    case cadence_screen_v1_Key_KEY_MENU:
      return SDLK_MENU;
    case cadence_screen_v1_Key_KEY_ASSISTANT:
      return SDLK_G;
    case cadence_screen_v1_Key_KEY_PLAY_PAUSE:
      return SDLK_MEDIA_PLAY_PAUSE;
    case cadence_screen_v1_Key_KEY_PLAY:
      return SDLK_MEDIA_PLAY;
    case cadence_screen_v1_Key_KEY_PAUSE:
      return SDLK_MEDIA_PAUSE;
    case cadence_screen_v1_Key_KEY_STOP:
      return SDLK_MEDIA_STOP;
    case cadence_screen_v1_Key_KEY_NEXT:
      return SDLK_MEDIA_NEXT_TRACK;
    case cadence_screen_v1_Key_KEY_PREVIOUS:
      return SDLK_MEDIA_PREVIOUS_TRACK;
    case cadence_screen_v1_Key_KEY_FAST_FORWARD:
      return SDLK_MEDIA_FAST_FORWARD;
    case cadence_screen_v1_Key_KEY_REWIND:
      return SDLK_MEDIA_REWIND;
    case cadence_screen_v1_Key_KEY_VOLUME_UP:
      return SDLK_VOLUMEUP;
    case cadence_screen_v1_Key_KEY_VOLUME_DOWN:
      return SDLK_VOLUMEDOWN;
    case cadence_screen_v1_Key_KEY_VOLUME_MUTE:
      return SDLK_MUTE;
    case cadence_screen_v1_Key_KEY_POWER:
      return SDLK_POWER;
    default:
      return std::nullopt;
  }
}

void pushKey(SDL_Keycode key, bool down) {
  SDL_Event e{};
  e.type = down ? SDL_EVENT_KEY_DOWN : SDL_EVENT_KEY_UP;
  e.key.key = key;
  e.key.down = down;
  e.key.repeat = false;
  SDL_PushEvent(&e);
}

}  // namespace

Sim::Sim(Options options) : options_(std::move(options)), fake_(FakeDaemon::Options{}) {}

Sim::~Sim() {
  stopPcmFile();
  shutdown();
}

bool Sim::start(std::string* error) {
  if (started_) return true;
  if (!options_.fixturesDir.empty() && !fake_.loadFixtures(options_.fixturesDir, error)) return false;
  fake_.setLanguage(options_.language);
  fake_.start();
  if (!startScreen(error)) return false;
  started_ = true;
  // the boot sequence answers the screen's Hello; the first page is drawn once the ui laid it out
  if (!pumpFor([this] { return fake_.helloSeen() && ui_ != nullptr && ui_->ready() && app_->presented() > 0; },
               20000)) {
    if (error != nullptr) *error = "boot";
    return false;
  }
  return true;
}

bool Sim::startScreen(std::string* error) {
  // the routed ui the device runs, with the fake's painted artwork instead of the server's proxy
  auto fetcher = std::make_shared<routes::fake::FakeImages>();
  ui::images::CacheOptions cacheOptions;
  cacheOptions.inlineDecode = !options_.asyncImages;  // a screenshot never races a fetch
  images_ = std::make_unique<ui::images::ImageCache>(std::move(fetcher), cacheOptions);
  auto shell = std::make_unique<ui::ShellLayer>();
  routes::Router::Options routerOptions;
  routerOptions.images = images_.get();
  auto router = std::make_unique<routes::Router>(std::move(routerOptions));
  routes::registerBrowseRoutes(*router);
  routes::registerSystemRoutes(*router);
  routes::registerPlaybackRoutes(*router);
  router_ = router.get();
  shell->setPages(std::move(router));
  ui_ = shell.get();

  app::AppOptions o;
  o.socket = fake_.path();
  o.platform.width = options_.width;
  o.platform.height = options_.height;
  o.platform.vsync = options_.vsync;
  o.hud = options_.hud;
  app::applyPerfEnv(o, std::getenv("CADENCE_SCREEN_PERF"), std::getenv("CADENCE_SCREEN_HUD"));
  o.platform.hidden = options_.headless;
  app_ = std::make_unique<app::App>(std::move(o), std::move(shell), [](render::RenderBackend& backend) {
    return std::make_unique<FakePlayer>(backend);
  });
  return app_->start(error);
}

void Sim::shutdown() {
  started_ = false;
  app_.reset();
  ui_ = nullptr;
  router_ = nullptr;
  images_.reset();
  fake_.stop();
}

void Sim::pump(int ms) {
  if (app_ == nullptr) return;
  const auto until = steadyMs() + ms;
  // the loop's own sleep, bounded by the wait's end: an idle screen wakes as rarely here as on the device (the
  // perf command's turns a second), and the fake's messages and the keys still wake it at once
  for (auto now = steadyMs(); now < until; now = steadyMs()) {
    if (!app_->step(static_cast<int>(std::min<std::int64_t>(250, until - now)))) return;
  }
}

bool Sim::pumpFor(const std::function<bool()>& pred, int timeoutMs) {
  const auto until = steadyMs() + timeoutMs;
  while (steadyMs() < until) {
    if (pred()) return true;
    if (app_ != nullptr && !app_->step(5)) return pred();
  }
  return pred();
}

bool Sim::sendKey(const std::string& name, bool down) {
  const auto key = sdlKey(name);
  if (!key) return false;
  pushKey(*key, down);
  return true;
}

bool Sim::press(const Command& c) {
  if (!sendKey(c.args[0], true)) {
    std::fprintf(stderr, "cadence-screen-sim: %s: unknown key %s\n", c.where().c_str(), c.args[0].c_str());
    return false;
  }
  pump(70);
  sendKey(c.args[0], false);
  pump(50);
  return true;
}

bool Sim::hold(const Command& c) {
  char* end = nullptr;
  const long ms = std::strtol(c.args[1].c_str(), &end, 10);
  if (end == nullptr || *end != '\0' || ms < 0 || ms > 60000) {
    std::fprintf(stderr, "cadence-screen-sim: %s: bad hold time %s\n", c.where().c_str(), c.args[1].c_str());
    return false;
  }
  if (!sendKey(c.args[0], true)) {
    std::fprintf(stderr, "cadence-screen-sim: %s: unknown key %s\n", c.where().c_str(), c.args[0].c_str());
    return false;
  }
  // a real hold: the app's own KeyTracker turns it into the long press (BACK past 500 ms is the assistant)
  pump(static_cast<int>(ms));
  sendKey(c.args[0], false);
  pump(50);
  return true;
}

bool Sim::screenshot(const Command& c) {
  auto image = app_->capture();
  if (image) {
    // the path's dir, when it has one that does not exist yet
    std::error_code ec;
    const auto parent = std::filesystem::path(c.args[0]).parent_path();
    if (!parent.empty()) std::filesystem::create_directories(parent, ec);
  }
  if (!image || !writePng(c.args[0], *image)) {
    std::fprintf(stderr, "cadence-screen-sim: %s: cannot write %s\n", c.where().c_str(), c.args[0].c_str());
    return false;
  }
  std::printf("screenshot %s %dx%d\n", c.args[0].c_str(), image->width, image->height);
  return true;
}

bool Sim::bands(const Command& c) {
  std::array<std::uint8_t, 16> values{};
  for (std::size_t i = 0; i < values.size(); ++i) {
    char* end = nullptr;
    const long value = std::strtol(c.args[i].c_str(), &end, 10);
    if (end == nullptr || *end != '\0' || value < 0 || value > 255) {
      std::fprintf(stderr, "cadence-screen-sim: %s: bad band %s\n", c.where().c_str(), c.args[i].c_str());
      return false;
    }
    values[i] = static_cast<std::uint8_t>(value);
  }
  auto* feed = router_ != nullptr ? router_->vizFeed() : nullptr;
  if (feed == nullptr) {
    std::fprintf(stderr, "cadence-screen-sim: %s: no visualizer feed\n", c.where().c_str());
    return false;
  }
  feed->bands(values);
  return true;
}

bool Sim::pcm(const Command& c) {
  constexpr std::size_t frames = 512;
  constexpr std::uint32_t rate = 48000;
  std::int64_t monoNs = 0;
  if (c.args.empty() || c.args[0] == "now") {
    monoNs = steadyNs() - static_cast<std::int64_t>(frames) * 1'000'000'000LL / rate;
  } else {
    char* end = nullptr;
    monoNs = std::strtoll(c.args[0].c_str(), &end, 10);
    if (end == nullptr || *end != '\0' || monoNs < 0) {
      std::fprintf(stderr, "cadence-screen-sim: %s: bad pcm timestamp %s\n", c.where().c_str(), c.args[0].c_str());
      return false;
    }
  }
  // one audiod-sized stereo snapshot; successive commands advance the tone, including the untimestamped cast case
  std::vector<float> samples(frames * 2);
  for (std::size_t i = 0; i < frames; ++i) {
    const auto at = static_cast<double>(pcmFrame_ + i);
    const float left = static_cast<float>(0.25 * std::sin(2.0 * 3.14159265358979323846 * 440.0 * at / rate));
    samples[2 * i] = left;
    samples[2 * i + 1] = static_cast<float>(0.2 * std::sin(2.0 * 3.14159265358979323846 * 660.0 * at / rate));
  }
  pcmFrame_ += frames;
  auto* feed = router_ != nullptr ? router_->vizFeed() : nullptr;
  if (feed == nullptr) {
    std::fprintf(stderr, "cadence-screen-sim: %s: no visualizer feed\n", c.where().c_str());
    return false;
  }
  feed->pcm(samples, 2, rate, monoNs);
  return true;
}

void Sim::stopPcmFile() {
  pcmRunning_ = false;
  if (pcmThread_.joinable()) pcmThread_.join();
}

bool Sim::pcmFile(const Command& c) {
  stopPcmFile();
  std::FILE* f = std::fopen(c.args[0].c_str(), "rb");
  if (f == nullptr) {
    std::fprintf(stderr, "cadence-screen-sim: %s: no pcm file %s\n", c.where().c_str(), c.args[0].c_str());
    return false;
  }
  std::vector<float> pcm;
  float buf[4096];
  for (std::size_t n; (n = std::fread(buf, sizeof(float), 4096, f)) > 0;) pcm.insert(pcm.end(), buf, buf + n);
  std::fclose(f);
  auto* feed = router_ != nullptr ? router_->vizFeed() : nullptr;
  if (feed == nullptr || pcm.size() < 4096) {
    std::fprintf(stderr, "cadence-screen-sim: %s: no visualizer feed or an empty file\n", c.where().c_str());
    return false;
  }
  pcmRunning_ = true;
  pcmThread_ = std::thread([this, feed, pcm = std::move(pcm)] {
    // 2048-frame snapshots every 16 ms, stamped with when their first frame sounds (the tap's own shape)
    constexpr std::size_t kFrames = 2048;
    const auto start = steadyNs();
    const std::size_t total = pcm.size() / 2;
    while (pcmRunning_) {
      const auto now = steadyNs();
      const auto heard = static_cast<std::size_t>((now - start) / 1000 * 48 / 1000) % total;
      const std::size_t first = heard >= kFrames ? heard - kFrames : 0;
      std::vector<float> snap(pcm.begin() + static_cast<std::ptrdiff_t>(first * 2), pcm.begin() + static_cast<std::ptrdiff_t>(heard * 2));
      if (!snap.empty()) {
        const auto monoNs = now - static_cast<std::int64_t>(heard - first) * 1'000'000'000LL / 48000;
        feed->pcm(snap, 2, 48000, monoNs);
      }
      std::this_thread::sleep_for(std::chrono::milliseconds(16));
    }
  });
  return true;
}

bool Sim::selectorHolds(const std::string& selector, std::string* error) {
  const auto cut = selector.find(':');
  const std::string what = cut == std::string::npos ? selector : selector.substr(0, cut);
  const std::string value = cut == std::string::npos ? "" : selector.substr(cut + 1);
  if (what == "ready") return ui_ != nullptr && ui_->ready();
  if (what == "link") return fake_.linked();
  if (what == "drawer") return ui_ != nullptr && ui_->status().drawerOpen;
  if (what == "route") {
    if (auto route = routeOf(value)) return ui_ != nullptr && ui_->status().route == *route;
    if (error != nullptr) *error = "unknown route " + value;
    return false;
  }
  if (what == "media") {
    if (value != "playing" && value != "paused" && value != "gated" && value != "idle" && value != "buffering") {
      if (error != nullptr) *error = "unknown media state " + value;
      return false;
    }
    return fake_.lastMediaState() == value;
  }
  if (what == "cast") return fake_.lastCastCommand() == value;
  if (router_ == nullptr) return false;
  const auto context = router_->computeContext();
  if (what == "focus") return context.focusedRef == value;
  if (what == "pageref") return context.pageRef == value;
  if (what == "query") return context.query == value;
  if (error != nullptr) *error = "unknown selector " + what + " (ready, link, drawer, route:, focus:, pageref:, query:, media:, cast:)";
  return false;
}

void Sim::printContext() {
  if (router_ == nullptr) {
    std::printf("context -\n");
    return;
  }
  const auto c = router_->computeContext();
  std::printf("context route=%s page_ref=%s focused_ref=%s query=%s drawer=%s", routeName(c.route).c_str(),
              c.pageRef.c_str(), c.focusedRef.c_str(), c.query.c_str(), c.drawerOpen ? "open" : "closed");
  // where the bar is, as the routes show it: a seek's checks read it
  if (c.hasPlayer) std::printf(" position_ms=%llu", static_cast<unsigned long long>(c.positionMs));
  for (const auto& ref : c.visibleRefs) std::printf(" %s", ref.c_str());
  std::printf("\n");
}

bool Sim::run(const Command& c) {
  if (c.is("start")) {
    if (started_) {
      std::fprintf(stderr, "cadence-screen-sim: %s: already started\n", c.where().c_str());
      return false;
    }
    for (std::size_t i = 0; i < c.args.size(); i++) {
      const std::string& a = c.args[i];
      auto value = [&]() -> std::string { return i + 1 < c.args.size() ? c.args[++i] : ""; };
      if (a == "--headless") {
        options_.headless = true;
      } else if (a == "--windowed") {
        options_.headless = false;
      } else if (a == "--vsync") {
        options_.vsync = true;
      } else if (a == "--async-images") {
        options_.asyncImages = true;
      } else if (a == "--hud") {
        options_.hud = true;
      } else if (a == "--lang") {
        options_.language = value();
      } else if (a == "--size") {
        const std::string size = value();
        const auto x = size.find('x');
        if (x == std::string::npos) {
          std::fprintf(stderr, "cadence-screen-sim: %s: bad --size %s\n", c.where().c_str(), size.c_str());
          return false;
        }
        options_.width = std::atoi(size.c_str());
        options_.height = std::atoi(size.c_str() + x + 1);
        if (options_.width <= 0 || options_.height <= 0) {
          std::fprintf(stderr, "cadence-screen-sim: %s: bad --size %s\n", c.where().c_str(), size.c_str());
          return false;
        }
      } else {
        std::fprintf(stderr, "cadence-screen-sim: %s: unknown flag %s\n", c.where().c_str(), a.c_str());
        return false;
      }
    }
    std::string error;
    if (!start(&error)) {
      std::fprintf(stderr, "cadence-screen-sim: %s: start failed (%s)\n", c.where().c_str(), error.c_str());
      return false;
    }
    std::printf("started %dx%d lang=%s %s\n", options_.width, options_.height, fake_.language().c_str(),
                options_.headless ? "headless" : "windowed");
    return true;
  }

  if (c.is("quit")) {
    shutdown();
    return true;
  }

  // before start too: a method the screen calls at boot (a subscription at the link's start) answers it
  if (c.is("rpc-answer")) {
    std::string error;
    auto answer = parseAnswer(c.args[1], &error);
    if (!answer) {
      std::fprintf(stderr, "cadence-screen-sim: %s: %s\n", c.where().c_str(), error.c_str());
      return false;
    }
    fake_.setAnswer(c.args[0], std::move(*answer));
    return true;
  }

  if (!started_) {
    std::fprintf(stderr, "cadence-screen-sim: %s: the screen is not started\n", c.where().c_str());
    return false;
  }

  if (c.is("press")) return press(c);
  if (c.is("hold")) return hold(c);
  if (c.is("wait")) {
    char* end = nullptr;
    const long ms = std::strtol(c.args[0].c_str(), &end, 10);
    if (end == nullptr || *end != '\0' || ms < 0 || ms > 600000) {
      std::fprintf(stderr, "cadence-screen-sim: %s: bad wait %s\n", c.where().c_str(), c.args[0].c_str());
      return false;
    }
    pump(static_cast<int>(ms));
    return true;
  }
  if (c.is("wait-for")) {
    std::string error;
    const int timeout = c.args.size() > 1 ? std::atoi(c.args[1].c_str()) : 10000;
    if (timeout <= 0) {
      std::fprintf(stderr, "cadence-screen-sim: %s: bad timeout %s\n", c.where().c_str(), c.args[1].c_str());
      return false;
    }
    const bool held = pumpFor([&] { return selectorHolds(c.args[0], &error); }, timeout);
    if (!held) {
      std::fprintf(stderr, "cadence-screen-sim: %s: %s after %d ms\n", c.where().c_str(),
                   (error.empty() ? std::string("not reached: ") + c.args[0] : error).c_str(), timeout);
      return false;
    }
    return true;
  }
  if (c.is("screenshot")) return screenshot(c);
  if (c.is("bands")) return bands(c);
  if (c.is("pcm")) return pcm(c);
  if (c.is("pcm-file")) return pcmFile(c);
  if (c.is("pcm-stop")) {
    stopPcmFile();
    return true;
  }
  if (c.is("route")) {
    std::printf("route %s\n", ui_ != nullptr ? routeName(ui_->status().route).c_str() : "-");
    return true;
  }
  if (c.is("focus")) {
    const auto context = router_ != nullptr ? router_->computeContext() : routes::ScreenContextData{};
    std::printf("focus %s\n", context.focusedRef.empty() ? "-" : context.focusedRef.c_str());
    return true;
  }
  if (c.is("context")) {
    printContext();
    return true;
  }
  if (c.is("send") || c.is("send-json")) {
    std::string error;
    std::string text = c.args[0];
    if (c.is("send-json")) {
      std::ifstream in(c.args[0], std::ios::binary);
      if (!in) {
        std::fprintf(stderr, "cadence-screen-sim: %s: cannot read %s\n", c.where().c_str(), c.args[0].c_str());
        return false;
      }
      text.assign(std::istreambuf_iterator<char>(in), {});
    }
    // alarms.act's answers edit the summary a script sent last
    if (const auto j = routes::Json::parse(text, nullptr, false); j.is_object() && j.contains("alarms_summary")) {
      fake_.rememberAlarms(j["alarms_summary"]);
    }
    auto built = buildSend(text, options_.cardDir, &error);
    if (!built) {
      std::fprintf(stderr, "cadence-screen-sim: %s: %s\n", c.where().c_str(), error.c_str());
      return false;
    }
    fake_.sendBuilt([&built](ipc::OutEnvelope& out) { out = std::move(built->envelope); });
    return true;
  }
  if (c.is("send-file")) {
    std::ifstream in(c.args[0], std::ios::binary);
    if (!in) {
      std::fprintf(stderr, "cadence-screen-sim: %s: cannot read %s\n", c.where().c_str(), c.args[0].c_str());
      return false;
    }
    const std::string bytes{std::istreambuf_iterator<char>(in), {}};
    fake_.sendRaw(reinterpret_cast<const std::uint8_t*>(bytes.data()), bytes.size());
    return true;
  }
  if (c.is("rpc-calls")) {
    const auto [n, last] = fake_.calls(c.args[0]);
    std::printf("rpc-calls %s %d %s\n", c.args[0].c_str(), n, last.c_str());
    std::fflush(stdout);
    return true;
  }
  if (c.is("perf")) return perf(c);
  if (c.is("drop-link")) {
    fake_.dropLink();
    pump(150);
    return true;
  }
  std::fprintf(stderr, "cadence-screen-sim: %s: unknown verb\n", c.where().c_str());
  return false;
}

bool Sim::perf(const Command& c) {
  if (!c.args.empty() && c.args[0] == "reset") {
    app_->resetPerf();
    return true;
  }
  using render::perf::Phase;
  using render::perf::Span;
  const auto s = app_->perfSummary();
  const auto ph = [&](Phase p) { return s.phaseMs[static_cast<std::size_t>(p)]; };
  const auto sp = [&](Span x) { return static_cast<std::size_t>(x); };
  std::printf("perf %s s=%.1f frames=%llu fps=%.1f anim=%llu anim_fps=%.1f late=%llu turns_per_s=%.0f idle_turns=%llu"
              " cpu_p50=%.2f cpu_p95=%.2f cpu_p99=%.2f cpu_max=%.2f frame_p95=%.2f update_p50=%.2f update_p95=%.2f"
              " rml_update_p95=%.2f draw_p50=%.2f draw_p95=%.2f rml_render_p95=%.2f swap_p95=%.2f uploads=%llu"
              " upload_ms=%.1f decode_ms=%.1f image_scan_ms=%.1f rml_update_ms=%.1f rml_render_ms=%.1f"
              " draws_p50=%.0f draws_p95=%.0f clips_p95=%.0f layers_p95=%.0f fill_mpx_p50=%.1f fill_mpx_p95=%.1f"
              " stencil_clear_mpx_p95=%.1f gradients_p95=%.0f gradient_mpx_p95=%.1f\n",
              c.args.empty() ? "-" : c.args[0].c_str(), s.seconds, static_cast<unsigned long long>(s.frames), s.fps,
              static_cast<unsigned long long>(s.chained), s.chainedFps, static_cast<unsigned long long>(s.late),
              s.turnsPerS, static_cast<unsigned long long>(s.idleTurns), s.cpuMs.p50, s.cpuMs.p95, s.cpuMs.p99,
              s.cpuMs.max, s.frameMs.p95, ph(Phase::Update).p50, ph(Phase::Update).p95,
              s.spanMs[sp(Span::RmlUpdate)].p95, ph(Phase::Draw).p50, ph(Phase::Draw).p95,
              s.spanMs[sp(Span::RmlRender)].p95, ph(Phase::Swap).p95,
              static_cast<unsigned long long>(s.spanCount[sp(Span::Upload)]), s.spanTotalMs[sp(Span::Upload)],
              s.spanTotalMs[sp(Span::Decode)], s.spanTotalMs[sp(Span::ImageScan)], s.spanTotalMs[sp(Span::RmlUpdate)],
              s.spanTotalMs[sp(Span::RmlRender)], s.counters[0].p50, s.counters[0].p95, s.counters[1].p95,
              s.counters[2].p95, s.counters[3].p50 / 1000.0, s.counters[3].p95 / 1000.0,
              s.counters[4].p95 / 1000.0, s.counters[5].p95, s.counters[6].p95 / 1000.0);
  // the worst frames, phase by phase (ms) and what they drew
  for (const auto& t : app_->perfRecorder().worst(3)) {
    std::printf("perf-worst %s", c.args.empty() ? "-" : c.args[0].c_str());
    for (std::size_t i = 0; i < render::perf::kPhases; ++i) {
      std::printf(" %s=%.2f", render::perf::phaseName(static_cast<Phase>(i)), static_cast<double>(t.ns[i]) / 1e6);
    }
    for (std::size_t i = 0; i < t.spans.size(); ++i) {
      if (t.spans[i].count == 0) continue;
      std::printf(" %s=%.2f/%u", render::perf::spanName(static_cast<Span>(i)), static_cast<double>(t.spans[i].ns) / 1e6,
                  t.spans[i].count);
    }
    std::printf(" draws=%llu\n", static_cast<unsigned long long>(t.counters[0]));
  }
  std::fflush(stdout);
  return true;
}

int runSim(const Sim::Options& options, const std::string& scriptPath) {
  Sim sim(options);
  auto fail = [&](const std::string& what) {
    std::fprintf(stderr, "cadence-screen-sim: %s\n", what.c_str());
    return 1;
  };
  auto joined = [](const Command& c) {
    std::string out;
    for (const auto& a : c.args) out += (out.empty() ? "" : " ") + a;
    return out;
  };
  if (!scriptPath.empty() && scriptPath != "-") {
    std::ifstream in(scriptPath, std::ios::binary);
    if (!in) return fail("cannot read " + scriptPath);
    const std::string text{std::istreambuf_iterator<char>(in), {}};
    std::string error;
    auto commands = parseScript(text, &error);
    if (!commands) return fail(error);
    for (const auto& c : *commands) {
      std::printf("> %s %s\n", c.verb.c_str(), joined(c).c_str());
      if (!sim.run(c)) return 1;
      if (c.is("quit")) return 0;
    }
    sim.shutdown();
    return 0;
  }
  // stdin, line by line: one command a line, interactive or piped
  const bool interactive = ::isatty(0) != 0;
  std::string line;
  for (;;) {
    if (interactive) {
      std::printf("> ");
      std::fflush(stdout);
    }
    if (!std::getline(std::cin, line)) break;
    std::string error;
    auto commands = parseScript(line, &error);
    if (!commands || commands->empty()) {
      if (!error.empty()) std::fprintf(stderr, "cadence-screen-sim: %s\n", error.c_str());
      continue;
    }
    for (const auto& c : *commands) {
      if (!sim.run(c)) return 1;
      if (c.is("quit")) return 0;
    }
  }
  sim.shutdown();
  return 0;
}

}  // namespace cadence::screen::sim
