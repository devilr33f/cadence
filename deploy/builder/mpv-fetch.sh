#!/usr/bin/env bash
# fetches the named inputs of media/third_party/mpv/mpv.pins into <dest>: a file as <dest>/<name> once its sha-256 is
# the pinned one, a git repository as <dest>/<name>/ at exactly the pinned commit (its submodules too, for git+sub, at
# the commits that commit names), without its .git. a mismatch fails naming the input and leaves nothing behind
# usage: mpv-fetch.sh <pins> <dest> <name>...
set -euo pipefail

die() { echo "mpv-fetch: $*" >&2; exit 1; }
[ $# -ge 3 ] || die "usage: mpv-fetch.sh <pins> <dest> <name>..."
pins=$1 dest=$2
shift 2
mkdir -p "$dest"

for want in "$@"; do
  line=$(awk -v n="$want" '$1 !~ /^#/ && $2 == n' "$pins")
  [ -n "$line" ] || die "no pin for $want"
  # shellcheck disable=SC2086
  set -- $line
  kind=$1 name=$2 url=$4 pin=$5
  case $kind in
    file)
      curl --fail --silent --show-error --location --retry 5 --retry-all-errors --output "$dest/$name.part" "$url"
      echo "$pin  $dest/$name.part" | sha256sum --check --strict --quiet - \
        || { rm -f "$dest/$name.part"; die "$name: not the pinned sha-256"; }
      mv "$dest/$name.part" "$dest/$name"
      ;;
    git | git+sub)
      tmp=$dest/$name.git
      rm -rf "$tmp"
      git init -q "$tmp"
      git -C "$tmp" fetch -q --depth 1 "$url" "$pin"
      git -C "$tmp" -c advice.detachedHead=false checkout -q FETCH_HEAD
      [ "$(git -C "$tmp" rev-parse HEAD)" = "$pin" ] || { rm -rf "$tmp"; die "$name: not at $pin"; }
      if [ "$kind" = git+sub ]; then
        git -C "$tmp" submodule -q update --init --depth 1 --recursive
      fi
      find "$tmp" -name .git -prune -exec rm -rf {} +
      rm -rf "${dest:?}/$name"
      mv "$tmp" "$dest/$name"
      ;;
    *) die "$name: unknown kind $kind" ;;
  esac
  echo "fetched $name $3"
done
