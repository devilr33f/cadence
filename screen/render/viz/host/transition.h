#pragma once

#include <string>
#include <vector>

namespace cadence::render::viz {

// a transition in gl-transitions' contract (https://github.com/gl-transitions/gl-transitions): glsl defining
//
//   vec4 transition(vec2 uv)
//
// over getFromColor(uv) and getToColor(uv), with progress (0..1) and ratio (width / height) in scope, and its own
// parameters as `uniform <type> <name>; // = <default>;`, which the stage sets to their defaults. the pack is fetched
// at build time at a pinned commit (transitions/fetch-transitions.sh), never committed; Crossfade() is built in
struct Transition {
  std::string name;
  std::string glsl;
};

Transition crossfade();

// a parameter's default, from its declaration line
struct TransitionParam {
  std::string type;
  std::string name;
  std::vector<float> values;
};
std::vector<TransitionParam> transition_params(const std::string& glsl);

// blends two textures into a target with a transition; compiled once per transition
class TransitionStage {
 public:
  // gl thread; transitions that fail to compile are dropped (their names in failed())
  bool init(const std::vector<Transition>& transitions);
  void release();

  [[nodiscard]] std::size_t size() const { return programs_.size(); }
  [[nodiscard]] const std::vector<std::string>& failed() const { return failed_; }
  [[nodiscard]] const std::string& name(std::size_t i) const { return names_[i]; }

  // draws transition i at progress from `from` to `to` into the bound fbo of width × height
  void draw(std::size_t i, unsigned from, unsigned to, float progress, int width, int height) const;

 private:
  struct Program {
    unsigned id = 0;
    int progress = -1, ratio = -1, from = -1, to = -1;
  };
  std::vector<Program> programs_;
  std::vector<std::string> names_, failed_;
};

}  // namespace cadence::render::viz
