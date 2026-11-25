#!/bin/sh
# usage: in-docker.sh <image> <command> [args...]
set -eu

[ $# -ge 2 ] || { echo "usage: in-docker.sh <image> <command> [args...]" >&2; exit 2; }

root=$(cd "$(dirname "$0")/../.." && pwd)
image=$1
shift

if [ "${IN_DOCKER_DIRECT:-}" = 1 ]; then
  cd "$root"
  exec "$@"
fi

user=
case $(uname -s) in
  MINGW* | MSYS*)
    root=$(cd "$root" && pwd -W)
    export MSYS_NO_PATHCONV=1
    ;;
  *) user=1 ;;
esac

if [ -n "$user" ]; then
  # shellcheck disable=SC2086
  exec docker run --rm --entrypoint "" -v "$root:/w" -w /w \
    --user "$(id -u):$(id -g)" -e HOME=/tmp ${IN_DOCKER_FLAGS:-} "$image" "$@"
fi
# shellcheck disable=SC2086
exec docker run --rm --entrypoint "" -v "$root:/w" -w /w ${IN_DOCKER_FLAGS:-} "$image" "$@"
