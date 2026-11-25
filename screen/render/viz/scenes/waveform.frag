// dual phosphor traces on a slowly rotating plane (the feedback rotates with the mids): a big
// waveform ring and a straight trace crossing it, with a graticule and beat flashes. toxic.
// palette: toxic
// from the viz poc's iteration 2 (original code written for the spike), on the host's audio texture
void main() {
  vec2 uv = vUv;
  // feedback: the whole phosphor plane rotates slowly; mids spin it, beats kick the zoom. thin
  // moving traces only: anything constant and wide would accumulate through the rotation
  float rot = uTime * (0.015 + 0.10 * uMid);
  float zoom = 1.02 + 0.20 * uBeatPulse;
  vec3 c = fb(0.5 + rot2(uv - 0.5, rot) / zoom, 0.900);

  // element: a circular waveform trace: radius follows the wave as it sweeps the circle
  float an = atan(uv.y - 0.5, (uv.x - 0.5) * uRes.x / uRes.y);
  float u = fract(an / 6.28318 + 0.5);
  float ringR = 0.26 + wave(u) * 0.16 * (0.3 + 2.0 * uLevel) + 0.03 * sin(u * 6.28318 * 2.0 + uTime);
  float dr = abs(length((uv - 0.5) * vec2(uRes.x / uRes.y, 1.0)) - ringR);
  float g1 = exp(-dr * dr * 1400.0) + exp(-dr * dr * 160.0) * 0.08;
  c += palette(0.33 + uPalShift) * g1 * (0.5 + 0.9 * uMid);

  // element: a straight second trace crossing the ring (the delayed channel)
  float amp = 0.10 + 0.26 * uLevel;
  float d2 = abs(uv.y - 0.5 - wave2(uv.x) * amp);
  d2 = min(d2, abs(uv.y - 0.5 - wave2(uv.x - 0.004) * amp));
  float g2 = exp(-d2 * d2 * 2400.0) * 0.8 + exp(-d2 * d2 * 260.0) * 0.06;
  c += palette(0.66 + uPalShift) * g2 * (0.5 + 0.9 * uTreble);

  // element: centre dot and beat flash on both traces. (no graticule: a constant grid would
  // accumulate through the rotating feedback into a solid texture, add/(1-decay) ~ 0.85)
  c += palette(0.15) * exp(-dot(uv - 0.5, uv - 0.5) * 4000.0) * (0.3 + uBass);
  c += vec3(1.0) * uBeatPulse * (g1 + g2) * 0.35;
  fragColor = vec4(min(c, vec3(2.5)), 1.0);
}
