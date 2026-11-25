#include "scene_source.h"

#include <algorithm>
#include <cmath>
#include <filesystem>
#include <fstream>
#include <regex>
#include <sstream>

namespace cadence::render::viz {

namespace {

const char* const kPreamble = R"(
uniform sampler2D uAudio;
uniform sampler2D uPrev;
uniform sampler2D uPal;
uniform float uPalRow;
uniform float uPalShift;
uniform vec2 uRes;
uniform float uTime;
uniform float uBass;
uniform float uMid;
uniform float uTreble;
uniform float uLevel;
uniform float uBeatPulse;
uniform float uBeatCount;
uniform vec4 uBeatRings[2];
in vec2 vUv;
out vec4 fragColor;

float fft(float u) { return texture(uAudio, vec2(clamp(u, 0.0, 1.0), 0.25)).r; }
// a log-frequency view: u spans bins 2..511 exponentially (~90 hz..24 khz)
float fftLog(float u) {
  float b = exp2(mix(1.0, log2(511.0), clamp(u, 0.0, 1.0)));
  return fft(b / 511.0);
}
float wave(float u) { return texture(uAudio, vec2(clamp(u, 0.0, 1.0), 0.75)).r * 2.0 - 1.0; }
float wave2(float u) { return wave(fract(u + 0.37)); }
vec3 palette(float x) { return texture(uPal, vec2(fract(x), (uPalRow + 0.5) / 8.0)).rgb; }
float hash12(vec2 p) {
  vec3 p3 = fract(vec3(p.xyx) * 0.1031);
  p3 += dot(p3, p3.yzx + 33.33);
  return fract((p3.x + p3.y) * p3.z);
}
vec2 rot2(vec2 p, float a) {
  float s = sin(a), c = cos(a);
  return mat2(c, -s, s, c) * p;
}
float beatAge(int i) { return i < 4 ? uBeatRings[0][i] : uBeatRings[1][i - 4]; }
// the feedback sample: uv warped by the scene, faded by decay, its luminance remapped through the palette (part of
// the colour kept) so trails change hue as they age; the edges fade so nothing smears in from outside
vec3 fb(vec2 uv, float decay) {
  vec3 c = texture(uPrev, clamp(uv, 0.0, 1.0)).rgb;
  float l = clamp(max(max(c.r, c.g), c.b), 0.0, 1.0);
  vec3 p = palette(uPalShift + l * 0.85);
  vec2 d = abs(uv - 0.5);
  float edge = smoothstep(0.52, 0.42, max(d.x, d.y));
  return mix(c, p * max(l, 0.12), 0.55) * decay * edge;
}
)";

const char* const kBright = R"(
uniform sampler2D uTex;
uniform vec2 uTexel;
in vec2 vUv;
out vec4 fragColor;
void main() {
  vec3 c = (texture(uTex, vUv + uTexel * vec2(-0.5, -0.5)).rgb + texture(uTex, vUv + uTexel * vec2(0.5, -0.5)).rgb +
            texture(uTex, vUv + uTexel * vec2(-0.5, 0.5)).rgb + texture(uTex, vUv + uTexel * vec2(0.5, 0.5)).rgb) * 0.25;
  float l = max(max(c.r, c.g), c.b);
  fragColor = vec4(c * smoothstep(0.45, 0.9, l), 1.0);
}
)";

const char* const kBlur = R"(
uniform sampler2D uTex;
uniform vec2 uDir;
in vec2 vUv;
out vec4 fragColor;
void main() {
  vec3 c = texture(uTex, vUv).rgb * 0.227;
  c += (texture(uTex, vUv + uDir * 1.3846).rgb + texture(uTex, vUv - uDir * 1.3846).rgb) * 0.316;
  c += (texture(uTex, vUv + uDir * 3.2308).rgb + texture(uTex, vUv - uDir * 3.2308).rgb) * 0.070;
  fragColor = vec4(c, 1.0);
}
)";

const char* const kComposite = R"(
uniform sampler2D uTex;
uniform sampler2D uBloom;
uniform float uBloomAmt;
uniform vec3 uTint;
uniform float uTintAmt;
uniform float uKeepAlpha;
in vec2 vUv;
out vec4 fragColor;
void main() {
  vec4 s = texture(uTex, vUv);
  vec3 c = s.rgb + texture(uBloom, vUv).rgb * uBloomAmt;
  // the artwork's colour biases without crushing what is there
  c = mix(c, c * uTint * 1.25, uTintAmt);
  // opaque, unless the caller keeps the scene's alpha (the drive scenes: how much of their backdrop they hide)
  fragColor = vec4(min(c, vec3(1.0)), mix(1.0, s.a, uKeepAlpha));
}
)";

struct Row {
  const char* name;
  float a[3], b[3], c[3], d[3];
};
// a + b * cos(2 pi (c t + d)) per channel
constexpr Row kRows[8] = {
    {"fire", {0.55f, 0.25f, 0.12f}, {0.45f, 0.35f, 0.20f}, {1, 1, 1}, {0.05f, 0.20f, 0.45f}},
    {"ice", {0.20f, 0.35f, 0.55f}, {0.25f, 0.30f, 0.45f}, {1, 1, 1}, {0.60f, 0.35f, 0.15f}},
    {"spectrum", {0.5f, 0.5f, 0.5f}, {0.5f, 0.5f, 0.5f}, {1, 1, 1}, {0.00f, 0.33f, 0.67f}},
    {"sunset", {0.50f, 0.25f, 0.45f}, {0.50f, 0.30f, 0.45f}, {1, 1, 1}, {0.90f, 0.60f, 0.20f}},
    {"toxic", {0.25f, 0.50f, 0.25f}, {0.30f, 0.45f, 0.25f}, {1, 1, 1}, {0.35f, 0.10f, 0.60f}},
    {"ocean", {0.15f, 0.40f, 0.50f}, {0.25f, 0.40f, 0.45f}, {1, 1, 1}, {0.50f, 0.25f, 0.10f}},
    {"ember", {0.50f, 0.30f, 0.20f}, {0.45f, 0.30f, 0.25f}, {1, 1, 1}, {0.05f, 0.15f, 0.35f}},
    {"mono", {0.5f, 0.5f, 0.5f}, {0.5f, 0.5f, 0.5f}, {0, 0, 0}, {0, 0, 0}},
};

void bindTexture(int unit, unsigned texture) {
  glActiveTexture(static_cast<GLenum>(GL_TEXTURE0 + unit));
  glBindTexture(GL_TEXTURE_2D, texture);
}

}  // namespace

int palette_row(const std::string& name) {
  for (int i = 0; i < 8; ++i)
    if (name == kRows[i].name) return i;
  return 0;
}

std::vector<std::uint8_t> palette_pixels() {
  std::vector<std::uint8_t> px(256 * 8 * 4);
  for (std::size_t r = 0; r < 8; ++r) {
    for (std::size_t x = 0; x < 256; ++x) {
      const float t = static_cast<float>(x) / 255.0f;
      // eased in from black, so trails fade to nothing
      const float ease = std::min(t * 6.0f, 1.0f);
      const std::size_t o = (r * 256 + x) * 4;
      for (std::size_t k = 0; k < 3; ++k) {
        const float v = kRows[r].a[k] + kRows[r].b[k] * std::cos(6.2831853f * (kRows[r].c[k] * t + kRows[r].d[k]));
        px[o + k] = static_cast<std::uint8_t>(std::lround(std::clamp(v * ease, 0.0f, 1.0f) * 255.0f));
      }
      px[o + 3] = 255;
    }
  }
  return px;
}

bool ScenePost::init(int width, int height) {
  if (users_++ > 0) return composite_ != 0;
  w_ = width;
  h_ = height;
  glGenTextures(1, &palette_);
  glBindTexture(GL_TEXTURE_2D, palette_);
  const auto px = palette_pixels();
  glPixelStorei(GL_UNPACK_ALIGNMENT, 1);
  glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA8, 256, 8, 0, GL_RGBA, GL_UNSIGNED_BYTE, px.data());
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_LINEAR);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_LINEAR);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_S, GL_CLAMP_TO_EDGE);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_T, GL_CLAMP_TO_EDGE);
  bright_ = gl::program(gl::kFullscreenVertex, kBright);
  blur_ = gl::program(gl::kFullscreenVertex, kBlur);
  composite_ = gl::program(gl::kFullscreenVertex, kComposite);
  const int bw = std::max(1, width / 4), bh = std::max(1, height / 4);
  if (!bright_ || !blur_ || !composite_ || !a_.make(bw, bh) || !b_.make(bw, bh)) {
    release();
    users_ = 0;
    return false;
  }
  return true;
}

void ScenePost::release() {
  if (users_ > 1) {
    --users_;
    return;
  }
  users_ = 0;
  for (unsigned* p : {&bright_, &blur_, &composite_}) {
    if (*p) glDeleteProgram(*p);
    *p = 0;
  }
  if (palette_) glDeleteTextures(1, &palette_);
  palette_ = 0;
  a_.release();
  b_.release();
}

unsigned ScenePost::bloom_only(unsigned scene) {
  glDisable(GL_BLEND);
  a_.bind();
  glUseProgram(bright_);
  bindTexture(0, scene);
  glUniform1i(glGetUniformLocation(bright_, "uTex"), 0);
  glUniform2f(glGetUniformLocation(bright_, "uTexel"), 1.0f / static_cast<float>(w_), 1.0f / static_cast<float>(h_));
  gl::fullscreen();
  glUseProgram(blur_);
  const auto dir = glGetUniformLocation(blur_, "uDir");
  glUniform1i(glGetUniformLocation(blur_, "uTex"), 0);
  b_.bind();
  bindTexture(0, a_.texture);
  glUniform2f(dir, 1.0f / static_cast<float>(a_.width), 0);
  gl::fullscreen();
  a_.bind();
  bindTexture(0, b_.texture);
  glUniform2f(dir, 0, 1.0f / static_cast<float>(a_.height));
  gl::fullscreen();
  glActiveTexture(GL_TEXTURE0);
  return a_.texture;
}

void ScenePost::finish(unsigned scene, unsigned fbo, float bloom_override, bool keep_alpha) {
  const float bloom = bloom_override >= 0 ? bloom_override : look_->bloom.load();
  glDisable(GL_BLEND);
  if (bloom > 0.001f) {
    // the bright pass, downsampling into a, then blur a -> b (across) and b -> a (down)
    a_.bind();
    glUseProgram(bright_);
    bindTexture(0, scene);
    glUniform1i(glGetUniformLocation(bright_, "uTex"), 0);
    glUniform2f(glGetUniformLocation(bright_, "uTexel"), 1.0f / static_cast<float>(w_), 1.0f / static_cast<float>(h_));
    gl::fullscreen();
    glUseProgram(blur_);
    const auto dir = glGetUniformLocation(blur_, "uDir");
    glUniform1i(glGetUniformLocation(blur_, "uTex"), 0);
    b_.bind();
    bindTexture(0, a_.texture);
    glUniform2f(dir, 1.0f / static_cast<float>(a_.width), 0);
    gl::fullscreen();
    a_.bind();
    bindTexture(0, b_.texture);
    glUniform2f(dir, 0, 1.0f / static_cast<float>(a_.height));
    gl::fullscreen();
  }
  glBindFramebuffer(GL_FRAMEBUFFER, fbo);
  glViewport(0, 0, w_, h_);
  glUseProgram(composite_);
  bindTexture(0, scene);
  bindTexture(1, a_.texture);
  glUniform1i(glGetUniformLocation(composite_, "uTex"), 0);
  glUniform1i(glGetUniformLocation(composite_, "uBloom"), 1);
  glUniform1f(glGetUniformLocation(composite_, "uBloomAmt"), bloom > 0.001f ? bloom : 0.0f);
  glUniform3f(glGetUniformLocation(composite_, "uTint"), look_->tint_r.load(), look_->tint_g.load(), look_->tint_b.load());
  glUniform1f(glGetUniformLocation(composite_, "uTintAmt"), look_->tint_amount.load());
  glUniform1f(glGetUniformLocation(composite_, "uKeepAlpha"), keep_alpha ? 1.0f : 0.0f);
  gl::fullscreen();
  glActiveTexture(GL_TEXTURE0);
}

bool SceneSource::init(int width, int height) {
  w_ = width;
  h_ = height;
  if (!post_ || !post_->init(width, height)) {
    error_ = "post";
    return false;
  }
  program_ = gl::program(gl::kFullscreenVertex, std::string(kPreamble) + "#line 1\n" + glsl_, &error_);
  if (!program_ || !ping_[0].make(width, height) || !ping_[1].make(width, height)) {
    release();
    return false;
  }
  u_res_ = glGetUniformLocation(program_, "uRes");
  u_time_ = glGetUniformLocation(program_, "uTime");
  u_audio_ = glGetUniformLocation(program_, "uAudio");
  u_prev_ = glGetUniformLocation(program_, "uPrev");
  u_pal_ = glGetUniformLocation(program_, "uPal");
  u_pal_row_ = glGetUniformLocation(program_, "uPalRow");
  u_pal_shift_ = glGetUniformLocation(program_, "uPalShift");
  u_bass_ = glGetUniformLocation(program_, "uBass");
  u_mid_ = glGetUniformLocation(program_, "uMid");
  u_treble_ = glGetUniformLocation(program_, "uTreble");
  u_level_ = glGetUniformLocation(program_, "uLevel");
  u_pulse_ = glGetUniformLocation(program_, "uBeatPulse");
  u_count_ = glGetUniformLocation(program_, "uBeatCount");
  u_rings_ = glGetUniformLocation(program_, "uBeatRings[0]");
  return true;
}

bool SceneSource::activate() {
  for (auto& t : ping_) {
    t.bind();
    glClearColor(0, 0, 0, 1);
    glClear(GL_COLOR_BUFFER_BIT);
  }
  shift_ = 0;
  return true;
}

void SceneSource::render(const FrameInputs& in, unsigned fbo) {
  // the palette steps on every onset and drifts between them
  shift_ = std::fmod(shift_ + (in.onset ? 0.085f * in.beat_pulse : 0.0f) + 0.018f * static_cast<float>(in.dt), 1.0f);
  const gl::Target& prev = ping_[front_];
  const gl::Target& next = ping_[front_ ^ 1];
  next.bind();
  glDisable(GL_BLEND);
  glUseProgram(program_);
  bindTexture(0, in.audio_texture);
  glUniform1i(u_audio_, 0);
  bindTexture(1, prev.texture);
  glUniform1i(u_prev_, 1);
  bindTexture(2, post_->palette());
  glUniform1i(u_pal_, 2);
  glUniform1f(u_pal_row_, static_cast<float>(palette_row_));
  glUniform1f(u_pal_shift_, shift_);
  glUniform2f(u_res_, static_cast<float>(w_), static_cast<float>(h_));
  glUniform1f(u_time_, static_cast<float>(std::fmod(in.time_s, 3600.0)));
  glUniform1f(u_bass_, in.bass_level);
  glUniform1f(u_mid_, in.mid_level);
  glUniform1f(u_treble_, in.treble_level);
  glUniform1f(u_level_, in.level);
  glUniform1f(u_pulse_, in.beat_pulse);
  glUniform1f(u_count_, static_cast<float>(in.beat_count % 4096));
  // newest first, so beatAge(0) is the latest onset
  std::array<float, 8> rings{};
  for (std::size_t i = 0; i < 8; ++i) rings[i] = in.beat_ages[7 - i];
  glUniform4fv(u_rings_, 2, rings.data());
  gl::fullscreen();
  front_ ^= 1;
  post_->finish(next.texture, fbo);
}

void SceneSource::release() {
  if (program_) glDeleteProgram(program_);
  program_ = 0;
  for (auto& t : ping_) t.release();
  if (post_ && w_ > 0) post_->release();
  w_ = 0;
}

std::vector<std::unique_ptr<SceneSource>> embedded_scenes(std::shared_ptr<SceneLook> look) {
  auto post = std::make_shared<ScenePost>(look ? std::move(look) : std::make_shared<SceneLook>());
  const std::regex pal(R"(//\s*palette:\s*([a-z]+))");
  std::vector<std::unique_ptr<SceneSource>> out;
  for (const auto& e : embedded_scene_sources()) {
    const std::string glsl = e.glsl;
    std::smatch m;
    const int row = std::regex_search(glsl, m, pal) ? palette_row(m[1].str()) : 0;
    out.push_back(std::make_unique<SceneSource>(e.name, glsl, row, post));
  }
  return out;
}

std::vector<std::unique_ptr<SceneSource>> load_scenes(const std::string& dir, std::shared_ptr<SceneLook> look) {
  std::vector<std::filesystem::path> files;
  std::error_code ec;
  for (const auto& e : std::filesystem::directory_iterator(dir, ec)) {
    if (e.path().extension() == ".frag") files.push_back(e.path());
  }
  std::sort(files.begin(), files.end());
  auto post = std::make_shared<ScenePost>(look ? std::move(look) : std::make_shared<SceneLook>());
  std::vector<std::unique_ptr<SceneSource>> out;
  const std::regex pal(R"(//\s*palette:\s*([a-z]+))");
  for (const auto& f : files) {
    std::ifstream in(f);
    std::stringstream body;
    body << in.rdbuf();
    const std::string glsl = body.str();
    std::smatch m;
    const int row = std::regex_search(glsl, m, pal) ? palette_row(m[1].str()) : 0;
    out.push_back(std::make_unique<SceneSource>(f.stem().string(), glsl, row, post));
  }
  return out;
}

}  // namespace cadence::render::viz
