// a scrolling spectrogram whose rows are palette-mapped as they age, with a bright new-spectrum
// edge on top, scope dots riding the edge and a slow zoom so history compresses. ocean.
// palette: ocean
// from the viz poc's iteration 2 (original code written for the spike), on the host's audio texture
void main() {
  vec2 uv = vUv;
  float row = 1.0 - uv.y;  // the newest row is at the top
  // feedback: the scroll itself (sample one row down), through the palette, plus a gentle zoom
  vec2 scrolled = 0.5 + (vec2(uv.x, uv.y + 1.5 / uRes.y) - 0.5) / (1.0 + 0.004 + 0.02 * uBass);
  vec3 c = fb(scrolled, 0.995);
  if (row < 2.5 / uRes.y) {
    // element: the new spectrum row, hot and bright
    float v = fftLog(uv.x);
    c = palette(0.95 - v * 0.8 + uPalShift) * (0.10 + 1.9 * pow(v, 1.5));
    // element: scope dots on the edge
    float w = abs(uv.x - (0.5 + wave(uv.x) * 0.30));
    c += palette(0.6) * exp(-w * w * 5000.0) * (0.4 + 1.2 * uMid);
  }
  // element: a soft grid and a level meter on the left edge
  vec2 g = abs(fract(uv * vec2(8.0, 4.5)) - 0.5);
  c += palette(0.55) * smoothstep(0.47, 0.5, max(g.x, g.y)) * 0.05;
  c += palette(0.85) * smoothstep(0.02, 0.0, uv.x) * step(uv.y, uLevel) * 0.35;
  c += palette(0.02) * uBeatPulse * smoothstep(4.0 / uRes.y, 0.0, row) * 0.5;
  fragColor = vec4(min(c, vec3(2.5)), 1.0);
}
