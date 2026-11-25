// the classic spectrum bars with falling peaks (the peak rides in alpha, which the composite
// ignores), now over a rising feedback halo, with a waveform ribbon through the middle. ice.
// palette: ice
// from the viz poc's iteration 2 (original code written for the spike), on the host's audio texture
void main() {
  vec2 uv = vUv;
  // feedback: slow rise, slight bass bloom
  float zoom = 1.012 + 0.03 * uBass + 0.10 * uBeatPulse;
  vec3 c = fb(0.5 + (uv - 0.5) / zoom, 0.955);

  const float NB = 48.0;
  float bar = floor(uv.x * NB);
  float fu = (bar + 0.5) / NB;
  float v = max(fftLog(fu - 0.4 / NB), max(fftLog(fu), fftLog(fu + 0.4 / NB)));
  float h = 0.05 + 0.85 * pow(v, 1.4) * (0.55 + 0.45 * uBass);
  float inBar = step(fract(uv.x * NB), 0.72);

  float prevPeak = texture(uPrev, vUv).a;
  float peak = max(h, prevPeak - 0.004);
  float d = uv.y;
  float barOn = (1.0 - smoothstep(h - 0.006, h, d)) * inBar;
  c += palette(fu * 0.6 + 0.5 - d * 0.22 + uPalShift) * barOn * (0.30 + 1.5 * pow(v, 1.2));
  float dTop = 1.0 - uv.y;
  float barTop = (1.0 - smoothstep(h - 0.006, h, dTop)) * inBar;
  c += palette(fu * 0.6 + 0.5 + uPalShift) * barTop * 0.20;
  c += vec3(0.95) * (1.0 - smoothstep(0.004, 0.014, abs(d - peak))) * inBar * (0.35 + 0.65 * v);

  // element: waveform ribbon through the middle
  float amp = 0.05 + 0.16 * uLevel;
  float dw = abs(uv.y - 0.5 - wave(uv.x) * amp);
  dw = min(dw, abs(uv.y - 0.5 - wave(uv.x - 0.004) * amp));
  c += palette(0.62) * exp(-dw * dw * 1800.0) * (0.5 + 0.8 * uMid);
  c += vec3(1.0) * uBeatPulse * exp(-dw * dw * 900.0) * 0.3;
  fragColor = vec4(min(c, vec3(2.5)), peak);
}
