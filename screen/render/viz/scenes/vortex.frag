// spectrum spokes drawn into an endless zoom tunnel; mids drive the spin, every beat punches the
// zoom, rings ride the spokes and a flare sits in the centre. palette: fire.
// palette: fire
// from the viz poc's iteration 2 (original code written for the spike), on the host's audio texture
void main() {
  vec2 p = vUv - 0.5;
  p.x *= uRes.x / uRes.y;
  float r = length(p);
  float a = atan(p.y, p.x);

  // feedback: zoom tunnel with a beat punch and a slow spin
  float zoom = 1.0 + 0.045 * uBass + 0.30 * uBeatPulse;
  float spin = uTime * (0.02 + 0.10 * uMid) + uBeatCount * 0.03;
  vec2 fuv = 0.5 + rot2(vUv - 0.5, -spin) / zoom;
  vec3 c = fb(fuv, 0.962);

  // element: spectrum spokes, brighter near the walls
  float u = fract(a / 6.28318 + spin * 0.25);
  float s = fft(u);
  float fog = smoothstep(0.72, 0.06, r);
  c += palette(u + uPalShift) * (s * s * 1.8 + 0.06 * s) * fog;

  // element: beat rings expanding down the pipe
  for (int i = 0; i < 4; i++) {
    float age = beatAge(i);
    if (age < 0.0 || age > 1.6) continue;
    float rad = 0.06 + age * 0.55;
    float ring = exp(-pow((r - rad) * (14.0 - age * 4.0), 2.0)) * (1.0 - age / 1.6);
    c += palette(float(i) * 0.22 + 0.05) * ring * (0.4 + 1.2 * uBass);
  }

  // element: centre flare and treble shimmer on the walls
  c += palette(0.02) * uBeatPulse * exp(-r * r * 22.0) * 1.4;
  c += palette(0.8) * uTreble * smoothstep(0.25, 0.6, r) * (0.5 + 0.5 * sin(a * 48.0 + uTime * 9.0)) * 0.35;
  fragColor = vec4(min(c, vec3(2.5)), 1.0);
}
