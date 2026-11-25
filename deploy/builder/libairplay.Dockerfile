# libairplay (media/third_party/uxplay.cmake): uxplay's lib/ with android-airplay-server's patches applied out of tree
# (deploy/builder/apply-patches.sh) and cadence's dnssd, static, for the screens' three targets
# (deploy/builder/libairplay.sh): linux amd64 and arm64 on trixie's packages, and android armeabi-v7a with ndk r29
# against openssl's libcrypto.a and libplist, built here from their pinned release tarballs. make media-test builds
# the test stage (each target built and checked, the test run on the build machine's cpu); the export stage is
# build/libairplay/<target>/ for the screens' builds. apt comes from the snapshot the base image was made from (plain
# http: apt checks the signed InRelease and every hash)
ARG SNAPSHOT=20261104T212339Z

# linux: the build machine's cpu natively, the other through trixie's cross gcc and that cpu's -dev packages
FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS linux
ARG SNAPSHOT
RUN set -e; grep -q "snapshot.debian.org/archive/debian/$SNAPSHOT" /etc/apt/sources.list.d/debian.sources; \
  sed -i "s|^URIs: http://deb.debian.org/\(debian\(-security\)\?\)\$|URIs: http://snapshot.debian.org/archive/\1/$SNAPSHOT|" \
    /etc/apt/sources.list.d/debian.sources; \
  ! grep -q deb.debian.org /etc/apt/sources.list.d/debian.sources; \
  printf '%s\n' 'Acquire::Check-Valid-Until "false";' 'Acquire::Retries "5";' >/etc/apt/apt.conf.d/90snapshot; \
  case $(dpkg --print-architecture) in \
    amd64) other=arm64 cross=gcc-aarch64-linux-gnu ;; \
    arm64) other=amd64 cross=gcc-x86-64-linux-gnu ;; \
    *) echo "libairplay builds on amd64 or arm64, not $(dpkg --print-architecture)" >&2; exit 1 ;; \
  esac; \
  dpkg --add-architecture "$other"; \
  apt-get update; \
  apt-get install --yes --no-install-recommends binutils-multiarch ca-certificates cmake dbus-daemon gcc git libc6-dev make \
    ninja-build pkg-config "$cross" libssl-dev libplist-dev libavahi-client-dev \
    "libc6-dev:$other" "libssl-dev:$other" "libplist-dev:$other" "libavahi-client-dev:$other"; \
  rm -rf /var/lib/apt/lists/*
COPY media/third_party/uxplay /w/media/third_party/uxplay
COPY media/third_party/android-airplay-server/app/src/main/cpp/patches \
  /w/media/third_party/android-airplay-server/app/src/main/cpp/patches
COPY media/third_party/libairplay /w/media/third_party/libairplay
COPY media/third_party/uxplay.cmake /w/media/third_party/uxplay.cmake
COPY deploy/builder/apply-patches.sh deploy/builder/libairplay.sh /w/deploy/builder/
RUN bash /w/deploy/builder/libairplay.sh linux-amd64 /w/media /out \
  && bash /w/deploy/builder/libairplay.sh linux-arm64 /w/media /out

# android: the ndk's host tools are x86_64 linux alone, so this stage is amd64 (emulated on an arm64 build machine).
# ndk r29 (29.0.14206865) by sha-256, and by the sha-1 google's repository2-3.xml lists for it; openssl 3.5.9 (the
# hash equals the .sha256 published beside its release) and libplist 2.6.0, both as spike S3b linked them
FROM --platform=linux/amd64 debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS ndk
ARG SNAPSHOT
RUN set -e; grep -q "snapshot.debian.org/archive/debian/$SNAPSHOT" /etc/apt/sources.list.d/debian.sources; \
  sed -i "s|^URIs: http://deb.debian.org/\(debian\(-security\)\?\)\$|URIs: http://snapshot.debian.org/archive/\1/$SNAPSHOT|" \
    /etc/apt/sources.list.d/debian.sources; \
  ! grep -q deb.debian.org /etc/apt/sources.list.d/debian.sources; \
  printf '%s\n' 'Acquire::Check-Valid-Until "false";' 'Acquire::Retries "5";' >/etc/apt/apt.conf.d/90snapshot; \
  apt-get update; \
  apt-get install --yes --no-install-recommends bzip2 ca-certificates cmake curl git make ninja-build perl \
    pkg-config unzip; \
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

FROM ndk AS android-deps
ARG ANDROID_API=30
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
  mkdir -p /deps; tar -xjf libplist.tar.bz2 -C /deps; mv "/deps/libplist-$PLIST_VERSION" /deps/libplist; \
  tar -xzf openssl.tar.gz; cd "openssl-$OPENSSL_VERSION"; \
  ./Configure android-arm "-D__ANDROID_API__=$ANDROID_API" no-shared no-tests no-apps no-docs no-module \
    --prefix=/deps/openssl --libdir=lib >/tmp/openssl-configure.log 2>&1 || { tail -40 /tmp/openssl-configure.log; exit 1; }; \
  make -j"$(nproc)" build_libs >/tmp/openssl-make.log 2>&1 || { tail -40 /tmp/openssl-make.log; exit 1; }; \
  mkdir -p /deps/openssl/lib; cp -R include /deps/openssl/; cp libcrypto.a /deps/openssl/lib/; \
  cp LICENSE.txt /deps/openssl/LICENSE.txt; cp /deps/libplist/COPYING /deps/libplist-COPYING 2>/dev/null || true; \
  grep -qF "OPENSSL_VERSION_TEXT \"OpenSSL $OPENSSL_VERSION " /deps/openssl/include/openssl/opensslv.h; \
  cd /tmp; rm -rf openssl* libplist.tar.bz2

FROM android-deps AS android
ARG ANDROID_API=30
ARG PLIST_VERSION=2.6.0
COPY media/third_party/uxplay /w/media/third_party/uxplay
COPY media/third_party/android-airplay-server/app/src/main/cpp/patches \
  /w/media/third_party/android-airplay-server/app/src/main/cpp/patches
COPY media/third_party/libairplay /w/media/third_party/libairplay
COPY media/third_party/uxplay.cmake /w/media/third_party/uxplay.cmake
COPY deploy/builder/apply-patches.sh deploy/builder/libairplay.sh /w/deploy/builder/
RUN ANDROID_API=$ANDROID_API OPENSSL_PREFIX=/deps/openssl PLIST_SOURCE=/deps/libplist PLIST_VERSION=$PLIST_VERSION \
  bash /w/deploy/builder/libairplay.sh android-armeabi-v7a /w/media /out

# audiod with its libairplay side (TV stream J: audiod/airplay-uxplay-*, AMediaCodec for aac, apple's alac decoder
# fetched pinned) for android armeabi-v7a, as the android-tv preset has it: compiled and linked as cadence-audiod, its
# needs checked, never run. opus and speexdsp from their release tarballs, as deploy/android/Dockerfile builds them
FROM android-deps AS android-audiod
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
  rm -rf /tmp/opus* /tmp/speexdsp*
COPY media /w/media
COPY deploy/builder/apply-patches.sh /w/deploy/builder/apply-patches.sh
RUN set -e; export PKG_CONFIG_LIBDIR=/deps/lib/pkgconfig; \
  cmake -S /w/media -B /tmp/audiod -G Ninja -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_TOOLCHAIN_FILE=/opt/ndk/build/cmake/android.toolchain.cmake -DANDROID_ABI=armeabi-v7a \
    -DANDROID_PLATFORM=android-$ANDROID_API -DANDROID_STL=c++_shared -DCMAKE_FIND_ROOT_PATH=/deps \
    -DCADENCE_MEDIA_TESTS=OFF -DCADENCE_MEDIA_AUDIO_BACKEND=aaudio -DCADENCE_MEDIA_AIRPLAY=uxplay \
    -DCADENCE_AIRPLAY_DEPS=prefix -DCADENCE_AIRPLAY_OPENSSL_ROOT=/deps/openssl \
    -DCADENCE_AIRPLAY_PLIST_SOURCE=/deps/libplist -DCADENCE_AIRPLAY_PLIST_VERSION=2.6.0 \
    -DCADENCE_MEDIA_URL=OFF -DCADENCE_MEDIA_SOLOIST=OFF -DCADENCE_SCREEN=ON -DCADENCE_MEDIA_SIM=OFF \
    >/tmp/audiod-configure.log 2>&1 || { tail -60 /tmp/audiod-configure.log; exit 1; }; \
  cmake --build /tmp/audiod --target cadence-audiod cadence_audiod_embedded; \
  llvm-readelf -d /tmp/audiod/cadence-audiod | grep NEEDED; \
  llvm-readelf -d /tmp/audiod/cadence-audiod | grep -q 'NEEDED.*libmediandk.so'; \
  llvm-readelf -h /tmp/audiod/cadence-audiod | grep -q 'Machine:.*ARM$'; \
  ! llvm-nm -D --undefined-only /tmp/audiod/cadence-audiod | grep -E ' (av_|avcodec_|plist_|AVAHI|avahi_)'; \
  mkdir -p /out/android-armeabi-v7a; \
  llvm-strip --strip-unneeded -o /out/android-armeabi-v7a/cadence-audiod-uxplay /tmp/audiod/cadence-audiod

# make media-test's gate: every target built and checked
FROM scratch AS test
COPY --from=linux /out/ /libairplay/
COPY --from=android /out/ /libairplay/
COPY --from=android-audiod /out/ /audiod/

FROM scratch AS export
COPY --from=test /libairplay/ /
