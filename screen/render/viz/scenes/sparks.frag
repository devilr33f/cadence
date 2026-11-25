// a fountain of shader-only sparks relaunching on beats, lit by their frequency column, over a
// zoom-punch feedback with expanding beat rings and a bass floor glow. ember.
// palette: ember
// from the viz poc's iteration 2 (original code written for the spike), on the host's audio texture
void main() {
  vec2 uv = vUv;
  // feedback: punch-zoom on the beat, faster fade so sparks leave comet tails
  float zoom = 1.02 + 0.45 * uBeatPulse + 0.05 * uBass;
  vec3 c = fb(0.5 + (uv - 0.5) / zoom, 0.900);

  // element: the fountain, one particle per coarse cell plus its neighbours
  const vec2 GRID = vec2(56.0, 32.0);
  const float LIFE = 1.9;
  float t = uTime + uBeatCount * 0.45;  // beats restart the arc
  vec2 cell = floor(uv * GRID);
  for (float j = -1.0; j <= 1.0; j++) {
    for (float i = -1.0; i <= 1.0; i++) {
      vec2 id = cell + vec2(i, j);
      float h1 = hash12(id + 7.1), h2 = hash12(id + 3.7), h3 = hash12(id + 9.9);
      float lt = mod(t + h1 * LIFE, LIFE) / LIFE;
      float u = (id.x + 0.5) / GRID.x;
      float s = fftLog(u);
      vec2 pos = vec2(u, 0.02) + vec2((h2 - 0.5) * 0.55, 0.42 + 1.05 * h3) * lt;
      pos.y -= 0.55 * lt * lt;
      vec2 d = pos - uv;
      // a tight exponential core with a faint halo: 1/d^2 tails overlap across the 3x3 cells and
      // saturate the whole lower half once the feedback amplifies them
      float d2 = dot(d, d);
      float glow = exp(-d2 * 2600.0) * 0.85 + exp(-d2 * 350.0) * 0.12;
      float g = (1.0 - lt) * (0.12 + 1.6 * s);
      c += palette(u * 0.7 + 0.1 + h1 * 0.08 + uPalShift) * glow * g;
    }
  }
  // element: beat rings from the floor
  for (int i = 0; i < 4; i++) {
    float age = beatAge(i);
    if (age < 0.0 || age > 2.0) continue;
    float rad = 0.05 + age * 0.42;
    float ring = exp(-pow((length((uv - vec2(0.5, 0.0)) * vec2(1.78, 1.0)) - rad) * 16.0, 2.0)) * (1.0 - age / 2.0);
    c += palette(0.08) * ring * (0.25 + 0.8 * uBass);
  }
  // element: bass floor glow and launch flash
  c += palette(0.03) * (uBass * 0.25 + uBeatPulse * 0.6) * smoothstep(0.16, 0.0, uv.y) * 0.45;
  fragColor = vec4(min(c, vec3(2.0)), 1.0);
}
