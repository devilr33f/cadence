// ember-pulse: a warm core that swells with the bass and flares on each beat, ringed by the spectrum
void mainImage(out vec4 fragColor, in vec2 fragCoord) {
  vec2 p = (fragCoord - 0.5 * iResolution.xy) / iResolution.y;
  float r = length(p);
  float a = atan(p.y, p.x);
  float flare = exp(-4.0 * uSinceBeat);
  float core = 0.18 + 0.08 * clamp(uBassAtt, 0.0, 2.0) + 0.05 * flare;
  float glow = exp(-6.0 * max(r - core, 0.0)) * (0.6 + 0.4 * flare);
  // the spectrum around the core: angle to frequency, mirrored so the ring is whole
  float f = fft(abs(a) / 3.14159 * 0.5);
  float ring = smoothstep(0.02, 0.0, abs(r - core - 0.12 - 0.25 * f)) * (0.4 + f);
  vec3 ember = vec3(1.0, 0.42, 0.12);
  vec3 col = ember * glow + vec3(1.0, 0.8, 0.55) * ring;
  col += 0.04 * vec3(0.3, 0.1, 0.2) * (1.0 - r);
  fragColor = vec4(col, 1.0);
}
