// spectrum-rings: concentric rings, one band each from the centre out, turning slowly with the treble
void mainImage(out vec4 fragColor, in vec2 fragCoord) {
  vec2 p = (fragCoord - 0.5 * iResolution.xy) / iResolution.y;
  float r = length(p) * 1.6;
  float a = atan(p.y, p.x) + iTime * 0.1 + 0.3 * uTrebleAtt;
  // log-ish frequency from the radius: low in the middle
  float x = clamp(r * r, 0.0, 1.0);
  float level = fft(x * 0.6);
  float bands = 0.5 + 0.5 * cos(r * 60.0);
  float seg = smoothstep(0.2, 0.8, 0.5 + 0.5 * sin(a * 12.0));
  float lit = level * bands * mix(0.6, 1.0, seg) * step(r, 1.0);
  vec3 cold = vec3(0.25, 0.55, 1.0);
  vec3 warm = vec3(1.0, 0.5, 0.3);
  vec3 col = mix(cold, warm, x) * lit * (0.8 + 0.4 * uBeat);
  fragColor = vec4(col, 1.0);
}
