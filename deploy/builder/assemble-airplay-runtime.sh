#!/usr/bin/env bash
# assemble-airplay-runtime.sh <arch> <prefix> <out.tar>: the airplay runtime as one tar, for arm64 (pickle), armhf
# (midi, hard-float throughout) or amd64 (M4d-D1, M4d-R4, M4d-R5, M2m-D6). a self-contained trixie userland subset:
# bin/ holds shairport-sync, nqptp and avahi-daemon from <prefix> (build-airplay.sh) and dbus-daemon from its pinned
# deb; lib/ every library they need, all the way down, from <prefix>/lib (our lgpl ffmpeg and avahi) or a pinned deb,
# and the runtime's own loader; licenses/ every notice, with nqptp's source tarball and avahi's source package, each
# checked against its pin.
# audiod starts each program as lib/<loader> --library-path lib bin/<program>: no chroot, a board's older glibc does
# not matter, and no library of the board is loaded. regular files only, each named as what needs it names it. any
# failed check fails the script, and then no tar is written.
#
# environment: AIRPLAY_DEBS keeps the debs between runs (downloaded when absent, always checked against their pins);
# AIRPLAY_PINS is a pins file to read instead of airplay-runtime.pins (the script's tests)
set -euo pipefail
umask 022
export LC_ALL=C

usage() {
  echo "usage: deploy/builder/assemble-airplay-runtime.sh arm64|armhf|amd64 <prefix> <out.tar>" >&2
  exit 2
}

fail() {
  echo "assemble-airplay-runtime.sh: $*" >&2
  exit 1
}

[ "$#" -eq 3 ] || usage
arch=$1
prefix=$2
out=$3

case $arch in
  arm64) triple=aarch64-linux-gnu machine=AArch64 loader=ld-linux-aarch64.so.1 ;;
  armhf) triple=arm-linux-gnueabihf machine=ARM loader=ld-linux-armhf.so.3 ;;
  amd64) triple=x86_64-linux-gnu machine='Advanced Micro Devices X86-64' loader=ld-linux-x86-64.so.2 ;;
  *) usage ;;
esac

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
pins=${AIRPLAY_PINS:-$here/airplay-runtime.pins}
[ -f "$pins" ] || fail "$pins is missing"
[ -d "$prefix/bin" ] && [ -d "$prefix/lib" ] || fail "$prefix has no bin/ and lib/"
prefix=$(cd "$prefix" && pwd -P)

work=$(mktemp -d)
trap 'rm -rf "$work" "$out.next"' EXIT
# canonical, as a library's resolved path is compared with it
work=$(cd "$work" && pwd -P)
debs=${AIRPLAY_DEBS:-$work/debs}
mkdir -p "$debs" "$work/deb"

# the debs, each checked against its pin whether it was kept or downloaded now, and unpacked into deb/<package>
declare -A version_of=() path_of=() sum_of=() used=()
while read -r _ _ package version path sum; do
  [ -z "${version_of[$package]:-}" ] || fail "$package is pinned twice for $arch"
  version_of[$package]=$version path_of[$package]=$path sum_of[$package]=$sum
  deb=$debs/${path##*/}
  if [ ! -f "$deb" ]; then
    curl --fail --silent --show-error --location --retry 5 --retry-all-errors --proto '=https' --proto-redir '=https' \
      --output "$deb.part" "https://snapshot.debian.org/archive/$path" || fail "cannot download ${path##*/}"
    mv "$deb.part" "$deb"
  fi
  echo "$sum  $deb" | sha256sum --check --strict --quiet - >/dev/null 2>&1 || fail "${path##*/} does not match its pin"
  # what the build linked against is what the builder has installed: it must be the version shipped
  installed=$(dpkg-query -W -f='${db:Status-Abbrev}${Version}' "$package:$arch" 2>/dev/null || true)
  case $installed in
    "ii "*)
      [ "${installed#ii }" = "$version" ] || fail "the builder has $package ${installed#ii }, the pin is $version"
      ;;
  esac
  mkdir "$work/deb/$package"
  dpkg-deb -x "$deb" "$work/deb/$package" || fail "cannot unpack ${path##*/}"
done < <(awk -v arch="$arch" '$1 == "deb" && $2 == arch' "$pins")
[ -n "${version_of[libc6]:-}" ] || fail "$pins pins no libc6 for $arch"
# the runtime's glibc: nothing in it may need a newer symbol version
glibc=${version_of[libc6]#*:}
glibc=${glibc%%-*}

tree=$work/tree
mkdir -p "$tree/bin" "$tree/lib" "$tree/licenses"
queue=()

# program NAME FILE [PACKAGE]: bin/NAME, a regular file of mode 0755 whatever FILE is
program() {
  [ -f "$2" ] || fail "$2 is missing"
  install -m 0755 "$2" "$tree/bin/$1"
  queue+=("$tree/bin/$1")
  [ -z "${3:-}" ] || used[$3]=1
}
program shairport-sync "$prefix/bin/shairport-sync"
program nqptp "$prefix/bin/nqptp"
program avahi-daemon "$prefix/sbin/avahi-daemon"
program dbus-daemon "$work/deb/dbus-daemon/usr/bin/dbus-daemon" dbus-daemon

# provide NAME: lib/NAME, the one file NAME names in <prefix>/lib or in a pinned deb's library directories
provide() {
  local name=$1 candidate root real found=()
  for candidate in "$prefix/lib/$name" "$work"/deb/*/usr/lib/"$triple/$name" "$work"/deb/*/lib/"$triple/$name"; do
    [ -e "$candidate" ] && found+=("$candidate")
  done
  [ "${#found[@]}" -gt 0 ] || fail "$name is needed, and neither $prefix/lib nor a pinned deb has it"
  [ "${#found[@]}" -eq 1 ] || fail "$name is in more than one place: ${found[*]}"
  case ${found[0]} in
    "$prefix/lib/"*) root=$prefix/lib ;;
    *)
      root=${found[0]#"$work/deb/"}
      used[${root%%/*}]=1
      root=$work/deb/${root%%/*}
      ;;
  esac
  # a soname is often a symlink to the versioned file: the file is taken, from where the symlink is
  real=$(readlink -f "${found[0]}")
  case $real in "$root"/*) ;; *) fail "$name leads out of where it is: $real" ;; esac
  [ -f "$real" ] || fail "$name is not a regular file"
  install -m 0755 "$real" "$tree/lib/$name"
}

# glibc loads libgcc_s.so.1 by name when a thread is cancelled, which no NEEDED entry shows: lib/ ships it on every
# cpu, so glibc never falls back to the device's /lib (32-bit only on midi, whose kernel runs this arm64 runtime)
provide libgcc_s.so.1
queue+=("$tree/lib/libgcc_s.so.1")

# every elf for the cpu, searching no directory of its own outside lib/, needing no newer glibc than the runtime's,
# and every library it needs in lib/
index=0
while [ "$index" -lt "${#queue[@]}" ]; do
  elf=${queue[$index]}
  index=$((index + 1))
  name=${elf#"$tree/"}
  header=$(readelf -hW "$elf" 2>/dev/null) || fail "$name is no elf file"
  grep -qE "^ +Machine: +$machine\$" <<<"$header" || fail "$name is not for $machine"
  # armhf passes floats in vfp registers (EF_ARM_ABI_FLOAT_HARD, 0x400 of e_flags): a soft-float file would not link
  if [ "$arch" = armhf ]; then
    flags=$(sed -n 's/^ *Flags: *\(0x[0-9a-f]*\).*/\1/p' <<<"$header")
    if [ -z "$flags" ] || ! ((flags & 0x400)); then fail "$name is not hard-float: e_flags ${flags:-unknown}"; fi
  fi
  dynamic=$(readelf -dW "$elf") || fail "cannot read the dynamic section of $name"
  for entry in $(sed -n 's/.*(R\(UN\)\{0,1\}PATH).*\[\(.*\)\]$/\2/p' <<<"$dynamic" | tr ':' ' '); do
    [ "$entry" = "\$ORIGIN" ] || fail "$name searches $entry for libraries"
  done
  versions=$(readelf -VW "$elf") || fail "cannot read the symbol versions of $name"
  newest=$(grep -oE 'GLIBC_[0-9]+(\.[0-9]+)+' <<<"$versions" | sed 's/^GLIBC_//' | sort -uV | tail -n 1 || true)
  [ -z "$newest" ] || [ "$(printf '%s\n%s\n' "$newest" "$glibc" | sort -V | tail -n 1)" = "$glibc" ] ||
    fail "$name needs glibc $newest, newer than the runtime's $glibc"
  for needed in $(sed -n 's/.*(NEEDED).*\[\(.*\)\]$/\1/p' <<<"$dynamic"); do
    [ ! -e "$tree/lib/$needed" ] || continue
    provide "$needed"
    queue+=("$tree/lib/$needed")
  done
done
[ -f "$tree/lib/$loader" ] || fail "lib/ has no $loader"

# run PROGRAM [ARGS...]: the program as audiod starts it, through the runtime's loader, every symbol bound at once
run() {
  local program=$1
  shift
  env -i LD_BIND_NOW=1 "$tree/lib/$loader" --library-path "$tree/lib" "$tree/bin/$program" "$@" 2>&1
}

source_version() {
  awk -v name="$1" '$1 == "source" && $2 == name { print $3 }' "$pins"
}

# shairport sync as configured: airplay 2 into a pipe, metadata, d-bus, avahi and openssl, no other audio backend
shairport=$(run shairport-sync -V) || fail "shairport-sync -V failed: $shairport"
read -ra features <<<"${shairport//-/ }"
shows() {
  local feature
  for feature in "${features[@]}"; do
    [ "$feature" != "$1" ] || return 0
  done
  return 1
}
[ "${features[0]:-}" = "$(source_version shairport-sync)" ] ||
  fail "shairport-sync -V is not version $(source_version shairport-sync): $shairport"
for feature in AirPlay2 pipe metadata dbus Avahi OpenSSL; do
  shows "$feature" || fail "shairport-sync -V lacks $feature: $shairport"
done
for feature in ALSA stdout pa pw ao sndio jack dummy soxr convolution mqtt mpris; do
  ! shows "$feature" || fail "shairport-sync -V shows $feature: $shairport"
done
# shairport sync and nqptp share their timing through shared memory of one layout, named in both -V
[[ $shairport =~ -smi([0-9]+) ]] || fail "shairport-sync -V names no shared memory interface: $shairport"
smi=${BASH_REMATCH[1]}
nqptp=$(run nqptp -V) || fail "nqptp -V failed: $nqptp"
[[ $nqptp == *"Version: $(source_version nqptp)."* ]] || fail "nqptp -V is not version $(source_version nqptp): $nqptp"
[[ $nqptp =~ Interface\ Version:\ smi([0-9]+) ]] || fail "nqptp -V names no shared memory interface: $nqptp"
[ "${BASH_REMATCH[1]}" = "$smi" ] || fail "shairport-sync speaks smi$smi, nqptp smi${BASH_REMATCH[1]}"
avahi=$(awk '$1 == "debsrc" && $2 == "avahi" { print $3; exit }' "$pins")
[ -n "${version_of[dbus-daemon]:-}" ] && [ -n "$avahi" ] || fail "$pins pins no dbus-daemon for $arch or no avahi"
for daemon in "dbus-daemon ${version_of[dbus-daemon]}" "avahi-daemon $avahi"; do
  read -r daemon upstream <<<"$daemon"
  upstream=${upstream#*:}
  upstream=${upstream%%-*}
  output=$(run "$daemon" --version) || fail "$daemon --version failed: $output"
  [[ $output == *"$upstream"* ]] || fail "$daemon --version is not version $upstream: $output"
done
# avahi-daemon keeps its pid file and socket and reads service files only where it was built to (M4d-R5): the private
# directory audiod prepares, never the host's /run/avahi-daemon or /etc/avahi, which a host avahi may own
private=/tmp/cadence-airplay/avahi
names=$(strings -a "$tree/bin/avahi-daemon") || fail "cannot read the strings of bin/avahi-daemon"
while read -r path; do
  case $path in "$private"/*) ;; *) fail "bin/avahi-daemon names $path" ;; esac
done < <(grep -oE "[^[:space:]\"'()]*(/run/avahi-daemon|/etc/avahi)[^[:space:]\"'()]*" <<<"$names" || true)
for path in "$private/run/socket" "$private/etc/avahi/services"; do
  grep -qxF "$path" <<<"$names" || fail "bin/avahi-daemon does not name $path"
done

# the notices: shairport sync's, nqptp's and avahi's with their source pointers, ffmpeg's, every deb's copyright
# (through a doc directory that is a symlink to another package's), the license texts those point at, and the debs
for name in shairport-sync nqptp avahi ffmpeg; do
  [ -d "$prefix/share/licenses/$name" ] || fail "$prefix/share/licenses/$name is missing"
  cp -R "$prefix/share/licenses/$name" "$tree/licenses/$name"
done
for file in shairport-sync/LICENSES shairport-sync/SOURCE nqptp/LICENSE nqptp/SOURCE avahi/LICENSE avahi/copyright \
  avahi/SOURCE ffmpeg/COPYING.LGPLv2.1; do
  [ -s "$tree/licenses/$file" ] || fail "licenses/$file is missing"
done
# the source the gpl (nqptp) and the lgpl (avahi) ask for: nqptp's tarball and every file of avahi's source package,
# each the very file its pin names
shipped() {
  [ -f "$tree/licenses/$1" ] || fail "licenses/$1 is missing"
  echo "$2  $tree/licenses/$1" | sha256sum --check --strict --quiet - >/dev/null 2>&1 ||
    fail "licenses/$1 does not match its pin"
}
nqptp=$(awk '$1 == "source" && $2 == "nqptp" { print $3, $6 }' "$pins")
[ -n "$nqptp" ] || fail "$pins has no source pin for nqptp"
shipped "nqptp/nqptp-${nqptp% *}.tar.gz" "${nqptp#* }"
while read -r path sum; do
  shipped "avahi/${path##*/}" "$sum"
done < <(awk '$1 == "debsrc" && $2 == "avahi" { print $4, $5 }' "$pins")
[ -n "${version_of[base-files]:-}" ] || fail "$pins pins no base-files for $arch"
used[base-files]=1
[ -d "$work/deb/base-files/usr/share/common-licenses" ] || fail "base-files has no common-licenses"
mkdir "$tree/licenses/common-licenses"
find "$work/deb/base-files/usr/share/common-licenses" -maxdepth 1 -type f -exec install -m 0644 -t \
  "$tree/licenses/common-licenses" {} +
mapfile -t packages < <(printf '%s\n' "${!used[@]}" | sort)
for package in "${packages[@]}"; do
  doc=$work/deb/$package/usr/share/doc/$package
  if [ -L "$doc" ]; then
    owner=$(basename "$(readlink "$doc")")
    [ -n "${version_of[$owner]:-}" ] || fail "$package's copyright is $owner's, which is not pinned"
    used[$owner]=1
    doc=$work/deb/$owner/usr/share/doc/$owner
  fi
  [ -f "$doc/copyright" ] && [ ! -L "$doc/copyright" ] || fail "$package has no copyright file"
  mkdir -p "$tree/licenses/debian/$package"
  install -m 0644 "$doc/copyright" "$tree/licenses/debian/$package/copyright"
done
# a pin nothing takes is a package nobody reviewed for the runtime
mapfile -t packages < <(printf '%s\n' "${!version_of[@]}" | sort)
for package in "${packages[@]}"; do
  [ -n "${used[$package]:-}" ] || fail "the pinned deb $package is not part of the runtime"
  echo "$package ${version_of[$package]} https://snapshot.debian.org/archive/${path_of[$package]} ${sum_of[$package]}"
done >"$tree/licenses/debian/PACKAGES"
find "$tree/licenses" -type d -exec chmod 0755 {} +
find "$tree/licenses" -type f -exec chmod 0644 {} +

[ -z "$(find "$tree" -type l)" ] || fail "the tree holds symlinks: $(find "$tree" -type l -printf '%P -> %l ')"
[ -z "$(find "$tree" ! -type f ! -type d)" ] || fail "the tree holds what is neither a file nor a directory"

# root owned whoever builds, in a fixed order with a fixed time: the same tree is the same bytes
mkdir -p "$(dirname "$out")"
tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --format=gnu -cf "$out.next" -C "$tree" . ||
  fail "cannot write $out"
mv "$out.next" "$out"
echo "assemble-airplay-runtime.sh: $arch: $(find "$tree" -type f | wc -l) files, $(wc -c <"$out") bytes," \
  "$(find "$tree/lib" -type f | wc -l) libraries, smi$smi, glibc $glibc"
