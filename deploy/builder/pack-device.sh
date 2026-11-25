#!/usr/bin/env bash
# pack-device.sh <tree> <out.tar> [header...]: a device tarball for the installer (M2-D10): MANIFEST first, then the tree
# as it is. MANIFEST holds each header as a "# " line, then the sha256 and path of every regular file as sha256sum
# prints them, so `sha256sum --check MANIFEST` verifies an unpacked tree (it skips the "#" lines). deterministic, with
# the runtimes' tar flags: names sorted, owner 0:0, time 0, gnu format, no ./ prefix. a symlink or any other special
# file refuses the tree, so MANIFEST names everything it holds. the tree itself is left unchanged
set -euo pipefail
export LC_ALL=C
umask 022

usage() {
  echo "usage: deploy/builder/pack-device.sh <tree> <out.tar> [header...]" >&2
  exit 2
}

fail() {
  echo "pack-device.sh: $*" >&2
  exit 1
}

[ "$#" -ge 2 ] || usage
tree=${1%/}
out=$2
shift 2
[ -d "$tree" ] || fail "$tree is not a directory"
[ ! -e "$tree/MANIFEST" ] || fail "$tree already holds a MANIFEST"
special=$(cd "$tree" && find . -mindepth 1 ! -type f ! -type d -printf '%P\n')
[ -z "$special" ] || fail "not a regular file or directory: $special"

work=$(mktemp -d)
trap 'rm -rf "$work" "$out.next"' EXIT
{
  for header in "$@"; do printf '# %s\n' "$header"; done
  (cd "$tree" && find . -type f -printf '%P\0' | sort -z | xargs -0 -r sha256sum --)
} >"$work/MANIFEST"
chmod 644 "$work/MANIFEST"

top=()
while IFS= read -r -d '' name; do top+=("$name"); done < <(find "$tree" -mindepth 1 -maxdepth 1 -printf '%P\0' | sort -z)
tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --format=gnu -cf "$out.next" \
  -C "$work" MANIFEST -C "$tree" -- "${top[@]}" || fail "cannot write $out"
mv "$out.next" "$out"
