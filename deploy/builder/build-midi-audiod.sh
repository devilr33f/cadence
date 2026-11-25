#!/bin/sh
# build-midi-audiod.sh deps | audiod <out> <version>: midi's audiod and everything it loads from bin/lib, cross-built
# with zig-armhf.sh for midi's glibc 2.30 (device.Dockerfile's midi-cross stage, on the build machine's own cpu). no
# debian release with a c++20 gcc has a glibc that old, and a binary linked against a newer one needs its symbols.
# deps: armhf prefixes from bookworm's armhf packages (libopus and speexdsp static; libasound as an unversioned stub,
# see below), openssl 3.0 built from its release tarball into /opt/openssl, ffmpeg through build-ffmpeg.sh into
# /opt/ffmpeg. audiod: cadence-audiod from /src/media, then <out> as device.Dockerfile's audiod-yandex-midi stage takes
# it: cadence-audiod, lib/ (ffmpeg's and openssl's libraries under their sonames), licenses/ (openssl's, and llvm's
# libc++, libc++abi and libunwind with zig's own, which zig links in) and openssl-version. CADENCE_TRACING (ON or OFF,
# default ON) switches audiod's trace marks
set -eu

OPENSSL_VERSION=3.0.22
OPENSSL_SHA256=67ebca7e50d17383028045486653492195b83db95f8558709701bb47b5c1ef81
triplet=/usr/lib/arm-linux-gnueabihf

deps() {
  mkdir -p /opt/armhf/include /opt/armhf/lib/pkgconfig /opt/armhf/compat/sys
  # only these headers: /usr/include itself holds the build machine's glibc headers, which must not shadow zig's
  cp -R /usr/include/opus /usr/include/speex /usr/include/alsa /opt/armhf/include/
  cp "$triplet/libopus.a" "$triplet/libspeexdsp.a" /opt/armhf/lib/
  for name in opus speexdsp; do
    sed -e 's|^prefix=.*|prefix=/opt/armhf|' -e 's|^libdir=.*|libdir=${prefix}/lib|' \
      -e 's|^includedir=.*|includedir=${prefix}/include|' "$triplet/pkgconfig/$name.pc" >"/opt/armhf/lib/pkgconfig/$name.pc"
  done
  # audiod loads midi's own libasound, which has no symbol versions; linked against bookworm's, it would ask for
  # ALSA_0.9 versions (the loader warns at every start) and bookworm's glibc 2.34 behind them. so it links against a
  # stub of the same functions, unversioned, that is never shipped
  nm -D --defined-only "$triplet/libasound.so.2" | awk '$2 == "T" || $2 == "W" { sub(/@.*/, "", $3); print $3 }' |
    sort -u | sed 's/.*/void &(void) {}/' >/tmp/asound-stub.c
  armhf-cc -shared -nostdlib -Wl,-soname,libasound.so.2 -o /opt/armhf/lib/libasound.so /tmp/asound-stub.c
  rm /tmp/asound-stub.c
  # glibc 2.32 dropped this header while 2.30's stubs still have sysctl, so ffmpeg's configure finds the function and
  # libavutil includes the header; on linux it reads only bsd macros (HW_NCPU) from it, which glibc never defined
  printf '/* stands in for the header glibc 2.32 dropped: see build-midi-audiod.sh */\n' >/opt/armhf/compat/sys/sysctl.h

  work=/tmp/build-openssl
  rm -rf "$work"
  mkdir "$work"
  curl --fail --silent --show-error --location --retry 5 --retry-all-errors --output "$work/openssl.tar.gz" \
    "https://github.com/openssl/openssl/releases/download/openssl-$OPENSSL_VERSION/openssl-$OPENSSL_VERSION.tar.gz"
  echo "$OPENSSL_SHA256  $work/openssl.tar.gz" | sha256sum --check --strict -
  tar -xzf "$work/openssl.tar.gz" -C "$work"
  (
    cd "$work/openssl-$OPENSSL_VERSION"
    CC=armhf-cc AR=armhf-ar RANLIB=armhf-ranlib ./Configure linux-armv4 --prefix=/opt/openssl --libdir=lib shared \
      no-tests
    make -j"$(nproc)"
    make install_sw
    mkdir -p /opt/openssl/share/licenses
    cp LICENSE.txt /opt/openssl/share/licenses/Apache-2.0
  )
  rm -rf "$work"
  echo "$OPENSSL_VERSION" >/opt/openssl/share/licenses/version

  PKG_CONFIG_LIBDIR=/opt/openssl/lib/pkgconfig PKG_CONFIG_PATH='' sh /w/build-ffmpeg.sh /opt/ffmpeg \
    --enable-cross-compile --arch=arm --target-os=linux --cc=armhf-cc --cxx=armhf-c++ --ar=armhf-ar \
    --ranlib=armhf-ranlib --nm=nm --strip=strip --pkg-config=pkg-config --host-cc='zig cc' \
    --extra-cflags=-I/opt/armhf/compat
}

audiod() {
  out=${1:?usage: build-midi-audiod.sh audiod <out> <version>}
  version=${2:?usage: build-midi-audiod.sh audiod <out> <version>}
  # the loader searches a DT_RPATH, never a DT_RUNPATH, for what the stock library audiod dlopens needs itself
  # (M2m-R4); lld writes a DT_RUNPATH whatever the flags, so patchelf makes it a DT_RPATH
  runpath='$ORIGIN/lib:/system/vendor/quasar/libs'
  PKG_CONFIG_LIBDIR=/opt/armhf/lib/pkgconfig:/opt/ffmpeg/lib/pkgconfig PKG_CONFIG_PATH='' \
    cmake -S /src/media -B /build -G Ninja -DCMAKE_TOOLCHAIN_FILE=/w/zig-armhf.cmake -DCMAKE_BUILD_TYPE=Release \
    -DCADENCE_MEDIA_ALSA=ON -DCADENCE_MEDIA_STATIC=ON -DCADENCE_MEDIA_URL=ON -DCADENCE_MEDIA_AIRPLAY=ON \
    -DCADENCE_MEDIA_SOLOIST_JAIL=ON -DCADENCE_MEDIA_SIM=OFF -DCADENCE_MEDIA_TESTS=OFF \
    "-DCADENCE_AUDIOD_VERSION=$version" "-DCADENCE_AUDIOD_RUNPATH=$(echo "$runpath" | tr : ';')" \
    "-DCADENCE_TRACING=${CADENCE_TRACING:-ON}"
  cmake --build /build --target cadence-audiod
  mkdir -p "$out/lib" "$out/licenses/openssl" "$out/licenses/llvm"
  install -m 755 /build/cadence-audiod "$out/cadence-audiod"
  # zig's libc++ carries its debug info into the link (binutils-multiarch's strip reads arm)
  strip "$out/cadence-audiod"
  patchelf --force-rpath --set-rpath "$runpath" "$out/cadence-audiod"
  for name in avformat avcodec avutil swresample; do
    soname=$(readelf -d "/opt/ffmpeg/lib/lib$name.so" | sed -n 's/.*Library soname: \[\(.*\)\]/\1/p')
    install -m 755 "/opt/ffmpeg/lib/$soname" "$out/lib/$soname"
  done
  for soname in libssl.so.3 libcrypto.so.3; do
    install -m 755 "/opt/openssl/lib/$soname" "$out/lib/$soname"
    strip --strip-unneeded "$out/lib/$soname"
    patchelf --set-rpath '$ORIGIN' "$out/lib/$soname"
  done
  install -m 644 /opt/openssl/share/licenses/Apache-2.0 "$out/licenses/openssl/Apache-2.0"
  cp /opt/openssl/share/licenses/version "$out/openssl-version"
  printf '%s\n' "OpenSSL $OPENSSL_VERSION, built unmodified from its release tarball openssl-$OPENSSL_VERSION.tar.gz" \
    "(sha256 $OPENSSL_SHA256) as libssl.so.3 and libcrypto.so.3, whose RUNPATH patchelf set to \$ORIGIN." \
    "Copyright The OpenSSL Project Authors (AUTHORS.md in the tarball). Licensed under the Apache License 2.0, its text in" \
    "Apache-2.0 beside this file." >"$out/licenses/openssl/copyright"
  zig=$(dirname "$(readlink -f "$(command -v zig)")")
  install -m 644 "$zig/LICENSE" "$out/licenses/llvm/zig-LICENSE"
  for part in libcxx libcxxabi libunwind; do
    install -D -m 644 "$zig/lib/$part/LICENSE.TXT" "$out/licenses/llvm/$part/LICENSE.TXT"
  done
}

case ${1:-} in
  deps) deps ;;
  audiod)
    shift
    audiod "$@"
    ;;
  *)
    echo "usage: build-midi-audiod.sh deps | audiod <out> <version>" >&2
    exit 2
    ;;
esac
