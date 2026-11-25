# a board's tarball for the installer (M2-D10, M2-R4, M2m-D6): make device-build BOARD=<board> runs this under
# emulation for the board's platform, yandex-pickle and vk-capsula-mini on linux/arm64 and yandex-midi on linux/arm/v7,
# with BOARD and ARCH (its debian architecture) as build args and soloist's runtime (build/soloist, from
# media-host.Dockerfile) and airplay's (build/airplay, from airplay.Dockerfile) as the named contexts soloist and
# airplay, and pickle's ffmpeg (build/prebuilt/ffmpeg-pickle) or midi's or the vk capsula mini's cross deps
# (build/prebuilt/midi-deps, build/prebuilt/vk-deps) as the named context ffmpeg-pickle, midi-deps or vk-deps — the
# prebuilt cache (deploy/builder/prebuilt.sh) ensures each, so the pinned third-party parts are
# fetched, never recompiled. cadenced is static go,
# cross-compiled; pickle's audiod builds in debian bookworm, its glibc 2.36, and midi's, its ffmpeg and
# openssl cross-build with zig for its glibc 2.30 (build-midi-audiod.sh), the vk capsula mini's for aarch64 glibc 2.28
# (build-vk-audiod.sh), so they run with the board's own loader.
# pickle takes its openssl 3.0 from the device, midi and the vk capsula mini from bin/lib (M2m-R4).
# check-device-limits.sh refuses anything that would not run there; the two runtimes bring their own trixie loader and
# are gated by their own scripts (soloist's is arm64 on both boards: midi's 64-bit kernel runs it beside its 32-bit
# userland); the Lite and the vk capsula mini ship neither. out: build/device/cadence-<version>-<board>-<arch>.tar. the
# limits-test stage is make ci's native
# self-test of the checks and the packing
ARG SNAPSHOT=20261104T212339Z
ARG BOARD=yandex-pickle

# cadenced as make cross builds it, with the notices of everything linked in: go's own and each module's. arm is midi's
# v7 alone
FROM --platform=$BUILDPLATFORM golang:1.27-trixie@sha256:af1bfc2614ebb1dbd31af514b78740fd678850e76498762b425ecee4c5c6ea80 AS cadenced
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION=dev
WORKDIR /src
COPY go.work go.work.su[m] ./
COPY pkg/ pkg/
COPY server/ server/
COPY device/ device/
RUN --mount=type=cache,target=/go/pkg/mod,id=cadence-gomod,sharing=locked \
  --mount=type=cache,target=/root/.cache/go-build,id=cadence-gobuild-device,sharing=locked \
  set -e; cd device; \
  if [ "$TARGETARCH" = arm ]; then \
    [ "$TARGETVARIANT" = v7 ] || { echo "cadenced: arm builds for v7 only, not ${TARGETVARIANT:-none}" >&2; exit 1; }; \
    export GOARM=7; \
  fi; \
  CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/bin/cadenced ./cmd/cadenced; \
  [ "$TARGETARCH" != arm ] || go version -m /out/bin/cadenced | grep -qx "$(printf '\tbuild\tGOARM=7')"
RUN set -e; cd device; licenses=/out/licenses/go; goroot=$(go env GOROOT); \
  mkdir -p "$licenses" /meta; \
  cp "$goroot/LICENSE" "$goroot/PATENTS" "$licenses/"; \
  GOOS=linux GOARCH=$TARGETARCH go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Dir}}{{end}}{{end}}' \
    ./cmd/cadenced | sort -u | while read -r path dir; do \
    mkdir -p "$licenses/$path"; \
    find "$dir" -maxdepth 1 -type f \( -name 'LICEN[CS]E*' -o -name 'COPYING*' -o -name 'NOTICE*' -o -name PATENTS \) \
      -exec cp {} "$licenses/$path/" \; ; \
    [ -n "$(ls "$licenses/$path")" ] || { echo "$path has no license file" >&2; exit 1; }; \
  done; \
  go version | cut -d' ' -f3 >/meta/go-version; \
  find /out -type f | sort

# ffmpeg natively for the board's cpu in bookworm, apt from the snapshot the base image was made from (its
# debian.sources names it; plain http: apt checks the signed InRelease and every hash). the ca bundle the tarball
# ships is this image's pinned ca-certificates. the stage ends at the packages: ffmpeg comes from the prebuilt cache
# (the ffmpeg-build stage below builds it for that cache alone)
FROM debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc AS builder
ARG SNAPSHOT
RUN grep -q "snapshot.debian.org/archive/debian/$SNAPSHOT" /etc/apt/sources.list.d/debian.sources \
  && sed -i "s|^URIs: http://deb.debian.org/\(debian\(-security\)\?\)\$|URIs: http://snapshot.debian.org/archive/\1/$SNAPSHOT|" \
    /etc/apt/sources.list.d/debian.sources \
  && ! grep -q deb.debian.org /etc/apt/sources.list.d/debian.sources \
  && printf '%s\n' 'Acquire::Check-Valid-Until "false";' 'Acquire::Retries "5";' >/etc/apt/apt.conf.d/90snapshot \
  && apt-get update \
  && apt-get install --yes --no-install-recommends ca-certificates cmake curl g++ libasound2-dev libopus-dev \
    libspeexdsp-dev libssl-dev make ninja-build pkg-config xz-utils \
  && rm -rf /var/lib/apt/lists/*

# pickle's ffmpeg alone, for the prebuilt cache (deploy/builder/prebuilt.sh build ffmpeg-pickle): the same steps the
# builder ran before it, as one deterministic tar. make device-build never builds this stage: it takes the tar from
# the named context ffmpeg-pickle (build/prebuilt/ffmpeg-pickle, which prebuilt.sh fetched from the registry or built
# once), so no ffmpeg is compiled and the emulated build is audiod alone
FROM builder AS ffmpeg-build
COPY deploy/builder/build-ffmpeg.sh /tmp/build-ffmpeg.sh
RUN sh /tmp/build-ffmpeg.sh /opt/ffmpeg && rm /tmp/build-ffmpeg.sh \
  && mkdir /out \
  && tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --format=gnu -cf /out/ffmpeg.tar -C /opt ffmpeg
FROM scratch AS ffmpeg-export
COPY --from=ffmpeg-build /out/ffmpeg.tar /

# pickle's audiod: its runpath is $ORIGIN/lib alone; ffmpeg from the named context ffmpeg-pickle, checked against
# the sha256 prebuilt.sh put beside it
FROM builder AS audiod-yandex-pickle
COPY --from=ffmpeg-pickle /ffmpeg.tar /in/ffmpeg.tar
COPY --from=ffmpeg-pickle /ffmpeg.tar.sha256 /in/ffmpeg.tar.sha256
RUN cd /in && sha256sum --check --strict ffmpeg.tar.sha256 \
  && tar -xf ffmpeg.tar -C /opt && rm -rf /in
COPY media /src/media
ARG VERSION=dev
# audiod's trace marks (media/CMakeLists.txt, CADENCE_TRACING): on unless a build passes OFF
ARG CADENCE_TRACING=ON
RUN PKG_CONFIG_PATH=/opt/ffmpeg/lib/pkgconfig cmake -S /src/media -B /build -G Ninja -DCMAKE_BUILD_TYPE=Release \
    -DCADENCE_MEDIA_ALSA=ON -DCADENCE_MEDIA_STATIC=ON -DCADENCE_MEDIA_URL=ON -DCADENCE_MEDIA_AIRPLAY=ON \
    -DCADENCE_MEDIA_SOLOIST_JAIL=ON -DCADENCE_MEDIA_SIM=OFF -DCADENCE_MEDIA_TESTS=OFF \
    "-DCADENCE_AUDIOD_VERSION=$VERSION" "-DCADENCE_TRACING=$CADENCE_TRACING" \
  && cmake --build /build --target cadence-audiod \
  && /build/cadence-audiod --version | grep -qxF "cadence-audiod $VERSION"

# midi's audiod (M2m-R4): its firmware's glibc is 2.30, older than any debian release with a c++20 gcc, so
# build-midi-audiod.sh cross-builds it, ffmpeg and openssl with zig for exactly that glibc (zig-armhf.sh), on the build
# machine's own cpu. its runpath is $ORIGIN/lib, then the vendor's directory, as a DT_RPATH: the stock library it
# dlopens takes its own dependencies from there, and the loader searches only a DT_RPATH for them
FROM --platform=$BUILDPLATFORM debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc AS zig
ARG SNAPSHOT
ARG BUILDARCH
RUN grep -q "snapshot.debian.org/archive/debian/$SNAPSHOT" /etc/apt/sources.list.d/debian.sources \
  && sed -i "s|^URIs: http://deb.debian.org/\(debian\(-security\)\?\)\$|URIs: http://snapshot.debian.org/archive/\1/$SNAPSHOT|" \
    /etc/apt/sources.list.d/debian.sources \
  && ! grep -q deb.debian.org /etc/apt/sources.list.d/debian.sources \
  && printf '%s\n' 'Acquire::Check-Valid-Until "false";' 'Acquire::Retries "5";' >/etc/apt/apt.conf.d/90snapshot \
  && apt-get update \
  && apt-get install --yes --no-install-recommends ca-certificates curl xz-utils \
  && rm -rf /var/lib/apt/lists/*
# zig 0.15.2 for the build machine's cpu, pinned by its release's sha256, in /opt/zig
RUN set -e; case $BUILDARCH in \
    amd64) zig=zig-x86_64-linux-0.15.2 sum=02aa270f183da276e5b5920b1dac44a63f1a49e55050ebde3aecc9eb82f93239 ;; \
    arm64) zig=zig-aarch64-linux-0.15.2 sum=958ed7d1e00d0ea76590d27666efbf7a932281b3d7ba0c6b01b0ff26498f667f ;; \
    *) echo "no zig pinned for $BUILDARCH" >&2; exit 1 ;; \
  esac; \
  curl --fail --silent --show-error --location --retry 5 --retry-all-errors --output /tmp/zig.tar.xz \
    "https://ziglang.org/download/0.15.2/$zig.tar.xz"; \
  echo "$sum  /tmp/zig.tar.xz" | sha256sum --check --strict -; \
  tar -xJf /tmp/zig.tar.xz -C /opt; rm /tmp/zig.tar.xz; mv "/opt/$zig" /opt/zig; \
  ln -s /opt/zig/zig /usr/local/bin/zig; zig version

# midi's cross tools: the armhf packages and the zig wrappers the deps and the audiod build share
FROM zig AS midi-tools
RUN dpkg --add-architecture armhf \
  && apt-get update \
  && apt-get install --yes --no-install-recommends binutils-multiarch cmake make ninja-build patchelf perl pkg-config \
    libasound2-dev:armhf libopus-dev:armhf libspeexdsp-dev:armhf \
  && rm -rf /var/lib/apt/lists/*
COPY --chmod=755 deploy/builder/zig-armhf.sh /usr/local/bin/zig-armhf
RUN printf '#!/bin/sh\nexec zig-armhf cc "$@"\n' >/usr/local/bin/armhf-cc \
  && printf '#!/bin/sh\nexec zig-armhf c++ "$@"\n' >/usr/local/bin/armhf-c++ \
  && printf '#!/bin/sh\nexec zig ar "$@"\n' >/usr/local/bin/armhf-ar \
  && printf '#!/bin/sh\nexec zig ranlib "$@"\n' >/usr/local/bin/armhf-ranlib \
  && chmod 755 /usr/local/bin/armhf-*

# midi's cross-built dependencies (the armhf prefixes, openssl 3.0 and ffmpeg for its glibc 2.30) alone, for the
# prebuilt cache (prebuilt.sh build midi-deps): build-midi-audiod.sh deps as one deterministic tar. their output is
# armhf whatever cpu builds it, so the cache is keyed without a platform. the audiod build never runs this stage: it
# takes the tar from the named context midi-deps (build/prebuilt/midi-deps, fetched or built once)
FROM midi-tools AS midi-deps-build
COPY deploy/builder/build-ffmpeg.sh deploy/builder/build-midi-audiod.sh deploy/builder/zig-armhf.cmake /w/
RUN sh /w/build-midi-audiod.sh deps \
  && mkdir /out \
  && tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --format=gnu -cf /out/deps.tar \
    -C /opt armhf ffmpeg openssl
FROM scratch AS midi-deps-export
COPY --from=midi-deps-build /out/deps.tar /

# midi's audiod: the deps from the named context midi-deps, checked against the sha256 prebuilt.sh put beside it,
# then cadence-audiod itself from /src/media (ours, built every time)
FROM midi-tools AS midi-cross
COPY --from=midi-deps /deps.tar /in/deps.tar
COPY --from=midi-deps /deps.tar.sha256 /in/deps.tar.sha256
RUN cd /in && sha256sum --check --strict deps.tar.sha256 \
  && tar -xf deps.tar -C /opt && rm -rf /in
COPY deploy/builder/build-midi-audiod.sh deploy/builder/zig-armhf.cmake /w/
COPY media /src/media
ARG VERSION=dev
# the script reads it from the environment
ARG CADENCE_TRACING=ON
RUN sh /w/build-midi-audiod.sh audiod /out "$VERSION"

# midi's audiod checked on armhf (under emulation, with bookworm's newer glibc), laid out as the tree stage reads a
# board's audiod: /build/cadence-audiod, /opt/ffmpeg, the sources' notices and the debian packages' copyrights, and a
# zig board's cross output in /cross
FROM debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc AS audiod-yandex-midi
ARG SNAPSHOT
ARG VERSION=dev
RUN grep -q "snapshot.debian.org/archive/debian/$SNAPSHOT" /etc/apt/sources.list.d/debian.sources \
  && sed -i "s|^URIs: http://deb.debian.org/\(debian\(-security\)\?\)\$|URIs: http://snapshot.debian.org/archive/\1/$SNAPSHOT|" \
    /etc/apt/sources.list.d/debian.sources \
  && ! grep -q deb.debian.org /etc/apt/sources.list.d/debian.sources \
  && printf '%s\n' 'Acquire::Check-Valid-Until "false";' 'Acquire::Retries "5";' >/etc/apt/apt.conf.d/90snapshot \
  && apt-get update \
  && apt-get install --yes --no-install-recommends binutils ca-certificates libasound2 \
  && rm -rf /var/lib/apt/lists/*
COPY media /src/media
COPY --from=midi-cross /opt/ffmpeg /opt/ffmpeg
COPY --from=midi-cross /out/ /cross/
COPY --from=midi-cross /usr/share/doc/libopus-dev/copyright /usr/share/doc/libopus-dev/copyright
COPY --from=midi-cross /usr/share/doc/libspeexdsp-dev/copyright /usr/share/doc/libspeexdsp-dev/copyright
RUN mkdir -p /build \
  && cp -R /cross/cadence-audiod /cross/lib /build/ \
  && /build/cadence-audiod --version | grep -qxF "cadence-audiod $VERSION" \
  && rm -rf /build/lib \
  && readelf -lW /build/cadence-audiod | grep -qF '[Requesting program interpreter: /lib/ld-linux-armhf.so.3]' \
  && readelf -dW /build/cadence-audiod \
    | grep -qE '\(RPATH\) +Library rpath: \[\$ORIGIN/lib:/system/vendor/quasar/libs\]$' \
  && ! readelf -dW /build/cadence-audiod | grep -qF '(RUNPATH)'

# the Lite uses the same armhf/glibc 2.30 audiod and dependencies as midi
FROM audiod-yandex-midi AS audiod-yandex-micro

# the vk capsula mini's audiod: its firmwares' glibc is 2.28 or 2.33 and their openssl 1.1, so build-vk-audiod.sh
# cross-builds it, ffmpeg and openssl 3.0 with zig for aarch64 glibc 2.28 (zig-aarch64.sh), on the build machine's own
# cpu, as midi's. no soloist and no airplay; its runpath is $ORIGIN/lib alone, as a DT_RPATH
FROM zig AS vk-tools
RUN { [ "$(dpkg --print-architecture)" = arm64 ] || dpkg --add-architecture arm64; } \
  && apt-get update \
  && apt-get install --yes --no-install-recommends binutils-multiarch cmake make ninja-build patchelf perl pkg-config \
    libasound2-dev:arm64 libopus-dev:arm64 libspeexdsp-dev:arm64 \
  && rm -rf /var/lib/apt/lists/*
COPY --chmod=755 deploy/builder/zig-aarch64.sh /usr/local/bin/zig-aarch64
RUN printf '#!/bin/sh\nexec zig-aarch64 cc "$@"\n' >/usr/local/bin/aarch64-cc \
  && printf '#!/bin/sh\nexec zig-aarch64 c++ "$@"\n' >/usr/local/bin/aarch64-c++ \
  && printf '#!/bin/sh\nexec zig ar "$@"\n' >/usr/local/bin/aarch64-ar \
  && printf '#!/bin/sh\nexec zig ranlib "$@"\n' >/usr/local/bin/aarch64-ranlib \
  && chmod 755 /usr/local/bin/aarch64-*

# the vk capsula mini's cross-built dependencies alone, for the prebuilt cache (prebuilt.sh build vk-deps), as
# midi-deps-build: aarch64 whatever cpu builds them, so the cache is keyed without a platform
FROM vk-tools AS vk-deps-build
COPY deploy/builder/build-ffmpeg.sh deploy/builder/build-vk-audiod.sh /w/
RUN sh /w/build-vk-audiod.sh deps \
  && mkdir /out \
  && tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --format=gnu -cf /out/deps.tar \
    -C /opt aarch64 ffmpeg openssl
FROM scratch AS vk-deps-export
COPY --from=vk-deps-build /out/deps.tar /

# the deps from the named context vk-deps, checked against the sha256 prebuilt.sh put beside it, then cadence-audiod
FROM vk-tools AS vk-cross
COPY --from=vk-deps /deps.tar /in/deps.tar
COPY --from=vk-deps /deps.tar.sha256 /in/deps.tar.sha256
RUN cd /in && sha256sum --check --strict deps.tar.sha256 \
  && tar -xf deps.tar -C /opt && rm -rf /in
COPY deploy/builder/build-vk-audiod.sh deploy/builder/zig-aarch64.cmake /w/
COPY media /src/media
ARG VERSION=dev
# the script reads it from the environment
ARG CADENCE_TRACING=ON
RUN sh /w/build-vk-audiod.sh audiod /out "$VERSION"

# checked on arm64 (under emulation, with bookworm's newer glibc), laid out as audiod-yandex-midi lays out midi's
FROM debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc AS audiod-vk-capsula-mini
ARG SNAPSHOT
ARG VERSION=dev
RUN grep -q "snapshot.debian.org/archive/debian/$SNAPSHOT" /etc/apt/sources.list.d/debian.sources \
  && sed -i "s|^URIs: http://deb.debian.org/\(debian\(-security\)\?\)\$|URIs: http://snapshot.debian.org/archive/\1/$SNAPSHOT|" \
    /etc/apt/sources.list.d/debian.sources \
  && ! grep -q deb.debian.org /etc/apt/sources.list.d/debian.sources \
  && printf '%s\n' 'Acquire::Check-Valid-Until "false";' 'Acquire::Retries "5";' >/etc/apt/apt.conf.d/90snapshot \
  && apt-get update \
  && apt-get install --yes --no-install-recommends binutils ca-certificates libasound2 \
  && rm -rf /var/lib/apt/lists/*
COPY media /src/media
COPY --from=vk-cross /opt/ffmpeg /opt/ffmpeg
COPY --from=vk-cross /out/ /cross/
COPY --from=vk-cross /usr/share/doc/libopus-dev/copyright /usr/share/doc/libopus-dev/copyright
COPY --from=vk-cross /usr/share/doc/libspeexdsp-dev/copyright /usr/share/doc/libspeexdsp-dev/copyright
RUN mkdir -p /build \
  && cp -R /cross/cadence-audiod /cross/lib /build/ \
  && /build/cadence-audiod --version | grep -qxF "cadence-audiod $VERSION" \
  && rm -rf /build/lib \
  && readelf -lW /build/cadence-audiod | grep -qF '[Requesting program interpreter: /lib/ld-linux-aarch64.so.1]' \
  && readelf -dW /build/cadence-audiod | grep -qE '\(RPATH\) +Library rpath: \[\$ORIGIN/lib\]$' \
  && ! readelf -dW /build/cadence-audiod | grep -qF '(RUNPATH)'

# the boot wrapper cadence-install puts in init's table (M2-D3), the same on every board, and the board's hook it
# sources from beside itself; midi's suspends its stock assistant before every start (M2m-D2)
FROM scratch AS wrapper-yandex-pickle
COPY --chmod=755 device/internal/install/files/cadence-run /cadence-run
COPY --chmod=644 device/boards/yandex-pickle/files/board-hook /board-hook
FROM scratch AS wrapper-yandex-midi
COPY --chmod=755 device/internal/install/files/cadence-run /cadence-run
COPY --chmod=644 device/boards/yandex-midi/files/board-hook /board-hook
FROM scratch AS wrapper-yandex-micro
COPY --chmod=755 device/internal/install/files/cadence-run /cadence-run
COPY --chmod=644 device/boards/yandex-micro/files/board-hook /board-hook
FROM scratch AS wrapper-vk-capsula-mini
COPY --chmod=755 device/internal/install/files/cadence-run /cadence-run
COPY --chmod=644 device/boards/vk-capsula-mini/files/board-hook /board-hook
FROM wrapper-${BOARD} AS wrapper

# only the full speaker boards build these stages; the Lite and the vk capsula mini select an empty one
FROM scratch AS runtime-yandex-micro
FROM scratch AS runtime-vk-capsula-mini
FROM scratch AS runtime-speakers
COPY --from=soloist /soloist-runtime-arm64.tar /in/soloist-runtime-arm64.tar
COPY --from=soloist /soloist-runtime-arm64.tar.sha256 /in/soloist-runtime-arm64.tar.sha256
COPY --from=airplay /airplay-runtime-arm64.tar /in/airplay-runtime-arm64.tar
COPY --from=airplay /airplay-runtime-arm64.tar.sha256 /in/airplay-runtime-arm64.tar.sha256
FROM runtime-speakers AS runtime-yandex-pickle
FROM runtime-speakers AS runtime-yandex-midi
FROM runtime-${BOARD} AS runtime

# the owner's rendered spoken prompts, <lang>/<id>.opus, when dist.sh passes build/prompts as this context; empty
# otherwise, and audiod plays its error earcon in their place
FROM scratch AS prompts

# the tarball's tree: bin/ (cadenced, audiod, each ffmpeg library under its soname in audiod's runpath $ORIGIN/lib, and
# on the zig boards (midi, the Lite, the vk capsula mini) openssl's beside them, which get ffmpeg's $ORIGIN runpath),
# both runtimes where the board ships them, the ca bundle, and the notices of what bin/ links in or share/ ships
# (libopus and speexdsp from these debian packages; on pickle libstdc++ and libgcc under gcc's runtime library
# exception, on the zig boards llvm's libc++, libc++abi and libunwind with zig's own; nanopb and nlohmann/json vendored;
# on the zig boards openssl from its release tarball with its licence text; on the vk capsula mini tensorflow lite
# micro's, flatbuffers' and cmsis-nn's, built in)
FROM audiod-${BOARD} AS tree
ARG SNAPSHOT
ARG VERSION=dev
ARG BOARD
ARG ARCH
RUN set -e; tree=/tree; licenses=$tree/licenses; \
  [ "$(dpkg --print-architecture)" = "$ARCH" ] \
    || { echo "this image is $(dpkg --print-architecture), not ${ARCH:-unset}" >&2; exit 1; }; \
  mkdir -p "$tree/bin/lib" "$tree/share" "$licenses/ffmpeg" "$licenses/opus" "$licenses/speexdsp" \
    "$licenses/nanopb" "$licenses/nlohmann-json" "$licenses/ca-certificates"; \
  install -m 755 /build/cadence-audiod "$tree/bin/cadence-audiod"; \
  for name in avformat avcodec avutil swresample; do \
    soname=$(readelf -d "/opt/ffmpeg/lib/lib$name.so" | sed -n 's/.*Library soname: \[\(.*\)\]/\1/p'); \
    install -m 755 "/opt/ffmpeg/lib/$soname" "$tree/bin/lib/$soname"; \
  done; \
  install -m 644 /etc/ssl/certs/ca-certificates.crt "$tree/share/ca-certificates.crt"; \
  install -m 644 /opt/ffmpeg/share/licenses/ffmpeg/* "$licenses/ffmpeg/"; \
  install -m 644 /usr/share/doc/libopus-dev/copyright "$licenses/opus/copyright"; \
  install -m 644 /usr/share/doc/libspeexdsp-dev/copyright "$licenses/speexdsp/copyright"; \
  case $BOARD in \
    yandex-midi | yandex-micro | vk-capsula-mini) cp -R /cross/licenses/llvm "$licenses/llvm" ;; \
    *) install -D -m 644 /usr/share/doc/gcc-12-base/copyright "$licenses/gcc/copyright" ;; \
  esac; \
  install -m 644 /src/media/third_party/nanopb/LICENSE.txt "$licenses/nanopb/LICENSE.txt"; \
  install -m 644 /src/media/third_party/nlohmann/LICENSE.MIT "$licenses/nlohmann-json/LICENSE.MIT"; \
  install -m 644 /usr/share/doc/ca-certificates/copyright "$licenses/ca-certificates/copyright"
RUN set -e; case $BOARD in \
    yandex-midi | yandex-micro) loader=/lib/ld-linux-armhf.so.3 ;; \
    vk-capsula-mini) loader=/lib/ld-linux-aarch64.so.1 ;; \
    *) exit 0 ;; \
  esac; \
  for soname in libssl.so.3 libcrypto.so.3; do \
    install -m 755 "/cross/lib/$soname" "/tree/bin/lib/$soname"; \
    readelf -dW "/tree/bin/lib/$soname" | grep -qF 'Library runpath: [$ORIGIN]'; \
  done; \
  install -D -m 644 /cross/licenses/openssl/copyright /tree/licenses/openssl/copyright; \
  install -m 644 /cross/licenses/openssl/Apache-2.0 /tree/licenses/openssl/Apache-2.0; \
  if [ "$BOARD" = vk-capsula-mini ]; then \
    cp -R /cross/licenses/tflite-micro /cross/licenses/flatbuffers /cross/licenses/cmsis-nn /tree/licenses/; \
  fi; \
  mkdir -p /meta; cp /cross/openssl-version /meta/openssl-version; \
  "$loader" --list /tree/bin/cadence-audiod >/tmp/resolved; \
  for library in /tree/bin/lib/*; do \
    grep -qF "${library##*/} => $library " /tmp/resolved \
      || { echo "audiod does not load ${library##*/} from bin/lib" >&2; cat /tmp/resolved >&2; exit 1; }; \
  done
# where each notice is, the runtimes' own included
COPY --chmod=644 deploy/builder/device-licenses.txt /tree/licenses/README
COPY --from=wrapper / /tree/share/
COPY --from=cadenced /out/ /tree/
COPY --from=cadenced /meta/ /meta/
COPY --from=runtime / /
COPY --from=prompts / /prompts/
COPY deploy/builder/add-prompts.sh /w/add-prompts.sh
RUN bash /w/add-prompts.sh /prompts /tree
RUN set -e; case $BOARD in yandex-micro | vk-capsula-mini) exit 0 ;; esac; \
  cd /in && sha256sum --check --strict soloist-runtime-arm64.tar.sha256 airplay-runtime-arm64.tar.sha256 \
  && mkdir -p /tree/soloist/runtime /tree/airplay/runtime \
  && tar -xf soloist-runtime-arm64.tar -C /tree/soloist/runtime \
  && tar -xf airplay-runtime-arm64.tar -C /tree/airplay/runtime
COPY deploy/builder/check-device-limits.sh deploy/builder/pack-device.sh deploy/builder/check-device-tree.sh /w/
RUN case $BOARD in \
    yandex-pickle) bash /w/check-device-limits.sh --machine AArch64 /tree ;; \
    yandex-midi | yandex-micro) bash /w/check-device-limits.sh --machine ARM --vendor-libs /system/vendor/quasar/libs /tree ;; \
    vk-capsula-mini) bash /w/check-device-limits.sh --board vk-capsula-mini --machine AArch64 /tree ;; \
    *) echo "no limits for board $BOARD" >&2; exit 1 ;; \
  esac
RUN set -e; out="/out/cadence-$VERSION-$BOARD-$ARCH.tar"; mkdir -p /out; \
  set -- "cadence $VERSION" "board $BOARD" "arch $ARCH" \
    "go $(cat /meta/go-version)" "builder debian $(cat /etc/debian_version) snapshot $SNAPSHOT" \
    "ffmpeg $(cat /opt/ffmpeg/share/licenses/ffmpeg/version.txt)" \
    "ca-certificates $(dpkg-query --show --showformat '${Version}' ca-certificates)"; \
  [ ! -f /meta/openssl-version ] || set -- "$@" "openssl $(cat /meta/openssl-version)"; \
  case $BOARD in \
    yandex-micro | vk-capsula-mini) set -- "$@" "runtimes none" ;; \
    *) set -- "$@" "soloist-runtime sha256 $(sha256sum /in/soloist-runtime-arm64.tar | cut -d' ' -f1)" \
      "airplay-runtime sha256 $(sha256sum /in/airplay-runtime-arm64.tar | cut -d' ' -f1)" ;; \
  esac; \
  bash /w/pack-device.sh /tree "$out" "$@"; \
  mkdir /unpacked; tar -xf "$out" -C /unpacked; cd /unpacked; \
  sha256sum --check --strict --quiet MANIFEST; \
  bash /w/check-device-tree.sh "$BOARD" /unpacked; \
  if [ "$BOARD" = yandex-midi ]; then \
    readelf -hW soloist/runtime/lib/ld-linux-aarch64.so.1 | grep -qE '^ +Machine: +AArch64$' \
      || { echo "soloist's runtime is not arm64" >&2; exit 1; }; \
    readelf -hW airplay/runtime/bin/shairport-sync | grep -qE '^ +Machine: +AArch64$' \
      || { echo "airplay's runtime is not arm64" >&2; exit 1; }; \
  fi; \
  grep '^#' MANIFEST; ls -l /out

FROM scratch AS export
COPY --from=tree /out/ /

# make device-limits-test's go programs: cadenced's shape, static for midi's arm v7 and pickle's arm64
FROM --platform=$BUILDPLATFORM golang:1.27-trixie@sha256:af1bfc2614ebb1dbd31af514b78740fd678850e76498762b425ecee4c5c6ea80 AS limits-go
WORKDIR /tiny
RUN printf 'module tiny\n\ngo 1.27\n' >go.mod && printf 'package main\n\nfunc main() {}\n' >main.go \
  && CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -trimpath -ldflags '-s -w' -o /out/cadenced-arm . \
  && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags '-s -w' -o /out/cadenced-arm64 .

# make device-limits-test (in make ci): the checks' and the packing's tests, natively, on trixie's glibc 2.41, which
# lets them build elf files that need more than the boards have; midi's with trixie's armhf cross compiler
FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS limits-test
ARG SNAPSHOT
RUN grep -q "snapshot.debian.org/archive/debian/$SNAPSHOT" /etc/apt/sources.list.d/debian.sources \
  && sed -i "s|^URIs: http://deb.debian.org/\(debian\(-security\)\?\)\$|URIs: http://snapshot.debian.org/archive/\1/$SNAPSHOT|" \
    /etc/apt/sources.list.d/debian.sources \
  && ! grep -q deb.debian.org /etc/apt/sources.list.d/debian.sources \
  && printf '%s\n' 'Acquire::Check-Valid-Until "false";' 'Acquire::Retries "5";' >/etc/apt/apt.conf.d/90snapshot \
  && apt-get update \
  && apt-get install --yes --no-install-recommends g++ gcc-arm-linux-gnueabihf libc6-dev-armhf-cross patchelf \
  && rm -rf /var/lib/apt/lists/*
# midi's and the vk capsula mini's trees are built with the zig their audiod is built with
COPY --from=zig /opt/zig /opt/zig
RUN ln -s /opt/zig/zig /usr/local/bin/zig && zig version
COPY --from=limits-go /out/ /w/go/
ENV GO_ARM_ELF=/w/go/cadenced-arm GO_ARM64_ELF=/w/go/cadenced-arm64
COPY deploy/builder/check-device-limits.sh deploy/builder/check-device-limits.test.sh deploy/builder/pack-device.sh \
  deploy/builder/pack-device.test.sh deploy/builder/check-device-tree.sh deploy/builder/check-device-tree.test.sh \
  deploy/builder/add-prompts.sh deploy/builder/add-prompts.test.sh /w/
COPY deploy/builder/pack-device.sh /w/deploy/builder/pack-device.sh
RUN bash /w/check-device-limits.test.sh && bash /w/pack-device.test.sh && bash /w/check-device-tree.test.sh \
  && bash /w/add-prompts.test.sh
