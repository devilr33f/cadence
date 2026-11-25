#!/bin/sh
# jq-native.sh never hands out a build whose sha-256 is not the pinned one, fetched (then removed) or found in place
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

assets='jq-windows-amd64.exe jq-linux-amd64 jq-linux-arm64 jq-macos-amd64 jq-macos-arm64'
mkdir "$tmp/release"
for a in $assets; do echo 'not jq' >"$tmp/release/$a"; done
release=$tmp/release
if command -v cygpath >/dev/null 2>&1; then release=$(cygpath -m "$release"); fi
export JQ_RELEASE_URL="file:///${release#/}"

rc=0
out=$(sh "$root/deploy/dev/jq-native.sh" "$tmp/tools" 2>/dev/null) || rc=$?
[ "$rc" = 1 ] || fail "a fetched build with another sha-256: exit $rc, want 1"
[ -z "$out" ] || fail "a fetched build with another sha-256 was handed out: $out"
[ -z "$(find "$tmp/tools" -type f 2>/dev/null)" ] || fail "a fetched build with another sha-256 was kept"

mkdir -p "$tmp/tools/jq-1.8.2"
for a in $assets; do echo 'not jq either' >"$tmp/tools/jq-1.8.2/$a"; done
rc=0
out=$(sh "$root/deploy/dev/jq-native.sh" "$tmp/tools" 2>/dev/null) || rc=$?
[ "$rc" = 1 ] || fail "a build in place with another sha-256: exit $rc, want 1"
[ -z "$out" ] || fail "a build in place with another sha-256 was handed out: $out"

echo ok
