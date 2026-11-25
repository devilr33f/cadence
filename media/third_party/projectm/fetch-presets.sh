#!/bin/sh
# usage: fetch-presets.sh <out-dir> [list] [cache-dir]
# the presets a list names (default presets.txt: the shipped, measured ones; presets.candidates for measuring), from the
# cream-of-the-crop pack at its pin, checked by sha-256, into <out-dir> keeping their folders. the pack carries no
# explicit license, so it is fetched at build time and never committed. a name the pack lacks fails the run
set -eu
here=$(cd "$(dirname "$0")" && pwd)
. "$here/pins.conf"
out=$1
list=${2:-$here/presets.txt}
cache=${3:-$out/../dl}
mkdir -p "$out" "$cache"
f="$cache/presets-$PRESETS_COMMIT.tar.gz"
if [ ! -f "$f" ]; then
  curl --fail --silent --show-error --location --output "$f.part" \
    "https://github.com/$PRESETS_REPO/archive/$PRESETS_COMMIT.tar.gz"
  mv "$f.part" "$f"
fi
if ! echo "$PRESETS_SHA256  $f" | sha256sum --check --strict --status - 2>/dev/null &&
  ! echo "$PRESETS_SHA256  $f" | shasum -a 256 --check --status - 2>/dev/null; then
  echo "fetch-presets.sh: sha-256 mismatch: $f" >&2
  rm -f "$f"
  exit 1
fi
top="presets-cream-of-the-crop-$PRESETS_COMMIT"
# the whole pack into a scratch directory, then each named file copied: tar's member lists are patterns on bsdtar
# (macos), where a name's [ ] would not match itself
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
tar -xzf "$f" -C "$tmp"
n=0
while IFS= read -r name; do
  case $name in '' | '#'*) continue ;; esac
  [ -f "$tmp/$top/$name" ] || { echo "$(basename "$0"): $name is not in the pack" >&2; exit 1; }
  mkdir -p "$out/$(dirname "$name")"
  cp "$tmp/$top/$name" "$out/$name"
  n=$((n + 1))
done < "$list"
[ "$n" -gt 0 ] || { echo "$(basename "$0"): no names in $list" >&2; exit 1; }
# the pack's own statement (its presets carry no explicit license) beside them
cp "$tmp/$top/LICENSE.md" "$out/LICENSE.md"
echo "fetch-presets.sh: $n presets in $out"
