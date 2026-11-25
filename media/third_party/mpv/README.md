# mpv with `ao_cadence`

Owner decision 5 (TV plan, 2026-11-14): every screen's audio goes through audiod's mixer, so mpv is built by us with
one patch, `0001-ao_cadence.patch`. FFmpeg is never compiled.

- **The patch** adds `audio/out/ao_cadence.c` (a pull AO writing f32 48 kHz stereo into a sealed memfd ring, woken by
  an eventfd: `--ao=cadence --ao-cadence-ring-fd=N --ao-cadence-event-fd=N`), a copy of the ring's header
  `audio/out/cadence_ring.h`, and 11 lines of glue (`ao.c` after `null`, so it is never autoprobed; `meson.build`;
  `meson.options`' `-Dcadence=auto`, Linux and Android only). It applies unchanged to mpv 0.41.0 and to Debian
  trixie's 0.40.0. The ring's canonical header lives in libcadence-media; the copy here must stay equal to it.
- **The inputs** are pinned in `mpv.pins` (SHA-256 for files, full commits for git) and fetched by
  `deploy/builder/mpv-fetch.sh`.
- **The builds** (`make mpv MPV_TARGET=…`, `deploy/builder/mpv.sh`):
  - `deb-0.40` (default for the screens on Linux) and `deb-0.41`: Debian's source package with the patch as the
    quilt patch `debian/patches/cadence-ao.patch`, versioned `+cadence1`, built with `dpkg-buildpackage` against
    trixie's own `-dev` packages, for arm64 (the Pi) or amd64.
  - `android-armeabi-v7a`: mpv 0.41.0 for NDK r29, API 26, against the jdtech AAR's FFmpeg n8.1 `.so` files, with the
    libraries the AAR links into its `libmpv.so` (libass, libplacebo, freetype, fribidi, harfbuzz, libunibreak,
    fontconfig, libxml2, Lua) built static at the AAR's versions and options. The result must match the AAR's
    `libmpv.so` in NEEDED, SONAME and every export, so it replaces it in `jniLibs` with the AAR's other libraries.
- **Outputs** are kept in the docker volume `cadence-mpv-cache` by the hash of their recipe; `MPV_REBUILD=1` builds
  again and fails unless the result equals the kept one.
