#!/usr/bin/env bash
# tests of check-device-limits.sh, run natively by make device-limits-test (in make ci): small elf files built here for
# the build machine's cpu stand in for pickle's, each tree breaking one limit. needs gcc, g++ and binutils on a glibc of
# 2.38 or newer, whose strlcpy is GLIBC_2.38 and arc4random GLIBC_2.36. the libraries a device provides (libssl, libm and an
# unexpected one) are stubs built here, with the symbol versions each case needs.
# midi's trees (M2m-D6) are armhf: their audiod built as device.Dockerfile builds midi's, with zig for midi's glibc
# 2.30 (and patchelf for a DT_RPATH), or with arm-linux-gnueabihf-gcc on its glibc of 2.38 or newer for one midi
# refuses; their libraries are stubs built with that gcc. their cadenced is go's own static arm v7 program
# (GO_ARM_ELF), and pickle's arm64 one (GO_ARM64_ELF) is the aarch64 file a midi tree refuses.
# the vk capsula mini's trees are aarch64, their audiod and libraries built with zig for glibc 2.28 as
# build-vk-audiod.sh builds the board's, their cadenced GO_ARM64_ELF
# shellcheck disable=SC2016 # $ORIGIN is the linker's, never the shell's
set -euo pipefail
export LC_ALL=C

: "${GO_ARM_ELF:?names the static go program for arm v7}" "${GO_ARM64_ELF:?names the static go program for arm64}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script=$here/check-device-limits.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failures=0

pass() { echo "ok - $1"; }
flunk() {
  echo "not ok - $1" >&2
  failures=$((failures + 1))
}

src=$tmp/src
stubs=$tmp/stubs
mkdir -p "$src" "$stubs"

cat >"$src/ssl.c" <<'EOF'
int ssl_old(void) { return 1; }
int ssl_new(void) { return 2; }
EOF
cat >"$src/ssl.map" <<'EOF'
OPENSSL_3.0.0 { global: ssl_old; local: *; };
OPENSSL_3.2.0 { global: ssl_new; } OPENSSL_3.0.0;
EOF
gcc -shared -fPIC -Wl,-soname,libssl.so.3 -Wl,--version-script="$src/ssl.map" -o "$stubs/libssl.so.3" "$src/ssl.c"
echo 'int extra(void) { return 3; }' >"$src/extra.c"
gcc -shared -fPIC -Wl,-soname,libextra.so.1 -o "$stubs/libextra.so.1" "$src/extra.c"
# a libm whose one symbol is versioned GLIBC_PRIVATE: a glibc version that is no dotted number
echo 'int private_thing(void) { return 4; }' >"$src/private.c"
echo 'GLIBC_PRIVATE { global: private_thing; local: *; };' >"$src/private.map"
gcc -shared -fPIC -Wl,-soname,libm.so.6 -Wl,--version-script="$src/private.map" -o "$stubs/libm.so.6" "$src/private.c"

cat >"$src/cadenced.c" <<'EOF'
#include <stdio.h>
int main(void) { return puts("cadenced") < 0; }
EOF
gcc -static -o "$tmp/cadenced" "$src/cadenced.c"

cat >"$src/avformat.c" <<'EOF'
#include <string.h>
int av_stub(const char *name) {
#ifdef NEW_GLIBC
  char buffer[8];
  return (int)strlcpy(buffer, name, sizeof buffer);
#else
  return (int)strlen(name);
#endif
}
EOF
gcc -shared -fPIC -Wl,-soname,libavformat.so.63 -o "$tmp/libavformat.so.63" "$src/avformat.c"

# audiod: glibc up to GLIBC_2.36 (arc4random), libssl at OPENSSL_3.0.0 and ffmpeg from bin/lib; each case adds one more
cat >"$src/audiod.c" <<'EOF'
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
int av_stub(const char *name);
int ssl_old(void);
int ssl_new(void);
int extra(void);
int private_thing(void);
#ifdef GLIBC_2_29
int posix_spawn_file_actions_addchdir_np(void *actions, const char *path);
#endif
int main(int argc, char **argv) {
  int result = av_stub(argv[0]) + ssl_old() + argc;
#ifndef MIDI_GLIBC
  result += (int)(arc4random() % 2u);
#endif
#ifdef NEW_GLIBC
  char buffer[8];
  result += (int)strlcpy(buffer, argv[0], sizeof buffer);
#endif
#ifdef NEW_SSL
  result += ssl_new();
#endif
#ifdef EXTRA
  result += extra();
#endif
#ifdef PRIVATE_GLIBC
  result += private_thing();
#endif
#ifdef GLIBC_2_29
  result += posix_spawn_file_actions_addchdir_np(0, argv[0]);
#endif
  return printf("%d\n", result) < 0;
}
EOF
cat >"$src/audiod.cpp" <<'EOF'
#include <stdexcept>
#include <string>
extern "C" int av_stub(const char *name);
extern "C" int ssl_old(void);
int main(int argc, char **argv) {
  std::string name(argv[0]);
  if (argc > 5) throw std::runtime_error(name);
  return av_stub(name.c_str()) + ssl_old() == 0;
}
EOF

# tree NAME [COMPILER ARGS...]: a tree laid out as the tarball is, its audiod built from audiod.c with the arguments
tree() {
  local dir=$tmp/$1
  shift
  mkdir -p "$dir/bin/lib"
  cp "$tmp/cadenced" "$dir/bin/cadenced"
  cp "$tmp/libavformat.so.63" "$dir/bin/lib/libavformat.so.63"
  gcc -o "$dir/bin/cadence-audiod" "$src/audiod.c" "$dir/bin/lib/libavformat.so.63" "$stubs/libssl.so.3" "$@"
}

# accepted NAME MESSAGE [OPTIONS...]: the script passes the tree NAME and prints MESSAGE
accepted() {
  local name=$1 message=$2
  shift 2
  if ! bash "$script" "$@" "$tmp/$name" >"$tmp/$name.log" 2>&1; then
    cat "$tmp/$name.log" >&2
    flunk "$name: accepted"
  elif ! grep -qF -- "$message" "$tmp/$name.log"; then
    cat "$tmp/$name.log" >&2
    flunk "$name: accepted with $message"
  else
    pass "$name: accepted with $message"
  fi
}

# refused NAME MESSAGE [OPTIONS...]: the script fails on the tree NAME, naming MESSAGE
refused() {
  local name=$1 message=$2
  shift 2
  if bash "$script" "$@" "$tmp/$name" >"$tmp/$name.log" 2>&1; then
    cat "$tmp/$name.log" >&2
    flunk "$name: refused"
  elif ! grep -qF -- "$message" "$tmp/$name.log"; then
    cat "$tmp/$name.log" >&2
    flunk "$name: refused for $message"
  else
    pass "$name: refused for $message"
  fi
}

host_machine=$(readelf -hW "$tmp/cadenced" | sed -n 's/^ *Machine: *//p')

tree good -Wl,-rpath,'$ORIGIN/lib'
accepted good "bin/cadence-audiod: GLIBC_2.36, OPENSSL_3.0.0" --machine "$host_machine"
accepted good "bin/cadenced: static"
accepted good "bin/lib/libavformat.so.63: GLIBC_"

tree glibc -Wl,-rpath,'$ORIGIN/lib' -DNEW_GLIBC
refused glibc "bin/cadence-audiod: needs GLIBC_2.38, pickle has GLIBC_2.36"

tree private -Wl,-rpath,'$ORIGIN/lib' -DPRIVATE_GLIBC "$stubs/libm.so.6"
refused private "bin/cadence-audiod: needs GLIBC_PRIVATE, pickle has GLIBC_2.36"

tree glibcxx
g++ -o "$tmp/glibcxx/bin/cadence-audiod" "$src/audiod.cpp" "$tmp/glibcxx/bin/lib/libavformat.so.63" \
  "$stubs/libssl.so.3" -Wl,-rpath,'$ORIGIN/lib'
refused glibcxx "bin/cadence-audiod: needs GLIBCXX_"
refused glibcxx "bin/cadence-audiod: needs CXXABI_"

tree needed -Wl,-rpath,'$ORIGIN/lib' -DEXTRA "$stubs/libextra.so.1"
refused needed "bin/cadence-audiod: needs libextra.so.1, which pickle does not have"

tree openssl -Wl,-rpath,'$ORIGIN/lib' -DNEW_SSL
refused openssl "bin/cadence-audiod: needs OPENSSL_3.2.0, pickle has OPENSSL_3.0.0"

tree interp -Wl,-rpath,'$ORIGIN/lib'
gcc -o "$tmp/interp/bin/cadenced" "$src/cadenced.c"
refused interp "bin/cadenced: has PT_INTERP"

# a shared library has no PT_INTERP, yet needs libc
tree shared -Wl,-rpath,'$ORIGIN/lib'
cp "$tmp/libavformat.so.63" "$tmp/shared/bin/cadenced"
refused shared "bin/cadenced: needs shared libraries"

# readelf reports a cut-off file on stderr but may exit 0
tree truncated -Wl,-rpath,'$ORIGIN/lib'
head -c 300 "$tmp/cadenced" >"$tmp/truncated/bin/cadenced"
refused truncated "bin/cadenced: readelf cannot read it"

tree script -Wl,-rpath,'$ORIGIN/lib'
printf '#!/bin/sh\n' >"$tmp/script/bin/cadence-audiod"
refused script "bin/cadence-audiod: is not an elf file"

tree link -Wl,-rpath,'$ORIGIN/lib'
ln -s cadence-audiod "$tmp/link/bin/audiod"
refused link "bin/audiod: is not a regular file"

tree ffmpeg -Wl,-rpath,'$ORIGIN/lib'
gcc -shared -fPIC -Wl,-soname,libavformat.so.63 -DNEW_GLIBC -o "$tmp/ffmpeg/bin/lib/libavformat.so.63" \
  "$src/avformat.c"
refused ffmpeg "bin/lib/libavformat.so.63: needs GLIBC_2.38"

tree runpath
refused runpath "bin/cadence-audiod: needs libavformat.so.63, but no runpath reaches bin/lib"

tree absolute -Wl,-rpath,'/usr/local/lib:$ORIGIN/lib'
refused absolute "bin/cadence-audiod: runpath /usr/local/lib is outside \$ORIGIN"

tree missing -Wl,-rpath,'$ORIGIN/lib'
rm "$tmp/missing/bin/cadence-audiod"
refused missing "bin/cadence-audiod: is missing"

refused good "bin/cadenced: is for $host_machine, not NoSuchMachine" --machine NoSuchMachine

# pickle has neither librt nor libatomic
echo 'int atomic_stub(void) { return 6; }' >"$src/atomic.c"
gcc -shared -fPIC -Wl,-soname,libatomic.so.1 -o "$stubs/libatomic.so.1" "$src/atomic.c"
tree atomic -Wl,-rpath,'$ORIGIN/lib' -Wl,--no-as-needed "$stubs/libatomic.so.1"
refused atomic "bin/cadence-audiod: needs libatomic.so.1, which pickle does not have"

# the vendor's library directory midi's audiod searches (M2m-R4) is outside $ORIGIN for pickle, and only --vendor-libs
# lets it in
vendor=/system/vendor/quasar/libs
tree vendor -Wl,--disable-new-dtags -Wl,-rpath,"\$ORIGIN/lib:$vendor"
refused vendor "bin/cadence-audiod: runpath $vendor is outside \$ORIGIN"
accepted vendor "bin/cadence-audiod: GLIBC_2.36" --vendor-libs "$vendor"

# rpi: a pi's trixie (glibc 2.41, openssl 3.5, alsa and openssl from its apt), its loader named as the pi's whatever
# cpu builds the test
rpibt=(--board rpi)
pi=(-Wl,-rpath,'$ORIGIN/lib' -Wl,--dynamic-linker=/lib/ld-linux-aarch64.so.1)
tree pigood "${pi[@]}" -DNEW_GLIBC -DNEW_SSL
accepted pigood "bin/cadence-audiod: GLIBC_2.38, OPENSSL_3.2.0" "${rpibt[@]}"
# a glibc newer than trixie's, from a libm stub that versions its one symbol so
echo 'int private_thing(void) { return 4; }' >"$src/future.c"
echo 'GLIBC_2.99 { global: private_thing; local: *; };' >"$src/future.map"
mkdir -p "$stubs/future"
gcc -shared -fPIC -Wl,-soname,libm.so.6 -Wl,--version-script="$src/future.map" -o "$stubs/future/libm.so.6" \
  "$src/future.c"
tree piglibc "${pi[@]}" -DPRIVATE_GLIBC "$stubs/future/libm.so.6"
refused piglibc "bin/cadence-audiod: needs GLIBC_2.99, rpi has GLIBC_2.41" "${rpibt[@]}"
tree pineeded "${pi[@]}" -DEXTRA "$stubs/libextra.so.1"
refused pineeded "bin/cadence-audiod: needs libextra.so.1, which rpi does not have" "${rpibt[@]}"
tree piinterp -Wl,-rpath,'$ORIGIN/lib' -Wl,--dynamic-linker=/opt/other/ld.so
refused piinterp "bin/cadence-audiod: asks for the loader /opt/other/ld.so, rpi's is /lib/ld-linux-aarch64.so.1" \
  "${rpibt[@]}"
tree piglibcxx
g++ -o "$tmp/piglibcxx/bin/cadence-audiod" "$src/audiod.cpp" "$tmp/piglibcxx/bin/lib/libavformat.so.63" \
  "$stubs/libssl.so.3" "${pi[@]}"
refused piglibcxx "bin/cadence-audiod: needs GLIBCXX_" "${rpibt[@]}"
refused pigood "no limits for board nowhere" --board nowhere

# midi: armhf trees, checked as device.Dockerfile checks midi's
armcc=arm-linux-gnueabihf-gcc
# midi's glibc is 2.30 (its firmware's), where arc4random (2.36) is not
armzig=(zig cc -target arm-linux-gnueabihf.2.30 -mcpu=cortex_a7 -DMIDI_GLIBC)
midi=(--machine ARM --vendor-libs "$vendor")
armstubs=$tmp/armstubs
mkdir -p "$armstubs/old"
# bin/lib's openssl as bookworm's: libssl needs OPENSSL_3.0.3 of the libcrypto beside it, which defines it; the old
# libcrypto stops at OPENSSL_3.0.0
cat >"$src/crypto.c" <<'EOF'
int crypto_old(void) { return 1; }
int crypto_new(void) { return 2; }
EOF
cat >"$src/crypto.map" <<'EOF'
OPENSSL_3.0.0 { global: crypto_old; local: *; };
OPENSSL_3.0.3 { global: crypto_new; } OPENSSL_3.0.0;
EOF
echo 'OPENSSL_3.0.0 { global: crypto_old; crypto_new; local: *; };' >"$src/crypto-old.map"
"$armcc" -shared -fPIC -Wl,-soname,libcrypto.so.3 -Wl,--version-script="$src/crypto.map" \
  -o "$armstubs/libcrypto.so.3" "$src/crypto.c"
"$armcc" -shared -fPIC -Wl,-soname,libcrypto.so.3 -Wl,--version-script="$src/crypto-old.map" \
  -o "$armstubs/old/libcrypto.so.3" "$src/crypto.c"
cat >"$src/armssl.c" <<'EOF'
int crypto_new(void);
int ssl_old(void) { return crypto_new(); }
int ssl_new(void) { return 2; }
EOF
"$armcc" -shared -fPIC -Wl,-soname,libssl.so.3 -Wl,--version-script="$src/ssl.map" -Wl,-rpath,'$ORIGIN' \
  -o "$armstubs/libssl.so.3" "$src/armssl.c" "$armstubs/libcrypto.so.3"
"$armcc" -shared -fPIC -Wl,-soname,libextra.so.1 -o "$armstubs/libextra.so.1" "$src/extra.c"
"$armcc" -shared -fPIC -Wl,-soname,libatomic.so.1 -o "$armstubs/libatomic.so.1" "$src/atomic.c"
echo 'int asound_stub(void) { return 7; }' >"$src/asound.c"
"$armcc" -shared -fPIC -Wl,-soname,libasound.so.2 -o "$armstubs/libasound.so.2" "$src/asound.c"
"$armcc" -shared -fPIC -Wl,-soname,libavformat.so.63 -Wl,-rpath,'$ORIGIN' -o "$armstubs/libavformat.so.63" \
  "$src/avformat.c"

# armtree NAME [--gcc] [COMPILER ARGS...]: a midi tree, go's arm v7 cadenced and an armhf audiod built from audiod.c
# with the arguments, ffmpeg and openssl in bin/lib, alsa and libatomic from the device. zig builds the audiod, as
# midi's is built, unless --gcc; with -Wl,--disable-new-dtags its runpath becomes a DT_RPATH through patchelf, as
# build-midi-audiod.sh makes it, since lld writes a DT_RUNPATH whatever the flags
armtree() {
  local dir=$tmp/$1 audiod runpath
  local cc=("${armzig[@]}")
  shift
  if [ "${1:-}" = --gcc ]; then
    cc=("$armcc")
    shift
  fi
  mkdir -p "$dir/bin/lib"
  install -m 755 "$GO_ARM_ELF" "$dir/bin/cadenced"
  cp "$armstubs/libavformat.so.63" "$armstubs/libssl.so.3" "$armstubs/libcrypto.so.3" "$dir/bin/lib/"
  audiod=$dir/bin/cadence-audiod
  "${cc[@]}" -o "$audiod" "$src/audiod.c" "$dir/bin/lib/libavformat.so.63" "$dir/bin/lib/libssl.so.3" \
    -Wl,--no-as-needed "$armstubs/libasound.so.2" "$armstubs/libatomic.so.1" "$@"
  if [ "${cc[0]}" = zig ] && [[ " $* " == *" -Wl,--disable-new-dtags "* ]]; then
    runpath=$(readelf -dW "$audiod" | sed -n 's/.*(RUNPATH).*\[\(.*\)\]$/\1/p')
    [ -z "$runpath" ] || patchelf --force-rpath --set-rpath "$runpath" "$audiod"
  fi
}

# soft PATH: the elf32 file's e_flags (at 36) say soft-float, eabi version 5
soft() { printf '\000\002\000\005' | dd of="$1" bs=1 seek=36 conv=notrunc status=none; }

rpath=("-Wl,--disable-new-dtags" "-Wl,-rpath,\$ORIGIN/lib:$vendor")
armtree armgood "${rpath[@]}"
accepted armgood "bin/cadence-audiod: GLIBC_2.27, OPENSSL_3.0.0; needs libavformat.so.63 libssl.so.3 libasound.so.2 \
libatomic.so.1 libpthread.so.0 libc.so.6 ld-linux-armhf.so.3" "${midi[@]}"
accepted armgood "bin/cadenced: static" "${midi[@]}"
accepted armgood "bin/lib/libavformat.so.63: GLIBC_" "${midi[@]}"
accepted armgood "bin/lib/libssl.so.3: no GLIBC, OPENSSL_3.0.3; needs libcrypto.so.3" "${midi[@]}"

# bin/lib's libssl needs a version its libcrypto does not define
armtree armcrypto "${rpath[@]}"
cp "$armstubs/old/libcrypto.so.3" "$tmp/armcrypto/bin/lib/libcrypto.so.3"
refused armcrypto "bin/lib/libssl.so.3: needs OPENSSL_3.0.3, bin/lib/libcrypto.so.3 has OPENSSL_3.0.0" "${midi[@]}"

armtree armsoft "${rpath[@]}"
soft "$tmp/armsoft/bin/cadence-audiod"
refused armsoft "bin/cadence-audiod: is soft-float (e_flags 0x5000200), midi is hard-float" "${midi[@]}"

# go writes no float abi into a static program's e_flags, but one that says soft-float is refused all the same
armtree armsoftgo "${rpath[@]}"
soft "$tmp/armsoftgo/bin/cadenced"
refused armsoftgo "bin/cadenced: is soft-float (e_flags 0x5000200), midi is hard-float" "${midi[@]}"

# a library without the hard-float flag
armtree armnoflag "${rpath[@]}"
printf '\000\000\000\005' | dd of="$tmp/armnoflag/bin/lib/libavformat.so.63" bs=1 seek=36 conv=notrunc status=none
refused armnoflag "bin/lib/libavformat.so.63: is not hard-float (e_flags 0x5000000)" "${midi[@]}"

armtree armaarch64 "${rpath[@]}"
install -m 755 "$GO_ARM64_ELF" "$tmp/armaarch64/bin/cadenced"
refused armaarch64 "bin/cadenced: is for AArch64, not ARM" "${midi[@]}"

armtree armglibc --gcc "${rpath[@]}" -DNEW_GLIBC
refused armglibc "bin/cadence-audiod: needs GLIBC_2.38, midi has GLIBC_2.30" "${midi[@]}"

# regression: midi's first v2 audiod was built in bookworm, needed its glibc 2.36 and never started on a midi (2.30)
armtree armbookworm --gcc "${rpath[@]}"
refused armbookworm "bin/cadence-audiod: needs GLIBC_2.36, midi has GLIBC_2.30" "${midi[@]}"

armtree armneeded "${rpath[@]}" -DEXTRA "$armstubs/libextra.so.1"
refused armneeded "bin/cadence-audiod: needs libextra.so.1, which midi does not have" "${midi[@]}"

armtree armopenssl "${rpath[@]}" -DNEW_SSL
refused armopenssl "bin/cadence-audiod: needs OPENSSL_3.2.0, bin/lib has OPENSSL_3.0.0" "${midi[@]}"

# midi's openssl is the one bin/lib holds (M2m-R4), never the device's
armtree armssl "${rpath[@]}"
rm "$tmp/armssl/bin/lib/libssl.so.3"
refused armssl "bin/cadence-audiod: needs libssl.so.3, which bin/lib does not hold" "${midi[@]}"

# v1's private loader
armtree arminterp "${rpath[@]}" -Wl,--dynamic-linker=/data/cadence/runtime/lib/ld-linux-armhf.so.3
refused arminterp \
  "bin/cadence-audiod: asks for the loader /data/cadence/runtime/lib/ld-linux-armhf.so.3, midi's is \
/lib/ld-linux-armhf.so.3" \
  "${midi[@]}"

# the vendor directory as a DT_RUNPATH, which the loader never searches for what a dlopened library needs
armtree armrunpath -Wl,--enable-new-dtags -Wl,-rpath,"\$ORIGIN/lib:$vendor"
refused armrunpath "bin/cadence-audiod: runpath $vendor is a DT_RUNPATH" "${midi[@]}"

# the vendor directory ahead of bin/lib, where a vendor library of a name bin/lib holds would win
armtree armvendorfirst -Wl,--disable-new-dtags -Wl,-rpath,"$vendor:\$ORIGIN/lib"
refused armvendorfirst "bin/cadence-audiod: runpath $vendor comes before bin/lib" "${midi[@]}"

# the vendor directory in any runpath but audiod's
armtree armvendorlib "${rpath[@]}"
"$armcc" -shared -fPIC -Wl,-soname,libavformat.so.63 -Wl,--disable-new-dtags -Wl,-rpath,"\$ORIGIN:$vendor" \
  -o "$tmp/armvendorlib/bin/lib/libavformat.so.63" "$src/avformat.c"
refused armvendorlib "bin/lib/libavformat.so.63: runpath $vendor is outside \$ORIGIN" "${midi[@]}"

# vk capsula mini: aarch64 trees for glibc 2.28 (other firmwares of the board have it), checked as device.Dockerfile
# checks the board's; openssl 3.0 from bin/lib (the firmware's is 1.1), alsa from the device
vk=(--board vk-capsula-mini --machine AArch64)
vkzig=(zig cc -target aarch64-linux-gnu.2.28 -mcpu=cortex_a53)
vkstubs=$tmp/vkstubs
mkdir -p "$vkstubs"
"${vkzig[@]}" -shared -fPIC -Wl,-soname,libcrypto.so.3 -Wl,--version-script="$src/crypto.map" \
  -o "$vkstubs/libcrypto.so.3" "$src/crypto.c"
"${vkzig[@]}" -shared -fPIC -Wl,-soname,libssl.so.3 -Wl,--version-script="$src/ssl.map" -Wl,-rpath,'$ORIGIN' \
  -o "$vkstubs/libssl.so.3" "$src/armssl.c" "$vkstubs/libcrypto.so.3"
"${vkzig[@]}" -shared -fPIC -Wl,-soname,libasound.so.2 -o "$vkstubs/libasound.so.2" "$src/asound.c"
"${vkzig[@]}" -shared -fPIC -Wl,-soname,libavformat.so.63 -Wl,-rpath,'$ORIGIN' -o "$vkstubs/libavformat.so.63" \
  "$src/avformat.c"
# the firmware's own openssl 1.1
"${vkzig[@]}" -shared -fPIC -Wl,-soname,libssl.so.1.1 -o "$vkstubs/libssl.so.1.1" "$src/extra.c"

# vktree NAME [--glibc VERSION] [COMPILER ARGS...]: a vk capsula mini tree, go's arm64 cadenced and an aarch64 audiod
# built by zig from audiod.c for glibc 2.28 (or VERSION) with the arguments, ffmpeg and openssl in bin/lib, alsa from
# the device. its runpath becomes a DT_RPATH through patchelf, as build-vk-audiod.sh makes it
vktree() {
  local dir=$tmp/$1 glibc=2.28 audiod runpath
  shift
  if [ "${1:-}" = --glibc ]; then
    glibc=$2
    shift 2
  fi
  mkdir -p "$dir/bin/lib"
  install -m 755 "$GO_ARM64_ELF" "$dir/bin/cadenced"
  cp "$vkstubs/libavformat.so.63" "$vkstubs/libssl.so.3" "$vkstubs/libcrypto.so.3" "$dir/bin/lib/"
  audiod=$dir/bin/cadence-audiod
  zig cc -target "aarch64-linux-gnu.$glibc" -mcpu=cortex_a53 -DMIDI_GLIBC -o "$audiod" "$src/audiod.c" \
    "$dir/bin/lib/libavformat.so.63" "$dir/bin/lib/libssl.so.3" -Wl,--no-as-needed "$vkstubs/libasound.so.2" "$@"
  runpath=$(readelf -dW "$audiod" | sed -n 's/.*(RUNPATH).*\[\(.*\)\]$/\1/p')
  [ -z "$runpath" ] || patchelf --force-rpath --set-rpath "$runpath" "$audiod"
}

vkrpath=(-Wl,-rpath,'$ORIGIN/lib')
vktree vkgood "${vkrpath[@]}"
accepted vkgood "bin/cadence-audiod: GLIBC_2.27, OPENSSL_3.0.0; needs libavformat.so.63 libssl.so.3 libasound.so.2 \
libpthread.so.0 libc.so.6" "${vk[@]}"
accepted vkgood "bin/cadenced: static" "${vk[@]}"
accepted vkgood "bin/lib/libavformat.so.63: GLIBC_" "${vk[@]}"
accepted vkgood "bin/lib/libssl.so.3: no GLIBC, OPENSSL_3.0.3; needs libcrypto.so.3" "${vk[@]}"
# the board names its machine itself
accepted vkgood "check-device-limits.sh: ok" --board vk-capsula-mini

# built for a glibc newer than 2.28
vktree vkglibc --glibc 2.29 "${vkrpath[@]}" -DGLIBC_2_29
refused vkglibc "bin/cadence-audiod: needs GLIBC_2.29, vk-capsula-mini has GLIBC_2.28" "${vk[@]}"

vktree vkinterp "${vkrpath[@]}" -Wl,--dynamic-linker=/data/cadence/runtime/lib/ld-linux-aarch64.so.1
refused vkinterp "bin/cadence-audiod: asks for the loader /data/cadence/runtime/lib/ld-linux-aarch64.so.1, \
vk-capsula-mini's is /lib/ld-linux-aarch64.so.1" "${vk[@]}"

# openssl is bin/lib's alone: neither the system's libssl.so.3 (the firmware has none) nor its 1.1
vktree vkssl "${vkrpath[@]}"
rm "$tmp/vkssl/bin/lib/libssl.so.3"
refused vkssl "bin/cadence-audiod: needs libssl.so.3, which bin/lib does not hold" "${vk[@]}"
vktree vkssl11 "${vkrpath[@]}" -DEXTRA "$vkstubs/libssl.so.1.1"
refused vkssl11 "bin/cadence-audiod: needs libssl.so.1.1, which vk-capsula-mini does not have" "${vk[@]}"

# no runpath outside $ORIGIN, and no vendor directory to let one in
vktree vkabsolute -Wl,-rpath,'$ORIGIN/lib:/usr/lib'
refused vkabsolute "bin/cadence-audiod: runpath /usr/lib is outside \$ORIGIN" "${vk[@]}"
refused vkabsolute "vk-capsula-mini takes no --vendor-libs" "${vk[@]}" --vendor-libs /usr/lib

# midi's armhf tree is not the board's
refused armgood "bin/cadenced: is for ARM, not AArch64" --board vk-capsula-mini
refused vkgood "vk-capsula-mini is AArch64, not ARM" --board vk-capsula-mini --machine ARM

if [ "$failures" -ne 0 ]; then
  echo "check-device-limits.test.sh: $failures failed" >&2
  exit 1
fi
echo "check-device-limits.test.sh: all passed"
