# Visualizer

Audio mode's visualizer (tv plan item 16, the owner's redesign of 2026-11-14): a host with pluggable sources and GPU
transitions, rendering on a thread of its own with a GL context shared with the UI's, never in the UI or player loop.

```text
audiod tap (PcmSnapshot, stamped when heard) ─► PcmTimeline ─► AudioFeatures ─► FrameInputs
                                                                                    │
 Host (viz thread) ── Renderer ── current VizSource ─► slot FBO ─┐                  │
                               └─ outgoing VizSource ─► slot FBO ─┴► TransitionStage ─► copy into 1 of 3 textures
                                                                                     ─► the UI draws the newest
```

## Parts

| Part | Where | GL |
| --- | --- | --- |
| Audio aligned to what is heard: each snapshot written at its place on the timeline, the window taken at "heard now", contiguous | `host/pcm_timeline.*` | no |
| FrameInputs: a 1024-point FFT as 512 bins, the waveform, bass/mid/treble against their own averages (and smoothed), a beat flag | `host/features.*` | no |
| Switching: on a beat after `every_s`, after `max_wait_s` more without one, at once on ← and →; a new track picks another source | `host/scheduler.*` | no |
| Budget: a source over 20 ms a frame for 2 s is skipped and kept in the device's slow list | `host/budget.h`, `host/playlist.*` | no |
| Strobe check: more than 3 full-frame flashes in a second fails a source | `host/strobe.*` | no |
| `VizSource`: id, `init(w, h)`, `activate`, `render(FrameInputs, fbo)`, `deactivate`, `release` | `host/source.h` | yes |
| Shader presets (ours) | `host/shader_source.*`, `presets/*.frag` | yes |
| Feedback scenes (the poc's iteration 2, the direction after the owner's "pretty basic"): each frame warps and fades its own previous frame, remaps it through a palette lut that steps on onsets, draws 2-3 elements on top, then a quarter-size bloom and the artwork's tint (`SceneLook`) | `host/scene_source.*`, `scenes/*.frag` | yes |
| The drive (owner's issue #1): onsets per band (kick, snare, hat, the full band where a track has no kick) from spectral flux over an adaptive threshold, a kick on the frame it is heard; loudness, band energies and a drop's intensity against the track's own range | `host/beat.*` | no |
| Drive scenes (`drive:cover`, `drive:warp`, `drive:tunnel`): a shape per track (triangle, square, pentagon, hexagon, circle, star, diamond) throwing the spectrum out as spikes, the cover blurred behind, particles at the energy's speed; a post pass punches, shakes, splits rgb and flares on kicks; the ui writes the title in the shape | `host/drive_source.*` | yes |
| Families: the owner's pick plays alone (`drive:`, `scene:`, `milkdrop:`), all of them where it has nothing usable; 60 fps, halved while frames cost over ~13 ms | `host/playlist.*`, `host/host.*` | no |
| Offline preview: any source rendered from a track as a video with its sound (`tools/preview.sh build/viz-gl/viz-render song.mp3 drive:cover out.mp4 30 0 cover.jpg`; cgl on macos) | `host/bench/viz-render.cpp` | yes |
| Onsets (older, kept for its tests): raw bass energy, short window over long, 250 ms refractory, the tempo from clustered intervals; a switch lands on a bar of four once it holds | `host/onset.*` | no |
| Milkdrop through projectM, one instance while shown | `milkdrop/milkdrop_source.*` | yes |
| The shipped presets: `milkdrop_dir()` (where this device has them) and `milkdrop_sources(dir)` (one source a preset, in `presets.txt`'s order; empty without projectM) | `milkdrop/milkdrop_sources.*` | no |
| Classic bars, mirror, scope | `host/classic_source.*` over `classic.*` | yes |
| Transitions in gl-transitions' contract, fetched at a pinned commit (`transitions/`), crossfade built in | `host/transition*` | yes |
| Renderer (synchronous: host, benchmark, tests) and Host (thread, shared context, triple-buffered output) | `host/renderer.*`, `host/host.*` | yes |
| Benchmark and curation tool: each source N seconds, ranked table with the strobe check | `host/bench/viz-bench.cpp` | yes |

## Shader preset contract

GLSL ES 3.00 defining Shadertoy's `void mainImage(out vec4 fragColor, in vec2 fragCoord)`. In scope: `iResolution`,
`iTime`, `uAudio` (512 × 2: row 0 the spectrum 0..1, row 1 the waveform mapped to 0..1), `uBass`, `uMid`, `uTreble`
(about 1 at the band's own average), `uBassAtt`, `uMidAtt`, `uTrebleAtt` (the same, smoothed), `uBeat` (1 on a beat
frame), `uSinceBeat` (seconds), `fft(x)` and `wave(x)` for x in 0..1. One draw a frame, no extra textures or passes.

## Scene contract

GLSL ES 3.00 defining `main()` and writing `fragColor`. A `// palette: <name>` line picks its lut row (fire, ice,
spectrum, sunset, toxic, ocean, ember, mono). In scope: `vUv`, `uRes`, `uTime`, `uBass`, `uMid`, `uTreble`, `uLevel`
(absolute levels 0..1), `uBeatPulse`, `uBeatCount`, `uPrev` (the scene's previous frame, its alpha kept), `fft(u)`,
`fftLog(u)`, `wave(u)`, `wave2(u)`, `palette(x)`, `fb(uv, decay)`, `beatAge(i)` (seconds since the i-th last onset, -1
none), `hash12`, `rot2`. One scene draw and four post draws a frame. Keep anything constant and wide out of a rotating
feedback (it accumulates to add/(1-decay)), and glows exponential rather than 1/d² (they saturate and persist).

Costs on llvmpipe at 960 × 540 (`viz-bench --scenes scenes --seconds 3`, median ms a frame, post included, indicative
only; the stick ran the poc's bars at about twice llvmpipe's figure): vortex 5.5, waveform 6.3, bars 7.6, sparks 7.9,
flow 8.3, nebula 9.2, kaleido 9.9; the classic styles 0.3-0.4, the shader presets 1.3-3.4.

## Milkdrop on the devices

projectM (LGPL-2.1, a shared library) and its presets are built and fetched at build time from
`media/third_party/projectm`'s pins, never committed. `CADENCE_SCREEN_PROJECTM=ON` in the android-tv apk
(`deploy/android/Dockerfile`: `lib/armeabi-v7a/libprojectM-4.so`, the presets in `assets/viz/milkdrop`), the rpi deb
(`deploy/builder/rpi.Dockerfile`: `/opt/cadence/lib/libprojectM-4.so.4`, `/opt/cadence/share/viz/milkdrop`) and
`make screen-sim` (desktop GL 4.1 core on macOS, the presets in `build/screen-sim/deps/milkdrop`). The screen finds the
directory with `milkdrop_dir()`:

| Platform | Directory | How |
| --- | --- | --- |
| android-tv | `<files>/viz/milkdrop` | `UiAssets` copies the apk's `assets/viz`; `android-main.cpp` sets `CADENCE_SCREEN_MILKDROP` |
| rpi | `/opt/cadence/share/viz/milkdrop` | `cadence-screen.service` sets `CADENCE_SCREEN_MILKDROP` |
| any | `<ui root>/../viz/milkdrop` | beside the first root of `CADENCE_SCREEN_UI_ASSETS`, when the env above is unset |
| simulator | `build/screen-sim/deps/milkdrop` | compiled in (`CADENCE_VIZ_MILKDROP_DIR`) |

```cpp
#include "milkdrop/milkdrop_sources.h"
// no gl: any thread, before the renderer's init
for (auto& s : viz::milkdrop_sources(viz::milkdrop_dir())) renderer.add(std::move(s));
```

The devices ship `presets.candidates` as `presets.txt` until the owner curates on the stick; a preset over the 20 ms
budget for 2 s is skipped and kept in the slow list, and one projectM cannot read or compile is skipped for the run
(`viz_source_failed`), the next one shown in its place.

## Reconcile with stream A

`backend.h` (Canvas, Rect, Color, TexturedQuad) stands in for A's `RenderBackend`, and `host.h`'s `SharedContext` for
A's platform layer (an SDL or EGL context sharing objects with the UI's). The UI draws `Host::frame(dst)` (texture, uv
flipped, opacity) under its layer, upscaled.

## Tests

`make screen-p-test`: the GL-free parts, the classic golden images (CPU raster). `make screen-p-gl-test`: the GL host
on Mesa's llvmpipe (golden images of each shader preset and of a transition halfway from the fixed PCM fixture, a
broken preset dropped, a slow source skipped, the benchmark), with projectM built by `media/third_party/projectm`'s
scripts.
