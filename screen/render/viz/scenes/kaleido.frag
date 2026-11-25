// a plasma folded into kaleidoscope sectors; the feedback is folded with it, so echoes repeat in
// every sector; treble sparkles at the seams and every beat rotates the fold. spectrum.
// palette: spectrum
// from the viz poc's iteration 2 (original code written for the spike), on the host's audio texture
void main() {
  vec2 p = vUv - 0.5;
  p.x *= uRes.x / uRes.y;
  float k = 6.0 + floor(uMid * 5.0);
  float a = atan(p.y, p.x) + uTime * 0.06;
  float fold = 6.28318 / k;
  float af = abs(mod(a, fold) - fold * 0.5);
  float rr = length(p);
  vec2 kp = vec2(cos(af), sin(af)) * rr;

  // feedback: sample the previous frame through the same fold, slowly zooming out
  vec2 fuv = kp * 0.72 / (1.0 + 0.06 * uBass + 0.22 * uBeatPulse) / vec2(uRes.x / uRes.y, 1.0) + 0.5;
  vec3 c = fb(clamp(fuv, 0.0, 1.0), 0.950);

  // element: the plasma, mids warping its phases
  float t1 = uTime * (0.25 + 0.6 * uBass) + uBeatCount * 0.4;
  float t2 = uTime * 0.19 + uMid * 2.5;
  float t3 = uTime * 0.27 + uTreble * 3.0;
  float v = sin(kp.x * 6.5 + t1) + sin(kp.y * 6.5 + t2) + sin((kp.x + kp.y) * 4.5 + t3) +
            sin(rr * 10.0 - t1 * 1.3);
  v = v * 0.25 + 0.5;
  c += palette(v * 0.45 + uPalShift) * (0.10 + 1.1 * pow(v, 1.6)) * (0.30 + 0.70 * uLevel);
  // element: seam sparkles and a beat flash
  float seam = smoothstep(0.02, 0.0, abs(mod(a, fold) - fold * 0.5)) * uTreble;
  c += palette(0.75) * seam * (0.4 + 0.6 * hash12(floor(vUv * uRes / 2.0) + floor(uTime * 20.0))) * 0.9;
  c += palette(0.3) * uBeatPulse * v * 0.30;
  c *= smoothstep(1.12, 0.30, rr);
  fragColor = vec4(min(c, vec3(2.5)), 1.0);
}
