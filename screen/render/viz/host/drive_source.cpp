#include "drive_source.h"

#include "drive_motion.h"
#include "impact.h"

#include <algorithm>
#include <array>
#include <cmath>
#include <numbers>

namespace cadence::render::viz {

namespace {

constexpr int kSpikeLines = 160;

// the scene: background, the shape, its spikes. y up, p in units of the height around the middle
const char* const kScene = R"(
uniform sampler2D uAudio;
// the spikes' lengths (in shape radii), one a line, computed on the cpu each frame: a lookup a pixel, not the
// spectrum's log and pow
uniform sampler2D uSpikes;
uniform sampler2D uArt;
uniform float uHasArt;
uniform vec2 uRes;
uniform float uTime;
uniform int uVariant;
uniform int uShape;
uniform int uPrevShape;
uniform float uMorph;
uniform float uTravel;
uniform float uSpin;
uniform float uTurn;
uniform float uPunch;
uniform float uKick, uSnare, uHat;
uniform float uEnergy, uIntensity, uBright, uPhase, uDrop, uDropWave;
uniform float uBass, uMid, uTreble;
uniform vec3 uColA, uColB, uAccent;
// 0: the soft layer (nebula, particles, tunnel, the shape's glow) at half size; 1: the sharp one (the shape, its rim
// and spikes) at full size, with its alpha
uniform int uLayer;
// the tunnel's ring colours, worked out on the cpu
uniform vec3 uRingCol[6];
in vec2 vUv;
out vec4 fragColor;

const float PI = 3.14159265;
const float TAU = 6.2831853;

float fft(float u) { return texture(uAudio, vec2(clamp(u, 0.0, 1.0), 0.25)).r; }
float fftLog(float u) {
  float b = exp2(mix(1.0, log2(400.0), clamp(u, 0.0, 1.0)));
  return fft(b / 511.0);
}
float hash12(vec2 p) {
  vec3 p3 = fract(vec3(p.xyx) * 0.1031);
  p3 += dot(p3, p3.yzx + 33.33);
  return fract((p3.x + p3.y) * p3.z);
}
vec2 hash22(vec2 p) {
  vec3 p3 = fract(vec3(p.xyx) * vec3(0.1031, 0.1030, 0.0973));
  p3 += dot(p3, p3.yzx + 33.33);
  return fract((p3.xx + p3.yz) * p3.zy);
}
mat2 rot(float a) { float s = sin(a), c = cos(a); return mat2(c, -s, s, c); }

// the boundary's radius at angle a (the shape's own frame) for radius r
float boundary(int shape, float a, float r) {
  if (shape == 0) return r;
  if (shape == -1) {
    // a five-pointed star: straight edges between the points (r) and the notches (0.5 r), a point up
    float s = TAU / 10.0;
    float ro = r * 1.12, ri = r * 0.5;
    float f = mod(a - PI * 0.5, TAU);
    float k = floor(f / s);
    float phi = f - k * s;
    float r0 = mod(k, 2.0) < 0.5 ? ro : ri;
    float r1 = mod(k, 2.0) < 0.5 ? ri : ro;
    return r0 * r1 * sin(s) / (r0 * sin(phi) + r1 * sin(s - phi));
  }
  float n = shape == -2 ? 4.0 : float(shape);
  float off = shape == -2 ? PI / 4.0 : PI * 0.5 + PI / n;
  float apo = r * cos(PI / n);
  float rr = apo / cos(mod(a - off + PI / n, TAU / n) - PI / n);
  if (shape == -2) rr *= mix(1.0, 1.22, abs(sin(a)));
  return rr;
}

vec3 hsv(float h, float s, float v) {
  vec3 k = clamp(abs(mod(h * 6.0 + vec3(0.0, 4.0, 2.0), 6.0) - 3.0) - 1.0, 0.0, 1.0);
  return v * mix(vec3(1.0), k, s);
}

// the background's cover: fitted to cover the screen, blurred through its mips, punching in on kicks
vec3 cover(vec2 uv, float blur) {
  vec2 c = uv - 0.5;
  c.x *= uRes.x / uRes.y;
  c *= 0.62 / (1.0 + 0.05 * uPunch + 0.02 * uEnergy);
  c += vec2(sin(uTime * 0.07), cos(uTime * 0.05)) * 0.03;
  return textureLod(uArt, c + 0.5, blur).rgb;
}

vec3 nebula(vec2 p) {
  float t = uTime * 0.06;
  float n = sin(p.x * 2.1 + t * 3.0 + sin(p.y * 3.3 - t)) + sin(p.y * 2.7 - t * 2.0 + sin(p.x * 1.7 + t * 1.3));
  n = n * 0.25 + 0.5;
  vec3 c = mix(uColB * 0.35, uColA * 0.6, n);
  return c * (0.55 + 0.45 * uEnergy);
}

// a warp of particles flying out of the middle; speed is the travel the energy drives
vec3 particles(vec2 p, float layers, float streak) {
  vec3 acc = vec3(0.0);
  for (float i = 0.0; i < 4.0; i++) {
    if (i >= layers) break;
    float z = fract(uTravel * 0.12 + i / layers);
    float scale = mix(14.0, 0.6, z);
    float fade = smoothstep(0.0, 0.25, z) * smoothstep(1.0, 0.85, z);
    vec2 q = p * scale + vec2(i * 13.7, i * 7.1);
    vec2 cell = floor(q);
    vec2 f = fract(q) - 0.5;
    vec2 h = hash22(cell);
    vec2 d = f - (h - 0.5) * 0.7;
    // a streak along the ray from the middle, longer as it nears and as the music speeds it
    vec2 dir = normalize(p + 1e-4);
    float along = dot(d, dir);
    float across = dot(d, vec2(-dir.y, dir.x));
    float len = 0.02 + streak * z * z;
    float s = exp(-across * across * 3200.0) * smoothstep(len, 0.0, abs(along + len * 0.5) - len * 0.5);
    float glint = step(0.72, fract(h.x * 17.3 + floor(uTime * 12.0) * 0.37)) * uHat;
    float tw = 0.6 + 0.4 * sin(uTime * 3.0 + h.x * 40.0) + 1.8 * glint;
    vec3 col = mix(vec3(1.0), mix(uAccent, uColA, h.y), 0.55 * (1.0 - 0.7 * glint));
    acc += col * s * fade * tw * step(0.55, h.x) * (0.5 + 1.2 * z);
  }
  return acc;
}

// volumetric godrays radiating from behind the shape silhouette, driven by snares, kicks, and drops
vec3 godrays(float ang, float edge, float amount) {
  if (amount <= 0.005 || edge < -0.01) return vec3(0.0);
  float t = uTime;
  float r1 = pow(0.5 + 0.5 * sin(ang * 5.0 + t * 0.35 + uSpin * 1.2), 7.0);
  float r2 = pow(0.5 + 0.5 * sin(ang * 9.0 - t * 0.52 - uTurn * 0.5), 9.0);
  float r3 = pow(0.5 + 0.5 * sin(ang * 13.0 + t * 0.78), 12.0);
  float shafts = r1 * 0.55 + r2 * 0.35 + r3 * 0.25;
  float reach = 2.6 - 0.9 * clamp(amount, 0.0, 1.2);
  float radial = exp(-max(edge, 0.0) * reach) * smoothstep(-0.005, 0.018, edge);
  vec3 rayCol = mix(uAccent, vec3(1.0), 0.35 * r3 + 0.2 * radial);
  return rayCol * shafts * radial * amount;
}

void main() {
  float aspect = uRes.x / uRes.y;
  vec2 p = (vUv - vec2(0.5, 0.5)) * vec2(aspect, 1.0);
  float d = length(p);
  float ang = atan(p.y, p.x);

  // the backdrop (the cover) is the post's, drawn at full size under this pass: here only what goes over it, and
  // alpha says how much of it each pixel hides. without a cover the nebula stands in, added here
  // the shape, punched by the kick, turning slowly, morphing from the last track's
  float R = 0.235 * uPunch;
  float a = ang - uSpin;
  float rb = uMorph >= 1.0 ? boundary(uShape, a, R) : mix(boundary(uPrevShape, a, R), boundary(uShape, a, R), uMorph);
  float edge = d - rb;

  if (uLayer == 1) {
    // the sharp layer: nothing beyond the longest spike
    if (edge > R * 2.0) {
      fragColor = vec4(0.0);
      return;
    }
    vec3 col = vec3(0.0);
    float alpha = 0.0;
    // the spikes: a comb of radial lines whose lengths are the spectrum, a quarter of it per quadrant, mirrored
    // both ways: bass shoots up and down, treble to the sides
    float u = fract(a / TAU + 0.25);
    float lines = 160.0;
    float li = floor(u * lines);
    float uq = (li + 0.5) / lines;
    float len = R * texelFetch(uSpikes, ivec2(int(li), 0), 0).r;
    float lw = fract(u * lines);
    float line = smoothstep(0.42, 0.22, abs(lw - 0.5));
    float spike = clamp(step(0.0, edge) * line * smoothstep(len, len * 0.55, edge) * smoothstep(-0.002, 0.004, edge), 0.0, 1.0);
    float tip = smoothstep(len * 0.45, len + 1e-3, edge);
    vec3 spikeCol = mix(vec3(1.0), uAccent, 0.15 + 0.35 * smoothstep(0.0, len + 1e-3, edge));
    spikeCol += vec3(1.0) * tip * uHat * 0.65;
    // a little over full: the thin lines read as bright as they did drawn over the backdrop, and feed the bloom
    col = spikeCol * spike * 1.25;
    alpha = spike;
    // inside: dark glass over the cover, a white rim
    if (edge < 0.0) {
      vec3 inner = uHasArt > 0.5 ? cover(vUv, 3.0) * 0.32 : uColB * 0.25;
      inner = mix(inner, uColA * 0.18, 0.4);
      inner += uAccent * exp(edge * 30.0) * (0.15 + 0.4 * uKick);
      col = inner;
      alpha = 1.0;
    }
    float rim = clamp(exp(-pow(edge / (0.0035 + 0.002 * uKick + 0.0012 * uHat), 2.0)), 0.0, 1.0);
    col = mix(col, vec3(1.0), rim);
    alpha = max(alpha, rim);
    // premultiplied: the post lays it over the soft layer and the cover
    fragColor = vec4(col, alpha);
    return;
  }

  vec3 col = uHasArt > 0.5 ? vec3(0.0) : nebula(p * (uVariant == 1 ? 0.8 : 1.0)) * (uVariant == 0 ? 0.6 : 0.4);
  if (uVariant == 0) {
    col += uAccent * 0.06 * uKick;
    col += particles(p, 2.0, 0.05 + 0.3 * uEnergy + 0.45 * uIntensity + 1.1 * uDrop) * (0.6 + 0.7 * uDrop);
  } else if (uVariant == 1) {
    col += particles(rot(uTurn) * p, 3.0, 0.12 + 0.6 * uEnergy + 0.9 * uIntensity + 2.8 * uDrop) * (1.3 + 1.4 * uDrop);
  } else {
    // a tunnel of the shape's outlines coming at the viewer
    for (int k = 0; k < 6; k++) {
      float z = fract(uTravel * 0.09 + float(k) / 6.0);
      float r = 0.05 / max(1.0 - z, 0.04);
      float w = 0.003 + 0.01 * z;
      // an outline lies between its apothem (at most half of r for these shapes) and 1.25 r: a pixel well outside
      // that band skips the ring's work
      if (d < r * 0.45 - 3.0 * w || d > r * 1.25 + 3.0 * w) continue;
      float a = ang - uTurn * (0.3 + 0.2 * float(k)) - uSpin;
      float e = (d - boundary(uShape, a, r)) / w;
      float ring = exp(-e * e) * smoothstep(0.0, 0.3, z) * smoothstep(1.0, 0.7, z);
      col += uRingCol[k] * ring * (0.35 + 0.9 * uEnergy + 0.8 * uDrop);
    }
    col += particles(p, 1.0, 0.08 + 0.3 * uEnergy + 1.4 * uDrop) * (0.8 + 0.7 * uDrop);
  }

  // the glow round the shape, coloured, swelling on kicks and exploding on drops
  float glow = exp(-max(edge, 0.0) * (34.0 - 14.0 * uKick - 12.0 * uDrop)) * (0.12 + 0.65 * uKick + 0.15 * uEnergy + 0.65 * uDrop);
  col += uAccent * glow * step(0.0, edge);
  // volumetric godrays from behind the shape: snares/hits launch shafts, kicks & drops amplify them
  col += godrays(ang, edge, 0.75 * uSnare + 0.35 * uKick + 0.45 * uDrop);
  // a shockwave outline blasting outward from the shape when a drop lands
  if (uDropWave < 0.999) {
    float wr = R * (1.0 + 4.2 * pow(uDropWave, 0.72));
    float ww = 0.008 + 0.028 * uDropWave;
    float we = (d - boundary(uShape, a, wr)) / ww;
    float wave = exp(-we * we) * (1.0 - uDropWave) * (1.0 - uDropWave);
    col += mix(vec3(1.0), uAccent, 0.55) * wave * 1.8;
  }
  fragColor = vec4(col, 0.0);
}
)";

// the post: a camera punch and shake, an rgb split and a zoom blur on kicks, the flare across the middle, the
// vignette and grain, and a drop's lift
const char* const kPost = R"(
precision mediump float;
uniform sampler2D uTex;
uniform sampler2D uSoft;
uniform sampler2D uBloom;
uniform float uBloomAmt;
uniform vec3 uTint;
uniform float uTintAmt;
uniform vec2 uRes;
uniform highp float uTime;
uniform float uKick;
uniform float uPunch;
uniform vec2 uShake;
uniform float uFlare;
uniform float uIntensity;
uniform float uDrop;
uniform vec3 uAccent;
uniform sampler2D uArt;
uniform float uHasArt;
uniform vec2 uArtSize;
uniform float uBgDim;
uniform float uBgLod;
uniform float uEnergy;
uniform float uWarp;
in vec2 vUv;
out vec4 fragColor;
// the cover filling the screen without stretching (cropped to its middle band), drifting slowly
vec3 backdrop(vec2 uv) {
  vec2 c = uv - 0.5;
  float screen = uRes.x / uRes.y, art = uArtSize.x / max(uArtSize.y, 1.0);
  c *= screen > art ? vec2(1.0, art / screen) : vec2(screen / art, 1.0);
  c *= 0.94;
  c += vec2(sin(uTime * 0.05), cos(uTime * 0.04)) * 0.02;
  return textureLod(uArt, c + 0.5, uBgLod).rgb;
}
float hash12(vec2 p) {
  vec3 p3 = fract(vec3(p.xyx) * 0.1031);
  p3 += dot(p3, p3.yzx + 33.33);
  return fract((p3.x + p3.y) * p3.z);
}
void main() {
  vec2 c = vec2(0.5);
  vec2 uv = c + (vUv - c) / (1.0 + 0.045 * (uPunch - 1.0) * 4.0) + uShake;
  // a sine bend. how far it moves is the bass level (uWarp); the wave itself does not depend on anything else
  uv.x += uWarp * 0.012 * sin(uv.y * 8.0 + uTime * 2.0);
  uv.y += uWarp * 0.006 * sin(uv.x * 6.0 + uTime * 1.4);
  vec2 dir = uv - c;
  float ca = 0.004 + 0.042 * uKick + 0.012 * uIntensity + 0.045 * uDrop;
  vec4 sg = texture(uTex, uv);
  vec3 col = sg.rgb;
  // the rgb split, on a kick or a drop alone: two more reads a pixel
  if (ca > 0.0065) {
    col.r = texture(uTex, c + dir * (1.0 + ca)).r;
    col.b = texture(uTex, c + dir * (1.0 - ca)).b;
  }
  // under the sharp layer: the soft one (half size, upscaled) and the cover at full size, punching and shaking
  // with it
  vec3 under = texture(uSoft, uv).rgb;
  // a zoom blur toward the middle on the kick and drop: of what lies under the shape alone (the soft layer), never over it
  float zb = 0.14 * uKick + 0.18 * uDrop;
  if (zb > 0.003) {
    vec3 acc = under;
    for (float i = 1.0; i <= 4.0; i++) acc += texture(uSoft, c + dir * (1.0 - zb * i / 4.0)).rgb;
    under = mix(under, acc / 5.0, 0.72);
  }
  if (uHasArt > 0.5) under += backdrop(uv) * uBgDim * (0.85 + 0.3 * uEnergy);
  col += under * (1.0 - sg.a);
  col += texture(uBloom, uv).rgb * uBloomAmt;
  col = mix(col, col * uTint * 1.25, uTintAmt);
  // the horizontal glow (an anamorphic streak across the middle) is off for now
  // a drop radiates neon energy from the middle instead of washing the corners in flat fog
  float radial = exp(-length(dir * vec2(uRes.x / uRes.y, 1.0)) * 2.1);
  col += uAccent * (0.05 * uIntensity * radial + 0.38 * uDrop * radial + 0.03 * uDrop);
  float vig = smoothstep(1.15, 0.35, length((vUv - c) * vec2(1.25, 1.0)));
  col *= mix(0.55, 1.0, vig);
  fragColor = vec4(clamp(col, 0.0, 1.0), 1.0);
}
)";

void bind_tex(int unit, unsigned texture) {
  glActiveTexture(static_cast<GLenum>(GL_TEXTURE0 + unit));
  glBindTexture(GL_TEXTURE_2D, texture);
}

int loc(unsigned program, const char* name) { return glGetUniformLocation(program, name); }

// hue of an rgb, 0..1
float hue_of(float r, float g, float b) {
  const float mx = std::max({r, g, b}), mn = std::min({r, g, b});
  const float d = mx - mn;
  if (d < 1e-4f) return 0.75f;
  float h = 0;
  if (mx == r) h = std::fmod((g - b) / d, 6.0f);
  else if (mx == g) h = (b - r) / d + 2;
  else h = (r - g) / d + 4;
  h /= 6;
  return h < 0 ? h + 1 : h;
}

std::array<float, 3> hsv(float h, float s, float v) {
  std::array<float, 3> out{};
  for (int i = 0; i < 3; ++i) {
    const float k = std::fmod(h * 6.0f + static_cast<float>((6 - 2 * i) % 6), 6.0f);
    const float c = std::clamp(std::abs(k - 3.0f) - 1.0f, 0.0f, 1.0f);
    out[static_cast<std::size_t>(i)] = v * (1 - s + s * c);
  }
  return out;
}

double ease(double cur, double target, double dt, double tau) {
  return cur + (target - cur) * (1 - std::exp(-dt / std::max(tau, 1e-4)));
}

}  // namespace

DriveSource::DriveSource(DriveVariant v, std::shared_ptr<SceneLook> look, std::shared_ptr<ScenePost> post)
    : v_(v), look_(std::move(look)), post_(std::move(post)) {}

std::string DriveSource::id() const {
  switch (v_) {
    case DriveVariant::Cover: return "drive:cover";
    case DriveVariant::Warp: return "drive:warp";
    case DriveVariant::Tunnel: return "drive:tunnel";
  }
  return "drive";
}

float DriveSource::scene_scale() const {
  return std::clamp(look_ ? look_->scene_scale.load() : 0.5f, 0.25f, 1.0f);
}

int DriveSource::shape_for(std::uint64_t seed, DriveVariant v) {
  // splitmix, so neighbouring seeds land apart; each variant its own pick of the same track
  std::uint64_t z = seed + 0x9E3779B97F4A7C15ULL * (static_cast<std::uint64_t>(v) + 1);
  z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9ULL;
  z = (z ^ (z >> 27)) * 0x94D049BB133111EBULL;
  z ^= z >> 31;
  constexpr std::array<int, 8> kShapes{3, 0, 4, 6, -1, 5, -2, 3};
  return kShapes[static_cast<std::size_t>(z % kShapes.size())];
}

bool DriveSource::init(int width, int height) {
  w_ = width;
  h_ = height;
  if (!post_ || !post_->init(width, height)) {
    error_ = "post";
    return false;
  }
  scene_ = gl::program(gl::kFullscreenVertex, kScene, &error_);
  post_program_ = scene_ ? gl::program(gl::kFullscreenVertex, kPost, &error_) : 0;
  if (!scene_ || !post_program_ || !a_.make(std::max(1, static_cast<int>(static_cast<float>(width) * scene_scale())),
                                                std::max(1, static_cast<int>(static_cast<float>(height) * scene_scale()))) ||
      !c_.make(width, height)) {
    release();
    return false;
  }
  // the spikes' lengths, a float a line
  glGenTextures(1, &spikes_);
  glBindTexture(GL_TEXTURE_2D, spikes_);
  glTexImage2D(GL_TEXTURE_2D, 0, GL_R32F, kSpikeLines, 1, 0, GL_RED, GL_FLOAT, nullptr);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_NEAREST);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_NEAREST);
  // a black 1x1 until a cover comes
  glGenTextures(1, &art_);
  glBindTexture(GL_TEXTURE_2D, art_);
  const std::array<std::uint8_t, 4> black{0, 0, 0, 255};
  glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA8, 1, 1, 0, GL_RGBA, GL_UNSIGNED_BYTE, black.data());
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MIN_FILTER, GL_LINEAR_MIPMAP_LINEAR);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_MAG_FILTER, GL_LINEAR);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_S, GL_MIRRORED_REPEAT);
  glTexParameteri(GL_TEXTURE_2D, GL_TEXTURE_WRAP_T, GL_MIRRORED_REPEAT);
  glGenerateMipmap(GL_TEXTURE_2D);
  return true;
}

void DriveSource::sync_artwork() {
  std::vector<std::uint8_t> rgba;
  int w = 0, h = 0;
  if (!look_ || !look_->take_artwork(art_generation_, rgba, w, h)) return;
  has_art_ = w > 0 && h > 0 && rgba.size() == static_cast<std::size_t>(w) * static_cast<std::size_t>(h) * 4;
  if (!has_art_) return;
  art_w_ = w;
  art_h_ = h;
  // rows top down into gl's bottom-up texture: flipped as they go up
  std::vector<std::uint8_t> flipped(rgba.size());
  const auto row = static_cast<std::size_t>(w) * 4;
  for (int y = 0; y < h; ++y)
    std::copy_n(rgba.begin() + static_cast<std::ptrdiff_t>(static_cast<std::size_t>(y) * row), row,
                flipped.begin() + static_cast<std::ptrdiff_t>(static_cast<std::size_t>(h - 1 - y) * row));
  glBindTexture(GL_TEXTURE_2D, art_);
  glPixelStorei(GL_UNPACK_ALIGNMENT, 1);
  glTexImage2D(GL_TEXTURE_2D, 0, GL_RGBA8, w, h, 0, GL_RGBA, GL_UNSIGNED_BYTE, flipped.data());
  glGenerateMipmap(GL_TEXTURE_2D);
}

bool DriveSource::activate() {
  travel_ = spin_ = shake_ = flare_ = jolt_ = sway_ = warp_ = 0;
  punch_ = snare_ = hat_ = 0;
  drop_wave_ = 1;
  return true;
}

void DriveSource::render(const FrameInputs& in, unsigned fbo) {
  sync_artwork();
  const double dt = std::clamp(in.dt, 0.0, 0.1);
  // the shape follows the track; a new one morphs in over half a second
  const std::uint64_t track = look_ ? look_->track.load() : 0;
  const int forced = look_ ? look_->shape.load() : SceneLook::kAutoShape;
  const int want = forced != SceneLook::kAutoShape ? forced : shape_for(track, v_);
  if (track != seen_track_ || want != shape_) {
    prev_shape_ = seen_track_ == ~0ULL ? want : shape_;
    shape_ = want;
    seen_track_ = track;
    shape_change_ = 0;
  }
  shape_change_ = std::min(1.0, shape_change_ + dt / 0.5);
  const double morph = shape_change_ * shape_change_ * (3 - 2 * shape_change_);

  // particles: the average loudness alone (host/drive_motion.h)
  travel_ += dt * drive_travel_rate(in);
  // zoom, shake and rotation are the drum, shaped exponentially so a slight opening drum barely moves and a strong
  // one drives the whole punch
  const double strong = static_cast<double>(exponential_strength(in.kick_pulse));
  const double drop_lvl = static_cast<double>(in.drop_level);
  const double drop_hit = static_cast<double>(in.drop_pulse);
  if (in.drop) drop_wave_ = 0;
  else drop_wave_ = std::min(1.0, drop_wave_ + dt / 0.65);
  jolt_ = ease(jolt_, 0.0, dt, 0.22);
  if (in.kick) jolt_ += (0.16 + 0.08 * drop_lvl) * strong * (in.beat_count % 2 == 0 ? 1.0 : -1.0);
  if (in.drop) jolt_ += 0.22 * (in.beat_count % 2 == 0 ? 1.0 : -1.0);
  spin_ = jolt_;
  punch_ = std::max(punch_ * std::pow(0.02, dt / 0.3), strong);
  snare_ = std::max(snare_ * std::pow(0.04, dt / 0.28), static_cast<double>(exponential_strength(in.snare_pulse)));
  hat_ = std::max(hat_ * std::pow(0.04, dt / 0.14), static_cast<double>(exponential_strength(in.hat_pulse)));
  // the flare across the middle is off (below). this pulse is unused until it comes back
  flare_ = 0;
  // the bass level is how hard the sine bend is, gated by bass_presence^2 so intros and build-ups stay crisp
  const double bp = static_cast<double>(in.bass_presence);
  const double bass = static_cast<double>(in.bass_level) * bp * bp;
  warp_ = ease(warp_, bass, dt, bass > warp_ ? 0.04 : 0.18);
  shake_ = std::max(shake_ * std::pow(0.05, dt / 0.16), std::max(strong, drop_hit));
  // the accent's hue: the cover's, nudged on each beat and drifting
  const float base = hue_of(look_->tint_r.load(), look_->tint_g.load(), look_->tint_b.load());
  hue_ = std::fmod(hue_ + dt * 0.01 + (in.onset ? 0.035 * in.beat_pulse : 0.0) + (in.drop ? 0.15 : 0.0), 1.0);
  const auto accent = hsv(std::fmod(base + static_cast<float>(hue_) + 0.55f, 1.0f), 0.75f, 1.0f);
  const auto col_a = hsv(base, 0.6f, 0.95f);
  const auto col_b = hsv(std::fmod(base + 0.12f, 1.0f), 0.55f, 0.55f);
  // the shape grows on the drum (and slams harder on a drop), never on a quiet intro or build-up
  const double punch = 1.0 + (0.22 + 0.08 * drop_lvl) * punch_ + 0.10 * drop_hit;
  if (look_) look_->punch.store(static_cast<float>(punch));

  // the spikes' lengths, as the shader did them per pixel: a line's place mirrored into a quarter of the spectrum on
  // a log scale (its row of the audio texture, linearly between bins), lifted by a contrast curve
  std::array<float, kSpikeLines> spikes{};
  for (int li = 0; li < kSpikeLines; ++li) {
    const double uq = (li + 0.5) / kSpikeLines;
    const double m = std::abs(std::fmod(uq * 2.0, 1.0) * 2.0 - 1.0);
    const double x = 0.02 + (0.8 - 0.02) * (1.0 - m);
    const double bin = std::exp2(1.0 + (std::log2(400.0) - 1.0) * x);
    const double at = std::clamp(bin / 511.0 * static_cast<double>(kFftBins) - 0.5, 0.0, static_cast<double>(kFftBins - 1));
    const auto lo = static_cast<std::size_t>(at);
    const std::size_t hi = std::min(lo + 1, kFftBins - 1);
    const double v = in.fft[lo] + (in.fft[hi] - in.fft[lo]) * (at - static_cast<double>(lo));
    spikes[static_cast<std::size_t>(li)] = static_cast<float>(
        0.04 + 1.5 * std::pow(std::clamp(v, 0.0, 1.0), 2.6) * (0.75 + 0.45 * in.energy + 0.30 * drop_lvl + 0.25 * punch_));
  }
  glBindTexture(GL_TEXTURE_2D, spikes_);
  glPixelStorei(GL_UNPACK_ALIGNMENT, 4);
  glTexSubImage2D(GL_TEXTURE_2D, 0, 0, 0, kSpikeLines, 1, GL_RED, GL_FLOAT, spikes.data());

  a_.bind();
  glDisable(GL_BLEND);
  glUseProgram(scene_);
  bind_tex(0, in.audio_texture);
  glUniform1i(loc(scene_, "uAudio"), 0);
  bind_tex(1, art_);
  glUniform1i(loc(scene_, "uArt"), 1);
  bind_tex(2, spikes_);
  glUniform1i(loc(scene_, "uSpikes"), 2);
  glUniform1f(loc(scene_, "uHasArt"), has_art_ ? 1.0f : 0.0f);
  glUniform2f(loc(scene_, "uRes"), static_cast<float>(w_), static_cast<float>(h_));
  glUniform1f(loc(scene_, "uTime"), static_cast<float>(std::fmod(in.time_s, 3600.0)));
  glUniform1i(loc(scene_, "uVariant"), static_cast<int>(v_));
  glUniform1i(loc(scene_, "uShape"), shape_);
  glUniform1i(loc(scene_, "uPrevShape"), prev_shape_);
  glUniform1f(loc(scene_, "uMorph"), static_cast<float>(morph));
  glUniform1f(loc(scene_, "uTravel"), static_cast<float>(std::fmod(travel_, 1000.0)));
  glUniform1f(loc(scene_, "uSpin"), static_cast<float>(spin_));
  glUniform1f(loc(scene_, "uTurn"), static_cast<float>(std::fmod(travel_ * 0.03, 2 * std::numbers::pi)));
  glUniform1f(loc(scene_, "uPunch"), static_cast<float>(punch));
  glUniform1f(loc(scene_, "uKick"), static_cast<float>(punch_));
  glUniform1f(loc(scene_, "uSnare"), static_cast<float>(snare_));
  glUniform1f(loc(scene_, "uHat"), static_cast<float>(hat_));
  glUniform1f(loc(scene_, "uEnergy"), in.energy);
  glUniform1f(loc(scene_, "uIntensity"), in.intensity);
  // the hit plus most of the section: streaks, glow and the tunnel read the drop, not only its first frame
  const float drop_vis = std::min(1.6f, in.drop_pulse + 0.75f * in.drop_level);
  glUniform1f(loc(scene_, "uDrop"), drop_vis);
  glUniform1f(loc(scene_, "uDropWave"), static_cast<float>(drop_wave_));
  glUniform1f(loc(scene_, "uBright"), in.brightness);
  glUniform1f(loc(scene_, "uPhase"), in.beat_phase);
  glUniform1f(loc(scene_, "uBass"), in.bass_level);
  glUniform1f(loc(scene_, "uMid"), in.mid_level);
  glUniform1f(loc(scene_, "uTreble"), in.treble_level);
  glUniform3f(loc(scene_, "uColA"), col_a[0], col_a[1], col_a[2]);
  glUniform3f(loc(scene_, "uColB"), col_b[0], col_b[1], col_b[2]);
  glUniform3f(loc(scene_, "uAccent"), accent[0], accent[1], accent[2]);
  if (v_ == DriveVariant::Tunnel) {
    std::array<float, 18> rings{};
    for (int k = 0; k < 6; ++k) {
      const auto c = hsv(static_cast<float>(std::fmod(k * 0.11 + in.time_s * 0.03 + in.beat_phase * 0.08, 1.0)), 0.65f, 1.0f);
      for (int i = 0; i < 3; ++i) rings[static_cast<std::size_t>(k * 3 + i)] = c[static_cast<std::size_t>(i)] * 0.6f + accent[static_cast<std::size_t>(i)] * 0.4f;
    }
    glUniform3fv(loc(scene_, "uRingCol[0]"), 6, rings.data());
  }
  // the soft layer at half size into a, the sharp one at full size into c (the same program, uLayer apart)
  glUniform1i(loc(scene_, "uLayer"), 0);
  gl::fullscreen();
  c_.bind();
  glUniform1i(loc(scene_, "uLayer"), 1);
  gl::fullscreen();

  // the bloom of the sharp layer (quarter size), composited in the post itself: one full-size pass fewer
  const unsigned bloom = post_->bloom_only(c_.texture);
  glBindFramebuffer(GL_FRAMEBUFFER, fbo);
  glViewport(0, 0, w_, h_);
  glUseProgram(post_program_);
  bind_tex(0, c_.texture);
  glUniform1i(loc(post_program_, "uTex"), 0);
  bind_tex(3, bloom);
  glUniform1i(loc(post_program_, "uBloom"), 3);
  glUniform1f(loc(post_program_, "uBloomAmt"),
              0.55f + 0.35f * static_cast<float>(punch_) + 0.3f * in.drop_level);
  glUniform3f(loc(post_program_, "uTint"), look_->tint_r.load(), look_->tint_g.load(), look_->tint_b.load());
  glUniform1f(loc(post_program_, "uTintAmt"), look_->tint_amount.load());
  bind_tex(2, a_.texture);
  glUniform1i(loc(post_program_, "uSoft"), 2);
  glUniform2f(loc(post_program_, "uRes"), static_cast<float>(w_), static_cast<float>(h_));
  glUniform1f(loc(post_program_, "uTime"), static_cast<float>(std::fmod(in.time_s, 3600.0)));
  glUniform1f(loc(post_program_, "uKick"), static_cast<float>(punch_));
  // a kick zooms the frame in. exponential_strength keeps slight opening kicks gentle while strong kicks and drops slam
  glUniform1f(loc(post_program_, "uPunch"),
              static_cast<float>(1.0 + (0.95 + 0.35 * drop_lvl) * punch_ + 0.55 * drop_hit));
  const double t = in.time_s * 37.0;
  const double shake_amp = 0.014 + 0.008 * drop_lvl;
  glUniform2f(loc(post_program_, "uShake"), static_cast<float>(shake_ * shake_amp * std::sin(t * 1.7)),
              static_cast<float>(shake_ * shake_amp * std::cos(t * 2.3)));
  glUniform1f(loc(post_program_, "uFlare"), static_cast<float>(flare_));
  glUniform1f(loc(post_program_, "uWarp"), static_cast<float>(warp_));
  glUniform1f(loc(post_program_, "uIntensity"), in.intensity);
  glUniform1f(loc(post_program_, "uDrop"), drop_vis);
  glUniform3f(loc(post_program_, "uAccent"), accent[0], accent[1], accent[2]);
  bind_tex(1, art_);
  glUniform1i(loc(post_program_, "uArt"), 1);
  glUniform1f(loc(post_program_, "uHasArt"), has_art_ ? 1.0f : 0.0f);
  glUniform2f(loc(post_program_, "uArtSize"), static_cast<float>(art_w_), static_cast<float>(art_h_));
  // the cover scene shows its cover clearly; warp and tunnel dim and soften it under their busier foregrounds
  const float dim = v_ == DriveVariant::Cover ? 0.62f : v_ == DriveVariant::Warp ? 0.36f : 0.28f;
  const float lod = v_ == DriveVariant::Cover ? 0.0f : 2.5f;
  glUniform1f(loc(post_program_, "uBgDim"), dim);
  glUniform1f(loc(post_program_, "uBgLod"), lod);
  glUniform1f(loc(post_program_, "uEnergy"), in.energy);
  gl::fullscreen();
  glActiveTexture(GL_TEXTURE0);
}

void DriveSource::release() {
  for (unsigned* p : {&scene_, &post_program_}) {
    if (*p) glDeleteProgram(*p);
    *p = 0;
  }
  if (art_) glDeleteTextures(1, &art_);
  art_ = 0;
  if (spikes_) glDeleteTextures(1, &spikes_);
  spikes_ = 0;
  a_.release();
  c_.release();
  if (post_ && w_ > 0) post_->release();
  w_ = 0;
}

std::vector<std::unique_ptr<VizSource>> drive_sources(std::shared_ptr<SceneLook> look) {
  if (!look) look = std::make_shared<SceneLook>();
  auto post = std::make_shared<ScenePost>(look);
  std::vector<std::unique_ptr<VizSource>> out;
  for (auto v : {DriveVariant::Cover, DriveVariant::Warp, DriveVariant::Tunnel})
    out.push_back(std::make_unique<DriveSource>(v, look, post));
  return out;
}

}  // namespace cadence::render::viz
