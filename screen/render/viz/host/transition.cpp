#include "transition.h"

#include "gl.h"


namespace cadence::render::viz {

namespace {

const char* const kHeader = R"(
in vec2 vUv;
out vec4 cadenceOut;
uniform sampler2D cadenceFrom;
uniform sampler2D cadenceTo;
uniform float progress;
uniform float ratio;
vec4 getFromColor(vec2 uv) { return texture(cadenceFrom, uv); }
vec4 getToColor(vec2 uv) { return texture(cadenceTo, uv); }
)";

const char* const kFooter = R"(
void main() { cadenceOut = vec4(transition(vUv).rgb, 1.0); }
)";

}  // namespace

bool TransitionStage::init(const std::vector<Transition>& transitions) {
  for (const auto& t : transitions) {
    std::string error;
    const unsigned id = gl::program(gl::kFullscreenVertex, std::string(kHeader) + "#line 1\n" + t.glsl + kFooter, &error);
    if (!id) {
      failed_.push_back(t.name);
      continue;
    }
    Program p{id, glGetUniformLocation(id, "progress"), glGetUniformLocation(id, "ratio"),
              glGetUniformLocation(id, "cadenceFrom"), glGetUniformLocation(id, "cadenceTo")};
    glUseProgram(id);
    for (const auto& param : transition_params(t.glsl)) {
      const int loc = glGetUniformLocation(id, param.name.c_str());
      if (loc < 0) continue;
      const auto& v = param.values;
      const auto vi = [&](std::size_t i) { return static_cast<int>(i < v.size() ? v[i] : v.back()); };
      const auto vf = [&](std::size_t i) { return i < v.size() ? v[i] : v.back(); };
      if (param.type == "float") glUniform1f(loc, vf(0));
      else if (param.type == "int" || param.type == "bool") glUniform1i(loc, vi(0));
      else if (param.type == "vec2") glUniform2f(loc, vf(0), vf(1));
      else if (param.type == "vec3") glUniform3f(loc, vf(0), vf(1), vf(2));
      else if (param.type == "vec4") glUniform4f(loc, vf(0), vf(1), vf(2), vf(3));
      else if (param.type == "ivec2") glUniform2i(loc, vi(0), vi(1));
    }
    programs_.push_back(p);
    names_.push_back(t.name);
  }
  return !programs_.empty();
}

void TransitionStage::release() {
  for (auto& p : programs_) glDeleteProgram(p.id);
  programs_.clear();
  names_.clear();
}

void TransitionStage::draw(std::size_t i, unsigned from, unsigned to, float progress, int width, int height) const {
  const Program& p = programs_[i % programs_.size()];
  glViewport(0, 0, width, height);
  glDisable(GL_BLEND);
  glUseProgram(p.id);
  glActiveTexture(GL_TEXTURE0);
  glBindTexture(GL_TEXTURE_2D, from);
  glUniform1i(p.from, 0);
  glActiveTexture(GL_TEXTURE1);
  glBindTexture(GL_TEXTURE_2D, to);
  glUniform1i(p.to, 1);
  glActiveTexture(GL_TEXTURE0);
  glUniform1f(p.progress, progress);
  glUniform1f(p.ratio, static_cast<float>(width) / static_cast<float>(height));
  gl::fullscreen();
}

}  // namespace cadence::render::viz
