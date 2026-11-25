#include "app/routes/ambient/idle-renderer.h"

#include "ipc/log.h"

#include <cmath>
#include <string>

namespace cadence::screen::routes {

namespace {

constexpr std::string_view kFragment = R"(#version 300 es
precision highp float;
uniform vec2 u_screen;
uniform float u_time;
uniform vec4 u_color_a;
uniform vec4 u_color_b;
uniform float u_bright;
in vec2 v_uv;
out vec4 color;

float hash12(vec2 p) {
  vec3 p3 = fract(vec3(p.xyx) * 0.1031);
  p3 += dot(p3, p3.yzx + 33.33);
  return fract((p3.x + p3.y) * p3.z);
}

float noise(vec2 p) {
  vec2 i = floor(p), f = fract(p);
  vec2 u = f * f * (3.0 - 2.0 * f);
  return mix(mix(hash12(i), hash12(i + vec2(1.0, 0.0)), u.x), mix(hash12(i + vec2(0.0, 1.0)), hash12(i + vec2(1.0, 1.0)), u.x), u.y);
}

void main() {
  vec2 uv = vec2(v_uv.x, 1.0 - v_uv.y);
  float aspect = u_screen.x / u_screen.y;
  vec2 p = vec2(uv.x * aspect, uv.y);
  float t = u_time;

  // the sky: deep at the bottom, the colours faint at the top
  vec3 col = mix(vec3(0.012, 0.014, 0.024), u_color_b.rgb * 0.16, smoothstep(0.0, 1.0, uv.y));

  // stars, a few, twinkling slowly
  vec2 cell = floor(p * 70.0);
  float h = hash12(cell);
  if (h > 0.985) {
    vec2 c = (cell + 0.5 + (vec2(hash12(cell + 3.1), hash12(cell + 7.7)) - 0.5) * 0.6) / 70.0;
    float d = length(p - c) * 70.0;
    float tw = 0.5 + 0.5 * sin(t * (0.6 + h * 2.0) + h * 40.0);
    col += vec3(0.9, 0.92, 1.0) * exp(-d * d * 9.0) * tw * 0.55 * smoothstep(0.25, 0.8, uv.y);
  }

  // three curtains of aurora, each a wavering band with vertical rays
  for (float i = 0.0; i < 3.0; i++) {
    float x = p.x * (0.9 + i * 0.35) + i * 3.7;
    float wave = 0.58 + i * 0.07 + 0.09 * sin(x * 1.3 + t * (0.05 + i * 0.02)) + 0.05 * sin(x * 3.1 - t * 0.07 + i)
                 + 0.04 * (noise(vec2(x * 1.5, t * 0.04 + i)) - 0.5);
    float above = uv.y - wave;
    // a sharp lower edge and a long soft fall upwards, as curtains hang
    float band = exp(-above * above * 900.0) * step(above, 0.0) + exp(-max(above, 0.0) * 4.0) * step(0.0, above);
    float rays = 0.25 + 1.4 * pow(noise(vec2(x * 26.0, t * 0.12 + i * 5.0)), 1.6);
    float flicker = 0.75 + 0.25 * sin(t * 0.21 + i * 2.0 + x * 0.7);
    vec3 c = mix(u_color_a.rgb, u_color_b.rgb, 0.5 + 0.5 * sin(x * 0.8 + t * 0.03 + i));
    col += c * band * rays * flicker * (0.42 - i * 0.1);
  }

  // a soft horizon glow and the vignette
  col += u_color_a.rgb * 0.05 * exp(-uv.y * 6.0);
  float vig = smoothstep(1.25, 0.3, length((v_uv - 0.5) * vec2(1.2, 1.0)));
  col *= mix(0.6, 1.0, vig) * u_bright;
  // a little grain so the gradient never bands on an 8-bit panel
  col += (hash12(v_uv * u_screen + fract(t) * 91.0) - 0.5) / 255.0 * 2.0;
  color = vec4(col, 1.0);
}
)";

float channel(std::uint32_t rgb, int shift) { return static_cast<float>((rgb >> shift) & 0xffu) / 255.0f; }

}  // namespace

IdlePalette idlePalette(int hour) {
  if (hour >= 6 && hour < 11) return {0x5fd3b0, 0xf2a46b, 0.85f};   // morning: mint and peach
  if (hour >= 11 && hour < 17) return {0x4fc3f7, 0x9b7bff, 0.9f};   // day: sky and violet
  if (hour >= 17 && hour < 22) return {0xff6fa8, 0x7a5cff, 0.8f};   // evening: rose and indigo
  return {0x3ee8a0, 0x2a6cff, 0.55f};                                // night: the polar green, dim
}

void IdleRenderer::draw(render::RenderBackend& backend, render::Rect screen, double nowS, int localHour,
                        bool reducedMotion) {
  (void)screen;
  if (failed_) return;
  if (!program_) {
    std::string error;
    program_ = backend.compileProgram(render::kFullscreenVertexShader, kFragment, &error);
    if (!program_) {
      failed_ = true;
      CADENCE_LOG(Warn, "idle_backdrop_unavailable");
      return;
    }
    backend_ = &backend;
  }
  const auto pal = idlePalette(localHour);
  const auto bound = backend.boundSize();
  backend.setUniform(program_, "u_screen", static_cast<float>(bound.width), static_cast<float>(bound.height));
  // reduced motion: the same sky, drifting at a fifth of the pace
  backend.setUniform(program_, "u_time", static_cast<float>(std::fmod(nowS * (reducedMotion ? 0.2 : 1.0), 20000.0)));
  backend.setUniform(program_, "u_color_a", channel(pal.a, 16), channel(pal.a, 8), channel(pal.a, 0), 1.0f);
  backend.setUniform(program_, "u_color_b", channel(pal.b, 16), channel(pal.b, 8), channel(pal.b, 0), 1.0f);
  backend.setUniform(program_, "u_bright", pal.brightness);
  backend.drawFullscreen(program_, {}, render::Mat3::identity(), 1.0f, render::Blend::None);
}

void IdleRenderer::release() {
  if (backend_ != nullptr && program_) backend_->destroyProgram(program_);
  program_ = {};
  backend_ = nullptr;
  failed_ = false;
}

void IdleRenderer::abandon() {
  program_ = {};
  backend_ = nullptr;
  failed_ = false;
}

}  // namespace cadence::screen::routes
