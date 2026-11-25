// wave-tunnel: the waveform as a glowing line over stripes that rush toward the viewer with the mids
void mainImage(out vec4 fragColor, in vec2 fragCoord) {
  vec2 uv = fragCoord / iResolution.xy;
  vec2 p = (fragCoord - 0.5 * iResolution.xy) / iResolution.y;
  float depth = 0.25 / max(length(p), 0.05);
  float stripes = 0.5 + 0.5 * sin(depth * 6.0 - iTime * (2.0 + 2.0 * clamp(uMidAtt, 0.0, 2.0)));
  vec3 col = vec3(0.08, 0.05, 0.12) * stripes * clamp(1.0 - length(p), 0.0, 1.0);
  float w = wave(uv.x) * 0.35;
  float d = abs(uv.y - 0.5 - w);
  float line = exp(-d * 180.0) + 0.35 * exp(-d * 30.0);
  col += vec3(0.95, 0.9, 1.0) * line * (0.6 + 0.4 * clamp(uBass, 0.0, 2.0));
  fragColor = vec4(col, 1.0);
}
