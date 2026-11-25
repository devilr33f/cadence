#include "app/routes/audio/viz-view.h"

#include "host/classic_source.h"
#include "host/drive_source.h"
#include "milkdrop/milkdrop_sources.h"
#include "host/features.h"
#include "host/host.h"
#include "host/pcm_timeline.h"
#include "host/renderer.h"
#include "host/scene_source.h"
#include "ipc/log.h"
#include "render/gl.h"
#include "screen_backend.h"

#include <algorithm>
#include <atomic>
#include <utility>
#include <mutex>
#include <vector>

namespace cadence::screen::routes {

namespace viz = cadence::render::viz;
namespace vr = cadence::render;

namespace {

void addSources(viz::Renderer& r, const std::shared_ptr<viz::SceneLook>& look) {
  for (auto& s : viz::drive_sources(look)) r.add(std::move(s));
  for (auto& s : viz::embedded_scenes(look)) r.add(std::move(s));
  // projectM's presets, where this build ships them (none without it: the milkdrop family then plays the rest)
  for (auto& s : viz::milkdrop_sources(viz::milkdrop_dir())) r.add(std::move(s));
  for (auto st : {viz::Style::Bars, viz::Style::Mirror, viz::Style::Scope}) r.add(std::make_unique<viz::ClassicSource>(st));
}

viz::LogFn logFn() {
  return [](const std::string& event, const std::string& id, double value) {
    // ids are ours (scene:vortex, classic:bars): never user data
    CADENCE_LOG(Info, "viz_event", {{"event", ipc::Token(event)}, {"source", ipc::Token(id)}, {"ms", value}});
  };
}

void tintOf(viz::SceneLook& look, std::uint32_t rgb, float amount) {
  look.set_tint(static_cast<float>((rgb >> 16) & 0xff) / 255.0f, static_cast<float>((rgb >> 8) & 0xff) / 255.0f,
                static_cast<float>(rgb & 0xff) / 255.0f, amount);
}

class HostView final : public VizView {
 public:
  ~HostView() override { release(); }

  void pcm(std::span<const float> s, std::uint32_t ch, std::uint32_t rate, std::int64_t monoNs) override {
    if (auto* h = host_.load()) h->push_pcm(s, ch, rate, monoNs);
  }
  void setRunning(bool on) override { running_ = on; }
  void setPaused(bool paused) override {
    if (host_) host_.load()->set_paused(paused);
  }
  void setReducedMotion(bool on) override {
    reduced_ = on;
    if (host_) host_.load()->set_reduced_motion(on);
  }
  void next() override {
    if (host_) host_.load()->next();
  }
  void prev() override {
    if (host_) host_.load()->prev();
  }
  void onTrack(std::uint64_t seed) override {
    look_->track = seed;
    if (host_) host_.load()->on_track(seed);
  }
  void setTint(std::uint32_t rgb, float amount) override { tintOf(*look_, rgb, amount); }
  void setFamily(std::vector<std::string> prefixes) override {
    family_ = prefixes;
    if (host_) host_.load()->set_family(std::move(prefixes));
  }
  void setArtwork(std::vector<std::uint8_t> rgba, int width, int height) override {
    look_->set_artwork(std::move(rgba), width, height);
  }
  [[nodiscard]] float punch() const override { return look_->punch.load(); }
  void setPin(const std::string& id) override {
    pin_ = id;
    if (host_) host_.load()->set_pin(id);
  }
  void setShape(int code) override { look_->shape = code; }
  void setLead(int ms) override {
    if (ms == lead_) return;
    lead_ = ms;
    restart_ = owned_ != nullptr;
  }
  void setQuality(const std::string& quality) override {
    if (quality == quality_) return;
    quality_ = quality;
    // the host restarts at the next draw, at the new size, where the ui's context is current
    restart_ = owned_ != nullptr;
    failed_ = false;
  }
  void setWake(std::function<void()> wake) override { wake_ = std::move(wake); }

  bool draw(render::RenderBackend& backend, render::Rect dst) override {
    if (!running_) return false;
    if (std::exchange(restart_, false)) release();
    if (!owned_ && !failed_) start(backend, dst);
    auto* h = host_.load();
    if (h == nullptr) return false;
    const auto q = h->frame(vr::Rect{static_cast<float>(dst.x), static_cast<float>(dst.y), static_cast<float>(dst.width),
                                         static_cast<float>(dst.height)});
    if (q.texture == 0) return false;
    canvas_->draw_quad(q);
    last_ = q.texture;
    return true;
  }
  [[nodiscard]] bool frameReady() const override {
    auto* h = host_.load();
    return running_ && h != nullptr && h->frame({}).texture != last_;
  }
  [[nodiscard]] std::string current() const override { return {}; }
  void release() override {
    host_ = nullptr;
    if (owned_) owned_->stop();
    owned_.reset();
    canvas_.reset();
  }
  void abandon() override {
    host_ = nullptr;
    if (owned_) owned_->stop();
    // the host's own context is its thread's; the canvas's program died with the ui's context
    (void)owned_.release();
    (void)canvas_.release();
  }

 private:
  void start(render::RenderBackend& backend, render::Rect dst) {
    auto context = vr::make_shared_context();
    canvas_ = std::make_unique<vr::BackendCanvas>(backend);
    // Host::frame's uv already turns gl's bottom-up rows over; the canvas flipping them again drew the scenes
    // upside down (unseen while every scene was symmetric)
    canvas_->set_flipped(false);
    if (!context || !canvas_->ok()) {
      failed_ = true;
      CADENCE_LOG(Warn, "viz_unavailable");
      return;
    }
    viz::HostConfig hc;
    hc.on_frame = wake_;
    hc.lead_ms = lead_;
    // diagnosis (the owner's "the stick's picture is broken"): every beat drawn as a line, to line up with the song
    hc.log_beats = true;
    if (quality_ == "native" && dst.width >= 320 && dst.height >= 180) {
      // the screen's own output size, whatever it is
      hc.renderer.width = dst.width;
      hc.renderer.height = dst.height;
    } else {
      hc.renderer.width = quality_ == "low" ? 640 : quality_ == "high" ? 1280 : quality_ == "max" ? 1920 : 960;
      hc.renderer.height = hc.renderer.width * 9 / 16;
    }
    // the drive scenes' soft layer at half the size whatever it is; their sharp one is always at full size
    look_->scene_scale = 0.5f;
    // which size this gpu runs (its cost follows as viz_cost every 10 s); the quality's name is ours
    logFn()("viz_size", quality_, static_cast<double>(hc.renderer.width));
    owned_ = std::make_unique<viz::Host>(hc, std::move(context), logFn());
    addSources(owned_->renderer(), look_);
    if (!owned_->start()) {
      failed_ = true;
      owned_.reset();
      return;
    }
    owned_->set_reduced_motion(reduced_);
    if (!family_.empty()) owned_->set_family(family_);
    if (!pin_.empty()) owned_->set_pin(pin_);
    host_ = owned_.get();
  }

  std::shared_ptr<viz::SceneLook> look_ = std::make_shared<viz::SceneLook>();
  std::unique_ptr<viz::Host> owned_;
  std::atomic<viz::Host*> host_{nullptr};
  std::unique_ptr<vr::BackendCanvas> canvas_;
  std::atomic<bool> running_{false};
  bool failed_ = false;
  bool reduced_ = false;
  vr::TextureId last_ = 0;
  std::function<void()> wake_;
  std::vector<std::string> family_;
  std::string pin_;
  std::string quality_ = "medium";
  bool restart_ = false;
  int lead_ = 60;
};

class SyncView final : public VizView {
 public:
  SyncView(double fps, int warmup) : fps_(fps), warmup_(std::max(1, warmup)) {}
  ~SyncView() override { release(); }

  void pcm(std::span<const float> s, std::uint32_t ch, std::uint32_t rate, std::int64_t monoNs) override {
    std::lock_guard lock(mu_);
    // mono, appended: the gallery hands the fixture in order
    for (std::size_t i = 0; i + ch <= s.size(); i += ch) {
      float m = 0;
      for (std::uint32_t c = 0; c < ch; ++c) m += s[i + c];
      mono_.push_back(m / static_cast<float>(ch));
    }
    rate_ = rate;
    (void)monoNs;
  }
  void setRunning(bool on) override {
    // shown again: from the start of what it was handed, fresh trails (a gallery page draws the same each load)
    if (on && !running_) {
      release();
      std::lock_guard lock(mu_);
      cursor_ = 0;
      t_ = 0;
      features_ = viz::AudioFeatures{};
      in_ = viz::FrameInputs{};
      fresh_ = true;
    }
    running_ = on;
  }
  void setPaused(bool paused) override { paused_ = paused; }
  void setReducedMotion(bool on) override { reduced_ = on; }
  void next() override {
    if (r_) r_->next();
  }
  void prev() override {
    if (r_) r_->prev();
  }
  void onTrack(std::uint64_t seed) override {
    look_->track = seed;
    if (r_) r_->on_track(seed);
    if (!has_track_ || seed != last_track_) {
      features_.on_track();
      last_track_ = seed;
      has_track_ = true;
    }
  }
  void setTint(std::uint32_t rgb, float amount) override { tintOf(*look_, rgb, amount); }
  void setArtwork(std::vector<std::uint8_t> rgba, int width, int height) override {
    look_->set_artwork(std::move(rgba), width, height);
  }
  [[nodiscard]] float punch() const override { return look_->punch.load(); }
  void setShape(int code) override { look_->shape = code; }
  // shows one source at once (the gallery's pages)
  bool show(const std::string& id) { return r_ && r_->show(id); }

  bool draw(render::RenderBackend& backend, render::Rect dst) override {
    if (!running_) return false;
    if (!r_) {
      viz::RendererConfig rc;
      rc.budget_ms = 1e9;
      r_ = std::make_unique<viz::Renderer>(rc, logFn());
      addSources(*r_, look_);
      canvas_ = std::make_unique<vr::BackendCanvas>(backend);
      if (!r_->init()) return false;
      r_->set_auto(false);
      if (!pending_.empty()) r_->show(pending_);
    }
    // the feedback builds over frames: the first draw after a start renders the warm-up ones too
    const int frames = paused_ ? 0 : (std::exchange(fresh_, false) ? warmup_ : 1);
    for (int f = 0; f < frames; ++f) {
      // one frame of the pcm handed in, 1600 frames a frame at 30 fps
      std::vector<float> window(1024, 0.0f);
      {
        std::lock_guard lock(mu_);
        const auto hop = static_cast<std::size_t>(rate_ / fps_);
        const std::size_t end = std::min(mono_.size(), cursor_ + hop + 1024);
        if (end >= 1024) std::copy(mono_.begin() + static_cast<std::ptrdiff_t>(end - 1024), mono_.begin() + static_cast<std::ptrdiff_t>(end), window.begin());
        cursor_ += hop;
      }
      features_.update(window, rate_, 1.0 / fps_, in_);
      in_.time_s = t_;
      t_ += 1.0 / fps_;
      r_->render(in_, in_.time_s);
      texture_ = r_->last_fbo() != 0 ? lastTexture() : 0;
    }
    backend.resetState();
    backend.bindRenderTarget(nullptr);
    if (texture_ == 0) return false;
    canvas_->draw_quad(vr::TexturedQuad{texture_, {static_cast<float>(dst.x), static_cast<float>(dst.y),
                                                       static_cast<float>(dst.width), static_cast<float>(dst.height)}});
    return true;
  }
  [[nodiscard]] bool frameReady() const override { return running_ && !paused_; }
  [[nodiscard]] std::string current() const override { return r_ ? r_->current() : pending_; }
  void release() override {
    if (r_) r_->release();
    r_.reset();
    canvas_.reset();
  }
  void abandon() override {
    (void)r_.release();
    (void)canvas_.release();
  }
  void preselect(const std::string& id) {
    pending_ = id;
    if (r_) r_->show(id);
  }

 private:
  vr::TextureId lastTexture() const {
    GLint tex = 0;
    glBindFramebuffer(GL_FRAMEBUFFER, r_->last_fbo());
    glGetFramebufferAttachmentParameteriv(GL_FRAMEBUFFER, GL_COLOR_ATTACHMENT0, GL_FRAMEBUFFER_ATTACHMENT_OBJECT_NAME, &tex);
    glBindFramebuffer(GL_FRAMEBUFFER, 0);
    return static_cast<vr::TextureId>(tex);
  }

  double fps_;
  int warmup_ = 1;
  bool fresh_ = true;
  std::shared_ptr<viz::SceneLook> look_ = std::make_shared<viz::SceneLook>();
  std::unique_ptr<viz::Renderer> r_;
  std::unique_ptr<vr::BackendCanvas> canvas_;
  viz::AudioFeatures features_;
  bool has_track_ = false;
  std::uint64_t last_track_ = 0;
  viz::FrameInputs in_;
  std::mutex mu_;
  std::vector<float> mono_;
  std::size_t cursor_ = 0;
  std::uint32_t rate_ = 48000;
  double t_ = 0;
  vr::TextureId texture_ = 0;
  std::string pending_;
  bool running_ = false, paused_ = false, reduced_ = false;
};

}  // namespace

std::unique_ptr<VizView> makeHostVizView() { return std::make_unique<HostView>(); }

std::unique_ptr<VizView> makeSyncVizView(double fps, int warmupFrames) {
  return std::make_unique<SyncView>(fps, warmupFrames);
}

// the gallery picks a scene before the first frame
void preselectSyncScene(VizView& view, const std::string& id) {
  if (auto* s = dynamic_cast<SyncView*>(&view)) s->preselect(id);
}

}  // namespace cadence::screen::routes
