#!/usr/bin/env bash
# check-device-limits.sh [--machine NAME] [--vendor-libs DIR] [--board rpi|vk-capsula-mini] <tree>: refuses a device tree, laid out as its tarball is,
# whose programs would not run on the board's userland: pickle's (M2-D10), or with --machine ARM midi's armhf one
# (M2m-D6): glibc 2.36 on pickle and 2.30 on midi (its firmware's, which the first install found: a midi audiod that
# needed 2.36 never started), openssl 3.0 and no libstdc++ on both. bin/cadenced must be static (no PT_INTERP, no shared
# library). every other elf file under bin/ (cadence-audiod, ffmpeg's libraries in bin/lib) runs with the device's
# loader, so it may need no glibc symbol newer than the board's, no GLIBCXX or CXXABI version (libstdc++ is linked in),
# no OPENSSL version newer than OPENSSL_3.0.0, and only libraries the device has or bin/lib holds, found through
# runpaths under $ORIGIN. an elf file readelf cannot read cleanly (truncated, corrupt) is refused. with --machine, every
# one must be for that machine as readelf names it.
# midi (--machine ARM) has librt and maybe libatomic too, but takes openssl from bin/lib (M2m-R4), whose libssl may
# need of the libcrypto beside it any version that one defines; its elf files are 32-bit little endian, none
# soft-float, and all but cadenced hard-float (go writes no float abi into its static programs), and its loader is
# /lib/ld-linux-armhf.so.3. --vendor-libs DIR lets audiod's runpath name DIR, the one absolute entry, as a DT_RPATH
# and after an entry that reaches bin/lib: the loader searches a DT_RPATH for what the stock library audiod dlopens
# needs itself, never a DT_RUNPATH, and bin/lib's own libraries come first.
# --board rpi checks a raspberry pi's debian 13 trixie arm64 instead: glibc 2.41, openssl 3.5 and alsa from its apt,
# its loader /lib/ld-linux-aarch64.so.1 (pass --machine AArch64 too).
# --board vk-capsula-mini checks the vk capsula mini's aarch64 buildroot: glibc 2.28 (the oldest firmware of the
# board), its loader /lib/ld-linux-aarch64.so.1, alsa from the device and openssl 3.0 from bin/lib alone (the
# firmware's is 1.1); every runpath stays under $ORIGIN, so it takes no --vendor-libs, and its machine is AArch64.
# soloist's and airplay's runtimes bring their own loader and are checked by their own scripts, not here. prints one
# line per elf file; every refusal is listed before the script fails
set -euo pipefail
export LC_ALL=C

usage() {
  echo "usage: deploy/builder/check-device-limits.sh [--machine NAME] [--vendor-libs DIR] [--board rpi|vk-capsula-mini]" \
    "<tree>" >&2
  exit 2
}

machine='' vendor='' board=''
while [ "$#" -gt 1 ]; do
  case $1 in
    --machine) machine=$2 ;;
    --vendor-libs) vendor=$2 ;;
    --board) board=$2 ;;
    *) break ;;
  esac
  shift 2
done
[ "$#" -eq 1 ] || usage
tree=${1%/}
[ -d "$tree/bin" ] || { echo "check-device-limits.sh: $tree has no bin/" >&2; exit 1; }

glibc_limit=2.36
openssl_limit=3.0.0
refusals=0

# what the board's userland has: its name in refusals, its loader, the libraries beyond glibc's own it provides, and
# whose openssl the tree's programs get
device=pickle loader=ld-linux-aarch64.so.1 interp='' provided=' libasound.so.2 libssl.so.3 libcrypto.so.3 ' ssl=pickle
if [ "$machine" = ARM ]; then
  device=midi loader=ld-linux-armhf.so.3 interp=/lib/ld-linux-armhf.so.3 ssl=bin/lib glibc_limit=2.30
  provided=' libasound.so.2 librt.so.1 libatomic.so.1 '
fi
case $board in
  '') ;;
  rpi)
    device=rpi interp=/lib/ld-linux-aarch64.so.1 glibc_limit=2.41 openssl_limit=3.5.0
    provided=' libasound.so.2 libssl.so.3 libcrypto.so.3 ' ssl=rpi
    ;;
  vk-capsula-mini)
    [ -z "$machine" ] || [ "$machine" = AArch64 ] \
      || { echo "check-device-limits.sh: vk-capsula-mini is AArch64, not $machine" >&2; exit 2; }
    [ -z "$vendor" ] || { echo "check-device-limits.sh: vk-capsula-mini takes no --vendor-libs" >&2; exit 2; }
    machine=AArch64 device=vk-capsula-mini loader=ld-linux-aarch64.so.1 interp=/lib/ld-linux-aarch64.so.1
    glibc_limit=2.28 provided=' libasound.so.2 ' ssl=bin/lib
    ;;
  *) echo "check-device-limits.sh: no limits for board $board" >&2; exit 2 ;;
esac

refuse() {
  echo "check-device-limits.sh: $1: $2" >&2
  refusals=$((refusals + 1))
}

is_elf() { [ "$(head -c 4 "$1" | od -An -tx1 | tr -d ' \n')" = 7f454c46 ]; }

# newer VERSION LIMIT: VERSION is above LIMIT, or no dotted number at all (GLIBC_PRIVATE)
newer() {
  [[ $1 =~ ^[0-9]+(\.[0-9]+)*$ ]] || return 0
  [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -n 1)" != "$2" ]
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# read_elf FILE: readelf's file header, program headers, dynamic section and symbol versions of FILE into header,
# program, dynamic and versions, each read whole before anything greps it. readelf reports a truncated or corrupt file
# on stderr yet may exit 0, so anything on stderr fails too, its first line in $work/readelf.err
read_elf() {
  local flag output
  for flag in h l d V; do
    if ! output=$(readelf "-${flag}W" "$1" 2>"$work/readelf.err") || [ -s "$work/readelf.err" ]; then
      [ -s "$work/readelf.err" ] || echo "readelf -$flag failed" >"$work/readelf.err"
      return 1
    fi
    case $flag in
      h) header=$output ;;
      l) program=$output ;;
      d) dynamic=$output ;;
      V) versions=$output ;;
    esac
  done
}

# highest PREFIX: the highest PREFIX_ symbol version the file in $versions needs from any library, if any
highest() {
  awk -v prefix="$1_" '
    /^Version needs section/ { needs = 1; next }
    /^Version (definition|symbols) section/ { needs = 0 }
    needs { for (i = 1; i < NF; i++) if ($i == "Name:" && index($(i + 1), prefix) == 1) print $(i + 1) }' \
    <<<"$versions" | sort -uV | tail -n 1
}

# defines PREFIX FILE: the highest PREFIX_ symbol version FILE defines, if any (none for a missing file)
defines() {
  { readelf -VW "$2" 2>/dev/null || true; } | awk -v prefix="$1_" '
    /^Version definition section/ { defs = 1; next }
    /^Version (needs|symbols) section/ { defs = 0 }
    defs { for (i = 1; i < NF; i++) if ($i == "Name:" && index($(i + 1), prefix) == 1) print $(i + 1) }' |
    sort -uV | tail -n 1
}

header='' program='' dynamic='' versions='' libdir=''
[ ! -d "$tree/bin/lib" ] || libdir=$(cd "$tree/bin/lib" && pwd -P)

# check_arm REL: midi's abi from the file header in $header: elf32, little endian, eabi floats in vfp registers
# (EF_ARM_ABI_FLOAT_HARD, 0x400 of e_flags) and never soft-float (EF_ARM_ABI_FLOAT_SOFT, 0x200)
check_arm() {
  local rel=$1 class data flags
  class=$(sed -n 's/^ *Class: *//p' <<<"$header")
  data=$(sed -n 's/^ *Data: *//p' <<<"$header")
  flags=$(sed -n 's/^ *Flags: *\(0x[0-9a-f]*\).*/\1/p' <<<"$header")
  [ "$class" = ELF32 ] || refuse "$rel" "is $class, not ELF32"
  [[ $data == *'little endian' ]] || refuse "$rel" "is $data, not little endian"
  if [ -z "$flags" ]; then
    refuse "$rel" "has no e_flags readelf can read"
  elif ((flags & 0x200)); then
    refuse "$rel" "is soft-float (e_flags $flags), $device is hard-float"
  elif [ "$rel" != bin/cadenced ] && ! ((flags & 0x400)); then
    refuse "$rel" "is not hard-float (e_flags $flags)"
  fi
}

check_static() {
  local rel=$1
  if grep -qE '^\s+INTERP\s' <<<"$program"; then
    refuse "$rel" "has PT_INTERP, so it is not static"
  elif grep -qF '(NEEDED)' <<<"$dynamic"; then
    refuse "$rel" "needs shared libraries, so it is not static"
  else
    echo "check-device-limits.sh: $rel: static"
  fi
}

check_dynamic() {
  local rel=$1 dir glibc openssl ssl_owner=$ssl ssl_limit=$openssl_limit version tag paths entry asked reaches='' \
    library needed runpath=()
  dir=$(dirname "$tree/$rel")
  glibc=$(highest GLIBC)
  if [ -n "$glibc" ] && newer "${glibc#GLIBC_}" "$glibc_limit"; then
    refuse "$rel" "needs $glibc, $device has GLIBC_$glibc_limit"
  fi
  for version in "$(highest GLIBCXX)" "$(highest CXXABI)"; do
    [ -z "$version" ] || refuse "$rel" "needs $version: libstdc++ must be linked in"
  done
  openssl=$(highest OPENSSL)
  # bin/lib's openssl is one debian package: its libssl may need what the libcrypto beside it defines
  if [ "$ssl" = bin/lib ] && { [ "$rel" = bin/lib/libssl.so.3 ] || [ "$rel" = bin/lib/libcrypto.so.3 ]; }; then
    ssl_owner=bin/lib/libcrypto.so.3
    ssl_limit=$(defines OPENSSL "$libdir/libcrypto.so.3")
    ssl_limit=${ssl_limit#OPENSSL_}
  fi
  if [ -n "$openssl" ] && newer "${openssl#OPENSSL_}" "${ssl_limit:-0}"; then
    refuse "$rel" "needs $openssl, $ssl_owner has OPENSSL_${ssl_limit:-none}"
  fi
  if [ -n "$interp" ]; then
    asked=$(sed -n 's/.*\[Requesting program interpreter: \(.*\)\]$/\1/p' <<<"$program")
    [ -z "$asked" ] || [ "$asked" = "$interp" ] || refuse "$rel" "asks for the loader $asked, $device's is $interp"
  fi

  while read -r tag paths; do
    IFS=: read -ra runpath <<<"$paths"
    for entry in "${runpath[@]}"; do
      # shellcheck disable=SC2016 # $ORIGIN is the loader's, never the shell's
      case $entry in
        '$ORIGIN' | '$ORIGIN/'*)
          if [ -n "$libdir" ] && [ "$(cd "$dir${entry#'$ORIGIN'}" 2>/dev/null && pwd -P)" = "$libdir" ]; then
            reaches=1
          fi
          ;;
        *)
          if [ -z "$vendor" ] || [ "$entry" != "$vendor" ] || [ "$rel" != bin/cadence-audiod ]; then
            refuse "$rel" "runpath $entry is outside \$ORIGIN"
          elif [ "$tag" != RPATH ]; then
            refuse "$rel" "runpath $entry is a DT_RUNPATH, which the loader never searches for a dlopened library's own"
          elif [ -z "$reaches" ]; then
            refuse "$rel" "runpath $entry comes before bin/lib, so a vendor library of a name bin/lib holds would win"
          fi
          ;;
      esac
    done
  done < <(sed -n 's/.*(\(RUNPATH\|RPATH\)).*\[\(.*\)\]$/\1 \2/p' <<<"$dynamic")

  needed=$(sed -n 's/.*(NEEDED).*\[\(.*\)\]$/\1/p' <<<"$dynamic" | tr '\n' ' ')
  for library in $needed; do
    case $library in
      libc.so.6 | libm.so.6 | libpthread.so.0 | libdl.so.2 | "$loader") continue ;;
    esac
    [[ $provided != *" $library "* ]] || continue
    case $library in
      libssl.so.3 | libcrypto.so.3 | libavformat.so.[0-9]* | libavcodec.so.[0-9]* | libavutil.so.[0-9]* | \
        libswresample.so.[0-9]*)
        if [ -z "$reaches" ]; then
          refuse "$rel" "needs $library, but no runpath reaches bin/lib"
        elif [ ! -f "$libdir/$library" ]; then
          refuse "$rel" "needs $library, which bin/lib does not hold"
        fi
        ;;
      *) refuse "$rel" "needs $library, which $device does not have" ;;
    esac
  done
  echo "check-device-limits.sh: $rel: ${glibc:-no GLIBC}, ${openssl:-no OPENSSL}; needs ${needed% }"
}

for rel in bin/cadenced bin/cadence-audiod; do
  if [ ! -f "$tree/$rel" ]; then
    refuse "$rel" "is missing"
  elif ! is_elf "$tree/$rel"; then
    refuse "$rel" "is not an elf file"
  fi
done

while IFS= read -r -d '' file; do
  rel=${file#"$tree"/}
  if [ ! -f "$file" ] || [ -L "$file" ]; then
    refuse "$rel" "is not a regular file"
    continue
  fi
  is_elf "$file" || continue
  if ! read_elf "$file"; then
    refuse "$rel" "readelf cannot read it: $(head -n 1 "$work/readelf.err")"
    continue
  fi
  if [ -n "$machine" ]; then
    actual=$(sed -n 's/^ *Machine: *//p' <<<"$header")
    if [ "$actual" != "$machine" ]; then
      refuse "$rel" "is for $actual, not $machine"
    elif [ "$machine" = ARM ]; then
      check_arm "$rel"
    fi
  fi
  if [ "$rel" = bin/cadenced ]; then check_static "$rel"; else check_dynamic "$rel"; fi
done < <(find "$tree/bin" ! -type d -print0 | sort -z)

if [ "$refusals" -ne 0 ]; then
  echo "check-device-limits.sh: $refusals refused" >&2
  exit 1
fi
echo "check-device-limits.sh: ok"
