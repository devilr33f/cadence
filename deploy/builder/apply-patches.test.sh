#!/usr/bin/env bash
# apply-patches.sh: series in name order, directories in the order given, zero-context hunks, the source left as it
# was, the first patch that does not apply named and no output tree left, and the arguments it refuses
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
script=$here/apply-patches.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

# a pristine tree with a .git of its own, as a submodule has
src=$tmp/src
mkdir -p "$src/lib"
printf 'one\ntwo\nthree\n' >"$src/lib/a.c"
printf 'alpha\n' >"$src/b.txt"
echo 'gitdir: ../../.git/modules/src' >"$src/.git"
before=$(cd "$src" && find . -type f -exec cksum {} + | sort)

mkdir "$tmp/series" "$tmp/ours" "$tmp/bad" "$tmp/empty"
# 0002 needs 0001's line: name order, not creation order
cat >"$tmp/series/0002-second.patch" <<'EOF'
--- a/lib/a.c
+++ b/lib/a.c
@@ -2 +2 @@
-TWO
+TWO!
EOF
cat >"$tmp/series/0001-first.patch" <<'EOF'
--- a/lib/a.c
+++ b/lib/a.c
@@ -2 +2 @@
-two
+TWO
EOF
echo 'not a patch' >"$tmp/series/README"
# a second directory, after the first, with context
cat >"$tmp/ours/0001-ours.patch" <<'EOF'
--- a/b.txt
+++ b/b.txt
@@ -1 +1,2 @@
 alpha
+beta
EOF
cat >"$tmp/bad/0001-fine.patch" <<'EOF'
--- a/b.txt
+++ b/b.txt
@@ -1 +1 @@
-alpha
+gamma
EOF
cat >"$tmp/bad/0002-stale.patch" <<'EOF'
--- a/lib/a.c
+++ b/lib/a.c
@@ -1 +1 @@
-zero
+nothing
EOF

log=$("$script" "$src" "$tmp/out" "$tmp/series" "$tmp/ours")
[ "$(cat "$tmp/out/lib/a.c")" = "$(printf 'one\nTWO!\nthree')" ] || fail "series not applied in name order: $(cat "$tmp/out/lib/a.c")"
[ "$(cat "$tmp/out/b.txt")" = "$(printf 'alpha\nbeta')" ] || fail "second directory not applied"
[ "$log" = "$(printf 'applied 0001-first.patch\napplied 0002-second.patch\napplied 0001-ours.patch')" ] \
  || fail "unexpected log: $log"
[ ! -e "$tmp/out/.git" ] || fail "the copy kept a .git"
[ -z "$(find "$tmp" -maxdepth 1 -name 'out.tmp.*')" ] || fail "a work tree was left beside the output"
[ "$(cd "$src" && find . -type f -exec cksum {} + | sort)" = "$before" ] || fail "the source tree changed"

# a rerun replaces the output whole
echo stale >"$tmp/out/stale"
"$script" "$src" "$tmp/out" "$tmp/series" >/dev/null
[ ! -e "$tmp/out/stale" ] || fail "a rerun kept a file of the previous output"
[ "$(cat "$tmp/out/b.txt")" = alpha ] || fail "a rerun kept the previous series"

# the failing patch is named, nothing is left at the output, and the patches before it do not leak
rc=0
err=$("$script" "$src" "$tmp/out-bad" "$tmp/bad" 2>&1 >/dev/null) || rc=$?
[ "$rc" = 1 ] || fail "a stale patch: exit $rc, want 1"
printf '%s\n' "$err" | grep -qx 'apply-patches: patch does not apply: 0002-stale.patch' || fail "stale patch not named: $err"
[ ! -e "$tmp/out-bad" ] || fail "a failed run left its output"
[ -z "$(find "$tmp" -maxdepth 1 -name 'out-bad.tmp.*')" ] || fail "a failed run left its work tree"
# a failed run over an existing output leaves the old output as it was
"$script" "$src" "$tmp/out" "$tmp/series" >/dev/null
"$script" "$src" "$tmp/out" "$tmp/bad" >/dev/null 2>&1 && fail "a stale patch applied"
[ "$(cat "$tmp/out/lib/a.c")" = "$(printf 'one\nTWO!\nthree')" ] || fail "a failed run changed the previous output"

refused() { # what, then the arguments
  local what=$1 rc=0
  shift
  "$script" "$@" >/dev/null 2>&1 || rc=$?
  [ "$rc" = 2 ] || fail "$what: exit $rc, want 2"
}
refused "no patch directory" "$src" "$tmp/o1"
refused "a missing source" "$tmp/nowhere" "$tmp/o2" "$tmp/series"
refused "a missing patch directory" "$src" "$tmp/o3" "$tmp/nowhere"
refused "a directory without patches" "$src" "$tmp/o4" "$tmp/empty"
refused "an output inside the source" "$src" "$src/patched" "$tmp/series"
refused "an output equal to the source" "$src" "$src" "$tmp/series"
refused "a source inside the output" "$src" "$tmp" "$tmp/series"
[ ! -e "$src/patched" ] || fail "an output inside the source was written"

echo ok
