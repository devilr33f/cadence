#pragma once

#include "budget.h"
#include "features.h"
#include "gl.h"
#include "playlist.h"
#include "scheduler.h"
#include "source.h"
#include "transition.h"

#include <cstdint>
#include <functional>
#include <map>
#include <memory>
#include <string>
#include <vector>

namespace cadence::render::viz {

// a line for the device's log: an event code, a source or transition id, a number (no user data)
using LogFn = std::function<void(const std::string& event, const std::string& id, double value)>;

struct RendererConfig {
  // internal resolution: 960 × 540, down to 640 × 360 on weak gpus; the ui upscales the output texture
  int width = 960;
  int height = 540;
  // a source that costs more than this for budget_window_s is skipped, unless it is pinned or the last of the
  // family: a merely slow one stays and the host lowers its frame rate instead (the stick's milkdrop presets cost
  // 30-45 ms; skipping each after 2 s switched the picture every few seconds)
  double budget_ms = 50;
  double budget_window_s = 2;
  SchedulerConfig schedule;
  // the device's slow list (persisted); empty keeps it in memory
  std::string slow_file;
};

// a source's measured cost
struct Cost {
  double last_ms = 0;
  double mean_ms = 0;
  std::uint64_t frames = 0;
};

// the visualizer host's gl half, synchronous: on the viz thread (Host), in the benchmark and in the tests. each frame
// it renders the current source (and, during a transition, the outgoing one too) and blends them through a
// transition into its output texture
class Renderer {
 public:
  explicit Renderer(RendererConfig c, LogFn log = {});
  ~Renderer();

  // before init
  void add(std::unique_ptr<VizSource> source);
  void add_transitions(std::vector<Transition> transitions);

  // gl context current: the audio texture, targets, sources (one that fails init is dropped), transitions
  // (crossfade when none compiles); false when no source is left
  bool init();
  void release();

  // one frame; returns the output texture (gl's rows bottom up), whose fbo last_fbo() names; both are drawn again next
  // frame, so a reader on another context copies it first (Host does)
  unsigned render(FrameInputs& in, double now_s);
  [[nodiscard]] unsigned last_fbo() const { return last_fbo_; }

  // ← and → (a transition starts at the next frame), a new track (a random other source), auto switching
  void next() { scheduler_.ask(Scheduler::Move::Next); }
  void prev() { scheduler_.ask(Scheduler::Move::Prev); }
  void on_track(std::uint64_t seed);
  void set_auto(bool on) { scheduler_.set_auto(on); }
  // the family the owner picked (playlist.h): a transition into it when the current source is outside it
  void set_family(std::vector<std::string> prefixes, double now_s);
  // the owner's pick of one source ("drive:cover"): shown now and kept, through new tracks and slow frames; empty
  // lets new tracks pick again
  void set_pin(const std::string& id, double now_s);
  // shows a source at once, no transition (the benchmark, the tests)
  bool show(const std::string& id);

  [[nodiscard]] std::string current() const;
  [[nodiscard]] const std::map<std::string, Cost>& costs() const { return costs_; }
  [[nodiscard]] std::vector<std::string> ids() const;
  [[nodiscard]] const RendererConfig& config() const { return c_; }

 private:
  VizSource* find(const std::string& id);
  void begin(const std::string& to, bool transition, double now_s);
  // renders a source into a target, timed; true when it went over budget for its window
  bool draw(VizSource& s, const gl::Target& t, const FrameInputs& in, double now_s);
  void upload_audio(const FrameInputs& in);

  RendererConfig c_;
  LogFn log_;
  std::vector<std::unique_ptr<VizSource>> sources_;
  std::vector<Transition> transition_src_;
  TransitionStage transitions_;
  std::unique_ptr<Playlist> playlist_;
  Scheduler scheduler_;
  Budget budget_;
  std::map<std::string, Cost> costs_;

  unsigned audio_ = 0;
  gl::Target slots_[2];
  gl::Target out_;
  // the slot of the current source; the outgoing one renders into the other
  int cur_slot_ = 0;
  VizSource* cur_ = nullptr;
  VizSource* from_ = nullptr;
  std::size_t transition_ = 0;
  std::uint64_t track_seed_ = 0;
  bool track_asked_ = false;
  std::string pin_;
  bool initialized_ = false;
  unsigned last_fbo_ = 0;
};

}  // namespace cadence::render::viz
