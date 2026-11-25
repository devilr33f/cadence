#include "shader_source.h"

#include "gl.h"

#include <algorithm>
#include <filesystem>
#include <fstream>
#include <sstream>

namespace cadence::render::viz {

namespace {

const char* const kHeader = R"(
in vec2 vUv;
out vec4 cadenceOut;
uniform vec3 iResolution;
uniform float iTime;
uniform sampler2D uAudio;
uniform vec3 uBands;
uniform vec3 uBandsAtt;
uniform float uBeat;
uniform float uSinceBeat;
#define uBass uBands.x
#define uMid uBands.y
#define uTreble uBands.z
#define uBassAtt uBandsAtt.x
#define uMidAtt uBandsAtt.y
#define uTrebleAtt uBandsAtt.z
float fft(float x) { return texture(uAudio, vec2(clamp(x, 0.0, 1.0), 0.25)).r; }
float wave(float x) { return texture(uAudio, vec2(clamp(x, 0.0, 1.0), 0.75)).r * 2.0 - 1.0; }
)";

const char* const kFooter = R"(
void main() {
  vec4 c = vec4(0.0);
  mainImage(c, gl_FragCoord.xy);
  cadenceOut = vec4(c.rgb, 1.0);
}
)";

}  // namespace

bool ShaderSource::init(int width, int height) {
  w_ = width;
  h_ = height;
  program_ = gl::program(gl::kFullscreenVertex, std::string(kHeader) + "#line 1\n" + glsl_ + kFooter, &error_);
  if (!program_) return false;
  u_res_ = glGetUniformLocation(program_, "iResolution");
  u_time_ = glGetUniformLocation(program_, "iTime");
  u_audio_ = glGetUniformLocation(program_, "uAudio");
  u_bands_ = glGetUniformLocation(program_, "uBands");
  u_att_ = glGetUniformLocation(program_, "uBandsAtt");
  u_beat_ = glGetUniformLocation(program_, "uBeat");
  u_since_ = glGetUniformLocation(program_, "uSinceBeat");
  return true;
}

void ShaderSource::render(const FrameInputs& in, unsigned fbo) {
  glBindFramebuffer(GL_FRAMEBUFFER, fbo);
  glViewport(0, 0, w_, h_);
  glDisable(GL_BLEND);
  glUseProgram(program_);
  glUniform3f(u_res_, static_cast<float>(w_), static_cast<float>(h_), 1.0f);
  glUniform1f(u_time_, static_cast<float>(in.time_s));
  glActiveTexture(GL_TEXTURE0);
  glBindTexture(GL_TEXTURE_2D, in.audio_texture);
  glUniform1i(u_audio_, 0);
  glUniform3f(u_bands_, in.bass, in.mid, in.treble);
  glUniform3f(u_att_, in.bass_att, in.mid_att, in.treble_att);
  glUniform1f(u_beat_, in.beat ? 1.0f : 0.0f);
  glUniform1f(u_since_, in.since_beat);
  gl::fullscreen();
}

std::vector<std::unique_ptr<ShaderSource>> load_shader_presets(const std::string& dir) {
  std::vector<std::filesystem::path> files;
  std::error_code ec;
  for (const auto& e : std::filesystem::directory_iterator(dir, ec)) {
    if (e.path().extension() == ".frag") files.push_back(e.path());
  }
  std::sort(files.begin(), files.end());
  std::vector<std::unique_ptr<ShaderSource>> out;
  for (const auto& f : files) {
    std::ifstream in(f);
    std::stringstream body;
    body << in.rdbuf();
    out.push_back(std::make_unique<ShaderSource>(f.stem().string(), body.str()));
  }
  return out;
}

void ShaderSource::release() {
  if (program_) glDeleteProgram(program_);
  program_ = 0;
}

}  // namespace cadence::render::viz
