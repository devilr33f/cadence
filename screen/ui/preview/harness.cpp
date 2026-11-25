#include "ui/preview/harness.h"

#include "render/gl.h"

#if defined(__GNUC__)
#pragma GCC diagnostic push
#pragma GCC diagnostic ignored "-Wconversion"
#pragma GCC diagnostic ignored "-Wsign-conversion"
#pragma GCC diagnostic ignored "-Wshadow"
#pragma GCC diagnostic ignored "-Wmissing-field-initializers"
#endif
#define STB_IMAGE_WRITE_IMPLEMENTATION
#include <stb/stb_image_write.h>
#define STBI_NO_STDIO
#include <stb/stb_image.h>
#if defined(__GNUC__)
#pragma GCC diagnostic pop
#endif

#include <algorithm>
#include <chrono>
#include <cstdlib>
#include <fstream>
#include <iterator>
#include <map>

namespace cadence::screen::ui::preview {

namespace {

std::string nameOf(Rml::Element* e) {
  if (!e->GetId().empty()) return "#" + e->GetId();
  std::string path;
  for (auto* p = e; p != nullptr && path.size() < 120; p = p->GetParentNode()) {
    const auto cls = p->GetClassNames();
    path = (cls.empty() ? p->GetTagName() : cls.substr(0, cls.find(' '))) + (path.empty() ? "" : ">" + path);
    if (!p->GetId().empty()) break;
  }
  return path;
}

}  // namespace

double diffFraction(const render::Image& a, const render::Image& b, int tolerance) {
  if (a.width != b.width || a.height != b.height || a.rgba.empty()) return 1.0;
  std::size_t differ = 0;
  const std::size_t n = a.rgba.size() / 4;
  for (std::size_t i = 0; i < n; ++i) {
    for (int c = 0; c < 4; ++c) {
      const int d = static_cast<int>(a.rgba[i * 4 + static_cast<std::size_t>(c)]) -
                    static_cast<int>(b.rgba[i * 4 + static_cast<std::size_t>(c)]);
      if (d > tolerance || -d > tolerance) {
        ++differ;
        break;
      }
    }
  }
  return static_cast<double>(differ) / static_cast<double>(n);
}

bool writePng(const std::string& path, const render::Image& image) {
  return stbi_write_png(path.c_str(), image.width, image.height, 4, image.rgba.data(), image.width * 4) != 0;
}

bool readPng(const std::string& path, render::Image& image) {
  std::ifstream in(path, std::ios::binary);
  if (!in) return false;
  const std::string bytes{std::istreambuf_iterator<char>(in), {}};
  int w = 0, h = 0, n = 0;
  auto* data = stbi_load_from_memory(reinterpret_cast<const stbi_uc*>(bytes.data()), static_cast<int>(bytes.size()), &w,
                                     &h, &n, 4);
  if (data == nullptr) return false;
  image.width = w;
  image.height = h;
  image.rgba.assign(data, data + static_cast<std::size_t>(w) * static_cast<std::size_t>(h) * 4);
  stbi_image_free(data);
  return true;
}

Harness::Harness(HarnessOptions options) : options_(options), translator_(options.lang) {
  platform::PlatformOptions o;
  o.videoDriver = "offscreen";
  o.width = options.width;
  o.height = options.height;
  o.vsync = false;
  platform_ = platform::Platform::create(o, &error_);
  if (!platform_) return;
  backend_ = std::make_unique<render::GlesBackend>();
  if (!backend_->ok()) {
    error_ = "backend";
    return;
  }
  ui_ = std::make_unique<Ui>(translator_);
  cards_ = std::make_unique<cards::CardHost>(*ui_, [this](const cards::ActionCall& c) {
    sent_.push_back(c);
    return true;
  });
  ui_->onActivate([this](const Activation& a) {
    if (!cards_->activate(a)) activations_.push_back(a);
  });
  ui_->update(now_);
  const render::Size size{options.width, options.height};
  ok_ = ui_->attach(*backend_, size, std::min(options.width / 1920.0, options.height / 1080.0), &error_);
  if (ok_) warmGradients();
}

void Harness::warmGradients() {
  // rmlui keeps a shader decorator's element data (every gradient) in a pool made on first use, a function's static.
  // made here, before a harness held in a static is whole, the pool outlives it: a page with a gradient still loaded
  // at exit is released into a pool that is still there, not one the exit already destroyed
  ui_->setPage([] { return std::string("<div style=\"width: 8dp; height: 8dp; decorator: linear-gradient(#000, #fff);\"/>"); });
  tick();
  (void)capture();
}

Harness::~Harness() {
  cards_.reset();
  if (ui_) ui_->detach();
  ui_.reset();
  backend_.reset();
  platform_.reset();
}

void Harness::tick() {
  ui_->update(now_);
  cards_->tick(kCardEpochMs + (now_ - loadedMs_));
}

void Harness::step(int ms) {
  // 16 ms frames, as the device would update
  for (int t = 0; t < ms; t += 16) {
    now_ += std::min(16, ms - t);
    tick();
  }
}

void Harness::load(const Page& page) {
  // every page starts at the same phase of the always-on loops the ui drives (spinner frames, the skeleton pulse),
  // and its cards' clock at the gallery's epoch, so a page draws the same frame each time it is loaded
  constexpr std::int64_t kPhase = 67200;
  now_ = (now_ / kPhase + 1) * kPhase;
  loadedMs_ = now_;
  sent_.clear();
  activations_.clear();
  cards_->clear();
  ui_->setReducedMotion(false);
  while (ui_->closeLevel()) {
  }
  ui_->clearToasts();
  ui_->setPage(page.body);
  step(48);
  if (page.setup) {
    Context c{*ui_, *cards_, sent_};
    page.setup(c);
  }
  step(1500);
}

void Harness::key(cadence_screen_v1_Key k, cadence_screen_v1_KeyAction action) {
  platform::KeyEvent e;
  e.key = k;
  e.action = action;
  ui_->key(e);
}

void Harness::press(cadence_screen_v1_Key k) {
  key(k, cadence_screen_v1_KeyAction_KEY_ACTION_DOWN);
  step(16);
  key(k, k == cadence_screen_v1_Key_KEY_BACK ? cadence_screen_v1_KeyAction_KEY_ACTION_PRESS
                                              : cadence_screen_v1_KeyAction_KEY_ACTION_UP);
  step(34);
}

render::Image Harness::capture() {
  const render::Size size{options_.width, options_.height};
  backend_->beginFrame(size);
  ui_->markDirty();
  ui_->render(*backend_, {0, 0, size.width, size.height});
  return render::readPixels(size);
}

WalkResult Harness::walk() {
  WalkResult r;
  const auto all = ui_->focus().candidates();
  r.focusable = all.size();
  // reachability by the d-pad alone: from the page's first focus, every node is put in focus and each of the four
  // keys pressed; whatever focus lands on joins the search (row memory and anchored scrolling included)
  std::vector<Rml::Element*> queue;
  std::set<Rml::Element*> seen;
  if (auto* start = ui_->focus().current()) {
    queue.push_back(start);
    seen.insert(start);
  }
  for (std::size_t i = 0; i < queue.size(); ++i) {
    for (const auto dir : {cadence_screen_v1_Key_KEY_LEFT, cadence_screen_v1_Key_KEY_RIGHT, cadence_screen_v1_Key_KEY_UP,
                           cadence_screen_v1_Key_KEY_DOWN}) {
      ui_->focus().focus(queue[i]);
      step(16);
      key(dir, cadence_screen_v1_KeyAction_KEY_ACTION_DOWN);
      key(dir, cadence_screen_v1_KeyAction_KEY_ACTION_UP);
      step(16);
      // down past the last row goes into a toast with a button: the walk is the page's, it comes back out
      if (ui_->toastFocused()) {
        ui_->leaveToast();
        step(16);
        continue;
      }
      auto* now = ui_->focus().current();
      if (now != nullptr && seen.insert(now).second) queue.push_back(now);
    }
  }
  for (auto* e : all) {
    if (seen.count(e) != 0) {
      r.visited++;
    } else {
      r.missed.push_back(nameOf(e));
    }
  }
  step(300);
  return r;
}

PageReport Harness::measure(const Page& page) {
  PageReport r;
  r.page = page.name;
  r.maxLayers = page.maxLayers;
  r.maxAnimatedPct = page.maxAnimatedPct;
  load(page);
  // the work of one full redraw, glFinish included, the best of five (llvmpipe: indicative only)
  double best = 1e9;
  for (int i = 0; i < 5; ++i) {
    const auto t0 = std::chrono::steady_clock::now();
    const render::Size size{options_.width, options_.height};
    backend_->beginFrame(size);
    ui_->markDirty();
    ui_->update(now_);
    ui_->render(*backend_, {0, 0, size.width, size.height});
    glFinish();
    const auto t1 = std::chrono::steady_clock::now();
    best = std::min(best, std::chrono::duration<double, std::milli>(t1 - t0).count());
  }
  r.frameMs = best;
  const auto layers = analyseLayers(*ui_->context());
  r.layers = layers.translucentLayers;
  r.layerNames = layers.layers;
  r.overflows = layers.overflows;
  r.overflowing = layers.overflowing;
  // at rest: frames 100 ms apart
  auto prev = capture();
  double rest = 0;
  for (int i = 0; i < 4; ++i) {
    step(100);
    auto next = capture();
    rest = std::max(rest, diffFraction(prev, next));
    prev = std::move(next);
  }
  r.animatedRestPct = rest * 100;
  // focus moving: one key, frames every 33 ms over 330 ms
  if (page.walk) {
    auto* cur = ui_->focus().current();
    const bool inRow = cur != nullptr && Focus::ancestor(cur, "row") != nullptr;
    prev = capture();
    key(inRow ? cadence_screen_v1_Key_KEY_RIGHT : cadence_screen_v1_Key_KEY_DOWN, cadence_screen_v1_KeyAction_KEY_ACTION_DOWN);
    double moving = 0;
    for (int i = 0; i < 10; ++i) {
      step(33);
      auto next = capture();
      moving = std::max(moving, diffFraction(prev, next));
      if (const char* dir = std::getenv("CADENCE_UI_DEBUG_FRAMES")) writePng(std::string(dir) + "/" + page.name + "-move-" + std::to_string(i) + ".png", next);
      prev = std::move(next);
    }
    key(inRow ? cadence_screen_v1_Key_KEY_RIGHT : cadence_screen_v1_Key_KEY_DOWN, cadence_screen_v1_KeyAction_KEY_ACTION_UP);
    r.animatedFocusPct = moving * 100;
  }
  return r;
}

}  // namespace cadence::screen::ui::preview
