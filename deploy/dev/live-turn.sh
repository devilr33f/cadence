#!/bin/sh
# owner-gated live turn (spec §9): the sim image on this machine enrolls at https://HOST with CODE, says
# what-time once and waits for the reply. the device token stays in a throwaway docker volume, never printed
set -eu

if [ $# -ne 2 ]; then
  echo "usage: deploy/dev/live-turn.sh HOST CODE" >&2
  exit 2
fi
host=$1
code=$2
case $host in
  '' | *[!A-Za-z0-9.:-]*)
    echo "live-turn: HOST must be a host name, optionally with :port" >&2
    exit 2
    ;;
esac
case $code in
  '' | *[!A-Za-z0-9-]*)
    echo "live-turn: CODE must be the enroll code of a sim device" >&2
    exit 2
    ;;
esac

root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
# container paths must reach docker as written (git bash)
export MSYS_NO_PATHCONV=1
jq_image='ghcr.io/jqlang/jq:1.8.2@sha256:b9c68867e5766576263a222e91db3de422d802069c7af70440e667a95344e486'
image=cadenced-sim:live-turn
name=cadence-live-turn-$$
data=/var/lib/cadence
tmp=$(mktemp -d)

cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker volume rm "$name" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# the daemon logs events, ids and codes only
fail() {
  echo "live-turn: $1" >&2
  if docker inspect "$name" >/dev/null 2>&1; then
    docker logs --tail 40 "$name" >&2 2>&1 || true
  fi
  exit 1
}

# within SECONDS WHAT CHECK...: runs CHECK until it passes or the time is up
within() {
  limit=$1 what=$2
  shift 2
  deadline=$(($(date +%s) + limit))
  until "$@"; do
    [ "$(date +%s)" -lt "$deadline" ] || fail "$what after ${limit}s"
    sleep 1
  done
}

linked() { docker logs "$name" 2>&1 | grep -q '"msg":"link_up"'; }

replied() {
  rm -rf "$tmp/out" && mkdir "$tmp/out"
  docker cp "$name:$data/sim/out" - 2>/dev/null | tar -x -C "$tmp/out" -f - 2>/dev/null || return 1
  summary=$(cat "$tmp"/out/out/*.json 2>/dev/null |
    docker run -i --rm "$jq_image" -r -s 'map(select(.purpose == "reply" and .packets >= 10))[0] // empty | "packets=\(.packets) duration_ms=\(.duration_ms)"' 2>/dev/null) &&
    [ -n "$summary" ]
}

session_ok() { docker logs "$name" 2>&1 | grep '"msg":"session_end"' | grep -q '"code":"ok"'; }

docker build -q -f device/Dockerfile.sim -t "$image" . >/dev/null || fail "sim image build failed"
docker volume create "$name" >/dev/null

docker run --rm -v "$name:$data" "$image" enroll --server "https://$host" --code "$code" --board sim \
  --config $data/config.json --data-dir $data || fail "enroll failed"
docker run -d --name "$name" -v "$name:$data" "$image" >/dev/null || fail "sim did not start"
within 30 "no link to https://$host" linked

printf 'what-time\n' >"$tmp/say"
chmod 644 "$tmp/say"
# the say file arrives whole: the sim never reads a partial name
tar -C "$tmp" -cf - say | docker cp - "$name:$data/sim" || fail "say file not written"
within 60 "no reply stream" replied
within 10 "the session did not end ok" session_ok

echo "live-turn: reply $summary"
echo "live-turn: ok"
