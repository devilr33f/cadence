# projectM (milkdrop source of the screen's visualizer)

libprojectM 4 (LGPL-2.1) is built from source at a pinned commit and linked as a shared library; its preset pack
(cream-of-the-crop, no explicit license: its `LICENSE.md` says so and ships in the licenses) is fetched at build time.
Nothing of either is committed (rule 5): only the pins, our patch, scripts and the curated names.

Shipped in the android-tv apk and the rpi deb, and built for `make screen-sim` (`screen/render/viz/README.md`,
Milkdrop on the devices). Until the owner curates, the devices ship `presets.candidates` (as their `presets.txt`), not
the generated `presets.txt`: the host's budget skips the slow ones on each device.

| File | What |
| --- | --- |
| `pins.conf` | projectM v4.1.8, its `projectm-eval` submodule and the preset pack: commit and SHA-256 of each GitHub archive |
| `patches/0001-render-frame-fbo.patch` | backport of upstream's `projectm_opengl_render_frame_fbo` (4.1.8 always composites into framebuffer 0; spike S5), same signature, so it goes away with the next release |
| `fetch.sh <out>` | the patched source in `<out>/src` |
| `build.sh <src> <build> <prefix>` | GLES 3, shared, projectm-eval vendored, no playlist library; Android passes the NDK toolchain, macOS `-DENABLE_GLES=OFF` (desktop GL) |
| `fetch-presets.sh <out> [list]` | the presets a list names, out of the pinned pack |
| `presets.candidates` | 65 candidates: an even spread of each category's presets without custom warp or composite shaders (the cheap ones), plus the two S5 measured within budget |
| `measurements/stick.tsv` | `viz-bench` rows from the stick (S5's figures so far) |
| `measurements/strobe.tsv` | `viz-bench` rows from any GPU, for the strobe column |
| `rank-presets.sh` | `presets.txt` from the two tables |
| `presets.txt` | the shipped list: median ≤ 20 ms a frame on the stick and no strobing (generated) |

## Curating (owner-attended on the stick)

S5 measured a preset's frame at 14 ms to 870 ms on the stick's Mali-G31, so the list is curated by measurement:

1. `fetch-presets.sh build/presets presets.candidates`, then push the presets, `viz-bench` and `libprojectM-4.so`
   (built for armeabi-v7a) to `/data/local/tmp`.
2. On the stick: `viz-bench --milkdrop <dir> --list presets.candidates --seconds 10 --no-luma --source stick >
   stick.tsv`, copied back into `measurements/`.
3. Anywhere (`make screen-p-gl-test`'s image will do): the same with luminance on, into `measurements/strobe.tsv`.
4. `rank-presets.sh > presets.txt`.
5. Point the device builds at `presets.txt` instead of `presets.candidates` (`deploy/android/Dockerfile`,
   `deploy/builder/rpi.Dockerfile`, the Makefile's `screen-sim`).

At run time the host still skips a preset over its 20 ms budget for 2 s and keeps it in the device's slow list.
