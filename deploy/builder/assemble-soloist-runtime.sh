#!/usr/bin/env bash
# assemble-soloist-runtime.sh <arch> <out.tar>: soloist's runtime as one tar, for arm64 (pickle) or amd64 (the sim's
# fake soloist) (M4c-D12). spotify builds soloist against glibc 2.38 and a board may have less, so audiod runs it
# chrooted into this tree: debian trixie's loader, libc, libgcc_s and libatomic from pinned debs, our libpulse shim built
# for the cpu, a minimal etc/, each package's copyright, and the empty files and directories the jail binds over
# (media/audiod/soloist-jail.cpp). no symlink anywhere: the jail refuses them. the trixie loader finds its libraries in
# /lib/<triple> and /usr/lib/<triple> without a cache, so the tree needs none. any failed check fails the script.
#
# environment: SOLOIST_DEBS keeps the debs between runs (downloaded when absent, always checked against their pins);
# CADENCE_PULSE_SHIM is a libpulse.so.0 to take instead of building one (the script's tests), CADENCE_RESOLV_SHIM the
# resolv preload beside it
set -euo pipefail
umask 022
export LC_ALL=C

usage() {
  echo "usage: deploy/builder/assemble-soloist-runtime.sh arm64|amd64 <out.tar>" >&2
  exit 2
}

fail() {
  echo "assemble-soloist-runtime.sh: $*" >&2
  exit 1
}

[ "$#" -eq 2 ] || usage
arch=$1
out=$2

case $arch in
  arm64) triple=aarch64-linux-gnu processor=aarch64 machine=AArch64 loader=lib/ld-linux-aarch64.so.1 ;;
  amd64) triple=x86_64-linux-gnu processor=x86_64 machine='Advanced Micro Devices X86-64' loader=lib64/ld-linux-x86-64.so.2 ;;
  *) usage ;;
esac

# debian 13.7, all from one snapshot: each deb's pool path and SHA256 field from that snapshot's
# main/binary-<arch>/Packages.xz, whose own SHA256 is in dists/trixie/InRelease, signed by debian's archive keys
# (checked with gpgv against debian-archive-keyring). gcc-14-base only gives libgcc-s1 and libatomic1 their copyright
snapshot=https://snapshot.debian.org/archive/debian/20261026T142754Z/pool/main
pins() {
  case $arch in
    arm64)
      echo libc6 g/glibc/libc6_2.41-12+deb13u4_arm64.deb 8784eda966b189c777a384dac5ce009e8fc9b52d006926c5a013e7fa8aa688cc
      echo libgcc-s1 g/gcc-14/libgcc-s1_14.2.0-19_arm64.deb 1108bc87879833d6d9a145f22a4a15cddb34e065b4b5f4b97bee586adbac2851
      echo libatomic1 g/gcc-14/libatomic1_14.2.0-19_arm64.deb 7ccff90234484bf21ae5ecb79d25b6a84b2f4038f65dd139085b2e3a937cd8dc
      echo gcc-14-base g/gcc-14/gcc-14-base_14.2.0-19_arm64.deb 34ee90679b018c0e64234747a4c4c0ae6b7f63541115037465a8627c2dfbc594
      ;;
    amd64)
      echo libc6 g/glibc/libc6_2.41-12+deb13u4_amd64.deb 967aa62605721081c3eb2a17650611a792aa802d76a6511d1840242623d204c9
      echo libgcc-s1 g/gcc-14/libgcc-s1_14.2.0-19_amd64.deb 3c71917b490d1a17aed43196a2787a256ecf060526cdb20216a74bedc061b150
      echo libatomic1 g/gcc-14/libatomic1_14.2.0-19_amd64.deb 212b399aae2f7299203d261a57e49372e09565a9a5ea971905f94a3960366c05
      echo gcc-14-base g/gcc-14/gcc-14-base_14.2.0-19_amd64.deb 5b6825de4263824b78c4c51f6476414f3b4e89c2ab63e81dc8b9b5501e867cf6
      ;;
  esac
}
# the runtime's glibc: the shim may need no newer symbol version
glibc=2.41

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
symbols=$repo/media/pulse-shim/symbols.txt
# the cpu's binutils: the host's own when it is that cpu, else trixie's cross ones
prefix=
[ "$(uname -m)" = "$processor" ] || prefix=$triple-

work=$(mktemp -d)
trap 'rm -rf "$work" "$out.next"' EXIT
debs=${SOLOIST_DEBS:-$work/debs}
mkdir -p "$debs"

# the shim and the resolv preload as built for the cpu, without the media libraries (CADENCE_MEDIA_PULSE_ONLY)
shim=${CADENCE_PULSE_SHIM:-}
resolv_shim=${CADENCE_RESOLV_SHIM:-}
if [ -z "$shim" ] || [ -z "$resolv_shim" ]; then
  cross=()
  [ -z "$prefix" ] || cross=(-DCMAKE_SYSTEM_NAME=Linux "-DCMAKE_SYSTEM_PROCESSOR=$processor"
    "-DCMAKE_C_COMPILER=${prefix}gcc" "-DCMAKE_CXX_COMPILER=${prefix}g++")
  generator=()
  ! command -v ninja >/dev/null 2>&1 || generator=(-G Ninja)
  { cmake -S "$repo/media" -B "$work/build" "${generator[@]}" -DCMAKE_BUILD_TYPE=Release -DCADENCE_MEDIA_PULSE_ONLY=ON \
    "${cross[@]}" && cmake --build "$work/build" --target cadence_pulse cadence_resolv; } >"$work/build.log" 2>&1 ||
    { cat "$work/build.log" >&2; fail "cannot build libpulse.so.0 for $arch"; }
  [ -n "$shim" ] || shim=$work/build/pulse-shim/libpulse.so.0
  [ -n "$resolv_shim" ] || resolv_shim=$work/build/pulse-shim/libcadence-resolv.so
fi
[ -f "$shim" ] || fail "$shim is missing"
[ -f "$resolv_shim" ] || fail "$resolv_shim is missing"

# the shim, judged as it ships: for the cpu, stripped, exactly symbols.txt, and nothing the runtime lacks
lib=$work/libpulse.so.0
header=$(readelf -hW "$shim") || fail "$(basename "$shim") is no elf file"
grep -qE "^ +Machine: +$machine\$" <<<"$header" || fail "$(basename "$shim") is not for $machine"
grep -qE '^ +Type: +DYN ' <<<"$header" || fail "$(basename "$shim") is not a shared library"
"${prefix}strip" --strip-unneeded -o "$lib" "$shim" || fail "cannot strip $(basename "$shim")"
# soloist resolves these names by dlsym: one missing is a crash at the first play, one more is surface nobody reviewed
"${prefix}nm" -D --defined-only "$lib" | awk 'NF >= 3 { print $3 }' | sort -u >"$work/exported" ||
  fail "cannot list the exports of libpulse.so.0"
sort -u "$symbols" >"$work/wanted"
extra=$(comm -13 "$work/wanted" "$work/exported" | tr '\n' ' ')
absent=$(comm -23 "$work/wanted" "$work/exported" | tr '\n' ' ')
[ -z "$extra" ] || fail "libpulse.so.0 exports names symbols.txt lacks: ${extra% }"
[ -z "$absent" ] || fail "libpulse.so.0 lacks the exports symbols.txt names: ${absent% }"
dynamic=$(readelf -dW "$lib") || fail "cannot read the dynamic section of libpulse.so.0"
soname=$(printf '%s\n' "$dynamic" | sed -n 's/.*(SONAME).*\[\(.*\)\]$/\1/p')
[ "$soname" = libpulse.so.0 ] || fail "libpulse.so.0 has the soname '$soname'"
for needed in $(printf '%s\n' "$dynamic" | sed -n 's/.*(NEEDED).*\[\(.*\)\]$/\1/p'); do
  case $needed in
    libc.so.6 | libm.so.6 | libpthread.so.0 | "${loader##*/}") ;;
    *) fail "libpulse.so.0 needs $needed, which the runtime does not have" ;;
  esac
done
# a library of initial-exec tls takes room the loader may have given away when soloist loads it
! grep -q STATIC_TLS <<<"$dynamic" || fail "libpulse.so.0 has the STATIC_TLS flag"
versions=$(readelf -VW "$lib") || fail "cannot read the symbol versions of libpulse.so.0"
newest=$(grep -oE 'GLIBC_[0-9]+(\.[0-9]+)+' <<<"$versions" | sed 's/^GLIBC_//' | sort -uV | tail -n 1 || true)
[ -z "$newest" ] || [ "$(printf '%s\n%s\n' "$newest" "$glibc" | sort -V | tail -n 1)" = "$glibc" ] ||
  fail "libpulse.so.0 needs glibc $newest, newer than the runtime's $glibc"

# the resolv preload, judged as it ships: for the cpu, stripped, exporting getaddrinfo and freeaddrinfo alone
# (soloist's lookups reach audiod's socket through it), and nothing the runtime lacks
resolv=$work/libcadence-resolv.so
resolv_header=$(readelf -hW "$resolv_shim") || fail "$(basename "$resolv_shim") is no elf file"
grep -qE "^ +Machine: +$machine\$" <<<"$resolv_header" || fail "$(basename "$resolv_shim") is not for $machine"
grep -qE '^ +Type: +DYN ' <<<"$resolv_header" || fail "$(basename "$resolv_shim") is not a shared library"
"${prefix}strip" --strip-unneeded -o "$resolv" "$resolv_shim" || fail "cannot strip $(basename "$resolv_shim")"
"${prefix}nm" -D --defined-only "$resolv" | awk 'NF >= 3 { print $3 }' | sort -u >"$work/resolv-exported" ||
  fail "cannot list the exports of libcadence-resolv.so"
printf '%s\n' freeaddrinfo getaddrinfo | sort -u >"$work/resolv-wanted"
resolv_extra=$(comm -13 "$work/resolv-wanted" "$work/resolv-exported" | tr '\n' ' ')
resolv_absent=$(comm -23 "$work/resolv-wanted" "$work/resolv-exported" | tr '\n' ' ')
[ -z "$resolv_extra" ] || fail "libcadence-resolv.so exports names the preload's map lacks: ${resolv_extra% }"
[ -z "$resolv_absent" ] || fail "libcadence-resolv.so lacks the exports the preload's map names: ${resolv_absent% }"
resolv_dynamic=$(readelf -dW "$resolv") || fail "cannot read the dynamic section of libcadence-resolv.so"
for needed in $(printf '%s\n' "$resolv_dynamic" | sed -n 's/.*(NEEDED).*\[\(.*\)\]$/\1/p'); do
  case $needed in
    libc.so.6 | libm.so.6 | "${loader##*/}") ;;
    *) fail "libcadence-resolv.so needs $needed, which the runtime does not have" ;;
  esac
done
! grep -q STATIC_TLS <<<"$resolv_dynamic" || fail "libcadence-resolv.so has the STATIC_TLS flag"
resolv_versions=$(readelf -VW "$resolv") || fail "cannot read the symbol versions of libcadence-resolv.so"
resolv_newest=$(grep -oE 'GLIBC_[0-9]+(\.[0-9]+)+' <<<"$resolv_versions" | sed 's/^GLIBC_//' | sort -uV | tail -n 1 || true)
[ -z "$resolv_newest" ] || [ "$(printf '%s\n%s\n' "$resolv_newest" "$glibc" | sort -V | tail -n 1)" = "$glibc" ] ||
  fail "libcadence-resolv.so needs glibc $resolv_newest, newer than the runtime's $glibc"

# the debs, each checked against its pin whether it was kept or downloaded now
while read -r package path sum; do
  deb=$debs/$(basename "$path")
  if [ ! -f "$deb" ]; then
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --retry 5 \
      --retry-all-errors --connect-timeout 30 --output "$deb.part" "$snapshot/$path" ||
      fail "cannot download $(basename "$path")"
    mv "$deb.part" "$deb"
  fi
  echo "$sum  $deb" | sha256sum --check --strict --quiet - >/dev/null 2>&1 ||
    fail "$(basename "$path") does not match its pin"
  mkdir "$work/$package"
  dpkg-deb -x "$deb" "$work/$package" || fail "cannot unpack $(basename "$path")"
done < <(pins)

# built in a temp dir, not where the tar goes: a bind mount may keep no modes, and the loader must stay executable
tree=$work/tree
for dir in "lib/$triple" "usr/lib/$triple" "${loader%/*}" etc/ssl/certs dev proc home/soloist run/cadence \
  usr/share/doc/libc6 usr/share/doc/libgcc-s1 usr/share/doc/libatomic1; do
  mkdir -p "$tree/$dir"
done

# a library as a regular file, mode 0755: install follows a symlink such as libatomic.so.1 to the file it names
take() {
  local source="$work/$1/usr/lib/$triple/$2"
  [ -f "$source" ] || fail "$1 has no usr/lib/$triple/$2"
  install -m 0755 "$source" "$tree/$3"
}
# the loader is a regular file where soloist's PT_INTERP names it
take libc6 "${loader##*/}" "$loader"
for library in libc.so.6 libm.so.6 libpthread.so.0 libdl.so.2 librt.so.1; do
  take libc6 "$library" "lib/$triple/$library"
done
take libgcc-s1 libgcc_s.so.1 "lib/$triple/libgcc_s.so.1"
take libatomic1 libatomic.so.1 "lib/$triple/libatomic.so.1"
install -m 0755 "$lib" "$tree/usr/lib/$triple/libpulse.so.0"
install -m 0755 "$resolv" "$tree/usr/lib/$triple/libcadence-resolv.so"

# libgcc-s1's and libatomic1's doc dirs are symlinks to gcc-14-base's, where their copyright is
notice() {
  [ -f "$work/$1/usr/share/doc/$1/copyright" ] || fail "$1 has no copyright file"
  install -m 0644 "$work/$1/usr/share/doc/$1/copyright" "$tree/usr/share/doc/$2/copyright"
}
notice libc6 libc6
notice gcc-14-base libgcc-s1
notice gcc-14-base libatomic1

printf '%s\n' 'root:x:0:0:root:/:/bin/false' 'nobody:x:65534:65534:nobody:/home/soloist:/bin/false' >"$tree/etc/passwd"
printf '%s\n' 'root:x:0:' 'nogroup:x:65534:' >"$tree/etc/group"
printf '%s\n' 'passwd: files' 'group: files' 'hosts: files dns' >"$tree/etc/nsswitch.conf"
printf '%s\n' '127.0.0.1 localhost' '::1 localhost' >"$tree/etc/hosts"
chmod 0644 "$tree/etc/passwd" "$tree/etc/group" "$tree/etc/nsswitch.conf" "$tree/etc/hosts"

# the jail binds the build, the devices, the host's resolv.conf and the ca bundle over these empty files, and a fresh
# proc, soloist's home and its pcm socket's directory over these empty directories
files="soloist dev/null dev/urandom dev/zero etc/resolv.conf etc/ssl/certs/ca-certificates.crt"
directories="proc home/soloist run/cadence"
for placeholder in $files; do
  install -m 0644 /dev/null "$tree/$placeholder"
done

[ -z "$(find "$tree" -type l)" ] || fail "the tree holds symlinks: $(find "$tree" -type l | tr '\n' ' ')"
[ -z "$(find "$tree" ! -type f ! -type d)" ] || fail "the tree holds what is neither a file nor a directory"
for placeholder in $files; do
  [ -f "$tree/$placeholder" ] && [ ! -s "$tree/$placeholder" ] || fail "$placeholder is not an empty file"
done
for placeholder in $directories; do
  [ -d "$tree/$placeholder" ] && [ -z "$(ls -A "$tree/$placeholder")" ] || fail "$placeholder is not an empty directory"
done
[ -x "$tree/$loader" ] || fail "$loader is not executable"

# root owned whoever builds, in a fixed order with a fixed time: the same tree is the same bytes
mkdir -p "$(dirname "$out")"
tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --format=gnu -cf "$out.next" -C "$tree" . ||
  fail "cannot write $out"
mv "$out.next" "$out"
echo "assemble-soloist-runtime.sh: $arch: $(find "$tree" -type f | wc -l) files, $(wc -c <"$out") bytes," \
  "libpulse.so.0 exports $(wc -l <"$work/exported") names, glibc ${newest:-none} at most"
