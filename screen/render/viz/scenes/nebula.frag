// domain-warped fbm clouds breathing with the mids, stars twinkling on the treble; the feedback is
// swirled by the treble so the smoke folds into itself. sunset palette.
// palette: sunset
// from the viz poc's iteration 2 (original code written for the spike), on the host's audio texture
float vnoise(vec2 p) {
  vec2 i = floor(p), f = fract(p);
  f = f * f * (3.0 - 2.0 * f);
  float a = hash12(i), b = hash12(i + vec2(1, 0)), c = hash12(i + vec2(0, 1)), d = hash12(i + vec2(1, 1));
  return mix(mix(a, b, f.x), mix(c, d, f.x), f.y);
}
float fbm(vec2 p) {
  float v = 0.0, amp = 0.55;
  for (int i = 0; i < 4; i++) {
    v += amp * vnoise(p);
    p = p * 2.03 + 11.7;
    amp *= 0.52;
  }
  return v;
}
void main() {
  vec2 uv = vUv;
  vec2 p = (uv - 0.5) * vec2(uRes.x / uRes.y, 1.0);
  // feedback: a treble-driven swirl around the centre folds the smoke into itself
  float r = length(p);
  float sw = (0.15 + 1.1 * uTreble) * exp(-r * 1.8) * sin(r * 9.0 - uTime * 1.2);
  vec2 q = rot2(p, sw) / (1.0 + 0.03 * uBass);
  vec2 fuv = 0.5 + q * vec2(uRes.y / uRes.x, 1.0);
  vec3 c = fb(clamp(fuv, 0.0, 1.0), 0.958);

  // element: the clouds, mid-warped
  vec2 w = vec2(fbm(p * 1.3 + uTime * 0.045), fbm(p * 1.3 + 4.7 - uTime * 0.038));
  float n = fbm(p * 1.05 + w * (0.5 + 1.2 * uMid) + uTime * 0.025);
  float v = pow(max(n, 0.0), 1.5);
  c += palette(0.90 - v * 0.55 + uPalShift) * (0.05 + 1.35 * v * (0.35 + 0.65 * uLevel));
  // element: stars on the treble, and a beat bloom through the cloud
  vec2 sc = floor((uv + uTime * 0.004) * 92.0);
  float sh = hash12(sc);
  float tw = 0.5 + 0.5 * sin(uTime * (2.0 + 6.0 * sh) + sh * 40.0);
  c += vec3(0.9, 0.95, 1.0) * step(0.992, sh) * tw * (0.25 + 1.3 * uTreble) * smoothstep(0.9985, 0.9992, sh);
  c += palette(0.12) * uBeatPulse * v * 0.40;
  fragColor = vec4(min(c, vec3(2.5)), 1.0);
}
