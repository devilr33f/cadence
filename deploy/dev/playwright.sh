#!/bin/sh
# runs the dashboard's playwright specs (dashboard/e2e) against a running dev stack: compose's playwright service, in
# the edge's network namespace, so the browser's origin is CADENCE_DEV_ORIGIN (as the caller exported it for the stack,
# with CADENCE_DEV_PORT). usage: playwright.sh PROJECT DIR [PLAYWRIGHT ARGS...]
#   PROJECT  the stack's compose project
#   DIR      this run's private directory (mode 700), holding `password`, the owner's, which goes to the specs on their
#            stdin, and optionally `secrets`, the caller's other secrets of the run, one a line; this script writes
#            `learned` there: what the specs saw that no log or artifact may carry (the sessions' cookies, which they
#            name as learned-cookie, a token, a secret they typed), one a line, for the caller's privacy greps
# a spec asks for a turn on the sim on its output, which this script answers with the sim's say file, and for a line
# of the sim's log probe (letters, digits, spaces, "_" and "-"), which it writes into the sim's probe file. the output is
# printed without those requests, without what was learned and without any line that holds a secret of the run
# (counted instead); failure screenshots land in build/dashboard-e2e. the exit code is playwright's, else 3 when an
# artifact held a secret (build/dashboard-e2e is then removed whole: this grep reads a screenshot's bytes, its masks
# alone guard its pixels), else 4 when a sign-in did not report its two cookies (a marker this script could no longer
# read would leave them out of the greps)
set -eu

[ $# -ge 2 ] || { echo "usage: playwright.sh PROJECT DIR [PLAYWRIGHT ARGS...]" >&2; exit 2; }
project=$1
dir=$2
shift 2
root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
export MSYS_NO_PATHCONV=1
compose="docker compose -p $project -f deploy/dev/compose.yml"
name="$project-playwright"
mark=cadence-e2e:

# the screenshots belong to whoever runs this (docker desktop maps them to the host's user by itself)
case $(uname -s) in
  MINGW* | MSYS*) ;;
  *) export CADENCE_E2E_USER="$(id -u):$(id -g)" ;;
esac
rm -rf build/dashboard-e2e
mkdir -p build/dashboard-e2e
rm -f "$dir/output" "$dir/learned"

# say: one button session on the sim, its say file arriving whole
say() {
  printf 'what-time\n' >"$dir/say"
  chmod 644 "$dir/say"
  tar -C "$dir" -cf - say | $compose cp - device-sim:/var/lib/cadence/sim
  rm -f "$dir/say"
}

# logprobe TEXT: the sim's probe file holds TEXT as one line, readable by the sim's user (65532) alone, which ships it
# as source sim-probe and removes the file. the archive names its owner and mode, as git bash's files are all 644
logprobe() {
  case $1 in
    "" | *[!A-Za-z0-9\ _-]*) return 1 ;;
  esac
  (umask 077 && printf '%s\n' "$1" >"$dir/logprobe") || return 1
  _rc=0
  tar --owner=65532 --group=65532 --numeric-owner --mode=600 -C "$dir" -cf - logprobe |
    $compose cp -a - device-sim:/var/lib/cadence >/dev/null 2>&1 || _rc=$?
  rm -f "$dir/logprobe"
  return "$_rc"
}

pid=
stop() {
  [ -z "$pid" ] || kill "$pid" 2>/dev/null || true
  docker rm -f "$name" >/dev/null 2>&1 || true
}
trap stop EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

$compose --profile e2e run --rm --build --no-deps -T --name "$name" playwright "$@" <"$dir/password" >"$dir/output" 2>&1 &
pid=$!
said=0
probed=0
while kill -0 "$pid" 2>/dev/null; do
  asked=$(grep -c -x -F "${mark}want-turn" "$dir/output" 2>/dev/null || true)
  if [ "${asked:-0}" -gt "$said" ]; then
    said=$((said + 1))
    say || echo "playwright.sh: the say file was not written" >&2
  fi
  wanted=$(grep -c "^${mark}want-logprobe " "$dir/output" 2>/dev/null || true)
  if [ "${wanted:-0}" -gt "$probed" ]; then
    probed=$((probed + 1))
    text=$(sed -n "s/^${mark}want-logprobe //p" "$dir/output" | tr -d '\r' | sed -n "${probed}p")
    logprobe "$text" || echo "playwright.sh: the probe file was not written" >&2
  fi
  sleep 1
done
rc=0
wait "$pid" || rc=$?
pid=

# a sign-in's cookies come as learned-cookie, so the check below counts them apart from the other values
(umask 077 && sed -n -e "s/^${mark}learned //p" -e "s/^${mark}learned-cookie //p" "$dir/output" | tr -d '\r' |
  grep -v '^$' >"$dir/learned" || true)
# an empty pattern would match every line
(umask 077 && cat "$dir/password" "$dir/learned" | grep -v '^$' >"$dir/patterns")
[ ! -s "$dir/secrets" ] || grep -v '^$' "$dir/secrets" >>"$dir/patterns"
held=$(grep -v "^$mark" "$dir/output" | grep -c -F -f "$dir/patterns" || true)
grep -v "^$mark" "$dir/output" | grep -v -F -f "$dir/patterns" || true
[ "$held" = 0 ] || echo "playwright.sh: $held lines of the specs' output held a secret and were left out" >&2

# no artifact the specs left (a failure's screenshot and error context) holds a secret of the run
for f in $(find build/dashboard-e2e -type f 2>/dev/null); do
  if grep -q -a -F -f "$dir/patterns" "$f"; then
    rm -rf build/dashboard-e2e
    echo "playwright.sh: an artifact of the specs held a secret of the run: build/dashboard-e2e removed" >&2
    rc=3
    break
  fi
done
signins=$(grep -c -x -F "${mark}signed-in" "$dir/output" || true)
cookies=$(grep -c "^${mark}learned-cookie ." "$dir/output" || true)
if [ "$rc" = 0 ] && { [ "$signins" = 0 ] || [ "$cookies" -lt $((2 * signins)) ]; }; then
  echo "playwright.sh: $signins sign-ins reported, $cookies cookies learned: under two each" >&2
  rc=4
fi
rm -f "$dir/patterns" "$dir/output"
exit "$rc"
