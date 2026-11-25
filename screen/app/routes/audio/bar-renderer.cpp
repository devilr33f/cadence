#include "app/routes/audio/bar-renderer.h"

#include "ipc/log.h"

#include <string>

namespace cadence::screen::routes {

namespace {

constexpr std::string_view kFragment = R"(#version 300 es
precision highp float;
precision highp int;
uniform vec2 u_screen;
uniform vec4 u_band;
uniform vec4 u_row;
uniform vec4 u_color_a;
uniform vec4 u_color_b;
uniform float u_opacity;
uniform float u_heights[16];
in vec2 v_uv;
out vec4 color;

vec3 sleeve_color(vec3 rgb) {
  float luma = dot(rgb, vec3(0.2126, 0.7152, 0.0722));
  return rgb * min(1.0, 0.18 / max(luma, 0.0001));
}

float capsule_distance(vec2 p, vec2 half_size) {
  float radius = min(half_size.x, half_size.y);
  vec2 q = abs(p) - (half_size - vec2(radius));
  return length(max(q, vec2(0.0))) + min(max(q.x, q.y), 0.0) - radius;
}

void main() {
  vec2 p = v_uv * u_screen;
  float vertical = clamp(p.y / u_screen.y, 0.0, 1.0);
  vec3 dark_a = sleeve_color(u_color_a.rgb);
  vec3 dark_b = sleeve_color(u_color_b.rgb);
  vec3 base = vertical < 0.55 ? mix(dark_a, dark_b, vertical / 0.55)
                              : mix(dark_b, vec3(0.04706, 0.05490, 0.07451), (vertical - 0.55) / 0.45);

  float alpha = 0.0;
  vec3 tint = vec3(0.0);
  if (p.y >= u_band.y - 1.0 && p.y <= u_band.y + u_band.w + 1.0) {
    float first_x = u_row.x;
    float baseline = u_row.y;
    float pitch = u_row.z;
    float bar_width = u_row.w;
    int index = int(floor((p.x - first_x) / pitch));
    if (index >= 0 && index < 16 && p.y <= baseline + 1.0) {
      float height = u_heights[index];
      if (height >= 1.0) {
        float center_x = first_x + float(index) * pitch + bar_width * 0.5;
        vec2 center = vec2(center_x, baseline - height * 0.5);
        float distance = capsule_distance(p - center, vec2(bar_width, height) * 0.5);
        float coverage = 1.0 - smoothstep(-1.0, 1.0, distance);
        float row_width = 15.0 * pitch + bar_width;
        float across = clamp((p.x - first_x) / row_width, 0.0, 1.0);
        vec3 tint_a = u_color_a.rgb * 0.80 + vec3(0.18, 0.17, 0.19);
        vec3 tint_b = u_color_b.rgb * 0.80 + vec3(0.18, 0.17, 0.19);
        tint = mix(tint_a, tint_b, across);
        float down = clamp((p.y - (baseline - height)) / height, 0.0, 1.0);
        alpha = coverage * u_opacity * mix(0.60, 1.0, down);
      }
    }
  }
  color = vec4(mix(base, tint, alpha), 1.0);
}
)";

float channel(std::uint32_t rgb, int shift) {
  return static_cast<float>((rgb >> shift) & 0xffu) / 255.0f;
}

void setColor(render::RenderBackend& backend, render::Program program, const char* name, std::uint32_t rgb) {
  backend.setUniform(program, name, channel(rgb, 16), channel(rgb, 8), channel(rgb, 0), 1.0f);
}

}  // namespace

void BarRenderer::draw(render::RenderBackend& backend, render::Rect screen,
                       const std::array<double, BarSmoother::kBars>& heights, std::uint32_t colorA,
                       std::uint32_t colorB) {
  const BarLayout layout = barLayout(screen);
  if (!layout.valid() || failed_) return;
  if (!program_) {
    std::string error;
    program_ = backend.compileProgram(render::kFullscreenVertexShader, kFragment, &error);
    if (!program_) {
      failed_ = true;
      CADENCE_LOG(Warn, "audio_bars_unavailable");
      return;
    }
    backend_ = &backend;
  }
  const auto bound = backend.boundSize();
  backend.setUniform(program_, "u_screen", static_cast<float>(bound.width), static_cast<float>(bound.height));
  backend.setUniform(program_, "u_band", static_cast<float>(layout.band.x), static_cast<float>(layout.band.y),
                     static_cast<float>(layout.band.width), static_cast<float>(layout.band.height));
  backend.setUniform(program_, "u_row", static_cast<float>(layout.firstX), static_cast<float>(layout.baseline),
                     static_cast<float>(layout.pitch), static_cast<float>(layout.barWidth));
  setColor(backend, program_, "u_color_a", colorA);
  setColor(backend, program_, "u_color_b", colorB);
  for (int i = 0; i < BarSmoother::kBars; ++i) {
    const auto rect = barRect(layout, i, heights[static_cast<std::size_t>(i)]);
    const std::string name = "u_heights[" + std::to_string(i) + "]";
    backend.setUniform(program_, name.c_str(), static_cast<float>(rect.height));
  }
  // one full-screen quad: the sleeve's old gradient plus every capsule, with no rml decorator or per-bar geometry
  backend.drawFullscreen(program_, {}, render::Mat3::identity(), 0.20f, render::Blend::None);
}

void BarRenderer::release() {
  if (backend_ != nullptr && program_) backend_->destroyProgram(program_);
  program_ = {};
  backend_ = nullptr;
  failed_ = false;
}

void BarRenderer::abandon() {
  program_ = {};
  backend_ = nullptr;
  failed_ = false;
}

}  // namespace cadence::screen::routes
