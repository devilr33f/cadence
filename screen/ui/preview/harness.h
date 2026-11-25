#pragma once

#include "i18n/translator.h"
#include "platform/platform.h"
#include "render/capture.h"
#include "render/gles-backend.h"
#include "ui/cards/host.h"
#include "ui/core/budget.h"
#include "ui/core/ui.h"
#include "ui/preview/gallery.h"

#include <memory>
#include <set>
#include <string>
#include <vector>

namespace cadence::screen::ui::preview {

struct HarnessOptions {
  int width = 960;
  int height = 540;
  i18n::Lang lang = i18n::Lang::Ru;
};

struct WalkResult {
  std::size_t focusable = 0;
  std::size_t visited = 0;
  std::vector<std::string> missed;
  [[nodiscard]] bool ok() const { return missed.empty(); }
};

// design r2 section 25's per-screen report
struct PageReport {
  std::string page;
  // the ui's work for one full redraw on llvmpipe (indicative only), milliseconds
  double frameMs = 0;
  int layers = 0;
  std::vector<std::string> layerNames;
  // the share of the screen that changes between frames at rest (spinners, the live dot) and while focus moves
  double animatedRestPct = 0;
  double animatedFocusPct = 0;
  int overflows = 0;
  std::vector<std::string> overflowing;
  int maxLayers = 2;
  double maxAnimatedPct = 30;
  [[nodiscard]] bool layersFlagged() const { return layers > maxLayers; }
  [[nodiscard]] bool animatedFlagged() const {
    return animatedRestPct > maxAnimatedPct || animatedFocusPct > maxAnimatedPct;
  }
};

// the gallery offscreen: sdl's offscreen driver on mesa (egl surfaceless, llvmpipe), A's GlesBackend, the ui at a
// synthetic clock, a card host whose link is a list
class Harness {
 public:
  explicit Harness(HarnessOptions options);
  ~Harness();
  Harness(const Harness&) = delete;
  Harness& operator=(const Harness&) = delete;

  [[nodiscard]] bool ok() const { return ok_; }
  [[nodiscard]] const std::string& error() const { return error_; }

  // the page, its setup, then 1.5 s of synthetic time for every entrance to finish
  void load(const Page& page);
  void step(int ms);
  // the key's down and up (a press), 50 ms apart
  void press(cadence_screen_v1_Key key);
  void key(cadence_screen_v1_Key key, cadence_screen_v1_KeyAction action);
  // a full redraw, read back
  render::Image capture();

  WalkResult walk();
  PageReport measure(const Page& page);

  Ui& ui() { return *ui_; }
  i18n::Translator& translator() { return translator_; }
  cards::CardHost& cards() { return *cards_; }
  std::vector<cards::ActionCall>& sent() { return sent_; }
  [[nodiscard]] std::int64_t nowMs() const { return now_; }

 private:
  void tick();
  void warmGradients();

  HarnessOptions options_;
  bool ok_ = false;
  std::string error_;
  std::unique_ptr<platform::Platform> platform_;
  std::unique_ptr<render::GlesBackend> backend_;
  i18n::Translator translator_;
  std::unique_ptr<Ui> ui_;
  std::unique_ptr<cards::CardHost> cards_;
  std::vector<cards::ActionCall> sent_;
  std::vector<Activation> activations_;
  std::int64_t now_ = kStartMs;
  std::int64_t loadedMs_ = kStartMs;
};

// the share of pixels whose channels differ by more than tolerance
double diffFraction(const render::Image& a, const render::Image& b, int tolerance = 8);
bool writePng(const std::string& path, const render::Image& image);
bool readPng(const std::string& path, render::Image& image);

}  // namespace cadence::screen::ui::preview
