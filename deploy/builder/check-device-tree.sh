#!/usr/bin/env bash
# check-device-tree.sh <board> <dir>: refuses an unpacked device tarball (M2-D10, M2m-D6) that lacks a file the board's
# install needs: each must be a non-empty regular file MANIFEST lists. both boards need the programs, the ca bundle, the
# boot wrapper share/cadence-run (mode 755) with their hook beside it, share/board-hook (mode 644, which the wrapper
# sources), both runtimes and the notices; midi also its openssl in bin/lib with openssl's notices, the notice of the
# libc++ zig links into its audiod, and the runtimes' loaders. the Lite and the vk capsula mini need what midi does
# but the runtimes, the vk capsula mini also tensorflow lite micro's, flatbuffers' and cmsis-nn's notices. every
# refusal is listed before the script fails
set -euo pipefail
export LC_ALL=C

[ "$#" -eq 2 ] || { echo "usage: deploy/builder/check-device-tree.sh <board> <dir>" >&2; exit 2; }
board=$1
dir=${2%/}

speaker=(share/ca-certificates.crt share/cadence-run share/board-hook)
speaker_media=(soloist/runtime/usr/lib/aarch64-linux-gnu/libpulse.so.0 airplay/runtime/bin/shairport-sync
  airplay/runtime/licenses/nqptp/SOURCE)
required=(bin/cadenced bin/cadence-audiod licenses/README licenses/ffmpeg/COPYING.LGPLv2.1
  licenses/ffmpeg/COPYING.LGPLv3 licenses/go/LICENSE)
case $board in
  yandex-pickle) required+=("${speaker[@]}" "${speaker_media[@]}") ;;
  yandex-midi)
    required+=("${speaker[@]}" "${speaker_media[@]}" bin/lib/libssl.so.3 bin/lib/libcrypto.so.3 licenses/openssl/copyright
      licenses/openssl/Apache-2.0 licenses/llvm/libcxx/LICENSE.TXT soloist/runtime/lib/ld-linux-aarch64.so.1
      airplay/runtime/lib/ld-linux-aarch64.so.1)
    ;;
  yandex-micro | vk-capsula-mini)
    required+=("${speaker[@]}" bin/lib/libssl.so.3 bin/lib/libcrypto.so.3 licenses/openssl/copyright
      licenses/openssl/Apache-2.0 licenses/llvm/libcxx/LICENSE.TXT)
    # the keyword engine's tensorflow lite micro, flatbuffers and cmsis-nn, built into its audiod
    [ "$board" != vk-capsula-mini ] ||
      required+=(licenses/tflite-micro/LICENSE licenses/flatbuffers/LICENSE licenses/cmsis-nn/LICENSE)
    ;;
  *)
    echo "check-device-tree.sh: no required files for board $board" >&2
    exit 1
    ;;
esac

refusals=0
refuse() {
  echo "check-device-tree.sh: $1" >&2
  refusals=$((refusals + 1))
}

listed=$(grep -E '^[0-9a-f]{64}  ' "$dir/MANIFEST" 2>/dev/null | cut -c67- || true)
for file in "${required[@]}"; do
  if [ ! -f "$dir/$file" ] || [ -L "$dir/$file" ] || [ ! -s "$dir/$file" ]; then
    refuse "$file is missing or empty"
  elif ! grep -qxF -- "$file" <<<"$listed"; then
    refuse "$file is not in MANIFEST"
  fi
done

# mode FILE MODE: FILE, when there, has MODE
mode() {
  local actual
  [ -f "$dir/$1" ] || return 0
  actual=$(stat -c %a "$dir/$1")
  [ "$actual" = "$2" ] || refuse "$1 is mode $actual, not $2"
}
mode share/cadence-run 755
mode share/board-hook 644
mode install.sh 755
mode share/systemd/cadence.service 644
mode share/apt-packages 644

if [ "$refusals" -ne 0 ]; then
  echo "check-device-tree.sh: $refusals refused" >&2
  exit 1
fi
echo "check-device-tree.sh: $board: ok, ${#required[@]} required files"
