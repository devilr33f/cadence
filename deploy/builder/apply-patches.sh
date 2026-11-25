#!/usr/bin/env bash
# copies a pristine source tree (a submodule, never patched in place) to <out> and applies each patch series in order:
# every *.patch of a directory in byte order of its name, the directories as given. each patch is checked with
# git apply --check before it is applied, and the first that does not apply stops the run, naming it, with nothing
# written to <out> (the tree is patched beside it and moved in whole), so a build never sees a half-patched tree. --unidiff-zero: android-airplay-server's uxplay series has
# zero-context hunks, and its own gradle task applies them so. git runs in a throwaway repository inside the copy, so
# no path resolves against an enclosing repository (cadence's own, or the submodule's)
# usage: apply-patches.sh <src> <out> <patch-dir>...
set -euo pipefail

die() { echo "apply-patches: $1" >&2; exit "${2:-1}"; }
[ $# -ge 3 ] || die "usage: apply-patches.sh <src> <out> <patch-dir>..." 2
src=$1 out=$2
shift 2

[ -d "$src" ] || die "no source tree $src" 2
src=$(cd "$src" && pwd -P)
mkdir -p "$(dirname "$out")"
out=$(cd "$(dirname "$out")" && pwd -P)/$(basename "$out")
case "$out/" in "$src/"*) die "$out is inside the source tree $src" 2 ;; esac
case "$src/" in "$out/"*) die "the source tree $src is inside $out" 2 ;; esac

patches=()
for dir in "$@"; do
  [ -d "$dir" ] || die "no patch directory $dir" 2
  found=0
  while IFS= read -r name; do
    patches+=("$(cd "$dir" && pwd -P)/$name")
    found=1
  done < <(cd "$dir" && LC_ALL=C ls -1 | LC_ALL=C grep '\.patch$' || true)
  [ "$found" = 1 ] || die "no patches in $dir" 2
done

work=$out.tmp.$$
cleanup() { rm -rf "$work"; }
trap cleanup EXIT
rm -rf "$work"
mkdir -p "$work"
# the copy leaves the source's own repository behind (a submodule's .git is a file naming the superproject's)
tar -C "$src" --exclude=./.git -cf - . | tar -C "$work" -xf -
git -C "$work" init -q
err=$work/.git/apply-error
for p in "${patches[@]}"; do
  name=$(basename "$p")
  if ! git -C "$work" apply --unidiff-zero --check "$p" 2>"$err"; then
    echo "apply-patches: patch does not apply: $name" >&2
    sed 's/^/  /' "$err" >&2
    exit 1
  fi
  git -C "$work" apply --unidiff-zero "$p"
  echo "applied $name"
done
rm -rf "$work/.git"
rm -rf "$out"
mv "$work" "$out"
