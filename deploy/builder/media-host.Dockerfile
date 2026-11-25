# trixie's packages the media builder's stages share (the former base stage's apt list)
FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS packages
RUN apt-get update \
  && apt-get install --yes --no-install-recommends \
    ca-certificates \
    cmake \
    curl \
    g++ \
    libasound2-dev \
    libopus-dev \
    libspeexdsp-dev \
    libssl-dev \
    make \
    nasm \
    ninja-build \
    pkg-config \
    xz-utils \
  && rm -rf /var/lib/apt/lists/*

# the url source's ffmpeg, alone for the prebuilt cache (deploy/builder/prebuilt.sh build ffmpeg-mediahost-<arch>):
# the same steps the base stage ran before it, as one deterministic tar. the stages below never build this one: they
# take the tar from the named context ffmpeg-mediahost (build/prebuilt/ffmpeg-mediahost-<arch>, which prebuilt.sh
# fetched from the registry or built once), so a cache hit compiles no ffmpeg
FROM packages AS ffmpeg-build
COPY deploy/builder/build-ffmpeg.sh /tmp/build-ffmpeg.sh
RUN sh /tmp/build-ffmpeg.sh /opt/ffmpeg && rm /tmp/build-ffmpeg.sh \
  && mkdir /out \
  && tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --format=gnu -cf /out/ffmpeg.tar -C /opt ffmpeg
FROM scratch AS ffmpeg-export
COPY --from=ffmpeg-build /out/ffmpeg.tar /

# what the host build and the screen libraries build on: catch2 and the ffmpeg the context ffmpeg-mediahost names,
# checked against the sha256 prebuilt.sh put beside it
FROM packages AS base
ARG CATCH2_VERSION=3.16.1
ARG CATCH2_SHA256=d16bd1b8b2364918bd472cb216394acb93bde6784890df0d91bccfb3f28d777d

RUN curl --fail --silent --show-error --location --retry 5 --retry-all-errors --connect-timeout 30 \
    --output /tmp/catch2.tar.gz \
    "https://github.com/catchorg/Catch2/archive/refs/tags/v${CATCH2_VERSION}.tar.gz" \
  && echo "${CATCH2_SHA256}  /tmp/catch2.tar.gz" | sha256sum --check --strict - \
  && tar -xzf /tmp/catch2.tar.gz -C /tmp \
  && cmake -S "/tmp/Catch2-${CATCH2_VERSION}" -B /tmp/catch2-build -G Ninja \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_CXX_STANDARD=20 \
    -DBUILD_TESTING=OFF \
    -DCATCH_INSTALL_DOCS=OFF \
  && cmake --build /tmp/catch2-build \
  && cmake --install /tmp/catch2-build \
  && rm -rf /tmp/catch2.tar.gz "/tmp/Catch2-${CATCH2_VERSION}" /tmp/catch2-build

COPY --from=ffmpeg-mediahost /ffmpeg.tar /in/ffmpeg.tar
COPY --from=ffmpeg-mediahost /ffmpeg.tar.sha256 /in/ffmpeg.tar.sha256
RUN cd /in && sha256sum --check --strict ffmpeg.tar.sha256 \
  && tar -xf ffmpeg.tar -C /opt && rm -rf /in
ENV PKG_CONFIG_PATH=/opt/ffmpeg/lib/pkgconfig

FROM base AS test
# the real dbus-daemon the d-bus client's tests talk to, for the tests alone: debian's 1.16.2-2, the one the airplay
# runtime ships (airplay-runtime.pins), from the snapshot this base image was made from (plain http: apt checks the
# signed InRelease and every hash). both configurations name it, so a missing daemon fails those tests, never skips them
RUN grep -q 'snapshot.debian.org/archive/debian/20261104T212339Z' /etc/apt/sources.list.d/debian.sources \
  && printf '%s\n' 'Types: deb' 'URIs: http://snapshot.debian.org/archive/debian/20261104T212339Z' \
    'Suites: trixie trixie-updates' 'Components: main' 'Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp' '' \
    'Types: deb' 'URIs: http://snapshot.debian.org/archive/debian-security/20261104T212339Z' \
    'Suites: trixie-security' 'Components: main' 'Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp' \
    >/etc/apt/sources.list.d/debian.sources \
  && printf '%s\n' 'Acquire::Check-Valid-Until "false";' 'Acquire::Retries "5";' >/etc/apt/apt.conf.d/90snapshot \
  && apt-get update \
  && apt-get install --yes --no-install-recommends dbus-daemon=1.16.2-2 \
  && rm -rf /var/lib/apt/lists/*
# the screens' configuration below builds libairplay: its openssl (libssl-dev, in the base), libplist and avahi-client
# from the same snapshot, git for its patch script
RUN apt-get update \
  && apt-get install --yes --no-install-recommends git libavahi-client-dev libplist-dev \
  && rm -rf /var/lib/apt/lists/*
COPY media /w/media
COPY proto/fixtures/audiod /w/proto/fixtures/audiod
COPY deploy/builder/apply-patches.sh /w/deploy/builder/apply-patches.sh
# ctest's selection for the host and sim configurations below (say -R or -E with a pattern), every suite when empty
ARG MEDIA_CTEST_ARGS=
# the keyword engine too (tensorflow lite micro fetched pinned, media/cmake/tflm.cmake), which only the vk capsula
# mini's build turns on besides this one
RUN cmake -S /w/media -B /w/build -G Ninja -DCADENCE_DBUS_DAEMON=/usr/bin/dbus-daemon -DCADENCE_MEDIA_KWS=ON \
  && cmake --build /w/build \
  && ctest --test-dir /w/build -j"$(nproc)" --output-on-failure $MEDIA_CTEST_ARGS
# audiod's trace marks off (CADENCE_TRACING=OFF), so that build cannot rot: audiod and the suites that mark build, and
# their checks see no mark go out
RUN cmake -S /w/media -B /w/build-notrace -G Ninja -DCADENCE_TRACING=OFF -DCADENCE_DBUS_DAEMON=/usr/bin/dbus-daemon \
  && cmake --build /w/build-notrace --target cadence-audiod cadence_audiod_embedded trace.test jitter-buffer.test \
    capture-engine.test \
  && ctest --test-dir /w/build-notrace -R '^(trace|jitter-buffer): |^capture-engine: capture engines: a wake' \
    -j"$(nproc)" --output-on-failure
# the sim's configuration: no alsa, the file backends only, soloist unjailed (the sim has no CAP_SYS_ADMIN) and airplay
# without avahi or nqptp (the sim has no multicast and no ptp ports)
RUN cmake -S /w/media -B /w/build-sim -G Ninja -DCADENCE_MEDIA_ALSA=OFF -DCADENCE_MEDIA_SIM=ON \
    -DCADENCE_MEDIA_SOLOIST_JAIL=OFF -DCADENCE_MEDIA_AIRPLAY_LAN=OFF -DCADENCE_DBUS_DAEMON=/usr/bin/dbus-daemon \
  && cmake --build /w/build-sim \
  && ctest --test-dir /w/build-sim -j"$(nproc)" --output-on-failure $MEDIA_CTEST_ARGS
# a screen's configuration (TV stream J): libairplay as the airplay receiver (uxplay's lib/ patched out of tree, the
# avahi backend, apple's alac decoder fetched pinned, the base's ffmpeg for aac), no url source, the file backends.
# its own suites alone (uxplay-tests), and the tap's, which it changes; cadence-audiod links it whole
RUN cmake -S /w/media -B /w/build-screen -G Ninja -DCADENCE_MEDIA_AIRPLAY=uxplay -DCADENCE_AIRPLAY_DNSSD_AVAHI=ON \
    -DCADENCE_MEDIA_URL=OFF -DCADENCE_SCREEN=ON -DCADENCE_MEDIA_AUDIO_BACKEND=sim -DCADENCE_MEDIA_SIM=ON \
    -DCADENCE_MEDIA_SOLOIST_JAIL=OFF \
  && cmake --build /w/build-screen --target uxplay-tests tap-server.test cadence-audiod \
  && ctest --test-dir /w/build-screen -R '^(airplay-uxplay|tap-server)' -j"$(nproc)" --output-on-failure

# the jail's tests need root with CAP_SYS_ADMIN, which no build step has: make media-test-privileged runs them from
# this stage with docker run --privileged, where a missing privilege fails them instead of skipping them
FROM test AS privileged
ENV CADENCE_JAIL_REQUIRED=1
WORKDIR /w/build

# soloist's runtime (M4c-D12) for pickle's arm64, its shim cross-compiled with trixie's aarch64 g++, and for the sim's
# amd64, then the assembly script's tests; make media-test builds this stage beside the test stage. its own minimal
# stage: the shim builds CADENCE_MEDIA_PULSE_ONLY on the media files it alone reads (the root cmake list, the pcm
# frame, pulse-shim), so no ffmpeg, no catch2 and no audiod source can invalidate or slow it. the cross compiler
# comes from live trixie, unpinned as before; the runtime's debs are snapshot-pinned in the script itself
FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS soloist-runtime
RUN apt-get update \
  && apt-get install --yes --no-install-recommends \
    ca-certificates \
    cmake \
    curl \
    g++ \
    g++-aarch64-linux-gnu \
    make \
    ninja-build \
  && rm -rf /var/lib/apt/lists/*
ENV SOLOIST_DEBS=/opt/soloist-debs
COPY media/CMakeLists.txt /w/media/CMakeLists.txt
COPY media/lib/src/result.h /w/media/lib/src/result.h
COPY media/lib/src/pcmframe/ /w/media/lib/src/pcmframe/
COPY media/lib/src/resolv/ /w/media/lib/src/resolv/
COPY media/pulse-shim/ /w/media/pulse-shim/
COPY deploy/builder/assemble-soloist-runtime.sh deploy/builder/assemble-soloist-runtime.test.sh /w/deploy/builder/
RUN for arch in arm64 amd64; do \
    bash /w/deploy/builder/assemble-soloist-runtime.sh "$arch" "/out/soloist-runtime-$arch.tar" || exit 1; \
    tar -tvf "/out/soloist-runtime-$arch.tar" --numeric-owner; \
  done
RUN bash /w/deploy/builder/assemble-soloist-runtime.test.sh /out/soloist-runtime-arm64.tar \
  /out/soloist-runtime-amd64.tar

# make device-build takes soloist's arm64 runtime from here, once the stage above built and tested it; the amd64 one
# (the sim's) is its own export, for the prebuilt cache (deploy/builder/prebuilt.sh)
FROM scratch AS soloist-export
COPY --from=soloist-runtime /out/soloist-runtime-arm64.tar /
FROM scratch AS soloist-export-amd64
COPY --from=soloist-runtime /out/soloist-runtime-amd64.tar /
