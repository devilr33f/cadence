#!/bin/sh
# usage: fetch-transitions.sh <out-dir> [list] [cache-dir]
# the transitions a list names (default transitions.txt) from gl-transitions (MIT, each file carries its header) at a
# pinned commit, checked by sha-256, as <out-dir>/<Name>.glsl; fetched at build time, never committed. a name the
# pack lacks fails the run
set -eu
here=$(cd "$(dirname "$0")" && pwd)
GLT_REPO=gl-transitions/gl-transitions
GLT_COMMIT=d924b9e2db2917bd2d8d60df7bdbc8e98239f42d
GLT_SHA256=e0808f984f0e34821cc2d0c1a0291c6dd18aed1ac26f4b4af5e50a51107ed9dd
out=$1
list=${2:-$here/transitions.txt}
cache=${3:-$out/../dl}
mkdir -p "$out" "$cache"
f="$cache/gl-transitions-$GLT_COMMIT.tar.gz"
if [ ! -f "$f" ]; then
  curl --fail --silent --show-error --location --output "$f.part" "https://github.com/$GLT_REPO/archive/$GLT_COMMIT.tar.gz"
  mv "$f.part" "$f"
fi
if ! echo "$GLT_SHA256  $f" | sha256sum --check --strict --status - 2>/dev/null &&
  ! echo "$GLT_SHA256  $f" | shasum -a 256 --check --status - 2>/dev/null; then
  echo "fetch-transitions.sh: sha-256 mismatch: $f" >&2
  rm -f "$f"
  exit 1
fi
names=$(mktemp)
trap 'rm -f "$names"' EXIT
grep -v -e '^#' -e '^$' "$list" | sed "s|^|gl-transitions-$GLT_COMMIT/transitions/|; s|\$|.glsl|" > "$names"
[ -s "$names" ] || { echo "$(basename "$0"): no names in $list" >&2; exit 1; }
tar -xzf "$f" -C "$out" --strip-components=2 -T "$names"
echo "fetch-transitions.sh: $(wc -l < "$names" | tr -d ' ') transitions in $out"
