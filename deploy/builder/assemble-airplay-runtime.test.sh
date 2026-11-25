#!/usr/bin/env bash
# tests of assemble-airplay-runtime.sh, run in the airplay builder's test stage after it assembled the runtime from
# <prefix>: what the tar holds, that a second run writes the same bytes, and what the script and build-airplay.sh
# refuse. the refusals use fake programs built here, the debs AIRPLAY_DEBS and the tarballs AIRPLAY_SOURCES already
# hold, so they need no network.
# usage: assemble-airplay-runtime.test.sh <arch> <prefix> <tar>
set -euo pipefail
export LC_ALL=C

[ "$#" -eq 3 ] || { echo "usage: assemble-airplay-runtime.test.sh <arch> <prefix> <tar>" >&2; exit 2; }
arch=$1
prefix=$2
tar=$3

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script=$here/assemble-airplay-runtime.sh
pins=$here/airplay-runtime.pins
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failures=0

case $arch in
  arm64) triple=aarch64-linux-gnu loader=ld-linux-aarch64.so.1 ;;
  armhf) triple=arm-linux-gnueabihf loader=ld-linux-armhf.so.3 ;;
  amd64) triple=x86_64-linux-gnu loader=ld-linux-x86-64.so.2 ;;
  *) echo "assemble-airplay-runtime.test.sh: unknown arch $arch" >&2; exit 2 ;;
esac

pass() { echo "ok - $1"; }
flunk() {
  echo "not ok - $1" >&2
  failures=$((failures + 1))
}

# listing TAR: its entries as mode and path, sorted; every one owned by 0:0 at time 0
listing() {
  tar -tvf "$1" --numeric-owner | awk '
    $2 != "0/0" || $4 != "1970-01-01" || $5 != "00:00" { print "bad owner or time: " $0; next }
    { print $1, $NF }' | sort
}

# the tree's shape: the four programs, libraries flat in lib/, notices under licenses/, nothing else and no link
listing "$tar" >"$tmp/listing" || echo "unreadable: $tar" >"$tmp/listing"
awk '
  $2 ~ /^\.\/(bin\/|lib\/|licenses\/(.+\/)?)?$/ && $1 == "drwxr-xr-x" { next }
  $2 ~ /^\.\/bin\/(shairport-sync|nqptp|dbus-daemon|avahi-daemon)$/ && $1 == "-rwxr-xr-x" { next }
  $2 ~ /^\.\/lib\/[^\/]+$/ && $1 == "-rwxr-xr-x" { next }
  $2 ~ /^\.\/licenses\/.*[^\/]$/ && $1 == "-rw-r--r--" { next }
  { print "unexpected: " $0 }' "$tmp/listing" >"$tmp/unexpected"
missing=
for path in bin/shairport-sync bin/nqptp bin/dbus-daemon bin/avahi-daemon "lib/$loader" lib/libc.so.6 \
  lib/libavahi-client.so.3 lib/libavahi-core.so.7 lib/libdbus-1.so.3 lib/libcrypto.so.3 lib/libgio-2.0.so.0 \
  licenses/shairport-sync/LICENSES licenses/shairport-sync/SOURCE licenses/nqptp/LICENSE licenses/nqptp/SOURCE \
  licenses/avahi/LICENSE licenses/avahi/copyright licenses/avahi/SOURCE licenses/ffmpeg/COPYING.LGPLv2.1 \
  licenses/debian/dbus-daemon/copyright licenses/debian/libatomic1/copyright licenses/debian/PACKAGES \
  licenses/common-licenses/GPL-2 licenses/common-licenses/LGPL-2.1; do
  grep -qE " \./$path\$" "$tmp/listing" || missing="$missing $path"
done
grep -qE ' \./lib/libavcodec\.so\.[0-9]+$' "$tmp/listing" || missing="$missing lib/libavcodec.so.*"
if [ -s "$tmp/unexpected" ] || [ -n "$missing" ]; then
  cat "$tmp/unexpected" >&2
  [ -z "$missing" ] || echo "missing:$missing" >&2
  flunk "the tar holds the four programs, their libraries and the notices, nothing else"
else
  pass "the tar holds the four programs, their libraries and the notices, nothing else"
fi

# matches FILE SHA256: FILE is there with that sha256
matches() { [ -f "$1" ] && echo "$2  $1" | sha256sum --check --strict --quiet - >/dev/null 2>&1; }

# nqptp is gpl-2: its source pointer names the pinned tarball, built unmodified, and the tarball itself ships beside it
mkdir "$tmp/runtime"
tar -xf "$tar" -C "$tmp/runtime" 2>/dev/null || true
read -r version commit url sum < <(awk '$1 == "source" && $2 == "nqptp" { print $3, $4, $5, $6 }' "$pins")
if grep -q 'built unmodified' "$tmp/runtime/licenses/nqptp/SOURCE" &&
  grep -qx "url: $url" "$tmp/runtime/licenses/nqptp/SOURCE" &&
  grep -qx "tag: $version (commit $commit)" "$tmp/runtime/licenses/nqptp/SOURCE" &&
  grep -qx "sha256: $sum" "$tmp/runtime/licenses/nqptp/SOURCE" &&
  grep -q 'GNU GENERAL PUBLIC LICENSE' "$tmp/runtime/licenses/nqptp/LICENSE" &&
  matches "$tmp/runtime/licenses/nqptp/nqptp-$version.tar.gz" "$sum"; then
  pass "nqptp ships its gpl-2 text, a pointer to the tarball it was built from unmodified, and the tarball"
else
  flunk "nqptp ships its gpl-2 text, a pointer to the tarball it was built from unmodified, and the tarball"
fi

# avahi is lgpl: its source pointer names each pinned file of debian's source package it was built from, and each
# file ships beside it
ok=1
while read -r path sum; do
  grep -qF "https://snapshot.debian.org/archive/$path sha256 $sum" "$tmp/runtime/licenses/avahi/SOURCE" 2>/dev/null ||
    ok=
  matches "$tmp/runtime/licenses/avahi/${path##*/}" "$sum" || ok=
done < <(awk '$1 == "debsrc" && $2 == "avahi" { print $4, $5 }' "$pins")
grep -q 'GNU LESSER GENERAL PUBLIC LICENSE' "$tmp/runtime/licenses/avahi/LICENSE" 2>/dev/null || ok=
if [ -n "$ok" ]; then
  pass "avahi ships its lgpl text and each file of the source package it was built from, with a pointer to each"
else
  flunk "avahi ships its lgpl text and each file of the source package it was built from, with a pointer to each"
fi

if bash "$script" "$arch" "$prefix" "$tmp/again.tar" >"$tmp/again.log" 2>&1 && cmp -s "$tar" "$tmp/again.tar"; then
  pass "a second run writes the same bytes"
else
  cat "$tmp/again.log" >&2
  flunk "a second run writes the same bytes"
fi

mkdir "$tmp/fake" "$tmp/prefix"

# fake NAME LINE [C] [GCC ARGS...]: fake/NAME, a program printing LINE, plus the C given, built for this cpu
fake() {
  local name=$1 line=$2 extra=${3:-}
  shift 3 || shift $#
  printf '#include <stdio.h>\n%s\nint main(void) { puts("%s"); return 0; }\n' "$extra" "$line" >"$tmp/fake/$name.c"
  gcc -O2 -o "$tmp/fake/$name" "$tmp/fake/$name.c" "$@"
}

# with NAME [PATH]: prefix/NAME, a copy of <prefix> whose PATH (bin/nqptp, say) is fake/NAME
with() {
  cp -a "$prefix" "$tmp/prefix/$1"
  [ "$#" -lt 2 ] || install -D -m 0755 "$tmp/fake/$1" "$tmp/prefix/$1/$2"
}

# refused NAME PREFIX MESSAGE [PINS]: the script fails naming MESSAGE, and writes no tar
refused() {
  local name=$1 dir=$2 message=$3 file=${4:-$pins} out="$tmp/$1.tar"
  if AIRPLAY_PINS=$file bash "$script" "$arch" "$dir" "$out" >"$tmp/$name.log" 2>&1; then
    flunk "$name: refused"
  elif ! grep -qF -- "$message" "$tmp/$name.log"; then
    cat "$tmp/$name.log" >&2
    flunk "$name: refused for $message"
  elif [ -e "$out" ]; then
    flunk "$name: no tar written"
  else
    pass "$name: refused for $message"
  fi
}

good=5.6.2-AirPlay2-smi11-OpenSSL-Avahi-pipe-metadata-dbus-sysconfdir:/usr/local/etc
nqptp_line='Version: 1.2.9. Shared Memory Interface Version: smi11.'

# a shairport sync of no library leaves the pins of its libraries unused
fake bare "$good"
with bare bin/shairport-sync
refused bare "$tmp/prefix/bare" "is not part of the runtime"

fake nopipe 5.6.2-AirPlay2-smi11-OpenSSL-Avahi-metadata-dbus-sysconfdir:/usr/local/etc
with nopipe bin/shairport-sync
refused nopipe "$tmp/prefix/nopipe" "shairport-sync -V lacks pipe"

fake alsa 5.6.2-AirPlay2-smi11-OpenSSL-Avahi-ALSA-pipe-metadata-dbus-sysconfdir:/usr/local/etc
with alsa bin/shairport-sync
refused alsa "$tmp/prefix/alsa" "shairport-sync -V shows ALSA"

fake smi 5.6.2-AirPlay2-smi10-OpenSSL-Avahi-pipe-metadata-dbus-sysconfdir:/usr/local/etc
with smi bin/shairport-sync
refused smi "$tmp/prefix/smi" "shairport-sync speaks smi10, nqptp smi11"

# a library that only the build host has
mkdir "$tmp/outside"
printf 'void cadence_outside(void) {}\n' >"$tmp/outside/lib.c"
gcc -shared -fPIC -o "$tmp/outside/libcadence-outside.so.1" -Wl,-soname,libcadence-outside.so.1 "$tmp/outside/lib.c"
fake outside "$nqptp_line" 'extern void cadence_outside(void); void cadence_call(void) { cadence_outside(); }' \
  -Wl,--no-as-needed "$tmp/outside/libcadence-outside.so.1"
with outside bin/nqptp
refused outside "$tmp/prefix/outside" "libcadence-outside.so.1 is needed, and neither"

# a search path of its own would load the board's libraries before lib/
fake rpath "$nqptp_line" '' "-Wl,-rpath,/usr/lib/$triple"
with rpath bin/nqptp
refused rpath "$tmp/prefix/rpath" "bin/nqptp searches /usr/lib/$triple for libraries"

# armhf: a program whose e_flags (offset 36 of an elf32 header) say soft-float, EABI version 5
if [ "$arch" = armhf ]; then
  fake softfloat "$nqptp_line"
  printf '\000\002\000\005' | dd of="$tmp/fake/softfloat" bs=1 seek=36 conv=notrunc status=none
  with softfloat bin/nqptp
  refused softfloat "$tmp/prefix/softfloat" "bin/nqptp is not hard-float: e_flags 0x5000200"
fi

# a symbol of a glibc of the future, newer than the runtime's (the version is checked before the library it is in)
mkdir "$tmp/future"
printf 'GLIBC_2.99 { global: cadence_future; };\n' >"$tmp/future/lib.map"
printf 'void cadence_future(void) {}\n' >"$tmp/future/lib.c"
gcc -shared -fPIC -o "$tmp/future/libcadence-future.so.1" -Wl,-soname,libcadence-future.so.1 \
  -Wl,--version-script="$tmp/future/lib.map" "$tmp/future/lib.c"
fake future "$nqptp_line" 'extern void cadence_future(void); void cadence_call(void) { cadence_future(); }' \
  "$tmp/future/libcadence-future.so.1"
with future bin/nqptp
refused future "$tmp/prefix/future" "bin/nqptp needs glibc 2.99"

# a soname in <prefix>/lib that is a symlink to a library outside it
mkdir "$tmp/escape"
printf 'void cadence_escape(void) {}\n' >"$tmp/escape/lib.c"
gcc -shared -fPIC -o "$tmp/escape/libcadence-escape.so.1" -Wl,-soname,libcadence-escape.so.1 "$tmp/escape/lib.c"
fake escape "$nqptp_line" 'extern void cadence_escape(void); void cadence_call(void) { cadence_escape(); }' \
  -Wl,--no-as-needed "$tmp/escape/libcadence-escape.so.1"
with escape bin/nqptp
ln -s "$tmp/escape/libcadence-escape.so.1" "$tmp/prefix/escape/lib/libcadence-escape.so.1"
refused escape "$tmp/prefix/escape" \
  "libcadence-escape.so.1 leads out of where it is: $tmp/escape/libcadence-escape.so.1"

# an avahi-daemon that would keep its pid file where a host avahi keeps its own
fake hostpaths 'avahi-daemon 0.8' 'const char *cadence_pid = "/run/avahi-daemon/pid";'
with hostpaths sbin/avahi-daemon
refused hostpaths "$tmp/prefix/hostpaths" "bin/avahi-daemon names /run/avahi-daemon/pid"

# the sources the gpl and lgpl ask for: an altered tarball, a missing source package file
with sources
printf 'x' >>"$tmp/prefix/sources/share/licenses/nqptp/nqptp-$version.tar.gz"
refused sources "$tmp/prefix/sources" "licenses/nqptp/nqptp-$version.tar.gz does not match its pin"
dsc=$(awk '$1 == "debsrc" && $2 == "avahi" && $4 ~ /\.dsc$/ { n = split($4, p, "/"); print p[n] }' "$pins")
with nodsc
rm "$tmp/prefix/nodsc/share/licenses/avahi/$dsc"
refused nodsc "$tmp/prefix/nodsc" "licenses/avahi/$dsc is missing"

with symlink
ln -s /etc/passwd "$tmp/prefix/symlink/share/licenses/shairport-sync/escape"
refused symlink "$tmp/prefix/symlink" "the tree holds symlinks: licenses/shairport-sync/escape -> /etc/passwd"

# a pin altered after the deb was checked against it
awk -v arch="$arch" '$1 == "deb" && $2 == arch && $3 == "libplist-2.0-4" {
  $6 = (substr($6, 1, 1) == "0" ? "1" : "0") substr($6, 2) } 1' "$pins" >"$tmp/tampered.pins"
refused tampered "$prefix" "_$arch.deb does not match its pin" "$tmp/tampered.pins"

# a source tarball pin altered after the tarball was kept in AIRPLAY_SOURCES: build-airplay.sh stops before building
awk '$1 == "source" && $2 == "nqptp" { $6 = (substr($6, 1, 1) == "0" ? "1" : "0") substr($6, 2) } 1' "$pins" \
  >"$tmp/source.pins"
if sh "$here/build-airplay.sh" "$tmp/built" "$tmp/source.pins" >"$tmp/source.log" 2>&1; then
  flunk "a tampered source pin: refused"
elif ! grep -qF "nqptp's tarball does not match its pin" "$tmp/source.log"; then
  cat "$tmp/source.log" >&2
  flunk "a tampered source pin: refused for nqptp's tarball does not match its pin"
elif [ -e "$tmp/built/bin/nqptp" ]; then
  flunk "a tampered source pin: nothing built"
else
  pass "a tampered source pin: refused for nqptp's tarball does not match its pin"
fi

if bash "$script" riscv64 "$prefix" "$tmp/riscv64.tar" >/dev/null 2>&1; then flunk "an unknown arch is refused"; else
  [ "$?" -eq 2 ] && pass "an unknown arch is refused" || flunk "an unknown arch is refused with usage"
fi

[ "$failures" -eq 0 ] || { echo "assemble-airplay-runtime.test.sh: $failures failed" >&2; exit 1; }
echo "assemble-airplay-runtime.test.sh: all passed"
