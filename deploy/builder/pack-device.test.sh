#!/usr/bin/env bash
# tests of pack-device.sh, run natively by make device-limits-test (in make ci): a small tree packed twice gives the
# same bytes, MANIFEST verifies the unpacked tree and names every file, and a symlink refuses the tree
set -euo pipefail
export LC_ALL=C

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script=$here/pack-device.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
failures=0

pass() { echo "ok - $1"; }
flunk() {
  echo "not ok - $1" >&2
  failures=$((failures + 1))
}

tree=$tmp/tree
mkdir -p "$tree/bin/lib" "$tree/licenses/a b" "$tree/soloist/runtime/home"
printf 'elf\n' >"$tree/bin/cadenced"
printf 'lib\n' >"$tree/bin/lib/libavutil.so.61"
printf 'text\n' >"$tree/licenses/a b/COPYING"
: >"$tree/soloist/runtime/placeholder"
chmod 755 "$tree/bin/cadenced"
chmod 644 "$tree/bin/lib/libavutil.so.61" "$tree/licenses/a b/COPYING" "$tree/soloist/runtime/placeholder"

bash "$script" "$tree" "$tmp/first.tar" "cadence 1234" "board test" >"$tmp/first.log" 2>&1 ||
  { cat "$tmp/first.log" >&2; flunk "a tree packs"; }
touch -d '2020-02-02 02:02' "$tree/bin/cadenced" "$tree/licenses/a b"
bash "$script" "$tree" "$tmp/second.tar" "cadence 1234" "board test" >"$tmp/second.log" 2>&1 ||
  { cat "$tmp/second.log" >&2; flunk "a tree packs again"; }
if [ -s "$tmp/first.tar" ] && cmp -s "$tmp/first.tar" "$tmp/second.tar"; then
  pass "the same tree packs to the same bytes, whatever its times"
else
  flunk "the same tree packs to the same bytes, whatever its times"
fi

listing=$(tar -tvf "$tmp/first.tar" --numeric-owner)
if [ "$(awk '$2 != "0/0" || $4 != "1970-01-01" || $5 != "00:00"' <<<"$listing")" = "" ] &&
  [ "$(awk 'NR == 1 { print $NF }' <<<"$listing")" = MANIFEST ] &&
  grep -qE '^-rwxr-xr-x .* bin/cadenced$' <<<"$listing" && ! grep -q ' \./' <<<"$listing"; then
  pass "entries are owned by 0:0 at time 0, keep their modes, MANIFEST first and no ./ prefix"
else
  echo "$listing" >&2
  flunk "entries are owned by 0:0 at time 0, keep their modes, MANIFEST first and no ./ prefix"
fi

mkdir "$tmp/unpacked"
tar -xf "$tmp/first.tar" -C "$tmp/unpacked"
manifest=$tmp/unpacked/MANIFEST
if (cd "$tmp/unpacked" && sha256sum --check --strict --quiet MANIFEST) &&
  [ "$(grep -c '^[0-9a-f]\{64\}  ' "$manifest")" -eq 4 ] &&
  [ "$(sed -n '1,2p' "$manifest")" = "$(printf '# cadence 1234\n# board test')" ] &&
  grep -qx '[0-9a-f]\{64\}  licenses/a b/COPYING' "$manifest" && [ ! -e "$tree/MANIFEST" ]; then
  pass "MANIFEST holds the headers, then verifies every file of the tree, and the tree stays as it was"
else
  cat "$manifest" >&2
  flunk "MANIFEST holds the headers, then verifies every file of the tree, and the tree stays as it was"
fi

ln -s ../bin/cadenced "$tree/licenses/link"
if bash "$script" "$tree" "$tmp/link.tar" >"$tmp/link.log" 2>&1; then
  flunk "a symlink refuses the tree"
elif ! grep -qF "licenses/link" "$tmp/link.log" || [ -e "$tmp/link.tar" ]; then
  cat "$tmp/link.log" >&2
  flunk "a symlink refuses the tree, naming it, and no tar is written"
else
  pass "a symlink refuses the tree, naming it, and no tar is written"
fi

if [ "$failures" -ne 0 ]; then
  echo "pack-device.test.sh: $failures failed" >&2
  exit 1
fi
echo "pack-device.test.sh: all passed"
