#!/bin/sh
# usage: fetch.sh <out-dir> [cache-dir]
# projectM's source at its pin with projectm-eval in vendor/ and our patches applied, in <out-dir>/src (made afresh);
# tarballs are kept in the cache (default <out-dir>/dl) and checked by sha-256 on every run
set -eu
here=$(cd "$(dirname "$0")" && pwd)
. "$here/pins.conf"
out=$1
cache=${2:-$out/dl}
mkdir -p "$out" "$cache"

# fetch <repo> <commit> <sha256>: prints the checked tarball's path
fetch() {
  f="$cache/$(basename "$1")-$2.tar.gz"
  if [ ! -f "$f" ]; then
    curl --fail --silent --show-error --location --output "$f.part" "https://github.com/$1/archive/$2.tar.gz"
    mv "$f.part" "$f"
  fi
  if ! echo "$3  $f" | sha256sum --check --strict --status - 2>/dev/null &&
    ! echo "$3  $f" | shasum -a 256 --check --status - 2>/dev/null; then
    echo "fetch.sh: sha-256 mismatch: $f" >&2
    rm -f "$f"
    exit 1
  fi
  echo "$f"
}

pm=$(fetch "$PROJECTM_REPO" "$PROJECTM_COMMIT" "$PROJECTM_SHA256")
ev=$(fetch "$PROJECTM_EVAL_REPO" "$PROJECTM_EVAL_COMMIT" "$PROJECTM_EVAL_SHA256")
rm -rf "$out/src"
mkdir -p "$out/src"
tar -xzf "$pm" -C "$out/src" --strip-components=1
rm -rf "$out/src/vendor/projectm-eval"
mkdir -p "$out/src/vendor/projectm-eval"
tar -xzf "$ev" -C "$out/src/vendor/projectm-eval" --strip-components=1
for p in "$here"/patches/*.patch; do
  (cd "$out/src" && patch -p1 --quiet --forward < "$p")
done
echo "$out/src"
