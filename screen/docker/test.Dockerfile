# cadence-screen's builder and its offscreen test harness (wave 1 stream A): debian trixie's sdl3, libmpv and mesa
# (llvmpipe through egl surfaceless) from the snapshot the base image was made from, catch2 from its pinned tarball.
# stages: build (the project, any cpu: make screen-test builds it for linux/amd64 and linux/arm64), test (ctest with
# sdl's offscreen driver; nothing plays sound: every test player runs ao=null). rmlui (stream b's ui) has no debian
# package: its release tarball is built here at its pinned hash against trixie's freetype
FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS base
ARG CATCH2_VERSION=3.16.1
ARG CATCH2_SHA256=d16bd1b8b2364918bd472cb216394acb93bde6784890df0d91bccfb3f28d777d
ARG RMLUI_VERSION=6.3
ARG RMLUI_SHA256=d977298bb6147610e5984d5db85ddf284020d655a8713913f6982074f1dbdede

# plain http: apt checks the signed InRelease and every hash
RUN grep -q 'snapshot.debian.org/archive/debian/20261104T212339Z' /etc/apt/sources.list.d/debian.sources \
  && printf '%s\n' 'Types: deb' 'URIs: http://snapshot.debian.org/archive/debian/20261104T212339Z' \
    'Suites: trixie trixie-updates' 'Components: main' 'Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp' '' \
    'Types: deb' 'URIs: http://snapshot.debian.org/archive/debian-security/20261104T212339Z' \
    'Suites: trixie-security' 'Components: main' 'Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp' \
    >/etc/apt/sources.list.d/debian.sources \
  && printf '%s\n' 'Acquire::Check-Valid-Until "false";' 'Acquire::Retries "5";' >/etc/apt/apt.conf.d/90snapshot \
  && apt-get update \
  && apt-get install --yes --no-install-recommends \
    ca-certificates \
    cmake \
    curl \
    g++ \
    libegl-dev \
    libegl-mesa0 \
    libfreetype-dev \
    libgl1-mesa-dri \
    libgles-dev \
    libmpv-dev \
    libsdl3-dev \
    libstb-dev \
    ninja-build \
    pkg-config \
  && rm -rf /var/lib/apt/lists/*

RUN curl --fail --silent --show-error --location --output /tmp/catch2.tar.gz \
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

# rmlui's core library alone (its backends are not part of it: screen/ui/rml keeps the gl3 renderer it patches)
RUN curl --fail --silent --show-error --location --output /tmp/rmlui.tar.gz \
    "https://github.com/mikke89/RmlUi/archive/refs/tags/${RMLUI_VERSION}.tar.gz" \
  && echo "${RMLUI_SHA256}  /tmp/rmlui.tar.gz" | sha256sum --check --strict - \
  && tar -xzf /tmp/rmlui.tar.gz -C /tmp \
  && cmake -S "/tmp/RmlUi-${RMLUI_VERSION}" -B /tmp/rmlui-build -G Ninja \
    -DCMAKE_BUILD_TYPE=Release \
    -DBUILD_SHARED_LIBS=OFF \
    -DCMAKE_POSITION_INDEPENDENT_CODE=ON \
    -DRMLUI_SAMPLES=OFF \
    -DRMLUI_FONT_ENGINE=freetype \
    -DRMLUI_PRECOMPILED_HEADERS=OFF \
  && cmake --build /tmp/rmlui-build \
  && cmake --install /tmp/rmlui-build \
  && rm -rf /tmp/rmlui.tar.gz "/tmp/RmlUi-${RMLUI_VERSION}" /tmp/rmlui-build

# the test clips: ffmpeg is only in this stage, never in the build
FROM base AS clips
RUN apt-get update && apt-get install --yes --no-install-recommends ffmpeg && rm -rf /var/lib/apt/lists/*
COPY screen/tests/make-clips.sh /tmp/make-clips.sh
RUN sh /tmp/make-clips.sh /clips

FROM base AS build
COPY media/third_party/nanopb /w/media/third_party/nanopb
COPY media/gen /w/media/gen
COPY media/lib/src/ring/cadence_ring.h /w/media/lib/src/ring/cadence_ring.h
COPY media/third_party/nlohmann /w/media/third_party/nlohmann
COPY i18n/testdata /w/i18n/testdata
COPY screen /w/screen
RUN cmake -S /w/screen -B /w/build -G Ninja -DCMAKE_BUILD_TYPE=RelWithDebInfo \
  && cmake --build /w/build

FROM build AS test
COPY proto/fixtures/screen /w/proto/fixtures/screen
COPY proto/fixtures/cards /w/proto/fixtures/cards
COPY --from=clips /clips /w/clips
ENV SDL_VIDEO_DRIVER=offscreen EGL_PLATFORM=surfaceless LIBGL_ALWAYS_SOFTWARE=1 GALLIUM_DRIVER=llvmpipe \
  CADENCE_SCREEN_CLIPS=/w/clips
RUN ctest --test-dir /w/build -j4 --output-on-failure --timeout 120
