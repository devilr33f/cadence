# the android-tv apk (make dist DISTRO=android-tv, deploy/builder/dist.sh): every stage linux/amd64, as the ndk's and
# the sdk's host tools are x86_64 linux alone (emulated on an arm64 build machine). its inputs:
#   named contexts  mpv         build/mpv: android-armeabi-v7a/{lib/libmpv.so,include/mpv} (make mpv, ao_cadence)
#                   libairplay  build/libairplay (make libairplay): unused here, as audiod builds libairplay from
#                               media/third_party/uxplay itself; dist.sh still names it
#                   soloist     build/soloist: soloist's arm64 runtime (prebuilt.sh ensure soloist-runtime-arm64),
#                               an asset the app unpacks into files/cadence/soloist/runtime, where audiod runs
#                               spotify's arm64 build through its loader beside the armv7 app
#   build args      DISTRO, ARCH (armeabi-v7a alone so far), VERSION
#   secret          android_keystore: the debug key, kept outside the repo (dist.sh makes one at
#                   ~/.config/cadence/android-debug.keystore unless CADENCE_ANDROID_KEYSTORE names another)
# everything fetched is checked by hash: the ndk (sha-256 and google's sha-1), the sdk's cmdline-tools (the same), sdk
# packages by sdkmanager against google's index, gradle, sdl3's android zip, the jdtech libmpv aar (its ffmpeg and
# libc++_shared), opus and speexdsp's release tarballs (the sums xiph publishes), openssl and libplist (as
# libairplay.Dockerfile pins them), freetype (the same sum from savannah and sourceforge) and rmlui (as
# screen/docker/test.Dockerfile pins it), apple's alac decoder (media/CMakeLists.txt pins it); apt comes from the
# pinned snapshot.
# projectM (libprojectM-4, lgpl-2.1, shared: lib/armeabi-v7a/libprojectM-4.so beside libmain.so) is built from
# media/third_party/projectm's pins (github archives checked by sha-256, its fbo backport applied), and the milkdrop
# presets it shows are fetched at build time into assets/viz/milkdrop, which UiAssets unpacks to files/viz/milkdrop
# (never committed, rule 5).
# the export stage is the apk alone, cadence-<VERSION>-<DISTRO>-<ARCH>.apk
ARG SNAPSHOT=20261104T212339Z

# ndk r29, as libairplay.Dockerfile has it
FROM --platform=linux/amd64 debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS ndk
ARG SNAPSHOT
RUN set -e; grep -q "snapshot.debian.org/archive/debian/$SNAPSHOT" /etc/apt/sources.list.d/debian.sources; \
  sed -i "s|^URIs: http://deb.debian.org/\(debian\(-security\)\?\)\$|URIs: http://snapshot.debian.org/archive/\1/$SNAPSHOT|" \
    /etc/apt/sources.list.d/debian.sources; \
  ! grep -q deb.debian.org /etc/apt/sources.list.d/debian.sources; \
  printf '%s\n' 'Acquire::Check-Valid-Until "false";' 'Acquire::Retries "5";' >/etc/apt/apt.conf.d/90snapshot; \
  apt-get update; \
  apt-get install --yes --no-install-recommends bzip2 ca-certificates cmake curl file g++ git make ninja-build perl \
    pkg-config unzip xz-utils; \
  rm -rf /var/lib/apt/lists/*
ARG NDK=r29
ARG NDK_SHA256=4abbbcdc842f3d4879206e9695d52709603e52dd68d3c1fff04b3b5e7a308ecf
ARG NDK_SHA1=87e2bb7e9be5d6a1c6cdf5ec40dd4e0c6d07c30b
RUN set -e; curl --fail --silent --show-error --location --retry 5 --retry-all-errors --output /tmp/ndk.zip \
    "https://dl.google.com/android/repository/android-ndk-$NDK-linux.zip"; \
  echo "$NDK_SHA256  /tmp/ndk.zip" | sha256sum --check --strict -; \
  echo "$NDK_SHA1  /tmp/ndk.zip" | sha1sum --check --strict -; \
  unzip -q /tmp/ndk.zip -d /opt; rm /tmp/ndk.zip; mv "/opt/android-ndk-$NDK" /opt/ndk; \
  grep -qx 'Pkg.Revision = 29.0.14206865' /opt/ndk/source.properties
ENV ANDROID_NDK_ROOT=/opt/ndk
ENV PATH=/opt/ndk/toolchains/llvm/prebuilt/linux-x86_64/bin:$PATH

# the prebuilts: sdl3's android aar (SDLActivity's classes, libSDL3.so, its headers) and the jdtech libmpv aar, whose
# ffmpeg and libc++_shared ship beside our libmpv.so (owner decision 5); its own libmpv.so and jni wrapper do not
FROM ndk AS prebuilts
ARG SDL_SHA256=535320e58f3bd7235b3e15f940595cdeeb9b8c0aa6b3810ff127345e3567d4da
ARG MPV_AAR_SHA256=df146592480fc8418415a06b1f1a1d6318b0088e21f52254b0e9a82b61ca8fa2
RUN set -e; mkdir -p /prebuilts /tmp/p; cd /tmp/p; \
  curl --fail --silent --show-error --location --retry 5 --retry-all-errors --output sdl.zip \
    https://github.com/libsdl-org/SDL/releases/download/release-3.4.20/SDL3-devel-3.4.20-android.zip; \
  echo "$SDL_SHA256  sdl.zip" | sha256sum --check --strict -; \
  curl --fail --silent --show-error --location --retry 5 --retry-all-errors --output mpv.aar \
    https://repo1.maven.org/maven2/dev/jdtech/mpv/libmpv/1.0.0/libmpv-1.0.0.aar; \
  echo "$MPV_AAR_SHA256  mpv.aar" | sha256sum --check --strict -; \
  unzip -q sdl.zip SDL3-3.4.20.aar LICENSE.txt; mkdir /prebuilts/sdl3; unzip -q SDL3-3.4.20.aar -d /prebuilts/sdl3; \
  cp LICENSE.txt /prebuilts/sdl3/LICENSE.txt; \
  mkdir /prebuilts/mpv-aar; unzip -q mpv.aar 'jni/armeabi-v7a/*' -d /prebuilts/mpv-aar; \
  rm -rf /tmp/p; ls /prebuilts/mpv-aar/jni/armeabi-v7a

# opus and speexdsp for armeabi-v7a, static, from their release tarballs (the plan's vcpkg baseline is not pinned yet)
FROM ndk AS deps
ARG ANDROID_API=30
ARG OPUS_SHA256=65c1d2f78b9f2fb20082c38cbe47c951ad5839345876e46941612ee87f9a7ce1
ARG SPEEXDSP_SHA256=8c777343e4a6399569c72abc38a95b24db56882c83dbdb6c6424a5f4aeb54d3d
RUN set -e; cd /tmp; \
  curl --fail --silent --show-error --location --retry 5 --retry-all-errors --output opus.tar.gz \
    https://downloads.xiph.org/releases/opus/opus-1.5.2.tar.gz; \
  echo "$OPUS_SHA256  opus.tar.gz" | sha256sum --check --strict -; \
  curl --fail --silent --show-error --location --retry 5 --retry-all-errors --output speexdsp.tar.gz \
    https://downloads.xiph.org/releases/speex/speexdsp-1.2.1.tar.gz; \
  echo "$SPEEXDSP_SHA256  speexdsp.tar.gz" | sha256sum --check --strict -; \
  tar -xzf opus.tar.gz; tar -xzf speexdsp.tar.gz; \
  export CC=armv7a-linux-androideabi$ANDROID_API-clang AR=llvm-ar RANLIB=llvm-ranlib CFLAGS='-O2 -fPIC -g0'; \
  for d in opus-1.5.2 speexdsp-1.2.1; do \
    (cd "$d" && ./configure --host=armv7a-linux-androideabi --prefix=/deps --disable-shared --enable-static \
        --disable-doc --disable-extra-programs --disable-examples >"/tmp/$d.log" 2>&1 \
      && make -j"$(nproc)" >>"/tmp/$d.log" 2>&1 && make install >>"/tmp/$d.log" 2>&1) \
      || { tail -40 "/tmp/$d.log"; exit 1; }; \
  done; \
  mkdir -p /deps/licenses/opus /deps/licenses/speexdsp; \
  cp opus-1.5.2/COPYING /deps/licenses/opus/; cp speexdsp-1.2.1/COPYING /deps/licenses/speexdsp/; \
  rm -rf /tmp/opus* /tmp/speexdsp*; ls /deps/lib
# libairplay's openssl (libcrypto.a) and libplist (its sources, compiled by media/third_party/uxplay.cmake), as
# libairplay.Dockerfile's android-deps stage builds them
ARG OPENSSL_VERSION=3.5.9
ARG OPENSSL_SHA256=603f5602e2eef00d77fbd429d34dcd5822bb301757a1bc9cdb24c670f1eb859a
ARG PLIST_VERSION=2.6.0
ARG PLIST_SHA256=67be9ee3169366589c92dc7c22809b90f51911dd9de22520c39c9a64fb047c9c
RUN set -e; cd /tmp; \
  curl --fail --silent --show-error --location --retry 5 --retry-all-errors --output openssl.tar.gz \
    "https://github.com/openssl/openssl/releases/download/openssl-$OPENSSL_VERSION/openssl-$OPENSSL_VERSION.tar.gz"; \
  echo "$OPENSSL_SHA256  openssl.tar.gz" | sha256sum --check --strict -; \
  curl --fail --silent --show-error --location --retry 5 --retry-all-errors --output libplist.tar.bz2 \
    "https://github.com/libimobiledevice/libplist/releases/download/$PLIST_VERSION/libplist-$PLIST_VERSION.tar.bz2"; \
  echo "$PLIST_SHA256  libplist.tar.bz2" | sha256sum --check --strict -; \
  tar -xjf libplist.tar.bz2 -C /deps; mv "/deps/libplist-$PLIST_VERSION" /deps/libplist; \
  mkdir -p /deps/licenses/libplist /deps/licenses/openssl; cp /deps/libplist/COPYING /deps/licenses/libplist/; \
  tar -xzf openssl.tar.gz; cd "openssl-$OPENSSL_VERSION"; \
  ./Configure android-arm "-D__ANDROID_API__=$ANDROID_API" no-shared no-tests no-apps no-docs no-module \
    --prefix=/deps/openssl --libdir=lib >/tmp/openssl-configure.log 2>&1 || { tail -40 /tmp/openssl-configure.log; exit 1; }; \
  make -j"$(nproc)" build_libs >/tmp/openssl-make.log 2>&1 || { tail -40 /tmp/openssl-make.log; exit 1; }; \
  mkdir -p /deps/openssl/lib; cp -R include /deps/openssl/; cp libcrypto.a /deps/openssl/lib/; \
  cp LICENSE.txt /deps/licenses/openssl/; \
  grep -qF "OPENSSL_VERSION_TEXT \"OpenSSL $OPENSSL_VERSION " /deps/openssl/include/openssl/opensslv.h; \
  cd /tmp; rm -rf openssl* libplist.tar.bz2
# the screen's ui: freetype (no zlib, bzip2, png, harfbuzz or brotli: rmlui hands it plain ttf) and rmlui's core
# library, static, as screen/docker/test.Dockerfile builds them for linux
ARG FREETYPE_VERSION=2.13.3
ARG FREETYPE_SHA256=0550350666d427c74daeb85d5ac7bb353acba5f76956395995311a9c6f063289
ARG RMLUI_VERSION=6.3
ARG RMLUI_SHA256=d977298bb6147610e5984d5db85ddf284020d655a8713913f6982074f1dbdede
RUN set -e; cd /tmp; \
  curl --fail --silent --show-error --location --retry 5 --retry-all-errors --output freetype.tar.xz \
    "https://download.savannah.gnu.org/releases/freetype/freetype-$FREETYPE_VERSION.tar.xz"; \
  echo "$FREETYPE_SHA256  freetype.tar.xz" | sha256sum --check --strict -; \
  curl --fail --silent --show-error --location --retry 5 --retry-all-errors --output rmlui.tar.gz \
    "https://github.com/mikke89/RmlUi/archive/refs/tags/$RMLUI_VERSION.tar.gz"; \
  echo "$RMLUI_SHA256  rmlui.tar.gz" | sha256sum --check --strict -; \
  tar -xJf freetype.tar.xz; tar -xzf rmlui.tar.gz; \
  ndk="-DCMAKE_TOOLCHAIN_FILE=/opt/ndk/build/cmake/android.toolchain.cmake -DANDROID_ABI=armeabi-v7a \
    -DANDROID_PLATFORM=android-$ANDROID_API -DANDROID_STL=c++_shared -DCMAKE_BUILD_TYPE=Release \
    -DBUILD_SHARED_LIBS=OFF -DCMAKE_POSITION_INDEPENDENT_CODE=ON -DCMAKE_INSTALL_PREFIX=/deps -DCMAKE_FIND_ROOT_PATH=/deps"; \
  cmake -S "freetype-$FREETYPE_VERSION" -B ft -G Ninja $ndk -DFT_DISABLE_ZLIB=ON -DFT_DISABLE_BZIP2=ON \
    -DFT_DISABLE_PNG=ON -DFT_DISABLE_HARFBUZZ=ON -DFT_DISABLE_BROTLI=ON >/tmp/ft.log 2>&1 \
    && cmake --build ft >>/tmp/ft.log 2>&1 && cmake --install ft >>/tmp/ft.log 2>&1 || { tail -40 /tmp/ft.log; exit 1; }; \
  cmake -S "RmlUi-$RMLUI_VERSION" -B rml -G Ninja $ndk -DRMLUI_SAMPLES=OFF -DRMLUI_FONT_ENGINE=freetype \
    -DRMLUI_PRECOMPILED_HEADERS=OFF >/tmp/rml.log 2>&1 \
    && cmake --build rml >>/tmp/rml.log 2>&1 && cmake --install rml >>/tmp/rml.log 2>&1 || { tail -40 /tmp/rml.log; exit 1; }; \
  mkdir -p /deps/licenses/freetype /deps/licenses/rmlui; \
  cp "freetype-$FREETYPE_VERSION/LICENSE.TXT" "freetype-$FREETYPE_VERSION/docs/FTL.TXT" /deps/licenses/freetype/; \
  cp "RmlUi-$RMLUI_VERSION/LICENSE.txt" /deps/licenses/rmlui/; \
  rm -rf /tmp/freetype* /tmp/rmlui* /tmp/RmlUi-* /tmp/ft /tmp/rml; ls /deps/lib/cmake

# libprojectM-4 for armeabi-v7a, shared against the app's c++_shared, gles 3 (its cmake forces gles on android), and
# the presets the screen ships: presets.candidates until the owner curates on the stick
# (media/third_party/projectm/README.md); at run time the host skips one over its 20 ms budget and remembers it
FROM ndk AS projectm
ARG ANDROID_API=30
RUN set -e; apt-get update; apt-get install --yes --no-install-recommends patch; rm -rf /var/lib/apt/lists/*
COPY media/third_party/projectm /recipe
RUN set -e; sh /recipe/fetch.sh /tmp/pm >/dev/null; \
  sh /recipe/build.sh /tmp/pm/src /tmp/pm/build /projectm \
    -DCMAKE_TOOLCHAIN_FILE=/opt/ndk/build/cmake/android.toolchain.cmake -DANDROID_ABI=armeabi-v7a \
    "-DANDROID_PLATFORM=android-$ANDROID_API" -DANDROID_STL=c++_shared >/tmp/projectm.log 2>&1 \
    || { tail -60 /tmp/projectm.log; exit 1; }; \
  llvm-readelf -dW /projectm/lib/libprojectM-4.so | grep -qF 'Library soname: [libprojectM-4.so]'; \
  llvm-readelf -dW /projectm/lib/libprojectM-4.so | grep -qF 'Shared library: [libGLESv3.so]'; \
  mkdir -p /out/licenses/projectm /out/licenses/projectm-eval /out/licenses/milkdrop-presets /out/viz/milkdrop; \
  cp /tmp/pm/src/COPYING /tmp/pm/src/LICENSE.txt /tmp/pm/src/AUTHORS.txt /out/licenses/projectm/; \
  cp /tmp/pm/src/vendor/projectm-eval/LICENSE.md /out/licenses/projectm-eval/; \
  sh /recipe/fetch-presets.sh /out/viz/milkdrop /recipe/presets.candidates /tmp/pm-dl; \
  cp /recipe/presets.candidates /out/viz/milkdrop/presets.txt; \
  mv /out/viz/milkdrop/LICENSE.md /out/licenses/milkdrop-presets/LICENSE.md; \
  rm -rf /tmp/pm /tmp/pm-dl /tmp/projectm.log

# cadenced for android (S2): GOOS=android with cgo through the ndk's clang at api 30, so names resolve through bionic
# and the system's CA pool is android's; the notices of everything linked in, as device.Dockerfile collects them
FROM --platform=linux/amd64 golang:1.27-trixie@sha256:af1bfc2614ebb1dbd31af514b78740fd678850e76498762b425ecee4c5c6ea80 AS cadenced
ARG VERSION=dev
COPY --from=ndk /opt/ndk /opt/ndk
WORKDIR /src
COPY go.work go.work.su[m] ./
COPY pkg/ pkg/
COPY server/ server/
COPY device/ device/
# go's module and build caches stay in the builder between builds: a source change rebuilds what it touched alone
RUN --mount=type=cache,target=/go/pkg/mod,id=cadence-gomod,sharing=locked \
  --mount=type=cache,target=/root/.cache/go-build,id=cadence-android-gobuild,sharing=locked \
  set -e; cd device; \
  CC=/opt/ndk/toolchains/llvm/prebuilt/linux-x86_64/bin/armv7a-linux-androideabi30-clang \
  CGO_ENABLED=1 GOOS=android GOARCH=arm GOARM=7 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/libcadenced.so ./cmd/cadenced; \
  go version -m /out/libcadenced.so | grep -qx "$(printf '\tbuild\tGOOS=android')"; \
  licenses=/out/licenses/go; goroot=$(go env GOROOT); mkdir -p "$licenses"; \
  cp "$goroot/LICENSE" "$goroot/PATENTS" "$licenses/"; \
  GOOS=android GOARCH=arm CGO_ENABLED=1 go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Dir}}{{end}}{{end}}' \
    ./cmd/cadenced | sort -u | while read -r path dir; do \
    mkdir -p "$licenses/$path"; \
    find "$dir" -maxdepth 1 -type f \( -name 'LICEN[CS]E*' -o -name 'COPYING*' -o -name 'NOTICE*' -o -name PATENTS \) \
      -exec cp {} "$licenses/$path/" \; ; \
    [ -n "$(ls "$licenses/$path")" ] || { echo "$path has no license file" >&2; exit 1; }; \
  done

# the native libraries (android/native): cadence-screen as libmain.so, the daemon's libcadence-daemon.so
FROM ndk AS native
ARG VERSION=dev
# audiod's trace marks (media/CMakeLists.txt, CADENCE_TRACING): on unless a build passes OFF
ARG CADENCE_TRACING=ON
COPY --from=prebuilts /prebuilts /prebuilts
COPY --from=deps /deps /deps
COPY --from=projectm /projectm /projectm
COPY --from=mpv android-armeabi-v7a/lib/libmpv.so /deps/mpv/lib/libmpv.so
COPY --from=mpv android-armeabi-v7a/include/mpv /deps/mpv/include/mpv
COPY --from=mpv android-armeabi-v7a/licenses /deps/licenses/mpv-build
# (before the sources, so a source change never fetches it again) stb_image (public domain), as the linux screen takes it from debian's libstb-dev: its headers alone, for the ndk
RUN set -e; apt-get update; apt-get install --yes --no-install-recommends libstb-dev; rm -rf /var/lib/apt/lists/*; \
  mkdir -p /deps/stb/include; cp -R /usr/include/stb /deps/stb/include/; [ -f /deps/stb/include/stb/stb_image.h ]
COPY media /w/media
COPY screen /w/screen
COPY deploy/builder/apply-patches.sh /w/deploy/builder/apply-patches.sh
COPY android/native /w/android/native
# the ui's tokens-gen for this machine: the ndk's build cannot run what it compiles
RUN g++ -std=c++20 -O1 -isystem /w/media/third_party -o /usr/local/bin/tokens-gen /w/screen/ui/tokens/tokens-gen.cpp
# the build tree stays in the builder between builds (ninja rebuilds what the sources changed, the sources' paths
# and mtimes being the same each time); locked, so two builds never share it at once
RUN --mount=type=cache,target=/cache/native,id=cadence-android-native,sharing=locked \
  set -e; mkdir -p /deps/mpv/pkgconfig; \
  printf '%s\n' 'prefix=/deps/mpv' 'Name: mpv' 'Description: mpv 0.41.0 with ao_cadence (make mpv)' 'Version: 0.41.0' \
    'Libs: -L${prefix}/lib -lmpv' 'Cflags: -I${prefix}/include' >/deps/mpv/pkgconfig/mpv.pc; \
  export PKG_CONFIG_LIBDIR=/deps/lib/pkgconfig:/deps/mpv/pkgconfig; \
  cmake -S /w/android/native -B /cache/native -G Ninja -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_TOOLCHAIN_FILE=/opt/ndk/build/cmake/android.toolchain.cmake -DANDROID_ABI=armeabi-v7a \
    -DANDROID_PLATFORM=android-30 -DANDROID_STL=c++_shared "-DCMAKE_FIND_ROOT_PATH=/deps;/prebuilts;/projectm" \
    -DCADENCE_SCREEN_PROJECTM=ON \
    -DSDL3_DIR=/w/android/native/cmake/SDL3 -DCADENCE_SDL3_AAR=/prebuilts/sdl3 \
    -DCADENCE_SCREEN_TOKENS_GEN=/usr/local/bin/tokens-gen -DCADENCE_STB_INCLUDE=/deps/stb/include \
    -DCADENCE_AIRPLAY_OPENSSL_ROOT=/deps/openssl \
    -DCADENCE_AIRPLAY_PLIST_SOURCE=/deps/libplist -DCADENCE_AIRPLAY_PLIST_VERSION=2.6.0 \
    "-DCADENCE_VERSION=$VERSION" "-DCADENCE_TRACING=$CADENCE_TRACING" >/tmp/cmake-native.log 2>&1 \
    || { tail -60 /tmp/cmake-native.log; exit 1; }; \
  cmake --build /cache/native --target main cadence-daemon -- -k 0 >/tmp/build-native.log 2>&1 \
    || { grep -E 'error:|FAILED:|undefined' /tmp/build-native.log | head -150; exit 1; }; \
  mkdir -p /out/armeabi-v7a; \
  llvm-strip --strip-unneeded -o /out/armeabi-v7a/libmain.so /cache/native/libmain.so; \
  llvm-strip --strip-unneeded -o /out/armeabi-v7a/libcadence-daemon.so /cache/native/libcadence-daemon.so; \
  cp /deps/mpv/lib/libmpv.so /prebuilts/sdl3/prefab/modules/SDL3-shared/libs/android.armeabi-v7a/libSDL3.so \
    /out/armeabi-v7a/; \
  llvm-strip --strip-unneeded -o /out/armeabi-v7a/libprojectM-4.so /projectm/lib/libprojectM-4.so; \
  llvm-readelf -dW /out/armeabi-v7a/libmain.so | grep -qF 'Shared library: [libprojectM-4.so]'; \
  for l in avcodec avdevice avfilter avformat avutil swresample swscale; do \
    cp "/prebuilts/mpv-aar/jni/armeabi-v7a/lib$l.so" /out/armeabi-v7a/; \
  done; \
  cp /opt/ndk/toolchains/llvm/prebuilt/linux-x86_64/sysroot/usr/lib/arm-linux-androideabi/libc++_shared.so \
    /out/armeabi-v7a/; \
  llvm-readelf -d /out/armeabi-v7a/libcadence-daemon.so | grep -q 'NEEDED.*libmediandk.so'; \
  llvm-nm -D --defined-only /out/armeabi-v7a/libmain.so | grep -q ' JNI_OnLoad$'; \
  mkdir -p /out/ui/rcss /out/ui/fonts /out/licenses/alac /out/licenses/fonts; \
  cp /cache/native/screen/ui/gen/rcss/*.rcss /out/ui/rcss/; cp /w/screen/ui/fonts/*.ttf /out/ui/fonts/; \
  cp /w/screen/ui/fonts/*OFL.txt /w/screen/ui/fonts/*LICENSE.txt /out/licenses/fonts/; \
  cp /cache/native/alac-*/LICENSE /out/licenses/alac/; \
  mkdir -p /out/licenses/uxplay /out/licenses/android-airplay-server /out/licenses/nanopb; \
  cp /w/media/third_party/uxplay/LICENSE /out/licenses/uxplay/; \
  cp /w/media/third_party/android-airplay-server/LICENSE /out/licenses/android-airplay-server/; \
  cp /w/media/third_party/nanopb/LICENSE.txt /out/licenses/nanopb/; \
  [ -f /out/ui/rcss/tokens.rcss ] && [ -f /out/ui/fonts/Inter-Regular.ttf ]

# every library's needs met inside the apk or by the platform's public libraries, each built for 32-bit arm
FROM native AS checked
COPY --from=cadenced /out/libcadenced.so /out/armeabi-v7a/libcadenced.so
COPY deploy/android/check-libs.sh /w/check-libs.sh
RUN sh /w/check-libs.sh /out/armeabi-v7a

# the sdk, gradle and a jdk (cmdline-tools 19.0, the last classic sdkmanager, as spike s1 pinned them)
FROM --platform=linux/amd64 eclipse-temurin:17-jdk-noble@sha256:1bedb04434bd275ef072355e9caec2a22be5240fbe82ad51a59323f2bbcab4a6 AS sdk
ARG CMDLINE_TOOLS=commandlinetools-linux-13114758_latest.zip
ARG CMDLINE_TOOLS_SHA256=7ec965280a073311c339e571cd5de778b9975026cfcbe79f2b1cdcb1e15317ee
ARG CMDLINE_TOOLS_SHA1=5fdcc763663eefb86a5b8879697aa6088b041e70
ARG GRADLE=8.14.7
ARG GRADLE_SHA256=903bfc4bc7622323124054c10f509246212a1902948bdaf26c379cca8735e856
ENV ANDROID_HOME=/opt/android-sdk
RUN set -eu; apt-get update; apt-get install -y --no-install-recommends unzip; rm -rf /var/lib/apt/lists/*; cd /tmp; \
  curl -fsSL -o ct.zip "https://dl.google.com/android/repository/${CMDLINE_TOOLS}"; \
  echo "${CMDLINE_TOOLS_SHA256}  ct.zip" | sha256sum -c -; echo "${CMDLINE_TOOLS_SHA1}  ct.zip" | sha1sum -c -; \
  mkdir -p "$ANDROID_HOME/cmdline-tools"; unzip -q ct.zip -d "$ANDROID_HOME/cmdline-tools"; \
  mv "$ANDROID_HOME/cmdline-tools/cmdline-tools" "$ANDROID_HOME/cmdline-tools/latest"; rm ct.zip; \
  yes | "$ANDROID_HOME/cmdline-tools/latest/bin/sdkmanager" --licenses >/dev/null; \
  "$ANDROID_HOME/cmdline-tools/latest/bin/sdkmanager" --install "platforms;android-36" "build-tools;36.0.0" platform-tools >/tmp/sdk.log; \
  tail -2 /tmp/sdk.log; rm -rf /tmp/sdk.log "$ANDROID_HOME/.temp"; \
  curl -fsSL -o g.zip "https://services.gradle.org/distributions/gradle-${GRADLE}-bin.zip"; \
  echo "${GRADLE_SHA256}  g.zip" | sha256sum -c -; unzip -q g.zip -d /opt; rm g.zip; \
  ln -s "/opt/gradle-${GRADLE}/bin/gradle" /usr/local/bin/gradle
ENV GRADLE_OPTS=-Dorg.gradle.daemon=false GRADLE_USER_HOME=/gradle

# the apk: gradle compiles the java and packages what the stages above built, signed with the debug key
FROM sdk AS apk
ARG DISTRO=android-tv
ARG ARCH=armeabi-v7a
ARG VERSION=dev
COPY android /w/android
COPY --from=checked /out/armeabi-v7a /in/jniLibs/armeabi-v7a
COPY --from=native /out/ui /in/assets/ui
COPY --from=native /out/licenses /in/assets/licenses
COPY --from=projectm /out/viz /in/assets/viz
COPY --from=projectm /out/licenses /in/assets/licenses
# soloist's arm64 runtime (the speakers' tarballs ship the same one), as an asset tree with the manifest the app
# stamps its unpacked copy by; the arm64 loader is checked as the machine soloist needs on the 64-bit kernel
COPY --from=soloist /soloist-runtime-arm64.tar /in/soloist-runtime-arm64.tar
COPY --from=soloist /soloist-runtime-arm64.tar.sha256 /in/soloist-runtime-arm64.tar.sha256
RUN set -eu; cd /in; sha256sum --check --strict soloist-runtime-arm64.tar.sha256; \
  mkdir -p assets/soloist-runtime; tar -xf soloist-runtime-arm64.tar -C assets/soloist-runtime; \
  readelf -hW assets/soloist-runtime/lib/ld-linux-aarch64.so.1 | grep -qE '^ +Machine: +AArch64$'; \
  (cd assets/soloist-runtime && find . -type f | LC_ALL=C sort | xargs sha256sum) >assets/soloist-runtime/MANIFEST; \
  rm soloist-runtime-arm64.tar soloist-runtime-arm64.tar.sha256
COPY --from=prebuilts /prebuilts/sdl3/classes.jar /in/sdl3-classes.jar
COPY --from=prebuilts /prebuilts/sdl3/LICENSE.txt /in/assets/licenses/sdl3/LICENSE.txt
COPY --from=deps /deps/licenses /in/assets/licenses
COPY --from=native /deps/licenses/mpv-build /in/assets/licenses/mpv
COPY --from=cadenced /out/licenses /in/assets/licenses
RUN --mount=type=secret,id=android_keystore,required=true --mount=type=cache,target=/gradle,id=cadence-android-gradle \
  set -eu; [ "$ARCH" = armeabi-v7a ] || { echo "android: $ARCH is not built yet" >&2; exit 1; }; \
  cp /run/secrets/android_keystore /tmp/debug.keystore; \
  { echo "distro $DISTRO"; echo "version $VERSION"; echo "arch $ARCH"; \
    (cd /in/jniLibs/armeabi-v7a && sha256sum *.so); \
    (cd /in/assets && find ui viz -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum); } >/in/assets/MANIFEST; \
  code=$(date -u +%s); code=$((code / 60 - 29000000)); \
  cd /w/android; \
  gradle --no-daemon --no-watch-fs --console=plain -q assembleRelease -Pcadence.jniLibs=/in/jniLibs \
    -Pcadence.sdlJar=/in/sdl3-classes.jar -Pcadence.assets=/in/assets -Pcadence.keystore=/tmp/debug.keystore \
    "-Pcadence.version=$VERSION" "-Pcadence.versionCode=$code"; \
  mkdir /dist; cp app/build/outputs/apk/release/app-release.apk "/dist/cadence-$VERSION-$DISTRO-$ARCH.apk"; \
  rm /tmp/debug.keystore; \
  bt=$ANDROID_HOME/build-tools/36.0.0; apk="/dist/cadence-$VERSION-$DISTRO-$ARCH.apk"; \
  "$bt/apksigner" verify "$apk"; \
  "$bt/aapt2" dump badging "$apk" | grep -E "^(package|minSdkVersion|targetSdkVersion|native-code|leanback-launchable)"; \
  [ "$(unzip -l "$apk" | grep -c 'lib/armeabi-v7a/libc++_shared.so')" = 1 ]; \
  ! unzip -l "$apk" | grep -E ' lib/' | grep -v ' lib/armeabi-v7a/'; \
  unzip -l "$apk" | grep -q ' assets/ui/rcss/tokens.rcss$'; unzip -l "$apk" | grep -q ' assets/ui/fonts/CadenceIcons.ttf$'; \
  unzip -l "$apk" | grep -q ' assets/soloist-runtime/MANIFEST$'; \
  unzip -l "$apk" | grep -q ' lib/armeabi-v7a/libprojectM-4.so$'; \
  unzip -l "$apk" | grep -q ' assets/viz/milkdrop/presets.txt$'; \
  [ "$(unzip -l "$apk" | grep -c ' assets/viz/milkdrop/.*\.milk$')" -ge 60 ]; \
  unzip -l "$apk" | grep -q ' assets/licenses/projectm/COPYING$'; \
  unzip -l "$apk" | grep -q ' assets/soloist-runtime/lib/ld-linux-aarch64.so.1$'; \
  ls -l "$apk"

FROM scratch AS export
COPY --from=apk /dist/ /
