#!/usr/bin/env bash
# builds libairplay (media/third_party/libairplay, uxplay.cmake) for one target into <out>/<target>: lib/libairplay.a,
# the avahi backend on linux, libplist's and openssl's static libraries on android, include/airplay/ and
# bin/airplay-test, then checks the archive's and the test's machine. runs inside libairplay.Dockerfile's stages:
#   linux-amd64, linux-arm64   debian trixie's gcc (the other cpu's through its cross gcc and multiarch -dev packages),
#                              openssl and libplist from trixie, avahi-client backend; the test runs where the cpu is
#                              the build machine's
#   android-armeabi-v7a        ndk r29 clang, api $ANDROID_API, openssl's libcrypto.a and libplist from their pinned
#                              tarballs (prefix deps); the test is built, never run (no device, rule 7)
# usage: libairplay.sh <target> <media-dir> <out>
set -euo pipefail

die() { echo "libairplay: $*" >&2; exit 1; }
[ $# -eq 3 ] || die "usage: libairplay.sh <target> <media-dir> <out>"
target=$1 media=$2 out=$3
src=$media/third_party/libairplay
build=/tmp/libairplay-$target
prefix=$out/$target
rm -rf "$build" "$prefix"

case $target in
  linux-amd64) triple=x86_64-linux-gnu machine='Advanced Micro Devices X86-64' ;;
  linux-arm64) triple=aarch64-linux-gnu machine=AArch64 ;;
  android-armeabi-v7a) triple=arm-linux-androideabi machine=ARM ;;
  *) die "unknown target $target" ;;
esac

common=(-G Ninja -DCMAKE_BUILD_TYPE=Release "-DCMAKE_INSTALL_PREFIX=$prefix" -DCMAKE_C_FLAGS=-g0)
case $target in
  linux-*)
    native=$(gcc -dumpmachine)
    args=(-DCADENCE_AIRPLAY_DEPS=system -DCADENCE_AIRPLAY_DNSSD_AVAHI=ON)
    if [ "$native" != "$triple" ]; then
      args+=(-DCMAKE_SYSTEM_NAME=Linux "-DCMAKE_SYSTEM_PROCESSOR=${triple%%-*}" "-DCMAKE_C_COMPILER=$triple-gcc"
        "-DCMAKE_LIBRARY_ARCHITECTURE=$triple" -DCMAKE_CROSSCOMPILING_EMULATOR=/bin/false)
      export PKG_CONFIG_LIBDIR=/usr/lib/$triple/pkgconfig:/usr/share/pkgconfig
    fi
    ;;
  android-*)
    : "${ANDROID_NDK_ROOT:?}" "${ANDROID_API:?}" "${OPENSSL_PREFIX:?}" "${PLIST_SOURCE:?}" "${PLIST_VERSION:?}"
    args=(-DCMAKE_TOOLCHAIN_FILE="$ANDROID_NDK_ROOT/build/cmake/android.toolchain.cmake" -DANDROID_ABI=armeabi-v7a
      "-DANDROID_PLATFORM=android-$ANDROID_API" -DCADENCE_AIRPLAY_DEPS=prefix
      "-DCADENCE_AIRPLAY_OPENSSL_ROOT=$OPENSSL_PREFIX" "-DCADENCE_AIRPLAY_PLIST_SOURCE=$PLIST_SOURCE"
      "-DCADENCE_AIRPLAY_PLIST_VERSION=$PLIST_VERSION")
    ;;
esac

cmake -S "$src" -B "$build" "${common[@]}" "${args[@]}"
cmake --build "$build"
cmake --install "$build" >/dev/null
case $target in
  android-*)
    mkdir -p "$prefix/lib"
    cp "$OPENSSL_PREFIX/lib/libcrypto.a" "$prefix/lib/libcrypto.a"
    readelf=$ANDROID_NDK_ROOT/toolchains/llvm/prebuilt/linux-x86_64/bin/llvm-readelf
    ar=$ANDROID_NDK_ROOT/toolchains/llvm/prebuilt/linux-x86_64/bin/llvm-ar
    ;;
  *) readelf=readelf ar=ar ;;
esac

# every object in the archive, and the test, for the target's cpu
mkdir "$build/objects"
(cd "$build/objects" && "$ar" x "$prefix/lib/libairplay.a")
n=0
for o in "$build/objects"/*.o; do
  "$readelf" -h "$o" | grep -E "^ +Machine: +$machine *\$" >/dev/null || die "$(basename "$o") is not $machine"
  n=$((n + 1))
done
[ "$n" -ge 25 ] || die "libairplay.a holds $n objects"
"$readelf" -h "$prefix/bin/airplay-test" | grep -E "^ +Machine: +$machine *\$" >/dev/null || die "airplay-test is not $machine"
case $target in
  android-*)
    # bionic alone at run time: openssl and libplist are linked in
    needed=$("$readelf" -dW "$prefix/bin/airplay-test" | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p' | sort | tr '\n' ' ')
    [ "$needed" = "libc.so libdl.so libm.so " ] || die "airplay-test needs $needed"
    "$readelf" -lW "$prefix/bin/airplay-test" | grep -F '[Requesting program interpreter: /system/bin/linker]' >/dev/null \
      || die "airplay-test does not run with /system/bin/linker"
    # e_flags 0x05000000 and up: the arm eabi version 5 (llvm-readelf prints the number alone)
    "$readelf" -hW "$prefix/bin/airplay-test" | grep -E 'Flags: +0x5[0-9a-f]{6}$' >/dev/null || die "airplay-test is not EABI5"
    ;;
esac

if [ "$target" = "linux-$(dpkg --print-architecture)" ]; then
  # the avahi backend's client needs a system bus: a private one, with no avahi-daemon on it (it waits for one)
  bus=$(dbus-daemon --session --fork --print-address=1 --print-pid=3 3>"$build/dbus.pid")
  DBUS_SYSTEM_BUS_ADDRESS=$bus ctest --test-dir "$build" --output-on-failure || { kill "$(cat "$build/dbus.pid")"; exit 1; }
  kill "$(cat "$build/dbus.pid")"
fi
echo "libairplay $target: $(stat -c %s "$prefix/lib/libairplay.a") bytes, $n objects"
