#!/bin/sh
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
wrapper="$root/deploy/dev/in-docker.sh"
image='alpine:3.22@sha256:bfe7ceb9f09ec0566c48fb93356e45a47a00be5a1dd14fa5b909e7709c66b034'

fail() { echo "FAIL: $*" >&2; exit 1; }

out=$("$wrapper" "$image" sh -c 'test -f /w/AGENTS.md && pwd')
[ "$out" = /w ] || fail "workdir: got '$out'"

probe="$root/.in-docker-probe"
rm -f "$probe"
trap 'rm -f "$probe"' EXIT
"$wrapper" "$image" touch /w/.in-docker-probe
[ -f "$probe" ] || fail "probe file not in repo root"
case $(uname -s) in
  MINGW* | MSYS*) ;;
  *) [ "$(stat -c %u "$probe")" = "$(id -u)" ] || fail "probe owner is not the current user" ;;
esac
rm -f "$probe"

out=$(IN_DOCKER_DIRECT=1 "$wrapper" no-such-image:0 sh -c 'test -f AGENTS.md && echo direct')
[ "$out" = direct ] || fail "direct mode: got '$out'"

docker_argv=$("$wrapper" "$image" sh -c 'for a in "$@"; do printf "%s|" "$a"; done' x 'a b' '' c)
direct_argv=$(IN_DOCKER_DIRECT=1 "$wrapper" no-such-image:0 sh -c 'for a in "$@"; do printf "%s|" "$a"; done' x 'a b' '' c)
[ "$docker_argv" = "$direct_argv" ] || fail "argv differs: '$docker_argv' vs '$direct_argv'"
[ "$docker_argv" = 'a b||c|' ] || fail "argv: got '$docker_argv'"

echo ok
