# the visualizer's gl tests (make screen-p-gl-test): debian trixie with mesa's llvmpipe behind egl (surfaceless),
# gles 3 headers, catch2 at its pin, and libprojectM-4 built by media/third_party/projectm's own scripts at their pins.
# stream A's screen image (make screen-test) may take this over
FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS base
ARG CATCH2_VERSION=3.16.1
ARG CATCH2_SHA256=d16bd1b8b2364918bd472cb216394acb93bde6784890df0d91bccfb3f28d777d

RUN apt-get update \
  && apt-get install --yes --no-install-recommends \
    ca-certificates \
    cmake \
    curl \
    g++ \
    libegl-dev \
    libegl-mesa0 \
    libgl1-mesa-dri \
    libgles-dev \
    ninja-build \
    patch \
    pkg-config \
    zlib1g-dev \
  && rm -rf /var/lib/apt/lists/*

RUN curl --fail --silent --show-error --location --output /tmp/catch2.tar.gz \
    "https://github.com/catchorg/Catch2/archive/refs/tags/v${CATCH2_VERSION}.tar.gz" \
  && echo "${CATCH2_SHA256}  /tmp/catch2.tar.gz" | sha256sum --check --strict - \
  && tar -xzf /tmp/catch2.tar.gz -C /tmp \
  && cmake -S "/tmp/Catch2-${CATCH2_VERSION}" -B /tmp/catch2-build -G Ninja -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_CXX_STANDARD=20 -DBUILD_TESTING=OFF -DCATCH_INSTALL_DOCS=OFF \
  && cmake --build /tmp/catch2-build \
  && cmake --install /tmp/catch2-build \
  && rm -rf /tmp/catch2.tar.gz "/tmp/Catch2-${CATCH2_VERSION}" /tmp/catch2-build

ENV LIBGL_ALWAYS_SOFTWARE=1 GALLIUM_DRIVER=llvmpipe

FROM base AS projectm
COPY media/third_party/projectm /tmp/projectm-recipe
RUN sh /tmp/projectm-recipe/fetch.sh /tmp/pm \
  && { sh /tmp/projectm-recipe/build.sh /tmp/pm/src /tmp/pm/build /opt/projectm > /tmp/pm-build.log 2>&1 \
    || { tail -40 /tmp/pm-build.log; exit 1; }; } \
  && rm -rf /tmp/pm /tmp/pm-build.log \
  && sh /tmp/projectm-recipe/fetch-presets.sh /opt/milkdrop /tmp/projectm-recipe/presets.candidates /tmp/pm-dl \
  && cp /tmp/projectm-recipe/presets.candidates /opt/milkdrop/presets.txt && rm -rf /tmp/pm-dl
# the presets the devices ship, for the milkdrop source's test
ENV CMAKE_PREFIX_PATH=/opt/projectm LD_LIBRARY_PATH=/opt/projectm/lib CADENCE_VIZ_TEST_MILKDROP=/opt/milkdrop
