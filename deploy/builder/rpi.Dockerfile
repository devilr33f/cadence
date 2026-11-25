# rpi's deb (make dist DISTRO=rpi): one raspberry pi board with cadence-screen on hdmi (sdl kmsdrm), audiod on hdmi
# or bluealsa, cec and libairplay. built natively in trixie arm64. the visualizer's milkdrop: libprojectM-4 (lgpl-2.1,
# shared, in /opt/cadence/lib, found through cadence-screen's $ORIGIN runpath) built from media/third_party/projectm's
# pins, and its presets fetched at build time into /opt/cadence/share/viz/milkdrop (never committed, rule 5). the named contexts libairplay and mpv are dist.sh's;
# audiod compiles libairplay from source here, and the device installs mpv/deb-0.40-arm64's patched libmpv2 beside this
# deb. out: build/dist/cadence-<version>-rpi-arm64.deb
ARG SNAPSHOT=20261104T212339Z

FROM --platform=$BUILDPLATFORM golang:1.27-trixie@sha256:af1bfc2614ebb1dbd31af514b78740fd678850e76498762b425ecee4c5c6ea80 AS cadenced
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.work go.work.su[m] ./
COPY pkg/ pkg/
COPY server/ server/
COPY device/ device/
RUN --mount=type=cache,target=/go/pkg/mod,id=cadence-gomod,sharing=locked \
  --mount=type=cache,target=/root/.cache/go-build,id=cadence-gobuild-rpi,sharing=locked \
  set -e; cd device; \
  CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/bin/cadenced ./cmd/cadenced; \
  licenses=/out/licenses/go; goroot=$(go env GOROOT); mkdir -p "$licenses"; \
  cp "$goroot/LICENSE" "$goroot/PATENTS" "$licenses/"; \
  GOOS=linux GOARCH=$TARGETARCH go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Dir}}{{end}}{{end}}' \
    ./cmd/cadenced | sort -u | while read -r path dir; do \
    mkdir -p "$licenses/$path"; \
    find "$dir" -maxdepth 1 -type f \( -name 'LICEN[CS]E*' -o -name 'COPYING*' -o -name 'NOTICE*' -o -name PATENTS \) \
      -exec cp {} "$licenses/$path/" \; ; \
    [ -n "$(ls "$licenses/$path")" ] || { echo "$path has no license file" >&2; exit 1; }; \
  done

FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS native
ARG SNAPSHOT
RUN set -e; grep -q "snapshot.debian.org/archive/debian/$SNAPSHOT" /etc/apt/sources.list.d/debian.sources; \
  sed -i "s|^URIs: http://deb.debian.org/\(debian\(-security\)\?\)\$|URIs: http://snapshot.debian.org/archive/\1/$SNAPSHOT|" \
    /etc/apt/sources.list.d/debian.sources; \
  ! grep -q deb.debian.org /etc/apt/sources.list.d/debian.sources; \
  printf '%s\n' 'Acquire::Check-Valid-Until "false";' 'Acquire::Retries "5";' >/etc/apt/apt.conf.d/90snapshot; \
  apt-get update; \
  apt-get install --yes --no-install-recommends binutils ca-certificates cmake curl dpkg-dev g++ git \
    libasound2-dev libavahi-client-dev libavcodec-dev libavutil-dev libcurl4-openssl-dev libegl-dev \
    libfreetype-dev libgles-dev libmpv-dev libopus-dev libplist-dev libsdl3-dev libspeexdsp-dev \
    libssl-dev libstb-dev ninja-build pkg-config; \
  rm -rf /var/lib/apt/lists/*

# rmlui's core alone, static, at the same pin as the screen's test builder
ARG RMLUI_VERSION=6.3
ARG RMLUI_SHA256=d977298bb6147610e5984d5db85ddf284020d655a8713913f6982074f1dbdede
RUN set -e; curl --fail --silent --show-error --location --retry 5 --retry-all-errors --output /tmp/rmlui.tar.gz \
    "https://github.com/mikke89/RmlUi/archive/refs/tags/${RMLUI_VERSION}.tar.gz"; \
  echo "${RMLUI_SHA256}  /tmp/rmlui.tar.gz" | sha256sum --check --strict -; \
  tar -xzf /tmp/rmlui.tar.gz -C /tmp; \
  cmake -S "/tmp/RmlUi-${RMLUI_VERSION}" -B /tmp/rmlui-build -G Ninja -DCMAKE_BUILD_TYPE=Release \
    -DBUILD_SHARED_LIBS=OFF -DCMAKE_POSITION_INDEPENDENT_CODE=ON -DRMLUI_SAMPLES=OFF \
    -DRMLUI_FONT_ENGINE=freetype -DRMLUI_PRECOMPILED_HEADERS=OFF >/tmp/rmlui.log 2>&1; \
  cmake --build /tmp/rmlui-build >>/tmp/rmlui.log 2>&1; cmake --install /tmp/rmlui-build >>/tmp/rmlui.log 2>&1; \
  mkdir -p /out/licenses/rmlui; cp "/tmp/RmlUi-${RMLUI_VERSION}/LICENSE.txt" /out/licenses/rmlui/; \
  rm -rf /tmp/rmlui.tar.gz "/tmp/RmlUi-${RMLUI_VERSION}" /tmp/rmlui-build

# libprojectM-4 with its fbo backport, gles 3, shared, and the presets the screen ships: presets.candidates until the
# owner curates on a device (media/third_party/projectm/README.md); the host skips one over its 20 ms budget at run time
COPY media/third_party/projectm /src/projectm-recipe
RUN set -e; sh /src/projectm-recipe/fetch.sh /tmp/pm >/dev/null; \
  sh /src/projectm-recipe/build.sh /tmp/pm/src /tmp/pm/build /opt/projectm >/tmp/projectm.log 2>&1 \
    || { tail -60 /tmp/projectm.log; exit 1; }; \
  mkdir -p /out/lib /out/share/viz/milkdrop /out/licenses/projectm /out/licenses/projectm-eval \
    /out/licenses/milkdrop-presets; \
  cp -P /opt/projectm/lib/libprojectM-4.so.4 /opt/projectm/lib/libprojectM-4.so.4.* /out/lib/; \
  readelf -dW /out/lib/libprojectM-4.so.4 | grep -qF 'Library soname: [libprojectM-4.so.4]'; \
  ! readelf -dW /out/lib/libprojectM-4.so.4 | grep -q 'NEEDED.*libGL\.so'; \
  cp /tmp/pm/src/COPYING /tmp/pm/src/LICENSE.txt /tmp/pm/src/AUTHORS.txt /out/licenses/projectm/; \
  cp /tmp/pm/src/vendor/projectm-eval/LICENSE.md /out/licenses/projectm-eval/; \
  sh /src/projectm-recipe/fetch-presets.sh /out/share/viz/milkdrop /src/projectm-recipe/presets.candidates /tmp/pm-dl; \
  cp /src/projectm-recipe/presets.candidates /out/share/viz/milkdrop/presets.txt; \
  mv /out/share/viz/milkdrop/LICENSE.md /out/licenses/milkdrop-presets/LICENSE.md; \
  rm -rf /tmp/pm /tmp/pm-dl /tmp/projectm.log

COPY media /src/media
COPY screen /src/screen
COPY deploy/builder/apply-patches.sh /src/deploy/builder/apply-patches.sh
ARG VERSION=dev
ARG CADENCE_TRACING=ON
RUN set -e; cmake -S /src/media -B /build/audiod -G Ninja -DCMAKE_BUILD_TYPE=Release \
    -DCADENCE_MEDIA_AUDIO_BACKEND=alsa -DCADENCE_MEDIA_STATIC=ON -DCADENCE_MEDIA_URL=OFF \
    -DCADENCE_MEDIA_AIRPLAY=uxplay -DCADENCE_AIRPLAY_DNSSD_AVAHI=ON -DCADENCE_MEDIA_SOLOIST=OFF \
    -DCADENCE_MEDIA_SIM=OFF -DCADENCE_MEDIA_AO_RING=ON -DCADENCE_SCREEN=ON -DCADENCE_MEDIA_TESTS=OFF \
    "-DCADENCE_AUDIOD_VERSION=$VERSION" "-DCADENCE_TRACING=$CADENCE_TRACING" >/tmp/audiod-configure.log 2>&1 \
    || { tail -60 /tmp/audiod-configure.log; exit 1; }; \
  cmake --build /build/audiod --target cadence-audiod >/tmp/audiod-build.log 2>&1 \
    || { grep -E 'error:|FAILED:|undefined' /tmp/audiod-build.log | head -150; exit 1; }; \
  /build/audiod/cadence-audiod --version | grep -qxF "cadence-audiod $VERSION"
RUN set -e; cmake -S /src/screen -B /build/screen -G Ninja -DCMAKE_BUILD_TYPE=Release \
    -DCADENCE_SCREEN_TESTS=OFF -DCADENCE_SCREEN_AO=cadence "-DCADENCE_SCREEN_VERSION=$VERSION" \
    -DCADENCE_SCREEN_PROJECTM=ON -DCMAKE_PREFIX_PATH=/opt/projectm -DCMAKE_BUILD_WITH_INSTALL_RPATH=ON \
    '-DCMAKE_INSTALL_RPATH=$ORIGIN/../lib' \
    >/tmp/screen-configure.log 2>&1 || { tail -60 /tmp/screen-configure.log; exit 1; }; \
  cmake --build /build/screen --target cadence-screen >/tmp/screen-build.log 2>&1 \
    || { grep -E 'error:|FAILED:|undefined' /tmp/screen-build.log | head -150; exit 1; }; \
  readelf -dW /build/screen/cadence-screen | grep -qF 'Shared library: [libprojectM-4.so.4]'; \
  readelf -dW /build/screen/cadence-screen | grep -qF '[$ORIGIN/../lib]'; \
  mkdir -p /out/licenses/alac /out/licenses/uxplay /out/licenses/android-airplay-server \
    /out/licenses/nanopb /out/licenses/nlohmann-json /out/licenses/opus /out/licenses/speexdsp /out/licenses/gcc; \
  cp /build/audiod/alac-*/LICENSE /out/licenses/alac/; \
  cp /src/media/third_party/uxplay/LICENSE /out/licenses/uxplay/; \
  cp /src/media/third_party/android-airplay-server/LICENSE /out/licenses/android-airplay-server/; \
  cp /src/media/third_party/nanopb/LICENSE.txt /out/licenses/nanopb/; \
  cp /src/media/third_party/nlohmann/LICENSE.MIT /out/licenses/nlohmann-json/; \
  cp /usr/share/doc/libopus-dev/copyright /out/licenses/opus/copyright; \
  cp /usr/share/doc/libspeexdsp-dev/copyright /out/licenses/speexdsp/copyright; \
  cp /usr/share/doc/gcc-14-base/copyright /out/licenses/gcc/copyright

FROM native AS package
ARG DISTRO=rpi
ARG ARCH=arm64
ARG VERSION=dev
RUN set -e; [ "$DISTRO" = rpi ]; [ "$ARCH" = arm64 ]; [ "$(dpkg --print-architecture)" = arm64 ]; \
  mkdir -p /pkg/opt/cadence/bin /pkg/opt/cadence/lib /pkg/opt/cadence/share/ui /pkg/etc/systemd/system /out; \
  install -m755 /build/audiod/cadence-audiod /pkg/opt/cadence/bin/; \
  install -m755 /build/screen/cadence-screen /pkg/opt/cadence/bin/; \
  cp -R /build/screen/ui/gen/rcss /pkg/opt/cadence/share/ui/; \
  cp -R /src/screen/ui/fonts /pkg/opt/cadence/share/ui/; \
  find /pkg/opt/cadence/share/ui/fonts -type f ! -name '*.ttf' ! -name '*OFL.txt' ! -name '*LICENSE.txt' -delete; \
  cp -P /out/lib/* /pkg/opt/cadence/lib/; \
  cp -R /out/share/viz /pkg/opt/cadence/share/; \
  cp -R /out/licenses /pkg/opt/cadence/
COPY --from=cadenced /out/bin/cadenced /pkg/opt/cadence/bin/cadenced
COPY --from=cadenced /out/licenses /pkg/opt/cadence/licenses
COPY deploy/rpi/cadence.service /pkg/etc/systemd/system/cadence.service
COPY deploy/rpi/cadence-screen.service /pkg/etc/systemd/system/cadence-screen.service
COPY --chmod=755 deploy/rpi/postinst /pkg/DEBIAN/postinst
COPY deploy/rpi/licenses.txt /pkg/opt/cadence/licenses/README
COPY --from=mpv deb-0.40-arm64 /mpv
RUN set -e; cd /mpv; sha256sum --check --strict SHA256SUMS; \
  test "$(find . -maxdepth 1 -name 'libmpv2_*+cadence1_arm64.deb' | wc -l)" = 1; \
  cd /pkg/opt/cadence; \
  { printf '# cadence %s\n# board rpi\n# arch arm64\n# components cadenced cadence-audiod cadence-screen libairplay libprojectM-4\n' "$VERSION"; \
    find bin lib share licenses -type f | LC_ALL=C sort | xargs sha256sum; } >MANIFEST; \
  readelf -hW bin/cadenced bin/cadence-audiod bin/cadence-screen | grep -q 'Machine:.*AArch64'; \
  mkdir -p /pkg/DEBIAN /tmp/shlibs/debian; \
  printf 'Source: cadence-rpi\nPackage: cadence-rpi\nArchitecture: arm64\n' >/tmp/shlibs/debian/control; \
  pm=$(find /pkg/opt/cadence/lib -name 'libprojectM-4.so.4.*' -type f); \
  deps=$(cd /tmp/shlibs && dpkg-shlibdeps -O -l/pkg/opt/cadence/lib --ignore-missing-info \
    -e/pkg/opt/cadence/bin/cadence-audiod -e/pkg/opt/cadence/bin/cadence-screen "-e$pm" \
    | sed -n 's/^shlibs:Depends=//p' | tr ',' '\n' | sed 's/^ *//; s/ *$//' \
    | { grep -v -e '^libmpv2' -e '^$' || true; } | paste -sd, -); \
  test -n "$deps" || { echo 'shlibdeps produced no dependencies' >&2; exit 1; }; \
  { echo 'Package: cadence-rpi'; echo "Version: 2.0.0+$(printf '%s' "$VERSION" | sed 's/[^0-9A-Za-z.+~]/-/g')"; \
    echo 'Section: sound'; echo 'Priority: optional'; echo 'Architecture: arm64'; \
    echo 'Maintainer: Cadence <cadence@localhost>'; \
    printf 'Depends: %s, %s\n' "$deps" 'avahi-daemon, bluez, bluez-alsa-utils, ca-certificates, dbus, fonts-noto-cjk, fonts-noto-color-emoji, libasound2-plugin-bluez, libmpv2 (>= 0.40.0-3+deb13u1+cadence1)'; \
    echo 'Description: Cadence screen, HDMI-CEC and audio on a Raspberry Pi'; \
    echo ' Cadence screen on HDMI through SDL KMS/DRM, audiod on HDMI or Bluetooth speakers.'; \
  } >/pkg/DEBIAN/control; \
  dpkg-deb --root-owner-group --build "/pkg" "/out/cadence-$VERSION-rpi-arm64.deb"; \
  dpkg-deb -f "/out/cadence-$VERSION-rpi-arm64.deb" Depends | grep -F 'libmpv2 (>= 0.40.0-3+deb13u1+cadence1)'; \
  dpkg-deb -f "/out/cadence-$VERSION-rpi-arm64.deb" Depends | grep -q libsdl3; \
  rm -rf /tmp/checked; mkdir /tmp/checked; dpkg-deb -x "/out/cadence-$VERSION-rpi-arm64.deb" /tmp/checked; \
  test -x /tmp/checked/opt/cadence/bin/cadence-screen; \
  test -f /tmp/checked/opt/cadence/share/ui/rcss/tokens.rcss; \
  test -f /tmp/checked/opt/cadence/share/ui/fonts/Inter-Regular.ttf; \
  test -L /tmp/checked/opt/cadence/lib/libprojectM-4.so.4; \
  test -f /tmp/checked/opt/cadence/share/viz/milkdrop/presets.txt; \
  test "$(find /tmp/checked/opt/cadence/share/viz/milkdrop -name '*.milk' | wc -l)" -ge 60; \
  test -f /tmp/checked/opt/cadence/licenses/projectm/COPYING; \
  grep -q 'CADENCE_SCREEN_MILKDROP=/opt/cadence/share/viz/milkdrop' /tmp/checked/etc/systemd/system/cadence-screen.service; \
  test -f /tmp/checked/etc/systemd/system/cadence-screen.service; \
  ! grep -q '^\[Install\]' /tmp/checked/etc/systemd/system/cadence-screen.service; \
  dpkg-deb -I "/out/cadence-$VERSION-rpi-arm64.deb" postinst | grep -q 'graphical.target.wants/cadence-screen.service'; \
  (cd /tmp/checked/opt/cadence && sha256sum --check --strict --quiet MANIFEST); \
  ls -l /out

FROM scratch AS export
COPY --from=package /out/ /
