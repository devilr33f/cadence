#!/usr/bin/env bash
# tests of check-device-tree.sh, run natively by make device-limits-test (in make ci): each board's tree, packed by
# pack-device.sh and unpacked as device.Dockerfile does, must hold its required files, both boards' boot wrapper and
# hook among them, each listed in MANIFEST, the wrapper mode 755 and the hook 644
set -euo pipefail
export LC_ALL=C

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script=$here/check-device-tree.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failures=0

pass() { echo "ok - $1"; }
flunk() {
  echo "not ok - $1" >&2
  failures=$((failures + 1))
}

base=(bin/cadenced bin/cadence-audiod licenses/README licenses/ffmpeg/COPYING.LGPLv2.1 licenses/ffmpeg/COPYING.LGPLv3
  licenses/go/LICENSE)
common=("${base[@]}" share/ca-certificates.crt soloist/runtime/usr/lib/aarch64-linux-gnu/libpulse.so.0
  airplay/runtime/bin/shairport-sync airplay/runtime/licenses/nqptp/SOURCE)
midi=(bin/lib/libssl.so.3 bin/lib/libcrypto.so.3 licenses/openssl/copyright licenses/openssl/Apache-2.0
  licenses/llvm/libcxx/LICENSE.TXT soloist/runtime/lib/ld-linux-aarch64.so.1 airplay/runtime/lib/ld-linux-aarch64.so.1)
# the vk capsula mini: midi's openssl and libc++ notices, the keyword engine's, no runtimes
vk=("${base[@]}" share/ca-certificates.crt bin/lib/libssl.so.3 bin/lib/libcrypto.so.3 licenses/openssl/copyright
  licenses/openssl/Apache-2.0 licenses/llvm/libcxx/LICENSE.TXT licenses/tflite-micro/LICENSE
  licenses/flatbuffers/LICENSE licenses/cmsis-nn/LICENSE)

# unpacked NAME BOARD [BEFORE [AFTER]]: the board's whole tree with BEFORE run in it, packed, unpacked into $tmp/NAME
# and AFTER run there
unpacked() {
  local name=$1 board=$2 before=${3:-} after=${4:-} tree=$tmp/$1.tree file files
  files=("${common[@]}")
  [ "$board" != yandex-midi ] || files+=("${midi[@]}")
  [ "$board" != vk-capsula-mini ] || files=("${vk[@]}")
  for file in "${files[@]}"; do
    mkdir -p "$tree/$(dirname "$file")"
    echo "$file" >"$tree/$file"
  done
  mkdir -p "$tree/share"
  printf '#!/bin/sh\n' >"$tree/share/cadence-run"
  printf 'board_blank() { :; }\n' >"$tree/share/board-hook"
  chmod 755 "$tree/share/cadence-run"
  chmod 644 "$tree/share/board-hook"
  (cd "$tree" && eval "$before")
  bash "$here/pack-device.sh" "$tree" "$tmp/$name.tar" "board $board" >/dev/null
  mkdir "$tmp/$name"
  tar -xf "$tmp/$name.tar" -C "$tmp/$name"
  (cd "$tmp/$name" && eval "$after")
}

# accepted NAME BOARD: the script passes the unpacked tree NAME for BOARD
accepted() {
  if bash "$script" "$2" "$tmp/$1" >"$tmp/$1.log" 2>&1 && grep -qF "$2: ok" "$tmp/$1.log"; then
    pass "$1: accepted"
  else
    cat "$tmp/$1.log" >&2
    flunk "$1: accepted"
  fi
}

# refused NAME BOARD MESSAGE: the script fails on the unpacked tree NAME for BOARD, naming MESSAGE
refused() {
  if bash "$script" "$2" "$tmp/$1" >"$tmp/$1.log" 2>&1; then
    cat "$tmp/$1.log" >&2
    flunk "$1: refused"
  elif ! grep -qF -- "$3" "$tmp/$1.log"; then
    cat "$tmp/$1.log" >&2
    flunk "$1: refused for $3"
  else
    pass "$1: refused for $3"
  fi
}

unpacked pickle yandex-pickle
accepted pickle yandex-pickle
unpacked midi yandex-midi
accepted midi yandex-midi

# both boards ship the boot wrapper and their hook beside it
unpacked picklehook yandex-pickle 'rm share/board-hook'
refused picklehook yandex-pickle "share/board-hook is missing or empty"
unpacked pickleempty yandex-pickle ': >share/board-hook'
refused pickleempty yandex-pickle "share/board-hook is missing or empty"
unpacked midiwrapper yandex-midi 'rm share/cadence-run'
refused midiwrapper yandex-midi "share/cadence-run is missing or empty"
unpacked midihook yandex-midi 'rm share/board-hook'
refused midihook yandex-midi "share/board-hook is missing or empty"

# openssl's licence text beside its copyright
unpacked midiapache yandex-midi 'rm licenses/openssl/Apache-2.0'
refused midiapache yandex-midi "licenses/openssl/Apache-2.0 is missing or empty"

# the notice of the libc++ linked into midi's audiod
unpacked midillvm yandex-midi 'rm licenses/llvm/libcxx/LICENSE.TXT'
refused midillvm yandex-midi "licenses/llvm/libcxx/LICENSE.TXT is missing or empty"
# pickle needs none of midi's
unpacked picklemidi yandex-pickle
refused picklemidi yandex-midi "bin/lib/libssl.so.3 is missing or empty"

unpacked wrappermode yandex-midi 'chmod 644 share/cadence-run'
refused wrappermode yandex-midi "share/cadence-run is mode 644, not 755"
unpacked hookmode yandex-pickle 'chmod 755 share/board-hook'
refused hookmode yandex-pickle "share/board-hook is mode 755, not 644"

unpacked unlisted yandex-midi '' "sed -i '/  share\/board-hook\$/d' MANIFEST"
refused unlisted yandex-midi "share/board-hook is not in MANIFEST"

# vk capsula mini: no runtimes, openssl in bin/lib with its notices, the libc++ notice, and tensorflow lite micro's,
# flatbuffers' and cmsis-nn's
unpacked vk vk-capsula-mini
accepted vk vk-capsula-mini
unpacked vktflm vk-capsula-mini 'rm licenses/tflite-micro/LICENSE'
refused vktflm vk-capsula-mini "licenses/tflite-micro/LICENSE is missing or empty"
unpacked vkflatbuffers vk-capsula-mini ': >licenses/flatbuffers/LICENSE'
refused vkflatbuffers vk-capsula-mini "licenses/flatbuffers/LICENSE is missing or empty"
unpacked vkcmsisnn vk-capsula-mini 'rm licenses/cmsis-nn/LICENSE'
refused vkcmsisnn vk-capsula-mini "licenses/cmsis-nn/LICENSE is missing or empty"
unpacked vkssl vk-capsula-mini 'rm bin/lib/libssl.so.3'
refused vkssl vk-capsula-mini "bin/lib/libssl.so.3 is missing or empty"
unpacked vkcrypto vk-capsula-mini ': >bin/lib/libcrypto.so.3'
refused vkcrypto vk-capsula-mini "bin/lib/libcrypto.so.3 is missing or empty"
unpacked vkllvm vk-capsula-mini 'rm licenses/llvm/libcxx/LICENSE.TXT'
refused vkllvm vk-capsula-mini "licenses/llvm/libcxx/LICENSE.TXT is missing or empty"
unpacked vkhook vk-capsula-mini 'rm share/board-hook'
refused vkhook vk-capsula-mini "share/board-hook is missing or empty"
# pickle's tree lacks bin/lib's openssl
refused pickle vk-capsula-mini "bin/lib/libssl.so.3 is missing or empty"

unpacked other yandex-pickle
refused other yandex-other "no required files for board yandex-other"

if [ "$failures" -ne 0 ]; then
  echo "check-device-tree.test.sh: $failures failed" >&2
  exit 1
fi
echo "check-device-tree.test.sh: all passed"
