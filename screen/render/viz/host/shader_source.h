#pragma once

#include "source.h"

#include <memory>
#include <string>
#include <vector>

namespace cadence::render::viz {

// a shader preset: glsl es 3.00 defining shadertoy's entry point
//
//   void mainImage(out vec4 fragColor, in vec2 fragCoord)
//
// with these in scope: iResolution (vec3), iTime, uAudio (the 512 × 2 audio texture), uBass, uMid, uTreble, their
// _att forms, uBeat (1 on a beat frame), uSinceBeat, and fft(x) and wave(x) for x in 0..1 (wave is -1..1). no other
// texture, no buffer pass: one draw per frame
class ShaderSource final : public VizSource {
 public:
  ShaderSource(std::string name, std::string glsl) : name_(std::move(name)), glsl_(std::move(glsl)) {}

  [[nodiscard]] std::string id() const override { return "shader:" + name_; }
  bool init(int width, int height) override;
  void render(const FrameInputs& in, unsigned fbo) override;
  void release() override;
  // the compile or link log of a failed init
  [[nodiscard]] const std::string& error() const { return error_; }

 private:
  std::string name_, glsl_, error_;
  int w_ = 0, h_ = 0;
  unsigned program_ = 0;
  int u_res_ = -1, u_time_ = -1, u_audio_ = -1, u_bands_ = -1, u_att_ = -1, u_beat_ = -1, u_since_ = -1;
};

// every *.frag in dir as a ShaderSource named by its file stem, sorted by name
std::vector<std::unique_ptr<ShaderSource>> load_shader_presets(const std::string& dir);

}  // namespace cadence::render::viz
