#!/bin/sh
# the apk's native libraries (deploy/android/Dockerfile's checked stage): each a 32-bit arm elf, every NEEDED found
# among them or in the platform's public ndk libraries, and libcadenced.so an executable run by /system/bin/linker
# (S2: exec'd from the native library dir)
# usage: check-libs.sh <dir>
set -eu
dir=$1
readelf=llvm-readelf
public=" libc.so libm.so libdl.so liblog.so libandroid.so libEGL.so libGLESv2.so libaaudio.so \
libGLESv1_CM.so libGLESv3.so libOpenSLES.so libmediandk.so libz.so libvulkan.so libjnigraphics.so libnativewindow.so libsync.so "
fail() { echo "check-libs: $*" >&2; exit 1; }
n=0
for f in "$dir"/*.so; do
  name=$(basename "$f")
  "$readelf" -hW "$f" | grep -Eq '^ +Class: +ELF32$' || fail "$name is not 32-bit"
  "$readelf" -hW "$f" | grep -Eq '^ +Machine: +ARM$' || fail "$name is not arm"
  for need in $("$readelf" -dW "$f" | sed -n 's/.*(NEEDED).*\[\(.*\)\]/\1/p'); do
    [ -f "$dir/$need" ] && continue
    case $public in *" $need "*) continue ;; esac
    fail "$name needs $need, which neither the apk nor the platform has"
  done
  n=$((n + 1))
done
"$readelf" -lW "$dir/libcadenced.so" | grep -qF '[Requesting program interpreter: /system/bin/linker]' \
  || fail "libcadenced.so is not run by /system/bin/linker"
"$readelf" -hW "$dir/libcadenced.so" | grep -Eq 'Type: +DYN' || fail "libcadenced.so is not a pie"
for want in libmain.so libSDL3.so libmpv.so libcadence-daemon.so libc++_shared.so libavcodec.so; do
  [ -f "$dir/$want" ] || fail "no $want"
done
echo "check-libs: $n libraries ok"
