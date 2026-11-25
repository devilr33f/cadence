#!/usr/bin/env bash
# add-prompts.sh on small trees: none, two languages, and each refusal
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

bash "$here/add-prompts.sh" "$tmp/none" "$tmp/tree0" 2>/dev/null || fail "no prompts refused"
[ ! -e "$tmp/tree0/share/prompts" ] || fail "no prompts made a directory"

mkdir -p "$tmp/in/ru" "$tmp/in/en"
printf 'OggS' >"$tmp/in/ru/failed.opus"
printf 'OggS' >"$tmp/in/en/not_heard.opus"
bash "$here/add-prompts.sh" "$tmp/in" "$tmp/tree" >/dev/null || fail "good prompts refused"
for f in ru/failed.opus en/not_heard.opus; do
  [ -f "$tmp/tree/share/prompts/$f" ] || fail "$f not copied"
  [ "$(stat -c %a "$tmp/tree/share/prompts/$f" 2>/dev/null || stat -f %Lp "$tmp/tree/share/prompts/$f")" = 644 ] ||
    fail "$f not mode 644"
done

refused() { # what, then a setup run in a fresh input
  local what=$1
  rm -rf "$tmp/bad" "$tmp/badtree"
  mkdir -p "$tmp/bad/ru"
  eval "$2"
  if bash "$here/add-prompts.sh" "$tmp/bad" "$tmp/badtree" 2>/dev/null; then fail "$what accepted"; fi
}
refused "a bad id" 'printf x >"$tmp/bad/ru/Failed.opus"'
refused "a bad lang" 'mkdir -p "$tmp/bad/rus1"; printf x >"$tmp/bad/rus1/failed.opus"'
refused "another file" 'printf x >"$tmp/bad/ru/failed.wav"'
refused "a file at the top" 'printf x >"$tmp/bad/README"'
refused "an empty file" ': >"$tmp/bad/ru/failed.opus"'
refused "a symlink" 'printf x >"$tmp/x"; ln -s "$tmp/x" "$tmp/bad/ru/failed.opus"'
refused "a large file" 'head -c 262145 /dev/zero >"$tmp/bad/ru/failed.opus"'
echo "add-prompts.test.sh: ok"
