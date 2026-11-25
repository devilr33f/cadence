#!/bin/sh
# drives the sim slice end to end with fake providers: enroll, the volume set by command, config sync, one voice turn,
# a device restart that keeps both, a scripted tool turn that lowers the volume and says nothing and that the
# conversation history lists with its call (an ha token refused), then a url session from the compose network's media
# server with an announcement over it, checked in the mix, then soloist: the fake's
# build from the fake cdn over https, its track and tone, its slider by rule 7 and the commands it obeys, checked in
# the mix, then airplay: the receiver on its private bus with the fake shairport sync, whose cast takes the session from
# soloist with its track and tone, its slider by rule 7, set_level and pause, checked in the mix, and that switches it
# off again, then alarms (M6a): the timeZone set explicitly pushed as the alarms zone, a stop transcript that ends a turn
# silently, a follow-up that ends with its window and one the say file speaks into, where an alarm is refused, an alarm
# a woken turn set ringing in the mix with its ramp until a press stops it, and a timer that times out, then memory: a
# turn names a lamp, the compaction after it learns the alias, the next turn recalls it, and in approval mode the next
# memory waits for its approval, then background tasks on a fake hermes over https (after one
# deliberate server restart for the hermes setting): a task a turn starts finishes and its event turn's update is
# announced on the idle sim, an approval the dashboard gives lets a run end, and an update in quiet hours waits for the
# end of the next pressed session, then home assistant on a fake over wss (after another deliberate restart for the
# homeAssistant setting): the turn's home note lists the favorite, a turn turns a light off by name, and with sensitive
# devices denied a turn's unlock is refused with nothing sent, then mcp on a fake over https, configured through the
# api alone (no restart): a tool no device lists is not offered, an untrusted result keeps the rest of its turn from
# write tools (an mcp one and device_volume refused, nothing sent), a woken turn's write runs, and a changed tool is
# withdrawn until acknowledged, then t3 code on a fake over https (the api alone, no restart): a busy signal holds a
# thread's question until it ends, then it is spoken and written as a notice turn, the next woken turn's unread block
# taints nothing, its inspect does, its answer reaches the fake, a stop and a thread launched later are announced too,
# then telemetry: the device's lines (cadenced's, audiod's, the fake soloist's) stored
# stamped with it, a probe line holding the device token and the soloist key stored with both redacted and seen on the
# live feed, its metrics, top processes and prometheus series, and the lines it made while the server was stopped
# drained, then the dashboard: its image non-root on a read-only root, the edge's split of the one origin and the
# playwright specs as the owner in a browser; then home assistant's client (the component's client and feed, without
# home assistant core, in make ha-test's image) with an ha token: the devices, a url session's media_state, its pause,
# a say, the sim's restart and vitals on the feed, and the token revoked (1008, then 401); then multiroom: a second sim
# with a skewed group clock and the first as a stereo pair, a url the member plays on the leader in sync on both (the
# same frames heard within 1 ms on the two), a
# pause, a resume, a rejoin after the member's network dropped, the group deleted, and its key in no log; and no url,
# metadata, announced text, soloist key, receiver name, airplay client, transcript, memory text, hermes key, task text, home
# entity, home assistant token, cadence api token, mcp token, path, description, argument or result, t3 code token,
# question or question token, device token or probe text in any log, the fake hermes', the fake home assistant's, the
# fake mcp server's, the fake t3 code server's and the dashboard's own included (one line per request), nor a secret in
# the specs' artifacts.
# refused while a stack still answers on its port; always torn down; the admin token stays in this shell and is never
# printed
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
# container paths must reach docker as written (git bash)
export MSYS_NO_PATHCONV=1
# CADENCE_E2E_PROJECT and CADENCE_DEV_PORT let parallel worktrees run their own stacks side by side
project=${CADENCE_E2E_PROJECT:-cadence-e2e}
compose="docker compose -p $project -f deploy/dev/compose.yml"
host=${E2E_HOST:-127.0.0.1}
port=${CADENCE_DEV_PORT:-18080}
export CADENCE_DEV_PORT=$port
# the origin the server takes for this stack (M7a); plain http, so its cookies go without Secure
export CADENCE_DEV_ORIGIN="http://$host:$port"
# compactAfter counts seconds in this stack, so the memory block's compactions come within seconds
export CADENCE_DEV_COMPACT_SECONDS=1
# a pressed session keeps quiet hours from holding task updates this long, not 2 min, so the tasks block waits seconds
export CADENCE_DEV_AWAKE_SECONDS=10
# the sim's timers ring this long when nothing stops them, not a minute, so the alarms block's timer times out in
# seconds
export CADENCE_SIM_TIMER_SECONDS=5
# the images build for the docker server's architecture, so the sim asks the fake cdn for that one's soloist build
CADENCE_DEV_ARCH=$(docker version -f '{{.Server.Arch}}' 2>/dev/null) || CADENCE_DEV_ARCH=
case $CADENCE_DEV_ARCH in amd64 | arm64) ;; *) CADENCE_DEV_ARCH=amd64 ;; esac
export CADENCE_DEV_ARCH
base="http://$host:$port"
# a stack still answering there would take this run's calls: refused before anything is started or torn down. the
# shell discards the body: with MSYS_NO_PATHCONV a windows curl cannot open -o /dev/null
if curl -s -m 2 "$base/healthz" >/dev/null 2>&1; then
  echo "e2e: a previous stack is still up at $base (docker compose -p $project down -v)" >&2
  exit 1
fi
jq_image='ghcr.io/jqlang/jq:1.8.2@sha256:b9c68867e5766576263a222e91db3de422d802069c7af70440e667a95344e486'
# home assistant's client runs in make ha-test's pinned python image: one pin, the makefile's
ha_image=$(sed -n 's/^HA_IMAGE := //p' Makefile)
# one reference, pinned by a whole sha-256 digest
ha_digest=${ha_image##*@sha256:}
case ${ha_image%@sha256:*} in
  '' | *[[:space:]]* | "$ha_image") ha_digest= ;;
esac
case $ha_digest in
  *[!0-9a-f]*) ha_digest= ;;
esac
if [ "${#ha_digest}" != 64 ]; then
  echo "e2e: no pinned HA_IMAGE in the Makefile" >&2
  exit 1
fi
ha_container=$project-ha-client
data=/var/lib/cadence
tmp=$(mktemp -d)
feed_pid=
feed2_pid=
ha_pid=
ha_rc=

down() {
  [ -z "$feed_pid" ] || kill "$feed_pid" 2>/dev/null || true
  [ -z "$feed2_pid" ] || kill "$feed2_pid" 2>/dev/null || true
  # the dashboard block's browser shares the edge's network namespace: gone before the edge
  docker rm -f "$project-playwright" >/dev/null 2>&1 || true
  [ -z "$ha_pid" ] || kill "$ha_pid" 2>/dev/null || true
  # before compose: the ha client's container sits on the stack's network, which compose cannot remove while it does
  docker rm -f "$ha_container" >/dev/null 2>&1 || true
  $compose down -v --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap down EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# fail WHAT SERVICES: names the step, shows the services' logs
fail() {
  echo "e2e: $1" >&2
  $compose ps >&2 || true
  # shellcheck disable=SC2086 # one word per service
  $compose logs --tail 40 $2 >&2 || true
  exit 1
}

# native PATH: a host path for a windows binary (go) under git bash, unchanged elsewhere
native() { if command -v cygpath >/dev/null 2>&1; then cygpath -m "$1"; else printf "%s" "$1"; fi; }

# the image's jq as its release build for this host (jq-native.sh) where there is one: the checks call it hundreds of
# times, and a docker run costs seconds a call on docker desktop. -b keeps the windows build's line ends LF (a no-op
# elsewhere)
if jq_native=$(sh deploy/dev/jq-native.sh build/tools); then
  jqc() { "$jq_native" -b "$@"; }
else
  echo "e2e: no native jq 1.8.2 for this host, so the image's" >&2
  jqc() { docker run -i --rm "$jq_image" "$@"; }
fi

# built once: the checks read its stdout and stderr together, which go run fills with download lines on a cold cache
wavstat="$tmp/wavstat$(go env GOEXE)"
go build -o "$(native "$wavstat")" ./server/tools/wavstat || { echo "e2e: the wav stats helper not built" >&2; exit 1; }

# api METHOD PATH [BODY]: an admin call, tried again only while the connection is refused (curl exit 7: the request
# never left, so a POST cannot run twice); error bodies are fixed codes, never a value
api() {
  _try=0
  while :; do
    # `&& _rc=0 || _rc=$?` keeps set -e from ending the script before the exit code is seen
    if [ $# -gt 2 ]; then
      curl -sS -m 10 --fail-with-body -X "$1" -H "Authorization: Bearer $token" \
        -H 'Content-Type: application/json' --data-binary "$3" "$base$2" && _rc=0 || _rc=$?
    else
      curl -sS -m 10 --fail-with-body -X "$1" -H "Authorization: Bearer $token" "$base$2" && _rc=0 || _rc=$?
    fi
    [ "$_rc" -eq 7 ] && [ "$_try" -lt 5 ] || return "$_rc"
    _try=$((_try + 1))
    sleep 1
  done
}

# within SECONDS WHAT SERVICES CHECK...: runs CHECK until it passes or the time is up
within() {
  limit=$1 what=$2 services=$3
  shift 3
  deadline=$(($(date +%s) + limit))
  until "$@"; do
    [ "$(date +%s)" -lt "$deadline" ] || fail "$what after ${limit}s" "$services"
    sleep 1
  done
}

probe() { curl -fsS -m 2 "$base/healthz" 2>/dev/null | grep -q '"db":"ok"'; }

# healthy: three probes in a row a second apart, from a server that never restarted: one that crashed on its way up
# fails the run at once, with its logs
healthy() {
  probe && sleep 1 && probe && sleep 1 && probe || return 1
  restarts=$(docker inspect -f '{{.RestartCount}}' "$($compose ps -q server)" 2>/dev/null) || restarts=
  [ "$restarts" = 0 ] || fail "the server restarted ${restarts:-?} times on its way up" server
}

reported() {
  api GET "/api/v1/devices/$id" 2>/dev/null |
    jqc -e '.online and any(.capabilities[]; .id == "audio.output")' >/dev/null 2>&1
}

# synced: online at level 30, reported back; the device owns its level, so no config carries it
synced() {
  api GET "/api/v1/devices/$id" 2>/dev/null |
    jqc -e '.online and any(.capabilities[]; .id == "audio.output" and .state.level == 30)' >/dev/null 2>&1
}

# player_synced: online, the pushed media.player config applied and reported back
player_synced() {
  api GET "/api/v1/devices/$id" 2>/dev/null |
    jqc -e '.online and any(.capabilities[]; .id == "media.player" and .desiredGen > 0 and .appliedGen == .desiredGen and .appliedError == "" and .state.duckMode == "duck")' >/dev/null 2>&1
}

# set_level LEVEL: the device's level by command, an authenticated api call (rule 7); it answers the level it set
set_level() {
  http=$(status POST "/api/v1/devices/$id/capabilities/audio.output/commands/set_level" "{\"level\":$1}") || http=
  [ "$http" = 200 ] || fail "set_level $1 answered ${http:-nothing}: $(cat "$tmp/body" 2>/dev/null)" "server device-sim"
  jqc -e --argjson level "$1" '.result.level == $level' <"$tmp/body" >/dev/null 2>&1 ||
    fail "set_level $1 answered another level" "server device-sim"
}

fetch_out() {
  rm -rf "$tmp/out" && mkdir "$tmp/out"
  $compose cp device-sim:$data/sim/out - 2>/dev/null | tar -x -C "$tmp/out" -f - 2>/dev/null
}

replied() {
  fetch_out || return 1
  cat "$tmp"/out/out/*.json 2>/dev/null | jqc -e -s 'any(.[]; .purpose == "reply" and .packets >= 10)' >/dev/null 2>&1
}

# streams: how many reply streams the device began; audiod opens each wav at StreamStart
streams() {
  fetch_out || return 1
  ls "$tmp"/out/out/*.wav 2>/dev/null | wc -l | tr -d ' '
}

# audiod_ran: the device logged audiod's ready line and relayed its own log lines
audiod_ran() {
  logs=$($compose logs --no-log-prefix device-sim 2>/dev/null) || return 1
  printf '%s
' "$logs" | grep -q '"msg":"audiod_ready"' && printf '%s
' "$logs" | grep -q '"source":"audiod"'
}

# reply_wav: the first reply's per-stream wav (recorded before the software gain, so it proves format and content, not volume) is 48 kHz mono 16-bit, at least 0.5 s and not silent (the fake tts is a 440 Hz tone at 0.3, 28800 samples, rms ~6900; floor 1700); sets reply_rms
reply_wav() {
  fetch_out || return 1
  name=
  for f in "$tmp"/out/out/*.json; do
    if jqc -e '.purpose == "reply" and .packets >= 10' <"$f" >/dev/null 2>&1; then
      name=$(basename "$f" .json)
      break
    fi
  done
  [ -n "$name" ] || return 1
  stat=$("$wavstat" -in "$(native "$tmp/out/out/$name.wav")" 2>&1) || { echo "e2e: wavstat: $stat" >&2; return 1; }
  echo "e2e: reply wav $stat"
  case $stat in
    "rate=48000 channels=1 bits=16 "*) ;;
    *) return 1 ;;
  esac
  n=${stat##*samples=}
  n=${n%% *}
  rms=${stat##*rms=}
  rms=${rms%%.*}
  reply_rms=$rms
  [ "$n" -ge 24000 ] && [ "$rms" -ge 1700 ]
}

# mix_gain: the mix of the audiod run that played turn 1 (finalized when that run stopped) is 48 kHz stereo 16-bit and
# its rms is 0.2-0.45 of the reply's: the level-30 software gain was applied (rule 7); silence or unity gain fail
mix_gain() {
  stat=$("$wavstat" -in "$(native "$tmp/mix.wav")" 2>&1) || { echo "e2e: wavstat: $stat" >&2; return 1; }
  echo "e2e: mix wav $stat"
  case $stat in
    "rate=48000 channels=2 bits=16 "*) ;;
    *) return 1 ;;
  esac
  rms=${stat##*rms=}
  rms=${rms%%.*}
  [ "$rms" -ge $((reply_rms * 20 / 100)) ] && [ "$rms" -le $((reply_rms * 45 / 100)) ]
}

ok_turn_lines() {
  $compose logs --no-log-prefix server 2>/dev/null | grep '"msg":"turn"' | grep "\"device_id\":\"$id\"" | grep '"code":"ok"'
}

ok_turns() { ok_turn_lines | wc -l | tr -d ' '; }

turn_logged() { [ "$(ok_turns)" -ge 1 ]; }

# say: starts a button session on the sim; the say file arrives whole, so the sim never reads a partial name
say() {
  printf 'what-time\n' >"$tmp/say"
  chmod 644 "$tmp/say"
  tar -C "$tmp" -cf - say | $compose cp - device-sim:$data/sim || fail "say file not written" device-sim
}

# lowered: the volume tool's set_level 20 reported back
lowered() {
  api GET "/api/v1/devices/$id" 2>/dev/null |
    jqc -e 'any(.capabilities[]; .id == "audio.output" and .state.level == 20)' >/dev/null 2>&1
}

# silent_turn: the agent ran device_volume and ended the turn without speech, and voice closed it ok
silent_turn() {
  [ "$(ok_turns)" -ge 2 ] &&
    $compose logs --no-log-prefix server 2>/dev/null | grep '"msg":"agent_turn"' | grep "\"device_id\":\"$id\"" |
    grep '"tools":"device_volume"' | grep '"code":"ok"' | grep -q '"silent":true'
}

# restarted: linked again, with level 30 loaded from the device's own file (no config push) and the player config restored
restarted() {
  logs=$($compose logs --no-log-prefix device-sim 2>/dev/null) || return 1
  [ "$(printf '%s\n' "$logs" | grep -c '"msg":"link_up"')" -ge 2 ] &&
    printf '%s\n' "$logs" | grep '"msg":"volume_loaded"' | grep '"source":"local"' | grep -q '"volume":30' &&
    printf '%s\n' "$logs" | grep '"msg":"config_restored"' | grep -q '"capability":"media.player"'
}

# status METHOD PATH BODY: an admin call's http status; the body (codes only on errors) lands in $tmp/body
status() {
  curl -sS -m 15 -o "$(native "$tmp/body")" -w '%{http_code}' -X "$1" -H "Authorization: Bearer $token" \
    -H 'Content-Type: application/json' --data-binary "$3" "$base$2"
}

# playing_song: the url session with the play command's metadata, as the api shows it
playing_song() {
  api GET "/api/v1/devices/$id/media" 2>/dev/null |
    jqc -e '.state == "playing" and .source == "url" and .metadata.title == "Cadence E2E Song" and .metadata.artist == "Cadence E2E Artist"' >/dev/null 2>&1
}

media_idle() { api GET "/api/v1/devices/$id/media" 2>/dev/null | jqc -e '.state == "idle"' >/dev/null 2>&1; }

# announced: the announcement's stream summary (the fake tts speaks 300 ms) and the wav audiod recorded of it
announced() {
  fetch_out || return 1
  f=$(grep -l '"purpose":"announce"' "$tmp"/out/out/*.json 2>/dev/null | head -n 1)
  [ -n "$f" ] && [ -s "${f%.json}.wav" ] && jqc -e '.packets >= 10' <"$f" >/dev/null 2>&1
}

# music_duck: the 1000 Hz (left) level of the mix per 100 ms. at its loudest the music is -12 dBFS at volume 30
# (-22.5 dBFS) within 3 dB, held 5 s or more: it played resampled to 48 kHz (unresampled, the tone would sit at
# 1088 Hz). the right channel stays 20 dB under it, and inside the music the level dips 10 dB or more (the
# announcement's duck, 16.5 dB) and comes back
music_duck() {
  levels=$("$wavstat" -in "$(native "$tmp/music-mix.wav")" -goertzel 1000 -window 100 2>&1) || {
    echo "e2e: wavstat: $levels" >&2
    return 1
  }
  result=$(printf '%s\n' "$levels" | awk '
    { split($2, l, "="); split($3, r, "="); n++; left[n] = l[2] + 0; right[n] = r[2] + 0 }
    END {
      top = -1000; rmax = -1000; first = 0; last = 0; loud = 0
      for (i = 1; i <= n; i++) if (left[i] > top) top = left[i]
      for (i = 1; i <= n; i++) if (left[i] >= top - 3) { if (!first) first = i; last = i; loud++ }
      low = top; at = first
      for (i = first; i <= last; i++) { if (left[i] < low) { low = left[i]; at = i }; if (right[i] > rmax) rmax = right[i] }
      printf "music=%.1f ducked=%.1f depth=%.1f loud_windows=%d dip_at_ms=%d right=%.1f", top, low, top - low, loud, (at - first) * 100, rmax
      exit !(top >= -25.5 && top <= -19.5 && loud >= 50 && top - low >= 10 && at > first && at < last && rmax <= top - 20 && top - low <= 25)
    }') && { echo "e2e: music mix $result"; return 0; }
  echo "e2e: music mix $result" >&2
  return 1
}

# soloist_back: linked a third time (after the music's stop), online with media.soloist
soloist_back() {
  [ "$($compose logs --no-log-prefix device-sim 2>/dev/null | grep -c '"msg":"link_up"')" -ge 3 ] &&
    api GET "/api/v1/devices/$id" 2>/dev/null |
    jqc -e '.online and any(.capabilities[]; .id == "media.soloist")' >/dev/null 2>&1
}

# soloist_ready: the pushed config applied, soloist ready on a build whose date the state names
soloist_ready() {
  api GET "/api/v1/devices/$id" 2>/dev/null |
    jqc -e 'any(.capabilities[]; .id == "media.soloist" and .desiredGen > 0 and .appliedGen == .desiredGen and .state.state == "ready" and (.state.build // "" | test("^[0-9]{8}$")))' >/dev/null 2>&1
}

# soloist_media STATE: the fake's session with its track, as the api shows it
soloist_media() {
  api GET "/api/v1/devices/$id/media" 2>/dev/null |
    jqc -e --arg state "$1" '.state == $state and .source == "soloist" and .metadata.title == "Cadence Fake Track" and .metadata.artist == "Cadence Fake Artist"' >/dev/null 2>&1
}

# level LEVEL: the device reports it
level() {
  api GET "/api/v1/devices/$id" 2>/dev/null |
    jqc -e --argjson level "$1" 'any(.capabilities[]; .id == "audio.output" and .state.level == $level)' >/dev/null 2>&1
}

# soloist_commands: the fake's commands.log (unjailed in the scratch's home), one command a line
soloist_commands() {
  $compose cp device-sim:$data/run/soloist/home/commands.log - 2>/dev/null | tar -x -O -f - commands.log 2>/dev/null
}

# commanded FILTER [SEEN]: a command the fake received after the first SEEN (0) matches FILTER
commanded() {
  soloist_commands >"$tmp/commands.log" &&
    tail -n +$((${2:-0} + 1)) "$tmp/commands.log" | jqc -e -s "any(.[]; $1)" >/dev/null 2>&1
}

# commands_seen: how many commands the fake received so far
commands_seen() { soloist_commands | wc -l | tr -d ' '; }

# soloist_tone MOST: the fake's 1 kHz tone (-6 dBFS at 44.1 kHz) in the mix per 100 ms, left: at volume v it sits at
# -6.02 + 20 log10(v/100) dBFS, so 1 s or more within 0.5 dB of 25's -18.1 (the slider's move) and of 35's -15.1
# (set_level), never louder than 35 (the stored 55 would be -11.2), and at 35 for at most MOST seconds, as the pause
# ended it: the mix leaves silence out, so a tone that went on through the pause would only make that stretch longer.
# it played resampled to 48 kHz: unresampled the tone would sit at 1088 Hz
soloist_tone() {
  levels=$("$wavstat" -in "$(native "$tmp/soloist-mix.wav")" -goertzel 1000 -window 100 2>&1) || {
    echo "e2e: wavstat: $levels" >&2
    return 1
  }
  result=$(printf '%s\n' "$levels" | awk -v most="$1" '
    { split($2, l, "="); n++; left[n] = l[2] + 0 }
    END {
      at25 = 0; at35 = 0; top = -1000
      for (i = 1; i <= n; i++) {
        if (left[i] >= -18.56 && left[i] <= -17.56) at25++
        if (left[i] >= -15.64 && left[i] <= -14.64) at35++
        if (left[i] > top) top = left[i]
      }
      printf "level25_windows=%d level35_windows=%d (at most %d) loudest=%.1f", at25, at35, most * 10, top
      exit !(at25 >= 10 && at35 >= 10 && at35 <= most * 10 && top <= -14.1)
    }') && { echo "e2e: soloist mix $result"; return 0; }
  echo "e2e: soloist mix $result" >&2
  return 1
}

# airplay_ready: the pushed config applied, the receiver ready
airplay_ready() {
  api GET "/api/v1/devices/$id" 2>/dev/null |
    jqc -e 'any(.capabilities[]; .id == "media.airplay" and .desiredGen > 0 and .appliedGen == .desiredGen and .state.state == "ready")' >/dev/null 2>&1
}

# airplay_media STATE: the fake shairport's cast with its track, as the api shows it
airplay_media() {
  api GET "/api/v1/devices/$id/media" 2>/dev/null |
    jqc -e --arg state "$1" '.state == $state and .source == "airplay" and .metadata.title == "Cadence Fake AirPlay Track" and .metadata.artist == "Cadence Fake AirPlay Artist"' >/dev/null 2>&1
}

# airplay_calls: the fake shairport's log of the calls it got (in the receiver's scratch), one a line
airplay_calls() {
  $compose cp device-sim:$data/run/airplay/fake-shairport.log - 2>/dev/null |
    tar -x -O -f - fake-shairport.log 2>/dev/null
}

# airplay_called CALL [SEEN]: a call the fake shairport got after the first SEEN (0) is CALL
airplay_called() {
  airplay_calls >"$tmp/fake-shairport.log" &&
    tail -n +$((${2:-0} + 1)) "$tmp/fake-shairport.log" | grep -qxF "$1"
}

# airplay_calls_seen: how many calls the fake shairport got so far
airplay_calls_seen() { airplay_calls | wc -l | tr -d ' '; }

# airplay_off: the config that switched the receiver off applied
airplay_off() {
  api GET "/api/v1/devices/$id" 2>/dev/null |
    jqc -e 'any(.capabilities[]; .id == "media.airplay" and .appliedGen == .desiredGen and .state.state == "off")' >/dev/null 2>&1
}

# airplay_tone MOST: the fake's tone (1500 Hz left, 1000 Hz right, -6 dBFS, 48 kHz as the receiver's pipe carries it) in
# the mix per 100 ms, at 1500 Hz: at volume v the left sits at -6.02 + 20 log10(v/100) dBFS, so 1 s or more within
# 0.5 dB of 70's -9.1 (the slider's move) and of 40's -14.0 (set_level), never louder than 70, and at 40 for at most
# MOST seconds, as the pause ended it (the mix leaves silence out). the right stays 20 dB under the left: the channels
# kept their order
airplay_tone() {
  levels=$("$wavstat" -in "$(native "$tmp/airplay-mix.wav")" -goertzel 1500 -window 100 2>&1) || {
    echo "e2e: wavstat: $levels" >&2
    return 1
  }
  result=$(printf '%s\n' "$levels" | awk -v most="$1" '
    { split($2, l, "="); split($3, r, "="); n++; left[n] = l[2] + 0; right[n] = r[2] + 0 }
    END {
      at70 = 0; at40 = 0; top = -1000; rmax = -1000
      for (i = 1; i <= n; i++) {
        if (left[i] >= -9.62 && left[i] <= -8.62) at70++
        if (left[i] >= -14.48 && left[i] <= -13.48) at40++
        if (left[i] > top) top = left[i]
        if (right[i] > rmax) rmax = right[i]
      }
      printf "level70_windows=%d level40_windows=%d (at most %d) loudest=%.1f right=%.1f", at70, at40, most * 10, top, rmax
      exit !(at70 >= 10 && at40 >= 10 && at40 <= most * 10 && top <= -8.62 && rmax <= top - 20)
    }') && { echo "e2e: airplay mix $result"; return 0; }
  echo "e2e: airplay mix $result" >&2
  return 1
}

# leaks SERVICE: how many of its log lines carry a url, the metadata, the announced text, the soloist key, the receiver
# names, the airplay client, a transcript (the turns', the memo's, the alarms block's), the alarm reply, the memory texts
# (raw or json-escaped Cyrillic), the hermes key, a task's text: its instructions, question, result, notice or ack
# ('Cadence E2E task ...'), the home: an entity's id, name or state (as a json value, or the sensor's unit), or the home
# assistant token, or mcp: the fake's token, its url's path, echo's descriptions (both), a call's arguments or a result
# ('Cadence E2E ...' too), or the log probe's text, or t3 code: the fake's token, its question and approval texts (its
# threads' titles and messages are 'Cadence E2E ...')
leaks() {
  $compose logs --no-log-prefix "$1" 2>/dev/null |
    grep -c -F -e 'Cadence E2E' -e 'music.flac' -e 'media/' -e 'Cadence e2e doorbell' -e 'cadence-e2e-fake-key' \
      -e 'Cadence Fake' -e 'cadence-fake-airplay-client' -e 'soloist_release' -e 'soloist-cdn' -e 'e2e memo' -e 'лампа' \
      -e '\u043b\u0430\u043c\u043f\u0430' -e 'sofa_lamp' -e 'e2e summary' -e 'e2e preference' \
      -e 'cadence-e2e-fake-hermes-key' -e 'light.hall' -e 'lock.front' -e 'sensor.hall_temperature' -e 'Hall light' \
      -e 'Front door' -e 'Hall temperature' -e '"locked"' -e '"unlocked"' -e '"21.5"' -e '°C' -e '\u00b0C' \
      -e 'cadence-e2e-fake-ha-token' -e 'cadence-e2e-fake-mcp-token' -e 'cadence-e2e-mcp-path' -e 'echo description' \
      -e 'mcp echo argument' -e 'mcp note argument' -e 'note kept' -e 'what time is it' -e 'cadence-e2e-logprobe' \
      -e 'Which color should the button be' -e 'Run make test' -e 'cadence-e2e-fake-t3-token' -e 'group.ogg' || true
}

# secret_leaks FILE: how many lines of FILE (- for stdin) hold the device token or the soloist key, the patterns read
# from a file of their own, so neither is ever on a command line
secret_leaks() { grep -c -F -f "$tmp/device-secrets" "$1" || true; }

# online_again LINKS: linked LINKS times, online
online_again() {
  [ "$($compose logs --no-log-prefix device-sim 2>/dev/null | grep -c '"msg":"link_up"')" -ge "$1" ] &&
    api GET "/api/v1/devices/$id" 2>/dev/null | jqc -e '.online' >/dev/null 2>&1
}

# turned BEFORE: more successful turns than BEFORE
turned() { [ "$(ok_turns)" -gt "$1" ]; }

# compacted N MODEL: the device has N compactions, the newest written on MODEL
compacted() {
  api GET "/api/v1/devices/$id/compactions" 2>/dev/null |
    jqc -e --argjson n "$1" --arg model "$2" 'length == $n and .[0].model == $model and (.[0].summary | length) > 0' \
      >/dev/null 2>&1
}

# message_id WHERE: the max id of the device's conversation messages matching WHERE, 0 for none; ids only, the
# conversation itself is never read
message_id() {
  $compose exec -T postgres psql -U cadence -d cadence -tAq \
    -c "SELECT coalesce(max(id), 0) FROM conversation_messages WHERE device_id = '$id' AND $1" 2>/dev/null | tr -d '[:space:]'
}

# compacted_all: the device's newest compaction takes in its newest message
compacted_all() {
  newest=$(message_id true) && [ "${newest:-0}" -gt 0 ] || return 1
  api GET "/api/v1/devices/$id/compactions" 2>/dev/null |
    jqc -e --argjson newest "$newest" 'length > 0 and .[0].uptoMessageId == $newest' >/dev/null 2>&1
}

# learned N FILTER: the device has N learning runs, the newest matching FILTER
learned() {
  api GET "/api/v1/devices/$id/learning" 2>/dev/null | jqc -e --argjson n "$1" "length == \$n and (.[0] | $2)" >/dev/null 2>&1
}

# alias_hit: the learned alias counted a recall
alias_hit() {
  api GET "/api/v1/memories?scope=device&deviceId=$id&kind=alias" 2>/dev/null |
    jqc -e 'length == 1 and .[0].hits >= 1 and .[0].lastHitAt != null' >/dev/null 2>&1
}

# feed_connected: the live feed helper is on the feed
feed_connected() { grep -q '^connected' "$tmp/feed.out" 2>/dev/null; }

# links: how many times the device linked so far
links() { $compose logs --no-log-prefix device-sim 2>/dev/null | grep -c '"msg":"link_up"' || true; }

# hermes_ready: the restarted server's hermes kind probed the fake ready, over tls against the dev ca with the key
hermes_ready() { $compose logs --no-log-prefix server 2>/dev/null | grep -q '"msg":"hermes_ready"'; }

# task_llm NAME WORD: a scripted llm whose turn starts a hermes task scripted by WORD and acks it, besides the
# maintenance and event rules
task_llm() {
  call="{\"name\":\"task_start\",\"arguments\":{\"kind\":\"hermes\",\"instructions\":\"$2 Cadence E2E task instructions\"}}"
  script="\"script\":[{\"toolCalls\":[$call]},{\"text\":\"Cadence E2E task started.\"}]"
  printf '{"name":"%s","module":"fake-llm","config":{%s,"rules":[%s,%s]},"secrets":{}}' "$1" "$script" "$maintenance" \
    "$news" >"$tmp/$1.json"
  api POST /api/v1/providers "@$(native "$tmp/$1.json")" >/dev/null || fail "provider $1 not created" server
}

# task_settings LLM: the global settings of the tasks block, on LLM, over the ones set before (the memory block's embed
# and memoryApproval stay)
task_settings() {
  api GET /api/v1/settings/global >"$tmp/global.json" || fail "global settings not read" server
  jqc --arg llm "$1" '. + {stt: "stt-fake", llm: $llm, tts: "tts-fake", voice: "fake", model: "fake", language: "en",
    hermes: "hermes-fake"}' <"$tmp/global.json" >"$tmp/global-tasks.json" || fail "global settings not merged" server
  api PUT /api/v1/settings/global "@$(native "$tmp/global-tasks.json")" >/dev/null ||
    fail "global settings not switched to $1" server
}

# quiet_hours FROM TO: the device's quiet hours from FROM to TO hours after the hour it is now on its own clock,
# Asia/Bishkek (UTC+6 all year). a window around the hour there (-1 2) does not hold the hour it is in UTC, so only the
# device's zone makes it quiet now
quiet_hours() {
  utc=$(date -u +%H)
  utc=${utc#0}
  here=$(((${utc:-0} + 6) % 24))
  api PUT "/api/v1/devices/$id/settings" \
    "$(printf '{"timeZone":"Asia/Bishkek","quietHours":{"enabled":true,"start":"%02d:00","end":"%02d:00"}}' \
      $(((here + $1 + 24) % 24)) $(((here + $2 + 24) % 24)))" >/dev/null || fail "quiet hours not set" server
}

# announces: the announcements the sim played whole: a stream summary of 10+ packets beside its wav
announces() {
  fetch_out || return 1
  set --
  for f in "$tmp"/out/out/*.json; do
    grep -q '"purpose":"announce"' "$f" 2>/dev/null && [ -s "${f%.json}.wav" ] && set -- "$@" "$f"
  done
  [ $# -gt 0 ] || { echo 0; return 0; }
  cat "$@" | jqc -s '[.[] | select(.packets >= 10)] | length'
}

# announced_more N: more than N announcements played whole
announced_more() {
  n=$(announces) || return 1
  [ "$n" -gt "$1" ]
}

# task_count N: the device has N tasks
task_count() { api GET "/api/v1/tasks?device=$id&limit=200" 2>/dev/null | jqc -e --argjson n "$1" 'length == $n' >/dev/null 2>&1; }

# task_is ID FILTER [JQ ARGS]: the task's detail, which holds its result, question and notices, matches FILTER; it is
# never shown
task_is() {
  _task=$1 _filter=$2
  shift 2
  api GET "/api/v1/tasks/$_task" 2>/dev/null | jqc -e "$@" "$_filter" >/dev/null 2>&1
}

# release: the fake hermes' hold runs end
release() {
  : >"$tmp/release"
  tar -C "$tmp" -cf - release | $compose cp - hermes:/tmp >/dev/null 2>&1 || fail "release file not written" hermes
}

# restart_server BLOCK: one deliberate server restart, for a setting it reads at startup, until it is healthy and the
# device linked again. a manual restart resets docker's restart count, so the count is checked before it and counts
# from it on in healthy
restart_server() {
  restarts=$(docker inspect -f '{{.RestartCount}}' "$($compose ps -q server)" 2>/dev/null) || restarts=
  [ "$restarts" = 0 ] || fail "the server restarted ${restarts:-?} times before $1" server
  linked=$(links)
  $compose restart server >/dev/null 2>&1 || fail "server not restarted" server
  within 60 "server not healthy after its deliberate restart" server healthy
  within 60 "device not linked again after the server's restart" "server device-sim" online_again $((linked + 1))
}

# ha_settings LLM SENSITIVE: the global settings of the ha block, on LLM with haSensitive SENSITIVE, over the ones set
# before (the tasks block's hermes and the memory block's embed and memoryApproval stay)
ha_settings() {
  api GET /api/v1/settings/global >"$tmp/global.json" || fail "global settings not read" server
  jqc --arg llm "$1" --arg sensitive "$2" '. + {llm: $llm, homeAssistant: "ha-fake", haFavorites: ["light.hall"],
    haSensitive: $sensitive}' <"$tmp/global.json" >"$tmp/global-ha.json" || fail "global settings not merged" server
  api PUT /api/v1/settings/global "@$(native "$tmp/global-ha.json")" >/dev/null ||
    fail "global settings not switched to $1" server
}

# ha_llm NAME ARGS: a scripted llm whose turn calls ha_control with ARGS, then says one line, besides the maintenance
# rules
ha_llm() {
  call="{\"name\":\"ha_control\",\"arguments\":$2}"
  script="\"script\":[{\"toolCalls\":[$call]},{\"text\":\"Cadence E2E home done.\"}]"
  printf '{"name":"%s","module":"fake-llm","config":{%s,"rules":[%s]},"secrets":{}}' "$1" "$script" "$maintenance" \
    >"$tmp/$1.json"
  api POST /api/v1/providers "@$(native "$tmp/$1.json")" >/dev/null || fail "provider $1 not created" server
}

# ha_connected: the server's client is connected to the fake home assistant on the instance the setting names, and its
# view loaded the home: three entities in one area
ha_connected() {
  api GET /api/v1/integrations/homeassistant 2>/dev/null | jqc -e '.connected and .entities == 3 and .areas == 1 and
    .instance == "ha-fake" and .configured == "ha-fake" and (.restartRequired | not)' >/dev/null 2>&1
}

# ha_calls: the service calls the fake home assistant answered, one `domain service entity_id` a line
ha_calls() { $compose cp homeassistant:/tmp/calls.log - 2>/dev/null | tar -x -O -f - calls.log 2>/dev/null; }

# ha_called CALL: the fake home assistant answered CALL
ha_called() { ha_calls | grep -qxF "$1"; }

# ha_state ENTITY STATE: the home's live view, as the admin api shows it, has ENTITY in STATE
ha_state() {
  api GET "/api/v1/integrations/homeassistant/entities?q=$1" 2>/dev/null |
    jqc -e --arg entity "$1" --arg state "$2" 'any(.[]; .id == $entity and .state == $state)' >/dev/null 2>&1
}

# ha_used ENTITY: the device's home usage ranks ENTITY
ha_used() {
  api GET "/api/v1/integrations/homeassistant/usage?device=$id" 2>/dev/null |
    jqc -e --arg entity "$1" 'any(.[]; .entityId == $entity and .score > 0)' >/dev/null 2>&1
}

# ha_step ERRORS: the device's newest agent step that ran ha_control logged ERRORS as its tool errors (codes only)
ha_step() {
  $compose logs --no-log-prefix server 2>/dev/null | grep '"msg":"agent_step"' | grep "\"device_id\":\"$id\"" |
    grep '"tools":"ha_control"' | tail -n 1 | grep -q "\"tool_errors\":\"$1\""
}

# press BUTTON: the sim's press file, which acts as that button once (action, volume_up or volume_down)
press() {
  printf '%s\n' "$1" >"$tmp/press"
  chmod 644 "$tmp/press"
  tar -C "$tmp" -cf - press | $compose cp - device-sim:$data || fail "press file not written" device-sim
}

# lines SERVICE MSG [TEXT...]: how many of the service's log lines of MSG hold every TEXT
lines() {
  _out=$($compose logs --no-log-prefix "$1" 2>/dev/null | grep -F "\"msg\":\"$2\"") || { echo 0; return 0; }
  shift 2
  for _t in "$@"; do
    _out=$(printf '%s\n' "$_out" | grep -F -e "$_t") || { echo 0; return 0; }
  done
  printf '%s\n' "$_out" | wc -l | tr -d ' '
}

# more_lines BEFORE SERVICE MSG [TEXT...]: more such lines than BEFORE
more_lines() {
  _before=$1
  shift
  [ "$(lines "$@")" -gt "$_before" ]
}

# last_session TRIGGER: the id of the newest session the device began on TRIGGER
last_session() {
  $compose logs --no-log-prefix device-sim 2>/dev/null | grep -F '"msg":"session_start"' |
    grep -F "\"trigger\":\"$1\"" | tail -n 1 | sed 's/.*"session_id":"\([^"]*\)".*/\1/'
}

# follow_ups ENABLED: the device's follow-ups (an 8 s window, at most one in a row), as a device override, which a
# global put keeps: on in the alarms block alone, as the other blocks count their sessions without one
follow_ups() {
  api PUT "/api/v1/devices/$id/settings" "{\"followUp\":{\"enabled\":$1,\"windowMs\":8000,\"limit\":1}}" >/dev/null ||
    fail "device follow-ups not switched to $1" server
}

# m6a_settings STT LLM: the global settings of the alarms block, on STT and LLM, with timeZone set explicitly (R6)
m6a_settings() {
  api PUT /api/v1/settings/global "{\"stt\":\"$1\",\"llm\":\"$2\",\"tts\":\"tts-fake\",\"voice\":\"fake\",\"model\":\"fake\",\"language\":\"en\",\"timeZone\":\"Asia/Bishkek\"}" \
    >/dev/null || fail "global settings not switched to $1 and $2" server
}

# provider NAME MODULE CONFIG: a fake instance, its config as JSON
provider() {
  printf '{"name":"%s","module":"%s","config":%s,"secrets":{}}' "$1" "$2" "$3" >"$tmp/$1.json"
  api POST /api/v1/providers "@$(native "$tmp/$1.json")" >/dev/null || fail "provider $1 not created" server
}

# alarms_state: the device's alarms as its alarms capability reports them, never shown
alarms_state() { api GET "/api/v1/devices/$id" 2>/dev/null | jqc -c '.capabilities[] | select(.id == "alarms") | .state.alarms'; }

# zone_applied: the alarms config the server pushed holds the zone a scope set, and the device applied it
zone_applied() {
  api GET "/api/v1/devices/$id" 2>/dev/null |
    jqc -e 'any(.capabilities[]; .id == "alarms" and .desired.timeZone == "Asia/Bishkek" and .desiredGen > 0 and .appliedGen == .desiredGen and .appliedError == "")' >/dev/null 2>&1
}

# alarm_listed TIME: the device's one alarm rings at TIME at level 60
alarm_listed() {
  api GET "/api/v1/devices/$id" 2>/dev/null | jqc -e --arg t "$1" \
    'any(.capabilities[]; .id == "alarms" and (.state.alarms | length == 1 and .[0].time == $t and .[0].level == 60))' >/dev/null 2>&1
}

# ramp_levels: the levels the ring's ramp set on the hardware, in order, one a line
ramp_levels() {
  $compose logs --no-log-prefix device-sim 2>/dev/null | grep -F '"msg":"volume_transient_applied"' |
    grep -o '"audiod_level":[0-9]*' | cut -d: -f2
}

# ramp_reached LEVEL: the ramp set LEVEL or more
ramp_reached() { [ "$(ramp_levels | tail -n 1)" -ge "$1" ] 2>/dev/null; }

# ramped: the ramp only rose, in 5-point steps from one above the level in force (30) to at most the alarm's (60)
ramped() {
  ramp_levels | awk '
    { n++; if ($1 % 5 != 0 || $1 > 60 || (n == 1 && $1 != 35) || (n > 1 && $1 <= last)) bad = 1; last = $1 }
    END { printf "e2e: ramp %d steps to %d\n", n, last; exit !(n >= 2 && !bad) }'
}

# alarm_ramp: the alarm's 1100 Hz tone (the last of its three, -12 dBFS at full level, once per 2 s loop) in the mix per
# 50 ms, left: at volume v it sits at -12.03 + 20 log10(v/100) dBFS. the loudest window of each loop is that loop's
# level: the first at 30's -22.5 within 0.5 dB, none quieter than the one before it by more than 0.5 dB (the last may
# be cut by the press), reaching at least 40's -20.0 and never louder than 60's -16.5
alarm_ramp() {
  levels=$("$wavstat" -in "$(native "$tmp/alarm-mix.wav")" -goertzel 1100 -window 50 2>&1) || {
    echo "e2e: wavstat: $levels" >&2
    return 1
  }
  result=$(printf '%s\n' "$levels" | awk '
    { split($2, l, "="); v = l[2] + 0
      if (v >= -30) { if (!on) { loops++; top[loops] = v; on = 1 } else if (v > top[loops]) top[loops] = v } else on = 0 }
    END {
      loud = -1000; rising = 1
      for (i = 1; i <= loops; i++) if (top[i] > loud) loud = top[i]
      for (i = 2; i < loops; i++) if (top[i] < top[i - 1] - 0.5) rising = 0
      printf "alarm_loops=%d first=%.1f loudest=%.1f rising=%d", loops, top[1], loud, rising
      exit !(loops >= 5 && top[1] >= -23.0 && top[1] <= -22.0 && rising && loud >= -20.5 && loud <= -16.0)
    }') && { echo "e2e: alarm mix $result"; return 0; }
  echo "e2e: alarm mix $result" >&2
  return 1
}

# mcp_llm NAME STEPS: a scripted llm whose turn runs STEPS (a json array of script steps), besides the maintenance rules
mcp_llm() {
  printf '{"name":"%s","module":"fake-llm","config":{"script":%s,"rules":[%s]},"secrets":{}}' "$1" "$2" "$maintenance" \
    >"$tmp/$1.json"
  api POST /api/v1/providers "@$(native "$tmp/$1.json")" >/dev/null || fail "provider $1 not created" server
}

# mcp_settings LLM: the global settings of the mcp block, on LLM with the fake's instance running, over the ones set
# before (the ha block's, the tasks block's and the memory block's stay)
mcp_settings() {
  api GET /api/v1/settings/global >"$tmp/global.json" || fail "global settings not read" server
  jqc --arg llm "$1" '. + {llm: $llm, mcp: ["mcp-fake"]}' <"$tmp/global.json" >"$tmp/global-mcp.json" ||
    fail "global settings not merged" server
  api PUT /api/v1/settings/global "@$(native "$tmp/global-mcp.json")" >/dev/null ||
    fail "global settings not switched to $1" server
}

# mcp_connected: the fake's server connected in the modern era with both its tools listed and offerable, no failure
mcp_connected() {
  api GET /api/v1/mcp/servers 2>/dev/null | jqc -e 'length == 1 and (.[0] | .instance == "mcp-fake" and
    .status == "connected" and .era == "modern" and .host == "mcp:8443" and .lastCode == "" and
    .tools == {total: 2, offered: 2, changed: 0, skipped: 0})' >/dev/null 2>&1
}

# mcp_tool REMOTE FILTER: the fake's tool REMOTE, as the tools route shows it, matches FILTER; the body holds the
# tool's description, so it is never shown
mcp_tool() {
  api GET /api/v1/mcp/servers/mcp-fake/tools 2>/dev/null |
    jqc -e --arg remote "$1" "any(.[]; .remote == \$remote and ($2))" >/dev/null 2>&1
}

# mcp_calls: the calls the fake mcp server answered, one tool name a line
mcp_calls() { $compose cp mcp:/tmp/calls.log - 2>/dev/null | tar -x -O -f - calls.log 2>/dev/null; }

# mcp_called CALLS: the fake answered exactly CALLS (tool names, one a line, in order)
mcp_called() {
  _calls=$(mcp_calls) || return 1
  [ "$_calls" = "$1" ]
}

# turn_steps: the device's newest agent turn's steps in order, one `tools|tool_errors` line each: the registered tools
# a step called and its error codes, never a name the model made up
turn_steps() {
  logs=$($compose logs --no-log-prefix server 2>/dev/null) || return 1
  turn=$(printf '%s\n' "$logs" | grep '"msg":"agent_turn"' | grep "\"device_id\":\"$id\"" | tail -n 1 |
    sed -n 's/.*"turn_id":"\([^"]*\)".*/\1/p')
  [ -n "$turn" ] || return 1
  printf '%s\n' "$logs" | grep '"msg":"agent_step"' | grep "\"turn_id\":\"$turn\"" |
    sed -n 's/.*"tools":"\([^"]*\)","tool_errors":"\([^"]*\)".*/\1|\2/p'
}

# stepped STEPS: the device's newest agent turn ran STEPS (turn_steps' lines)
stepped() { [ "$(turn_steps)" = "$1" ]; }

# private_body FILE PATTERN...: FILE, an admin body, holds none of the patterns
private_body() {
  _file=$1
  shift
  for p in "$@"; do
    ! grep -q -F -e "$p" "$_file" || return 1
  done
}

# metrics_synced: the pushed system.metrics config (a round every 10 s) applied
metrics_synced() {
  api GET "/api/v1/devices/$id" 2>/dev/null |
    jqc -e 'any(.capabilities[]; .id == "system.metrics" and .desired.intervalS == 10 and .desiredGen > 0 and
      .appliedGen == .desiredGen and .appliedError == "")' >/dev/null 2>&1
}

# device_logs SOURCE [QUERY]: the device's stored lines of SOURCE, newest first, into $tmp/logs-SOURCE.json, QUERY more
# of the query string; their bodies are the lines as written (a child's) or with a secret's marker, never shown
device_logs() { api GET "/api/v1/logs?device=$id&source=$1&limit=1000${2:+&$2}" >"$tmp/logs-$1.json" 2>/dev/null; }

# stamped SOURCE FILTER [QUERY]: the device has stored lines of SOURCE, each stamped with its id, name and board, one
# of them matching FILTER
stamped() {
  device_logs "$1" "${3:-}" || return 1
  jqc -e --arg id "$id" --arg source "$1" --arg name sim-e2e --arg board sim "(.logs | length > 0) and all(.logs[];
    .deviceId == \$id and .deviceName == \$name and .board == \$board and .source == \$source) and any(.logs[]; $2)" \
    <"$tmp/logs-$1.json" >/dev/null 2>&1
}

# logprobe TEXT: the sim's probe file holds TEXT's lines, readable by the sim's user (65532, distroless' nonroot) alone:
# the archive names its owner and mode, as git bash's files are all 644, and cp -a keeps them; the local copy goes at
# once
logprobe() {
  (umask 077 && printf '%s\n' "$1" >"$tmp/logprobe") || fail "probe file not written" device-sim
  _rc=0
  tar --owner=65532 --group=65532 --numeric-owner --mode=600 -C "$tmp" -cf - logprobe |
    $compose cp -a - device-sim:$data >/dev/null 2>&1 || _rc=$?
  rm -f "$tmp/logprobe"
  [ "$_rc" = 0 ] || fail "probe file not copied" device-sim
}

# probe_taken: the sim read its probe file and removed it
probe_taken() { ! $compose cp device-sim:$data/logprobe - >/dev/null 2>&1; }

# probes: the bodies of the device's stored probe lines, one a line, into $tmp/probes; never shown
probes() { device_logs sim-probe && jqc -r '.logs[].body' <"$tmp/logs-sim-probe.json" >"$tmp/probes"; }

# probe_stored BODY [COUNT]: the device stored the probe line BODY COUNT times (1)
probe_stored() { probes && [ "$(grep -c -x -F -e "$1" "$tmp/probes")" = "${2:-1}" ]; }

# metrics_arrive: system.memory.used is a gauge whose newest three points lie within 25 s (rounds 10 s apart, by the
# device's clock), cadence.sessions a sum with points; into $tmp/metrics.json
metrics_arrive() {
  api GET "/api/v1/devices/$id/metrics?name=system.memory.used&name=cadence.sessions" >"$tmp/metrics.json" 2>/dev/null &&
    jqc -e 'any(.series[]; .name == "system.memory.used" and .kind == "gauge" and
        ([.points[].ts[0:19] + "Z" | fromdate] | sort | length >= 3 and .[-1] - .[-3] <= 25)) and
      any(.series[]; .name == "cadence.sessions" and .kind == "sum" and (.points | length) > 0)' <"$tmp/metrics.json" \
      >/dev/null 2>&1
}

# processes_listed: the device's top processes, into $tmp/processes.json: both lists, cadenced among them, and each
# entry its pid, comm, cpu and rss alone
processes_listed() {
  api GET "/api/v1/devices/$id/processes" >"$tmp/processes.json" 2>/dev/null &&
    jqc -e 'keys == ["topCpu", "topRss"] and (.topCpu | length > 0) and (.topRss | length > 0) and
      all(.topCpu[], .topRss[]; keys == ["comm", "cpu", "pid", "rss"]) and any(.topRss[]; .comm == "cadenced")' \
      <"$tmp/processes.json" >/dev/null 2>&1
}

# scraped: the prometheus exposition, read with the metrics token's header file into $tmp/prom.txt, has the device's
# memory gauges (the limit under its own name) and session counters, each series with the device's id, name and board
scraped() {
  curl -sS -m 10 --fail-with-body -H "@$(native "$tmp/metrics-header")" "$base/api/v1/metrics" >"$tmp/prom.txt" \
    2>/dev/null || return 1
  grep -qx '# TYPE cadence_system_memory_used gauge' "$tmp/prom.txt" &&
    grep -qx '# TYPE cadence_system_memory_limit gauge' "$tmp/prom.txt" &&
    grep -qx '# TYPE cadence_sessions_total counter' "$tmp/prom.txt" || return 1
  for _m in cadence_system_memory_used cadence_system_memory_limit cadence_sessions_total; do
    grep "^$_m{" "$tmp/prom.txt" | grep -F "device_id=\"$id\"" | grep -F 'device_name="sim-e2e"' |
      grep -q -F 'board="sim"' || return 1
  done
}

# logs_dropped: the records the device's ring dropped, as its logs capability last reported them
logs_dropped() {
  api GET "/api/v1/devices/$id" 2>/dev/null | jqc -r '.capabilities[] | select(.id == "logs") | .state.dropped'
}

# drained BEFORE: the three lines the probe made while the server was down are stored, each once, and the ring dropped
# nothing; or it overflowed meanwhile: dropped grew past BEFORE, and the newest of them is stored, as the ring drops the
# oldest. sets drain to the outcome
drained() {
  probes || return 1
  _now=$(logs_dropped) || return 1
  case $_now in
    '' | *[!0-9]*) return 1 ;;
  esac
  if [ "$_now" -gt "$1" ]; then
    drain="the ring overflowed: dropped $1 -> $_now"
    [ "$(grep -c -x -F -e 'cadence-e2e-logprobe offline 3' "$tmp/probes")" = 1 ]
    return
  fi
  drain="drained, nothing dropped"
  for _n in 1 2 3; do
    [ "$(grep -c -x -F -e "cadence-e2e-logprobe offline $_n" "$tmp/probes")" = 1 ] || return 1
  done
}

# ha_client LINE: the ha client's smoke printed LINE. a smoke that failed or ended before it fails the run with its
# lines, which carry codes alone. docker tells whether its container ended (kill -0 would not: an ended child a shell
# never reaps still answers it); then the cli that copies its output is waited for, keeping its exit code in ha_rc, and
# the line looked for once more
ha_client() {
  grep -qxF -e "$1" "$tmp/ha-client.out" 2>/dev/null && return 0
  grep -q '^failed' "$tmp/ha-client.out" 2>/dev/null &&
    fail "the ha client failed before \"$1\": $(cat "$tmp/ha-client.out")" server
  case $(docker inspect -f '{{.State.Status}}' "$ha_container" 2>/dev/null) in
    exited | dead | '')
      if [ -n "$ha_pid" ]; then
        wait "$ha_pid" && ha_rc=0 || ha_rc=$?
        ha_pid=
      fi
      grep -qxF -e "$1" "$tmp/ha-client.out" 2>/dev/null && return 0
      fail "the ha client ended before \"$1\": $(cat "$tmp/ha-client.out" 2>/dev/null)" server
      ;;
  esac
  return 1
}

$compose up -d --build

within 60 "server not healthy" server healthy

token=$($compose exec -T server /cadence-server admin token create --name e2e) || fail "admin token not created" server
case $token in
  cdn_*) ;;
  *) fail "admin token malformed" server ;;
esac

device=$(api POST /api/v1/devices '{"name":"sim-e2e","model":"sim"}') || fail "device not created" server
id=$(printf '%s' "$device" | jqc -r .id) || id=
code=$(printf '%s' "$device" | jqc -r .code) || code=
case "$id:$code" in
  :* | *: | null:* | *:null) fail "device reply without id or code" server ;;
esac

$compose exec -T device-sim /cadenced enroll --server http://server:8080 --code "$code" --board sim \
  --config $data/config.json --data-dir $data --audiod || fail "enroll failed" "server device-sim"

for p in stt tts; do
  api POST /api/v1/providers "{\"name\":\"$p-fake\",\"module\":\"fake-$p\",\"config\":{},\"secrets\":{}}" >/dev/null ||
    fail "provider $p-fake not created" server
done
# compactAfter counts seconds, so these turns are compacted long before the memory block: every llm of theirs answers
# the summary prompt and learns nothing, and the memory block starts from a conversation compacted to its end
maintenance='{"system":"running summary","text":"{\"summary\":\"e2e summary: earlier turns.\",\"openThreads\":[],\"taskRefs\":[]}"},{"system":"long-term memory","text":"{\"ops\":[]}"}'
printf '{"name":"llm-fake","module":"fake-llm","config":{"rules":[%s]},"secrets":{}}' "$maintenance" >"$tmp/llm-fake.json"
api POST /api/v1/providers "@$(native "$tmp/llm-fake.json")" >/dev/null || fail "provider llm-fake not created" server
api PUT /api/v1/settings/global \
  '{"stt":"stt-fake","llm":"llm-fake","tts":"tts-fake","voice":"fake","model":"fake","language":"en"}' >/dev/null ||
  fail "global settings not set" server
# a spoken reply opens a follow-up the say file speaks into (M6a-R4): off outside the alarms block
follow_ups false

within 30 "device not online with audio.output" "server device-sim" reported
# the list's online comes from the hub, as the detail's
api GET /api/v1/devices | jqc -e --arg id "$id" 'any(.[]; .id == $id and .online)' >/dev/null ||
  fail "the device list does not show the device online" server
set_level 30
within 30 "audio.output level 30 not reported" "server device-sim" synced
# the default duck mode, pushed: the config sync the mix check relies on
api PUT "/api/v1/devices/$id/capabilities/media.player/config" '{"duckMode":"duck"}' >/dev/null ||
  fail "media.player config refused" server
within 30 "media.player config not applied" "server device-sim" player_synced

say
within 30 "no reply stream of 10+ packets" "server device-sim" replied
within 30 "no successful turn line" server turn_logged
within 30 "audiod did not run on the device" device-sim audiod_ran
reply_wav || fail "reply wav is not a 48 kHz mono 16-bit non-silent file" device-sim
ls "$tmp"/out/out/*.opusframes >/dev/null 2>&1 && fail "the reply went through the fixture io, not audiod" device-sim

# stop, not restart: audiod finalizes mix-000.wav when it is stopped, and the next run's configure replaces it
$compose stop device-sim >/dev/null 2>&1 || fail "device not stopped" device-sim
$compose cp device-sim:$data/sim/out/mix - 2>/dev/null | tar -x -O -f - mix/mix-000.wav >"$tmp/mix.wav" 2>/dev/null || fail "mix wav not copied" device-sim
mix_gain || fail "mix wav is not 48 kHz stereo 16-bit at about 0.3x the reply (volume gain not applied, or silent)" device-sim
$compose start device-sim >/dev/null 2>&1 || fail "device not started" device-sim
within 30 "device not restarted with its level and config" device-sim restarted
within 30 "audio.output level 30 lost across the restart" "server device-sim" synced

# the scripted llm calls device_volume, then would answer with nothing: the volume speaks for itself (M3-R1)
script='"script":[{"toolCalls":[{"name":"device_volume","arguments":{"level":20}}]},{"text":""}]'
printf '{"name":"llm-script","module":"fake-llm","config":{%s,"rules":[%s]},"secrets":{}}' "$script" "$maintenance" \
  >"$tmp/llm-script.json"
api POST /api/v1/providers "@$(native "$tmp/llm-script.json")" >/dev/null || fail "provider llm-script not created" server
api PUT /api/v1/settings/global \
  '{"stt":"stt-fake","llm":"llm-script","tts":"tts-fake","voice":"fake","model":"fake","language":"en"}' >/dev/null ||
  fail "global settings not switched to llm-script" server
before=$(streams) || fail "sim streams not read" device-sim
[ "$before" -ge 1 ] || fail "the first turn's stream not found" device-sim
say
within 30 "volume tool's level 20 not applied" "server device-sim" lowered
within 30 "no silent device_volume turn" server silent_turn
# the server timed no first audio for it, and the device began no stream: a StreamStart precedes the close
if ok_turn_lines | tail -n 1 | grep -q '"firstAudioMs"'; then
  fail "the silent turn sent audio" server
fi
sleep 2
after=$(streams) || fail "sim streams not read" device-sim
[ "$after" = "$before" ] || fail "the silent turn began a stream" "server device-sim"

# conversation history (M7bs-D5, ruling M7bs-R4): the scripted turn is the newest, from a press, in fast mode, with its
# device_volume call by name and the transcript, never cached; the page after it is the first turn; its detail shows the
# call as stored; an ha token reaches neither route. the transcript stays out of every log (the privacy block). the ha
# token goes in a header file, never an argument, and is deleted again
history_start=$(date +%s)
http=$(curl -sS -m 10 -D "$(native "$tmp/turns-headers")" -o "$(native "$tmp/turns.json")" -w '%{http_code}' \
  -H "Authorization: Bearer $token" "$base/api/v1/devices/$id/turns?limit=1") || http=
[ "$http" = 200 ] || fail "the turns answered ${http:-nothing}" server
tr -d '\r' <"$tmp/turns-headers" | grep -qix 'cache-control: no-store' || fail "the turns are cacheable" server
jqc -e '.turns | length == 1 and (.[0] | .trigger == "button" and .mode == "fast" and .user == "what time is it" and
  .tools == [{name: "device_volume"}])' <"$tmp/turns.json" >/dev/null 2>&1 ||
  fail "the newest turn is not the scripted device_volume turn" server
turn_id=$(jqc -r '.turns[0].turnId' <"$tmp/turns.json") || turn_id=
next=$(jqc -r '.next' <"$tmp/turns.json") || next=
api GET "/api/v1/devices/$id/turns?limit=1&before=$next" >"$tmp/turns-2.json" || fail "the turns' second page not read" server
jqc -e --arg id "$turn_id" '.turns | length == 1 and .[0].turnId != $id and .[0].tools == [] and
  .[0].user == "what time is it"' <"$tmp/turns-2.json" >/dev/null 2>&1 || fail "the second page is not the first turn" server
api GET "/api/v1/devices/$id/turns/$turn_id" >"$tmp/turn.json" || fail "the turn's detail not read" server
jqc -e '[.messages[] | .toolCalls[]? | select(.name == "device_volume" and .arguments.level == 20) | .id] as $c |
  ($c | length) == 1 and any(.messages[]; .role == "tool" and .toolCallId == $c[0])' <"$tmp/turn.json" >/dev/null 2>&1 ||
  fail "the turn's detail does not show its device_volume call and result" server
api POST /api/v1/tokens '{"name":"e2e-turns-ha","scopes":["ha"]}' >"$tmp/turns-ha.json" || fail "the ha token not created" server
jqc -r '"Authorization: Bearer " + .token' <"$tmp/turns-ha.json" >"$tmp/turns-ha-header" || fail "the ha token not read" server
for p in "/api/v1/devices/$id/turns" "/api/v1/devices/$id/turns/$turn_id"; do
  http=$(curl -sS -m 10 -o "$(native "$tmp/body")" -w '%{http_code}' -H "@$(native "$tmp/turns-ha-header")" "$base$p") ||
    http=
  { [ "$http" = 403 ] && jqc -e '.error == "forbidden"' <"$tmp/body" >/dev/null 2>&1; } ||
    fail "the ha token's read of ${p#/api/v1/devices/"$id"} answered ${http:-nothing}" server
done
api DELETE /api/v1/tokens/e2e-turns-ha >/dev/null || fail "the ha token not deleted" server
jqc -r '.token' <"$tmp/turns-ha.json" >"$tmp/turns-ha-secret" || fail "the ha token not read" server
n=$($compose logs --no-log-prefix server 2>/dev/null | grep -c -F -f "$tmp/turns-ha-secret" || true)
[ "$n" = 0 ] || fail "$n server log lines carry the history block's ha token" server
echo "e2e: history block $(($(date +%s) - history_start)) s"

# a url session over the compose network, then an announcement over it, at the volume the mix check expects
set_level 30
within 30 "audio.output level 30 not reported before the music" "server device-sim" synced
http=$(status POST "/api/v1/devices/$id/capabilities/media.url/commands/play" \
  '{"url":"http://media/music.flac","metadata":{"title":"Cadence E2E Song","artist":"Cadence E2E Artist"}}') || http=
[ "$http" = 200 ] || fail "url play answered ${http:-nothing}: $(cat "$tmp/body" 2>/dev/null)" "server device-sim media"
played=$(date +%s)
within 10 "media state not playing the url with its metadata" "server device-sim" playing_song
# about 2 s into the 8 s file
while [ "$(date +%s)" -lt $((played + 2)) ]; do sleep 1; done
http=$(status POST "/api/v1/devices/$id/announce" '{"text":"Cadence e2e doorbell"}') || http=
[ "$http" = 202 ] || fail "announce answered ${http:-nothing}: $(cat "$tmp/body" 2>/dev/null)" "server device-sim"
within 15 "no announce stream summary with its wav" device-sim announced
remaining=$((played + 15 - $(date +%s)))
within $((remaining > 1 ? remaining : 1)) "media state not idle within 15 s of play" "server device-sim" media_idle

# stop: audiod finalizes this run's mix-000.wav
$compose stop device-sim >/dev/null 2>&1 || fail "device not stopped" device-sim
$compose cp device-sim:$data/sim/out/mix - 2>/dev/null | tar -x -O -f - mix/mix-000.wav >"$tmp/music-mix.wav" 2>/dev/null ||
  fail "music mix wav not copied" device-sim
music_duck || fail "the mix does not show the music at volume 30, ducked under the announcement and back" device-sim

# soloist (M4c): the key and the config enable it; the updater takes the fake's build from the fake cdn over https,
# audiod probes it, the switch starts it (unjailed: the sim's build), and the fake plays its own part
$compose start device-sim >/dev/null 2>&1 || fail "device not started" device-sim
within 30 "device not back online with media.soloist" "server device-sim" soloist_back
http=$(status PUT "/api/v1/devices/$id/capabilities/media.soloist/secrets/apiKey" '{"value":"cadence-e2e-fake-key"}') ||
  http=
[ "$http" = 204 ] || fail "the soloist key answered ${http:-nothing}: $(cat "$tmp/body" 2>/dev/null)" "server device-sim"
enabled=$(date +%s)
api PUT "/api/v1/devices/$id/capabilities/media.soloist/config" '{"enabled":true,"deviceName":"Cadence E2E"}' \
  >/dev/null || fail "media.soloist config refused" server
within 60 "media.soloist not ready on a build from the fake cdn" "server device-sim soloist-cdn" soloist_ready
echo "e2e: soloist ready $(($(date +%s) - enabled)) s after its config"
logs=$($compose logs --no-log-prefix device-sim 2>/dev/null)
[ "$(printf '%s\n' "$logs" | grep '"msg":"soloist_update"' | grep -c '"result":"ok"')" = 1 ] ||
  fail "the device did not install exactly one build" device-sim
printf '%s\n' "$logs" | grep '"msg":"soloist_switch"' | grep -q '"reason":"install"' ||
  fail "the device did not switch onto the build it installed" device-sim
within 15 "media state not playing the fake's track" "server device-sim" soloist_media playing

# rule 7: the app's stored level (55) moves nothing, the slider's move after it (25) sets the level like a press
within 15 "the level never became the fake's slider move" "server device-sim" level 25
logs=$($compose logs --no-log-prefix device-sim 2>/dev/null)
reported=$(printf '%s\n' "$logs" | grep -c '"msg":"soloist_sender_volume"' || true)
applied=$(printf '%s\n' "$logs" | grep '"msg":"media_volume"' | grep -c '"sender":"soloist"' || true)
[ "$reported" = 1 ] && [ "$applied" = 1 ] && printf '%s\n' "$logs" | grep '"msg":"media_volume"' | grep -q '"volume":25' ||
  fail "the fake's slider moved the level $applied times from $reported moves, not once to 25" device-sim
# each level holds 2 s for the mix check
sleep 2
at35=$(date +%s)
set_level 35
within 10 "the fake never got set_volume 35" device-sim commanded '.command == "set_volume" and .volume == 35'
sleep 2
http=$(status POST "/api/v1/devices/$id/capabilities/media.player/commands/pause" '{}') || http=
[ "$http" = 200 ] || fail "pause answered ${http:-nothing}: $(cat "$tmp/body" 2>/dev/null)" "server device-sim"
paused=$(date +%s)
within 10 "media state not paused on the fake's track" "server device-sim" soloist_media paused
within 10 "the fake never got pause" device-sim commanded '.command == "pause"'
# paused well beyond the slack below: a tone that went on would show
sleep 6

$compose stop device-sim >/dev/null 2>&1 || fail "device not stopped" device-sim
$compose cp device-sim:$data/sim/out/mix - 2>/dev/null | tar -x -O -f - mix/mix-000.wav >"$tmp/soloist-mix.wav" 2>/dev/null ||
  fail "soloist mix wav not copied" device-sim
# set_level to pause, with 3 s for the whole seconds and the pipeline's latency
soloist_tone $((paused - at35 + 3)) ||
  fail "the mix does not show the fake's tone at 25 then 35, ended by the pause" device-sim

# airplay (M4d): the config enables the receiver, which audiod runs from the runtime in the data dir: the private bus
# and the fake shairport sync in shairport's place, no avahi or nqptp (the sim's build). once the receiver connected,
# the fake casts on its own: its track and tone, the level its app stored (50), then its slider's move (70). soloist
# plays again after the restart, and the cast takes the session from it
airplay_start=$(date +%s)
$compose start device-sim >/dev/null 2>&1 || fail "device not started" device-sim
within 30 "device not back online for airplay" "server device-sim" online_again 4
within 30 "media state not playing the fake soloist's track after the restart" "server device-sim" soloist_media playing
within 15 "the level never became the fake soloist's slider move after the restart" "server device-sim" level 25
# only what the fake soloist gets from here on counts: the soloist block paused it before
seen=$(commands_seen)
api PUT "/api/v1/devices/$id/capabilities/media.airplay/config" '{"enabled":true,"name":"Cadence E2E"}' >/dev/null ||
  fail "media.airplay config refused" server
within 30 "media.airplay not ready" "server device-sim" airplay_ready
echo "e2e: airplay ready $(($(date +%s) - airplay_start)) s after the device started"
within 15 "media state not playing the fake shairport's track" "server device-sim" airplay_media playing
within 10 "soloist was not paused as the cast took the session" device-sim commanded '.command == "pause"' "$seen"
logs=$($compose logs --no-log-prefix device-sim 2>/dev/null)
printf '%s\n' "$logs" | grep '"msg":"airplay_daemon_started"' | grep -q '"daemon":"shairport"' ||
  fail "audiod did not start the fake shairport" device-sim
if printf '%s\n' "$logs" | grep '"msg":"airplay_daemon_started"' | grep -q -e '"daemon":"avahi"' -e '"daemon":"nqptp"'; then
  fail "the sim's audiod started avahi or nqptp" device-sim
fi
$compose cp device-sim:$data/run/airplay/shairport.conf - 2>/dev/null | tar -x -O -f - shairport.conf 2>/dev/null |
  grep -qxF '  name = "Cadence E2E";' || fail "shairport's config does not carry the receiver's name" device-sim

# rule 7: the level the app stored moves nothing, the slider's move after it sets the level like a press
within 15 "the level never became the airplay slider's move" "server device-sim" level 70
logs=$($compose logs --no-log-prefix device-sim 2>/dev/null)
reported=$(printf '%s\n' "$logs" | grep -c '"msg":"airplay_sender_volume"' || true)
applied=$(printf '%s\n' "$logs" | grep '"msg":"media_volume"' | grep -c '"sender":"airplay"' || true)
[ "$reported" = 1 ] && [ "$applied" = 1 ] &&
  printf '%s\n' "$logs" | grep '"msg":"media_volume"' | grep '"sender":"airplay"' | grep -q '"volume":70' ||
  fail "the airplay slider moved the level $applied times from $reported moves, not once to 70" device-sim
# each level holds 2 s for the mix check
sleep 2
at40=$(date +%s)
calls=$(airplay_calls_seen)
set_level 40
within 10 "the fake shairport never got the level 40 as its volume" device-sim airplay_called 'SetAirplayVolume -18.00' \
  "$calls"
sleep 2
calls=$(airplay_calls_seen)
http=$(status POST "/api/v1/devices/$id/capabilities/media.player/commands/pause" '{}') || http=
[ "$http" = 200 ] || fail "airplay pause answered ${http:-nothing}: $(cat "$tmp/body" 2>/dev/null)" "server device-sim"
paused=$(date +%s)
within 10 "media state not paused on the fake shairport's track" "server device-sim" airplay_media paused
within 10 "the fake shairport never got Pause" device-sim airplay_called Pause "$calls"
# paused well beyond the slack below: a tone that went on would show
sleep 6
# off again before the stop, so the next boot restores it off and the fake casts no more
api PUT "/api/v1/devices/$id/capabilities/media.airplay/config" '{"enabled":false}' >/dev/null ||
  fail "media.airplay config refused" server
within 30 "media.airplay not switched off" "server device-sim" airplay_off

$compose stop device-sim >/dev/null 2>&1 || fail "device not stopped" device-sim
$compose cp device-sim:$data/sim/out/mix - 2>/dev/null | tar -x -O -f - mix/mix-000.wav >"$tmp/airplay-mix.wav" 2>/dev/null ||
  fail "airplay mix wav not copied" device-sim
# set_level to pause, with 3 s for the whole seconds and the pipeline's latency
airplay_tone $((paused - at40 + 3)) ||
  fail "the mix does not show the fake shairport's tone at 70 then 40, ended by the pause" device-sim
echo "e2e: airplay block $(($(date +%s) - airplay_start)) s"

# alarms, timers, follow-ups and the stop request (M6a), on the sim and fakes alone (rule 7). the global timeZone, set
# explicitly while the device is down, reaches it as its alarms zone at its link (R6: none before). a stop transcript
# ends a pressed turn silently; a spoken reply opens a follow-up the server logs, which ends with its window when no
# say file speaks into it; a pressed turn's scripted model sets an alarm at the next whole minute 60 s or more away on
# Asia/Bishkek's clock (UTC+6), and its reply opens a follow-up the say file speaks into, where the same set is refused
# (wake_required) with nothing sent, and the chain stops at its limit of one; the alarm rings in the mix and ramps up
# from the level in force, a volume press stops it with no step, and the level in force comes back once; a timer rings
# and times out with the level untouched
m6a_start=$(date +%s)
api GET "/api/v1/devices/$id" 2>/dev/null | jqc -e 'any(.capabilities[]; .id == "alarms" and .desiredGen == 0)' >/dev/null 2>&1 ||
  fail "an alarms zone was pushed before a scope set timeZone" server
provider stt-stop fake-stt '{"text":"stop"}'
provider stt-alarm fake-stt '{"text":"Cadence E2E alarm transcript: wake me at the next minute please"}'
m6a_settings stt-stop llm-fake
follow_ups true
linked=$(links)
$compose start device-sim >/dev/null 2>&1 || fail "device not started" device-sim
within 30 "device not back online for the alarms block" "server device-sim" online_again $((linked + 1))
within 30 "the alarms zone not pushed at the link" "server device-sim" zone_applied
within 30 "media state not playing the fake soloist's track before the alarms" "server device-sim" soloist_media playing
within 15 "the level never became the fake soloist's slider move before the alarms" "server device-sim" level 25
set_level 30
within 30 "audio.output level 30 not reported before the alarms" "server device-sim" synced

# a stop transcript alone asks for silence: the turn ends stop_requested, with no reply stream and no agent turn
asked=$(lines server agent_turn "\"device_id\":\"$id\"")
stops=$(lines server turn "\"device_id\":\"$id\"" '"code":"stop_requested"')
before=$(streams) || fail "sim streams not read" device-sim
say
within 30 "the stop transcript did not end its turn stop_requested" server more_lines "$stops" server turn \
  "\"device_id\":\"$id\"" '"code":"stop_requested"' '"trigger":"TRIGGER_BUTTON"'
[ "$(lines server stop_requested "\"device_id\":\"$id\"")" -ge 1 ] || fail "no stop_requested line" server
sleep 2
[ "$(streams)" = "$before" ] || fail "the stop request began a reply stream" "server device-sim"
[ "$(lines server agent_turn "\"device_id\":\"$id\"")" = "$asked" ] || fail "the stop request reached the agent" server

# a spoken reply opens a follow-up, and with no say file its window ends it
m6a_settings stt-fake llm-fake
silences=$(lines device-sim session_end '"code":"follow_up_silence"')
say
within 40 "no follow-up ended with its window" device-sim more_lines "$silences" device-sim session_end \
  '"code":"follow_up_silence"'
silent=$(last_session TRIGGER_FOLLOW_UP)
[ "$(lines device-sim session_end "\"session_id\":\"$silent\"" '"code":"follow_up_silence"')" = 1 ] ||
  fail "the newest follow-up did not end with its window" device-sim
within 10 "the server logged no follow-up turn" server more_lines 0 server turn "\"session_id\":\"$silent\"" \
  '"trigger":"TRIGGER_FOLLOW_UP"'

# the alarm: the next whole minute 60 s or more away, on the device's clock, so it rings only after the follow-up's
# checks (about 20 s)
now=$(date +%s)
at=$(((now + 60 + 59) / 60 * 60))
here=$((at + 6 * 3600))
hhmm=$(printf '%02d:%02d' $((here / 3600 % 24)) $((here / 60 % 60)))
call="{\"name\":\"alarm\",\"arguments\":{\"action\":\"set\",\"time\":\"$hhmm\",\"level\":60}}"
provider llm-alarm fake-llm "{\"script\":[{\"toolCalls\":[$call]},{\"text\":\"Cadence E2E alarm reply.\"}],\"rules\":[$maintenance]}"
m6a_settings stt-alarm llm-alarm
follows=$(lines device-sim session_start '"trigger":"TRIGGER_FOLLOW_UP"')
say
within 30 "the woken turn's alarm not in the device's state" "server device-sim" alarm_listed "$hhmm"
[ "$(lines device-sim alarm_set)" = 1 ] || fail "the device did not set exactly one alarm" device-sim
kept=$(alarms_state) || fail "alarms state not read" server
within 20 "no follow-up after the alarm's reply" device-sim more_lines "$follows" device-sim session_start \
  '"trigger":"TRIGGER_FOLLOW_UP"'
fu=$(last_session TRIGGER_FOLLOW_UP)
# within the follow-up's window, into its microphone
say
within 30 "the follow-up's alarm set not refused wake_required" server more_lines 0 server agent_step \
  "\"session_id\":\"$fu\"" '"tool_errors":"wake_required"'
[ "$(lines server alarm_tool "\"session_id\":\"$fu\"" '"action":"set"' '"code":"wake_required"')" = 1 ] ||
  fail "the alarm tool did not refuse the follow-up's set once" server
within 30 "the server logged no turn for the follow-up" server more_lines 0 server turn "\"session_id\":\"$fu\"" \
  '"trigger":"TRIGGER_FOLLOW_UP"' '"code":"ok"'
# the chain stops at its limit of one: the server allows the follow-up's reply none, so the device opens none and
# refuses none
sleep 3
[ "$(lines device-sim ring_start '"kind":"alarm"')" = 0 ] ||
  fail "the alarm rang during the follow-up's checks: the host is too slow for this block" device-sim
[ "$(lines device-sim session_start '"trigger":"TRIGGER_FOLLOW_UP"')" = $((follows + 1)) ] ||
  fail "a follow-up opened past the limit" device-sim
[ "$(lines device-sim follow_up_refused "\"session_id\":\"$fu\"")" = 0 ] ||
  fail "the device, not the server, ended the chain at its limit" device-sim
# nothing sent: the device got one set_alarm, the woken turn's, and keeps its alarms as they were
[ "$(lines device-sim command '"capability":"alarms"' '"name":"set_alarm"')" = 1 ] ||
  fail "the refused set reached the device" device-sim
[ "$(alarms_state)" = "$kept" ] && [ "$(lines device-sim alarm_set)" = 1 ] ||
  fail "the device's alarms changed in the follow-up" "server device-sim"

# the ring: from the level in force (30), up 5 points each 5 s towards the alarm's 60; a press ends it at 40 or more
steps=$(lines device-sim volume_set)
restores=$(lines device-sim volume_reapplied)
wait=$((at - $(date +%s) + 30))
within $((wait > 1 ? wait : 1)) "the alarm did not ring within 30 s of its minute" device-sim more_lines 0 device-sim \
  ring_start '"kind":"alarm"'
echo "e2e: alarm rang $(($(date +%s) - at)) s after its minute"
within 20 "the ramp never reached 40" device-sim ramp_reached 40
# a whole 2 s loop at 40 plays before the press, for the mix check
sleep 3
press volume_up
within 10 "the press did not stop the alarm" device-sim more_lines 0 device-sim ring_stop '"kind":"alarm"' '"reason":"volume"'
sleep 2
ramped || fail "the ramp did not only rise in steps from the level in force" device-sim
[ "$(lines device-sim volume_set)" = "$steps" ] || fail "the press that stopped the ring stepped the volume" device-sim
[ "$(lines device-sim volume_reapplied)" = $((restores + 1)) ] &&
  [ "$(lines device-sim volume_reapplied '"level":30')" = $((restores + 1)) ] ||
  fail "the level in force was not restored exactly once" device-sim
level 30 || fail "the level in force moved" "server device-sim"

# the timer rings and times out after CADENCE_SIM_TIMER_SECONDS, with the level untouched: the check below covers
# those 5 s of ringing, and the alarms package's TestATimerNeverRampsAndTimesOutAfterAMinute a real board's full minute
timer='{"name":"timer","arguments":{"action":"set","seconds":5}}'
provider llm-timer fake-llm "{\"script\":[{\"toolCalls\":[$timer]},{\"text\":\"\"}],\"rules\":[$maintenance]}"
m6a_settings stt-alarm llm-timer
ramps=$(ramp_levels | wc -l | tr -d ' ')
say
within 30 "the timer did not ring" device-sim more_lines 0 device-sim ring_start '"kind":"timer"'
within $((CADENCE_SIM_TIMER_SECONDS + 15)) "the timer did not time out" device-sim more_lines 0 device-sim ring_stop \
  '"kind":"timer"' '"reason":"timeout"'
[ "$(ramp_levels | wc -l | tr -d ' ')" = "$ramps" ] && [ "$(lines device-sim volume_reapplied)" = $((restores + 1)) ] ||
  fail "the timer moved the hardware level" device-sim
level 30 || fail "the timer moved the level in force" "server device-sim"

follow_ups false
$compose stop device-sim >/dev/null 2>&1 || fail "device not stopped" device-sim
$compose cp device-sim:$data/sim/out/mix - 2>/dev/null | tar -x -O -f - mix/mix-000.wav >"$tmp/alarm-mix.wav" 2>/dev/null ||
  fail "alarm mix wav not copied" device-sim
alarm_ramp || fail "the mix does not show the alarm ringing from 30 and ramping up" device-sim
echo "e2e: alarms block $(($(date +%s) - m6a_start)) s"

# memory (M5a): the fake embed instance, a memo transcript that names a lamp, and one fake llm that answers the turns
# and, on the maintenance models, the compaction and learning prompts by their system prompts: the learning reply's
# {{lastUserId}} is the newest "[id] User:" entry the fake read. the earlier turns were compacted already (their llms
# answer the summary prompt), so each segment here holds its own turn only. the Cyrillic goes in files, never through
# a windows command line
memory_start=$(date +%s)
$compose start device-sim >/dev/null 2>&1 || fail "device not started" device-sim
within 30 "device not back online for the memory turns" "server device-sim" online_again 5
within 60 "the earlier turns not compacted" server compacted_all
runs=$(api GET "/api/v1/devices/$id/compactions" | jqc -r 'length') || fail "compactions not listed" server
within 15 "the earlier compactions not learned from" server learned "$runs" '.applied == 0 and .pending == 0'
cat >"$tmp/stt-memo.json" <<'EOF'
{"name":"stt-memo","module":"fake-stt","config":{"text":"e2e memo: turn on лампа у дивана"},"secrets":{}}
EOF
cat >"$tmp/llm-memo.json" <<'EOF'
{"name":"llm-memo","module":"fake-llm","secrets":{},"config":{"rules":[
  {"system":"running summary","text":"{\"summary\":\"e2e summary: the user switched on the sofa lamp.\",\"openThreads\":[],\"taskRefs\":[]}"},
  {"model":"fake-maintenance","system":"long-term memory","text":"{\"ops\":[{\"op\":\"add\",\"scope\":\"device\",\"kind\":\"alias\",\"text\":\"«лампа у дивана» means light.sofa_lamp\",\"evidence\":[{{lastUserId}}],\"confidence\":0.9}]}"},
  {"model":"fake-maintenance-2","system":"long-term memory","text":"{\"ops\":[{\"op\":\"add\",\"scope\":\"device\",\"kind\":\"preference\",\"text\":\"e2e preference: short answers\",\"evidence\":[{{lastUserId}}],\"confidence\":0.8}]}"}]}}
EOF
api POST /api/v1/providers '{"name":"embed-fake","module":"fake-embed","config":{},"secrets":{}}' >/dev/null ||
  fail "provider embed-fake not created" server
api POST /api/v1/providers "@$(native "$tmp/stt-memo.json")" >/dev/null || fail "provider stt-memo not created" server
api POST /api/v1/providers "@$(native "$tmp/llm-memo.json")" >/dev/null || fail "provider llm-memo not created" server
memo_settings='"stt":"stt-memo","llm":"llm-memo","tts":"tts-fake","voice":"fake","model":"fake","language":"en","embed":"embed-fake"'
api PUT /api/v1/settings/global "{$memo_settings}" >/dev/null || fail "global settings not switched to the memo" server
api PUT "/api/v1/devices/$id/settings" '{"compactAfter":1,"maintenanceModel":"fake-maintenance"}' >/dev/null ||
  fail "device memory settings not set" server

# the memo turn, then its compaction and the learning run after it: the segment is the memo turn alone, since the
# compaction before it ends at the newest message before the turn, and the turn's user message is the evidence
seen=$(message_id true)
before=$(ok_turns)
say
within 30 "no successful memo turn" server turned "$before"
said=$(date +%s)
within 30 "no compaction after the memo turn" server compacted $((runs + 1)) fake-maintenance
echo "e2e: compacted $(($(date +%s) - said)) s after the memo turn"
within 15 "no learning run that applied one memory" server learned $((runs + 1)) \
  '.applied == 1 and .pending == 0 and .dropped == 0'
memo=$(message_id "role = 'user' AND id > $seen")
[ "${memo:-0}" -gt "$seen" ] || fail "the memo turn's user message not found" server
api GET "/api/v1/devices/$id/compactions" | jqc -e --argjson seen "$seen" --argjson memo "$memo" \
  '.[1].uptoMessageId == $seen and .[0].uptoMessageId > $memo' >/dev/null 2>&1 ||
  fail "the memo compaction did not take the memo turn alone" server
api GET "/api/v1/memories?scope=device&deviceId=$id" >"$tmp/memories.json" || fail "memories not listed" server
jqc -e --arg id "$id" --argjson memo "$memo" 'length == 1 and (.[0] | .scope == "device" and .deviceId == $id and
  .kind == "alias" and .source == "learned" and .status == "active" and .embedded and
  (.text | contains("light.sofa_lamp")) and .evidence == [$memo])' <"$tmp/memories.json" \
  >/dev/null 2>&1 || fail "the alias is not one active, embedded, learned device memory with the memo turn as evidence" server

# the next turn recalls it (the live feed's memory_recall; the alias counts the hit), in approval mode on the second
# maintenance model, whose learning adds a preference
api PUT /api/v1/settings/global "{$memo_settings,\"memoryApproval\":true}" >/dev/null ||
  fail "global memoryApproval not set" server
api PUT "/api/v1/devices/$id/settings" '{"maintenanceModel":"fake-maintenance-2"}' >/dev/null ||
  fail "device maintenance model not switched" server
feed="$tmp/livefeed$(go env GOEXE)"
go build -o "$(native "$feed")" ./server/tools/livefeed || fail "the live feed helper not built" server
CADENCE_ADMIN_TOKEN=$token "$feed" -url "ws://$host:$port/api/v1/live?deviceId=$id" -type memory_recall -min-count 1 \
  -timeout 60s >"$tmp/feed.out" 2>&1 &
feed_pid=$!
# "connected" comes after the feed confirmed its subscription: the recall turn's event cannot slip past it
within 15 "the live feed helper not connected" server feed_connected
seen=$(message_id true)
before=$(ok_turns)
say
within 30 "no successful recall turn" server turned "$before"
said=$(date +%s)
wait "$feed_pid" || { feed_pid=; fail "the recall turn recalled no memory: $(cat "$tmp/feed.out")" server; }
feed_pid=
within 10 "the alias counted no recall" server alias_hit

# its compaction's learning proposes; approved through the api, the preference is active
within 30 "no compaction after the recall turn" server compacted $((runs + 2)) fake-maintenance-2
echo "e2e: compacted $(($(date +%s) - said)) s after the recall turn"
within 15 "no learning run with one pending memory" server learned $((runs + 2)) \
  '.applied == 0 and .pending == 1 and .dropped == 0'
recall=$(message_id "role = 'user' AND id > $seen")
[ "${recall:-0}" -gt "$seen" ] || fail "the recall turn's user message not found" server
api GET "/api/v1/memories?scope=device&deviceId=$id&status=pending" >"$tmp/pending.json" ||
  fail "pending memories not listed" server
jqc -e --argjson recall "$recall" 'length == 1 and (.[0] | .kind == "preference" and .source == "learned" and
  (.replaces // "") == "" and .evidence == [$recall])' <"$tmp/pending.json" >/dev/null 2>&1 ||
  fail "the pending memory is not one new learned preference with the recall turn as evidence" server
pending=$(jqc -r '.[0].id' <"$tmp/pending.json") || fail "pending memory id not read" server
# a 200 holds the memory text, so the body is shown for an error code only
http=$(status POST "/api/v1/memories/$pending/approve" '{}') || http=
[ "$http" = 200 ] || fail "approve answered ${http:-nothing}: $(cat "$tmp/body" 2>/dev/null)" server
jqc -e '.status == "active" and .kind == "preference"' <"$tmp/body" >/dev/null 2>&1 ||
  fail "the approved memory is not active" server
api GET "/api/v1/memories?scope=device&deviceId=$id&status=active" 2>/dev/null |
  jqc -e 'length == 2 and all(.[]; .source == "learned")' >/dev/null 2>&1 ||
  fail "the alias and the approved preference are not both active" server
echo "e2e: memory block $(($(date +%s) - memory_start)) s"

# tasks (M5b): the fake hermes, over https with a leaf from this stack's dev ca, which the server trusts through
# SSL_CERT_FILE, runs the tasks the scripted llms start with task_start. each update (waiting for an answer, done) runs
# an event turn, which a rule of every llm here answers with one fixed line: the notice, announced on the idle sim
# outside quiet hours. an approval is answered in the dashboard api, and an update in quiet hours (on the device's own
# clock) waits for the end of the next session a press began (M5b-R1). the hermes setting is read at startup: one
# deliberate server restart. the key is a dummy, still never printed
tasks_start=$(date +%s)
news='{"user":"A background task changed","text":"Cadence E2E task news."}'
cat >"$tmp/hermes.json" <<'EOF'
{"name":"hermes-fake","module":"hermes","config":{"baseUrl":"https://hermes:8443"},"secrets":{"apiKey":"cadence-e2e-fake-hermes-key"}}
EOF
api POST /api/v1/providers "@$(native "$tmp/hermes.json")" >/dev/null || fail "provider hermes-fake not created" server
task_llm llm-task-finish finish
task_llm llm-task-approve approve
task_llm llm-task-hold hold
printf '{"name":"llm-news","module":"fake-llm","config":{"rules":[%s,%s]},"secrets":{}}' "$maintenance" "$news" \
  >"$tmp/llm-news.json"
api POST /api/v1/providers "@$(native "$tmp/llm-news.json")" >/dev/null || fail "provider llm-news not created" server
task_settings llm-task-finish
quiet_hours 3 5
restart_server "the tasks block"
within 30 "the hermes kind never probed the fake hermes ready" "server hermes" hermes_ready
api GET /api/v1/tasks/kinds 2>/dev/null | jqc -e 'length == 1 and (.[0] | .name == "hermes" and
  .instance == "hermes-fake" and .configured == "hermes-fake")' >/dev/null 2>&1 ||
  fail "the hermes kind is not on the instance the setting names" server

# a task that finishes: its update is announced on the idle sim, and the task shows done, told, with its result
heard=$(announces) || fail "sim streams not read" device-sim
before=$(ok_turns)
say
within 30 "no successful turn that starts a task" server turned "$before"
within 10 "the turn started no task" server task_count 1
finished=$(api GET "/api/v1/tasks?device=$id&limit=1" | jqc -r '.[0].shortId') || fail "task not listed" server
within 30 "the finished task's update not announced" device-sim announced_more "$heard"
within 10 "the finished task is not done and told, with its result and one delivered notice" server task_is "$finished" \
  '.status == "done" and .reportedAt != null and .result == "Cadence E2E task result." and
  (.notices | length == 1 and .[0].deliveredAt != null and .[0].text == "Cadence E2E task news.")'
echo "e2e: finished task told $(($(date +%s) - tasks_start)) s into the tasks block"

# an approval: the question is announced, the dashboard approves the operation the question names (its token), and
# the run goes on to its end, announced too
task_settings llm-task-approve
heard=$(announces) || fail "sim streams not read" device-sim
before=$(ok_turns)
say
within 30 "no successful turn that starts the approval task" server turned "$before"
within 10 "the turn started no approval task" server task_count 2
approval=$(api GET "/api/v1/tasks?device=$id&limit=1" | jqc -r '.[0].shortId') || fail "task not listed" server
within 30 "the approval task asks no approval" server task_is "$approval" \
  '.status == "waiting_input" and .question.kind == "approval" and .question.options == ["once", "deny"] and
  (.question.token | length) > 0 and (.question.text | startswith("Cadence E2E task question"))'
within 30 "the approval question's update not announced" device-sim announced_more "$heard"
asked=$(api GET "/api/v1/tasks/$approval" | jqc -r '.question.token') || fail "question token not read" server
# a 200 holds the task's title, so the body is shown for an error code only
http=$(status POST "/api/v1/tasks/$approval/approve" "{\"decision\":\"once\",\"token\":\"$asked\"}") || http=
[ "$http" = 200 ] || fail "approve answered ${http:-nothing}: $(cat "$tmp/body" 2>/dev/null)" "server hermes"
within 30 "the approved task's update not announced" device-sim announced_more $((heard + 1))
within 10 "the approved task is not done and told after a dashboard approval" server task_is "$approval" \
  '.status == "done" and .reportedAt != null and .result == "Cadence E2E task approved result." and
  (.notices | length == 2 and all(.[]; .deliveredAt != null)) and
  any(.events[]; .kind == "answered" and .via == "dashboard" and .decision == "once")'

# quiet hours: a task that ends in them is told only after the next session a press began. the session that starts it
# is one, and keeps the user awake for CADENCE_DEV_AWAKE_SECONDS here (M5b-R8: 2 min), so the fake holds the run until
# that passed
quiet_hours -1 2
task_settings llm-task-hold
heard=$(announces) || fail "sim streams not read" device-sim
before=$(ok_turns)
say
within 30 "no successful turn that starts the held task" server turned "$before"
ended=$(date +%s)
within 10 "the turn started no held task" server task_count 3
held=$(api GET "/api/v1/tasks?device=$id&limit=1" | jqc -r '.[0].shortId') || fail "task not listed" server
task_is "$held" '.status == "running"' || fail "the held task is not running" server
# the session may end a few seconds after its turn was seen, so the wait keeps a margin past the window
while [ "$(date +%s)" -lt $((ended + CADENCE_DEV_AWAKE_SECONDS + 8)) ]; do sleep 1; done
release
within 15 "the held task not done once released" server task_is "$held" '.status == "done"'
done_at=$(date +%s)
within 15 "the held task's update not kept as a notice" server task_is "$held" \
  '.reportedAt == null and (.notices | length == 1 and .[0].deliveredAt == null)'
while [ "$(date +%s)" -lt $((done_at + 10)) ]; do sleep 1; done
now=$(announces) || fail "sim streams not read" device-sim
[ "$now" = "$heard" ] || fail "an update was announced in quiet hours" "server device-sim"
task_is "$held" '.reportedAt == null and (.notices | length == 1 and .[0].deliveredAt == null)' ||
  fail "the held task's update did not wait in quiet hours" server
task_settings llm-news
before=$(ok_turns)
say
within 30 "no successful session after quiet hours held the update" server turned "$before"
within 30 "the held update not announced after the session" device-sim announced_more "$heard"
within 10 "the held task is not told with its result" server task_is "$held" \
  '.reportedAt != null and .result == "Cadence E2E task result." and (.notices | length == 1 and .[0].deliveredAt != null)'
# the server logged the session's end before it announced the update
order=$($compose logs --no-log-prefix server 2>/dev/null | grep -n -e '"msg":"turn"' -e '"msg":"task_notice_announced"' |
  grep -e "\"device_id\":\"$id\"" | tail -n 2)
printf '%s\n' "$order" | head -n 1 | grep -q '"msg":"turn"' &&
  printf '%s\n' "$order" | tail -n 1 | grep '"msg":"task_notice_announced"' | grep -q "\"short_id\":\"$held\"" ||
  fail "the held update was not announced after the session ended" server
# the quiet hours end with the block, off rather than back to the default (23:00 to 08:00 on the device's clock, which
# is Asia/Bishkek's now): else a later block's update would wait for the next pressed session whenever the e2e runs in
# the device's night (the t3 block's held question, once busy ended)
api PUT "/api/v1/devices/$id/settings" '{"quietHours":{"enabled":false,"start":"23:00","end":"08:00"}}' >/dev/null ||
  fail "quiet hours not turned off" server
echo "e2e: tasks block $(($(date +%s) - tasks_start)) s"

# home assistant (M5c): the fake home assistant (server/tools/fakeha: hatest's websocket api with a scripted home: the
# hall light, on, and the hall's temperature sensor in the hall, the front door lock, locked) over wss with a leaf from
# this stack's dev ca. the homeAssistant setting is read at startup: one more deliberate server restart. in button
# sessions on scripted llms: the turn's home note lists the favorite with its live state; a turn turns the hall light
# off by name, which the fake answers and records, the view sees and the device's usage counts; with sensitive devices
# denied, a turn that would unlock the front door is refused with nothing sent; and with them woken again, the same turn
# in a button session unlocks it. the token is a dummy, still never printed
ha_start=$(date +%s)
cat >"$tmp/ha.json" <<'EOF'
{"name":"ha-fake","module":"homeassistant","config":{"baseUrl":"https://homeassistant:8443"},"secrets":{"token":"cadence-e2e-fake-ha-token"}}
EOF
api POST /api/v1/providers "@$(native "$tmp/ha.json")" >/dev/null || fail "provider ha-fake not created" server
# an empty answer fails a turn, so this llm's turn succeeds only once its rule found the favorite's line in a note
ha_note='{"note":"- light.hall (light, Hall) \"Hall light\": on","text":"Cadence E2E home note seen."}'
printf '{"name":"llm-ha-note","module":"fake-llm","config":{"reply":"","rules":[%s,%s]},"secrets":{}}' "$ha_note" \
  "$maintenance" >"$tmp/llm-ha-note.json"
api POST /api/v1/providers "@$(native "$tmp/llm-ha-note.json")" >/dev/null ||
  fail "provider llm-ha-note not created" server
ha_llm llm-ha-control '{"action":"turn_off","target":"hall light"}'
ha_llm llm-ha-unlock '{"action":"call_service","service":"lock.unlock","target":"front door"}'
ha_settings llm-ha-note woken
api PUT "/api/v1/devices/$id/settings" '{"haArea":"hall"}' >/dev/null || fail "device haArea not set" server
restart_server "the ha block"
within 30 "the server's client not connected to the fake home assistant with its home loaded" "server homeassistant" \
  ha_connected

# the home note: the favorite, the hall light, with its live state
before=$(ok_turns)
say
within 30 "the turn's home note did not list the favorite with its live state" server turned "$before"

# the hall light by name: the fake answers the call, the view sees the light off, the device's usage counts it
ha_settings llm-ha-control woken
before=$(ok_turns)
say
within 30 "no successful turn that turns the hall light off" server turned "$before"
ha_step '' || fail "the turn's ha_control call failed" server
within 10 "the fake home assistant answered no turn_off of the hall light" homeassistant ha_called \
  'homeassistant turn_off light.hall'
within 10 "the home's view does not show the hall light off" server ha_state light.hall off
within 10 "the hall light's use not counted for the device" server ha_used light.hall

# sensitive devices denied: unlocking the front door is refused, and the fake gets no call
ha_settings llm-ha-unlock deny
before=$(ok_turns)
say
within 30 "no successful turn that asks to unlock the front door" server turned "$before"
ha_step not_supported || fail "unlocking the front door was not refused as not_supported" server
[ "$(ha_calls | wc -l | tr -d ' ')" = 1 ] || fail "the fake home assistant got a call besides the turn_off" homeassistant
ha_state lock.front locked || fail "the front door is not locked" server

# sensitive devices need a woken turn again: the same unlock in a button session goes through
ha_settings llm-ha-unlock woken
before=$(ok_turns)
say
within 30 "no successful turn that unlocks the front door in a button session" server turned "$before"
ha_step '' || fail "unlocking the front door in a button session failed" server
within 10 "the fake home assistant answered no unlock of the front door" homeassistant ha_called 'lock unlock lock.front'
within 10 "the home's view does not show the front door unlocked" server ha_state lock.front unlocked
[ "$(ha_calls | wc -l | tr -d ' ')" = 2 ] ||
  fail "the fake home assistant got a call besides the turn_off and the unlock" homeassistant
ha_connected || fail "the server's client is no longer connected to the fake home assistant" "server homeassistant"
echo "e2e: ha block $(($(date +%s) - ha_start)) s"

# mcp (M5d): the fake mcp server (server/tools/fakemcp: the go-sdk's own server in the modern era, with echo, which
# answers its text, and note_add, which keeps nothing) over https with a leaf from this stack's dev ca, configured
# through the api alone: an mcp-http instance whose authorization secret is the fake's bearer token, named in the global
# mcp setting, which starts its server live (no restart). in button sessions on scripted llms: a tool no device lists
# is not offered (opt-in) and the fake gets no call; with both tools listed and echo marked read (still untrusted), a
# turn's echo is answered and taints the turn, so its later steps' note_add and device_volume are refused with nothing
# sent and the volume unchanged; a woken turn's note_add alone runs; echo's description changed and re-listed withdraws
# it until its hash is acknowledged. the token is a dummy, still never printed
mcp_start=$(date +%s)
echo_call='{"name":"mcp_e2e_echo","arguments":{"text":"Cadence E2E mcp echo argument"}}'
note_call='{"name":"mcp_e2e_note_add","arguments":{"note":"Cadence E2E mcp note argument"}}'
# asked for only to be refused (rule 7), and lower than any level the earlier blocks leave
volume_call='{"name":"device_volume","arguments":{"level":10}}'
mcp_done='{"text":"Cadence E2E mcp done."}'
mcp_llm llm-mcp-echo "[{\"toolCalls\":[$echo_call]},$mcp_done]"
mcp_llm llm-mcp-taint "[{\"toolCalls\":[$echo_call]},{\"toolCalls\":[$note_call]},{\"toolCalls\":[$volume_call]},$mcp_done]"
mcp_llm llm-mcp-note "[{\"toolCalls\":[$note_call]},$mcp_done]"
cat >"$tmp/mcp.json" <<'EOF'
{"name":"mcp-fake","module":"mcp-http","config":{"url":"https://mcp:8443/cadence-e2e-mcp-path","toolPrefix":"e2e"},"secrets":{"authorization":"Bearer cadence-e2e-fake-mcp-token"}}
EOF
api POST /api/v1/providers "@$(native "$tmp/mcp.json")" >/dev/null || fail "provider mcp-fake not created" server
mcp_settings llm-mcp-echo
within 30 "the server not connected to the fake mcp server with both its tools" "server mcp" mcp_connected
api GET /api/v1/mcp/servers >"$tmp/mcp-servers.json" || fail "mcp servers not listed" server
echo "e2e: mcp server connected $(($(date +%s) - mcp_start)) s into the mcp block"

# opt-in: with no tools setting on the device and no enabledDefault, echo is not offered, and the fake gets no call
before=$(ok_turns)
say
within 30 "no successful turn that asks for the unlisted echo" server turned "$before"
within 10 "the unlisted echo was not refused as not offered" server stepped "$(printf 'mcp_e2e_echo|unknown_tool\n|')"
mcp_called '' || fail "the fake mcp server got a call of a tool no device lists" mcp

# listed, and echo marked read (trust stays untrusted): echo is answered, and its untrusted result leaves the rest of the
# turn Read tools only: note_add in the next step and device_volume in the one after (each its own step, as a step logs
# each error code once) are refused, nothing is sent, the level stays
api PUT "/api/v1/devices/$id/settings" '{"tools":["device_volume","mcp_e2e_echo","mcp_e2e_note_add"]}' >/dev/null ||
  fail "device tools not set" server
# a 200 holds the tool's description, so the body is shown for an error code only
http=$(status PATCH /api/v1/mcp/servers/mcp-fake/tools/echo '{"effects":"read"}') || http=
[ "$http" = 200 ] || fail "echo's patch answered ${http:-nothing}: $(cat "$tmp/body" 2>/dev/null)" server
jqc -e '.effects == "read" and .trust == "untrusted" and (.changed | not)' <"$tmp/body" >/dev/null 2>&1 ||
  fail "echo is not marked read and untrusted" server
was=$(api GET "/api/v1/devices/$id" | jqc -r '.capabilities[] | select(.id == "audio.output") | .state.level') || was=
case $was in
  '' | *[!0-9]*) fail "the device's level not read" server ;;
esac
mcp_settings llm-mcp-taint
before=$(ok_turns)
say
within 30 "no successful turn that calls echo" server turned "$before"
within 10 "the fake mcp server answered no echo" mcp mcp_called echo
within 10 "the tainted turn's note_add and device_volume were not refused as not offered" server stepped \
  "$(printf 'mcp_e2e_echo|\nmcp_e2e_note_add|unknown_tool\ndevice_volume|unknown_tool\n|')"
sleep 2
mcp_called echo || fail "the fake mcp server got a call besides echo after the taint" mcp
level "$was" || fail "the level moved after the taint" "server device-sim"

# a woken turn (a button session) runs note_add alone: a Write tool with no untrusted result before it in the turn
mcp_settings llm-mcp-note
before=$(ok_turns)
say
within 30 "no successful turn that calls note_add" server turned "$before"
within 10 "the fake mcp server answered no note_add" mcp mcp_called "$(printf 'echo\nnote_add')"
stepped "$(printf 'mcp_e2e_note_add|\n|')" || fail "the woken turn's note_add failed" server

# a rug pull: echo's description changes, and the refresh's listing withdraws it (changed, its overrides kept) until the
# owner acknowledges its current hash: meanwhile no turn is offered it and the fake gets no call
: >"$tmp/mutate"
tar -C "$tmp" -cf - mutate | $compose cp - mcp:/tmp >/dev/null 2>&1 || fail "mutate file not written" mcp
http=$(status POST /api/v1/mcp/servers/mcp-fake/refresh '') || http=
[ "$http" = 202 ] || fail "refresh answered ${http:-nothing}: $(cat "$tmp/body" 2>/dev/null)" server
within 15 "echo not withdrawn after its description changed" "server mcp" mcp_tool echo '.changed and
  .skip == "changed" and .effects == "read" and (.ackedHash | length) == 64 and .hash != .ackedHash and
  (.description | startswith("[MCP e2e] Cadence E2E echo description two"))'
api GET /api/v1/mcp/servers/mcp-fake/tools >"$tmp/mcp-tools-changed.json" || fail "mcp tools not listed" server
mcp_settings llm-mcp-echo
before=$(ok_turns)
say
within 30 "no successful turn that asks for the changed echo" server turned "$before"
within 10 "the changed echo was not refused as not offered" server stepped "$(printf '|unknown_tool\n|')"
mcp_called "$(printf 'echo\nnote_add')" || fail "the fake mcp server answered the changed echo" mcp
hash=$(api GET /api/v1/mcp/servers/mcp-fake/tools | jqc -r '.[] | select(.remote == "echo") | .hash') || hash=
[ "${#hash}" = 64 ] || fail "echo's hash not read" server
http=$(status PATCH /api/v1/mcp/servers/mcp-fake/tools/echo "{\"acknowledge\":\"$hash\"}") || http=
[ "$http" = 200 ] || fail "echo's acknowledgement answered ${http:-nothing}: $(cat "$tmp/body" 2>/dev/null)" server
jqc -e '(.changed | not) and (.skip // "") == "" and .effects == "read"' <"$tmp/body" >/dev/null 2>&1 ||
  fail "the acknowledged echo is not offered again with its overrides" server
before=$(ok_turns)
say
within 30 "no successful turn that calls the acknowledged echo" server turned "$before"
within 10 "the fake mcp server answered no acknowledged echo" mcp mcp_called "$(printf 'echo\nnote_add\necho')"
stepped "$(printf 'mcp_e2e_echo|\n|')" || fail "the acknowledged echo's call failed" server
api GET /api/v1/mcp/servers/mcp-fake/tools >"$tmp/mcp-tools.json" || fail "mcp tools not listed" server
mcp_connected || fail "the server is no longer connected to the fake mcp server" "server mcp"

# the admin bodies carry the host, tool names and (the tools route) the cleaned descriptions: never the token, the url's
# path, an argument or a result, and the servers route no description either
for f in mcp-servers mcp-tools-changed mcp-tools; do
  private_body "$tmp/$f.json" cadence-e2e-fake-mcp-token cadence-e2e-mcp-path 'mcp echo argument' \
    'mcp note argument' 'note kept' || fail "the $f body carries the token, the url's path, an argument or a result" server
done
private_body "$tmp/mcp-servers.json" 'echo description' || fail "the mcp servers body carries a description" server
# the providers api shows the instance's config (its url) by design, and of its secret only that it is set
api GET /api/v1/providers >"$tmp/providers.json" || fail "providers not listed" server
private_body "$tmp/providers.json" cadence-e2e-fake-mcp-token || fail "the providers body carries the mcp token" server
jqc -e 'any(.[]; .name == "mcp-fake" and .secrets == {authorization: {set: true}})' <"$tmp/providers.json" >/dev/null \
  2>&1 || fail "the providers body does not show the mcp secret as set" server
echo "e2e: mcp block $(($(date +%s) - mcp_start)) s"

# t3 code (subscriptions, busy mode): the fake t3 code server (server/tools/faket3: one project and a thread per
# script, each scripted by a message's first word) over https with a leaf from this stack's dev ca, configured through
# the api alone: a t3code instance whose token is the fake's dummy bearer, named in the global t3code setting, which
# starts following it live (no restart), and a speaker subscription of the sim to every thread of it. in button sessions
# on scripted llms: a turn's t3_send starts the ask thread while a busy signal holds its question, which goes once busy
# ends: spoken on the sim and in the history as a notice turn. the next woken turn reads the unread block, which taints
# nothing (its t3_send to the hold thread runs), inspects the question, which taints (its t3_stop is refused), and
# answers it, which the fake applies; a turn's t3_stop stops the hold thread, announced; a thread launched after the
# subscription asks and is announced too, and the dashboard api answers it. the token is a dummy, still never printed
t3_start=$(date +%s)
# t3_calls: the commands the fake applied, one `type thread_id` a line
t3_calls() { $compose cp t3:/tmp/calls.log - 2>/dev/null | tar -x -O -f - calls.log 2>/dev/null; }
# t3_called CALL: the fake applied CALL
t3_called() { t3_calls | grep -qxF "$1"; }
# t3_connected: the status shows the fake's instance followed, connected and synced, with its seeded threads
t3_connected() {
  api GET /api/v1/t3code/status 2>/dev/null | jqc -e '.instances | length == 1 and (.[0] | .instance == "t3-fake" and
    .running and .connected and .synced and .threads >= 5 and (.disabled // "") == "")' >/dev/null 2>&1
}
# t3_settings LLM: the global settings of the t3 block, on LLM with the fake's instance followed, over the ones before
t3_settings() {
  jqc --arg llm "$1" '. + {llm: $llm, t3code: ["t3-fake"]}' <"$tmp/global-t3-before.json" >"$tmp/global-t3.json" ||
    fail "global settings not merged" server
  api PUT /api/v1/settings/global "@$(native "$tmp/global-t3.json")" >/dev/null ||
    fail "global settings not switched to $1" server
}
# t3_llm NAME STEPS: a scripted llm whose turn runs STEPS; besides the maintenance rules, one that answers any request
# whose notes hold the fake's question text, so a block that carried it changes the turn's steps
t3_llm() {
  printf '{"name":"%s","module":"fake-llm","config":{"script":%s,"rules":[%s,%s]},"secrets":{}}' "$1" "$2" "$maintenance" \
    '{"note":"Which color should the button be","text":"Cadence E2E t3 leak."}' >"$tmp/$1.json"
  api POST /api/v1/providers "@$(native "$tmp/$1.json")" >/dev/null || fail "provider $1 not created" server
}
# t3_item FILTER: one of the device's inbox items matches FILTER; the body holds the outside texts, so it is never shown
t3_item() {
  api GET "/api/v1/subscriptions/inbox?device=$id" 2>/dev/null | jqc -e "any(.items[]; $1)" >/dev/null 2>&1
}
# t3_gone FILTER: none of the device's inbox items (pending, unread or open) matches FILTER
t3_gone() {
  api GET "/api/v1/subscriptions/inbox?device=$id" 2>/dev/null | jqc -e "all(.items[]; ($1) | not)" >/dev/null 2>&1
}
# t3_token REF: the open question's token of the thread REF, from the inbox
t3_token() {
  api GET "/api/v1/subscriptions/inbox?device=$id" |
    jqc -r --arg ref "$1" '[.items[] | select(.refId == $ref and .open and .question != null)] | last | .question.token'
}
# notice_turn: the device's newest turn is a notice (a spoken update, no user message), shown as booleans alone
notice_turn() {
  api GET "/api/v1/devices/$id/turns?limit=1" 2>/dev/null |
    jqc -e '.turns | length == 1 and (.[0] | .trigger == "notice" and (.user // "") == "")' >/dev/null 2>&1
}

api GET /api/v1/settings/global >"$tmp/global-t3-before.json" || fail "global settings not read" server
# every tool, as the mcp block listed three alone; put back at the end
api PUT "/api/v1/devices/$id/settings" '{"tools":null}' >/dev/null || fail "device tools not reset" server
done_step='{"text":"Cadence E2E t3 done."}'
t3_llm llm-t3-ask "[{\"toolCalls\":[{\"name\":\"t3_send\",\"arguments\":{\"thread\":\"ask\",\"text\":\"ask Cadence E2E t3 question\"}}]},$done_step]"
t3_llm llm-t3-stop "[{\"toolCalls\":[{\"name\":\"t3_stop\",\"arguments\":{\"thread\":\"hold\"}}]},$done_step]"
t3_llm llm-t3-launch "[{\"toolCalls\":[{\"name\":\"t3_launch\",\"arguments\":{\"project\":\"Cadence\",\"text\":\"ask Cadence E2E t3 launched\",\"title\":\"Cadence E2E t3 launched\"}}]},$done_step]"
cat >"$tmp/t3.json" <<'EOF'
{"name":"t3-fake","module":"t3code","config":{"baseUrl":"https://t3:8443"},"secrets":{"token":"cadence-e2e-fake-t3-token"}}
EOF
api POST /api/v1/providers "@$(native "$tmp/t3.json")" >/dev/null || fail "provider t3-fake not created" server
t3_settings llm-t3-ask
within 30 "the server not following the fake t3 code server" "server t3" t3_connected
sub=$(api POST /api/v1/subscriptions "{\"topic\":\"t3code:t3-fake:*\",\"device\":\"$id\",\"delivery\":\"speaker\"}") ||
  fail "the sim's t3 subscription not created" server
sub_id=$(printf '%s' "$sub" | jqc -r '.id') || sub_id=
printf '%s' "$sub" | jqc -e '.topic == "t3code:t3-fake:*" and .events == ["stopped", "question", "approval", "error"] and
  .until == "unsubscribed"' >/dev/null 2>&1 || fail "the t3 subscription is not the instance's every event" server
echo "e2e: t3 code followed $(($(date +%s) - t3_start)) s into the t3 block"

# busy: a call holds the question the ask thread opens, never drops it; the end of the call lets it go, spoken, and
# written into the history as a notice
http=$(status PUT /api/v1/busy/e2e '{"reason":"call","ttlSeconds":120}') || http=
[ "$http" = 200 ] || fail "busy answered ${http:-nothing}: $(cat "$tmp/body" 2>/dev/null)" server
heard=$(announces) || fail "sim streams not read" device-sim
before=$(ok_turns)
say
within 30 "no successful turn that sends to the ask thread" server turned "$before"
within 10 "the ask turn's t3_send failed" server stepped "$(printf 't3_send|\n|')"
within 10 "the fake applied no message to the ask thread" t3 t3_called "message.dispatch thread-ask"
within 30 "the ask thread's question not held while busy" "server t3" t3_item \
  '.refId == "thread-ask" and .event == "question" and .heldBusy and .state == "pending" and .question.kind == "clarify"'
# past the hub's window and settle: a busy device says nothing
sleep 8
[ "$(announces)" = "$heard" ] || fail "an update was spoken while busy" "server device-sim"
http=$(status DELETE /api/v1/busy/e2e '') || http=
[ "$http" = 204 ] || fail "busy's end answered ${http:-nothing}: $(cat "$tmp/body" 2>/dev/null)" server
within 30 "the held question not announced once busy ended" "server device-sim" announced_more "$heard"
within 10 "the announced question is not delivered" server t3_item \
  '.refId == "thread-ask" and .event == "question" and .state == "delivered" and .open'
within 10 "the spoken question is not the newest turn as a notice" server notice_turn
token_ask=$(t3_token thread-ask) || token_ask=
case $token_ask in
  '' | null | *[!A-Za-z0-9_-]*) fail "the ask question's token not read" server ;;
esac
printf '%s\n' "$token_ask" >"$tmp/t3-tokens"

# the next woken turn reads the unread block, which carries no outside text and taints nothing: its t3_send to the
# hold thread runs (a write tool); its inspect of the question taints the turn, so the t3_stop after it is refused, while
# the taint-exempt subscription_reply answers option 2, which the fake applies
inspect_call="{\"name\":\"subscription_inspect\",\"arguments\":{\"token\":\"$token_ask\"}}"
reply_call="{\"name\":\"subscription_reply\",\"arguments\":{\"token\":\"$token_ask\",\"answers\":[\"2\"]}}"
t3_llm llm-t3-reply "[{\"toolCalls\":[{\"name\":\"t3_send\",\"arguments\":{\"thread\":\"hold\",\"text\":\"hold Cadence E2E t3 hold\"}}]},{\"toolCalls\":[$inspect_call]},{\"toolCalls\":[$reply_call]},{\"toolCalls\":[{\"name\":\"t3_stop\",\"arguments\":{\"thread\":\"hold\"}}]},$done_step]"
t3_settings llm-t3-reply
heard=$(announces) || fail "sim streams not read" device-sim
before=$(ok_turns)
say
within 30 "no successful turn that answers the question" server turned "$before"
within 10 "the reply turn's steps differ (the block tainted it, carried the question, or the reply failed)" server stepped \
  "$(printf 't3_send|\nsubscription_inspect|\nsubscription_reply|\nt3_stop|unknown_tool\n|')"
t3_called "message.dispatch thread-hold" || fail "the fake applied no message to the hold thread" t3
within 10 "the fake applied no answer to the ask thread" t3 t3_called "runtime-request.respond thread-ask"
t3_called "thread.stop thread-hold" && fail "the tainted turn's t3_stop reached the fake" t3
within 10 "the answered question is still in the inbox" server t3_gone '.refId == "thread-ask" and .event == "question"'
within 30 "the ask thread's stop not announced" "server device-sim" announced_more "$heard"

# a turn stops the hold thread: the fake applies thread.stop and the stop is announced
t3_settings llm-t3-stop
heard=$(announces) || fail "sim streams not read" device-sim
before=$(ok_turns)
say
within 30 "no successful turn that stops the hold thread" server turned "$before"
within 10 "the stop turn's t3_stop failed" server stepped "$(printf 't3_stop|\n|')"
within 10 "the fake applied no stop" t3 t3_called "thread.stop thread-hold"
within 30 "the hold thread's stop not delivered" server t3_item '.refId == "thread-hold" and .event == "stopped" and .state == "delivered"'
within 30 "the hold thread's stop not announced" "server device-sim" announced_more "$heard"

# a thread launched after the subscription is followed too: its question is announced, and the dashboard answers it
t3_settings llm-t3-launch
heard=$(announces) || fail "sim streams not read" device-sim
before=$(ok_turns)
say
within 30 "no successful turn that launches a thread" server turned "$before"
within 10 "the launch turn's t3_launch failed" server stepped "$(printf 't3_launch|\n|')"
launched=$(t3_calls | awk '$1 == "launchThread" { print $2 }' | tail -n 1)
[ -n "$launched" ] || fail "the fake launched no thread" t3
within 30 "the launched thread's question not delivered" server t3_item \
  ".refId == \"$launched\" and .event == \"question\" and .state == \"delivered\" and .open"
within 30 "the launched thread's question not announced" "server device-sim" announced_more "$heard"
token_new=$(t3_token "$launched") || token_new=
case $token_new in
  '' | null | *[!A-Za-z0-9_-]*) fail "the launched question's token not read" server ;;
esac
printf '%s\n' "$token_new" >>"$tmp/t3-tokens"
# a 200 holds the question, so the body is shown for an error code only
http=$(status POST /api/v1/subscriptions/reply "{\"device\":\"$id\",\"token\":\"$token_new\",\"message\":\"Cadence E2E t3 dashboard answer\"}") || http=
[ "$http" = 200 ] || fail "the dashboard's reply answered ${http:-nothing}: $(cat "$tmp/body" 2>/dev/null)" "server t3"
within 10 "the fake applied no dashboard answer" t3 t3_called "runtime-request.respond $launched"

# the status carries codes and counts: never a title, a question or the token; then the module goes off live
api GET /api/v1/t3code/status >"$tmp/t3-status.json" || fail "t3 code status not read" server
private_body "$tmp/t3-status.json" 'Cadence E2E' 'Which color' 'cadence-e2e-fake-t3-token' ||
  fail "the t3 code status carries a title, a question or the token" server
api GET /api/v1/providers >"$tmp/providers-t3.json" || fail "providers not listed" server
private_body "$tmp/providers-t3.json" cadence-e2e-fake-t3-token || fail "the providers body carries the t3 token" server
http=$(status DELETE "/api/v1/subscriptions/$sub_id" '') || http=
[ "$http" = 204 ] || fail "the t3 subscription's delete answered ${http:-nothing}" server
api PUT /api/v1/settings/global "@$(native "$tmp/global-t3-before.json")" >/dev/null || fail "global settings not restored" server
api PUT "/api/v1/devices/$id/settings" '{"tools":["device_volume","mcp_e2e_echo","mcp_e2e_note_add"]}' >/dev/null ||
  fail "device tools not restored" server
within 15 "t3 code still followed with the setting off" server more_lines 0 server t3code_follow_stopped '"instance":"t3-fake"'
echo "e2e: t3 block $(($(date +%s) - t3_start)) s"

# owner login, scoped tokens and the live feed's topics (M7a): the owner's password is set inside the server container
# through its cli from stdin (never an argument), the dev stack is plain http (CADENCE_COOKIE_INSECURE, so the cookies are
# cadence_session and cadence_browser) at the origin CADENCE_PUBLIC_ORIGIN names. what carries a credential here (the
# cookie jar, the bearer header files, the feed helper's environment, the password's body file) is read from a file or
# the environment, never from an argument. in order: a wrong password, a login without the csrf header or from another
# origin, the login, the cookie's reads and the csrf refusals of its writes, the feed's origin rule, a subscription to
# media alone that gets the media event and no event of another type during a turn, the openapi document, an ha token's
# reach, its refusals and its revocation, and the logout; the secrets are checked against the server's log in the
# privacy block. rule 7: no volume, alarm, timer or pairing
m7a_start=$(date +%s)
m7a_json='Content-Type: application/json'
m7a_csrf='X-Cadence-CSRF: 1'

# raw METHOD PATH CURL_ARGS...: the http status of a call; the body lands in $tmp/body
raw() {
  _method=$1 _path=$2
  shift 2
  curl -sS -m 15 -o "$(native "$tmp/body")" -w '%{http_code}' -X "$_method" "$@" "$base$_path"
}

# error_is CODE: the last body is {"error":CODE}
error_is() { jqc -e --arg code "$1" '.error == $code' <"$tmp/body" >/dev/null 2>&1; }

# jar_value NAME: a cookie's value from the jar
jar_value() { awk -F'\t' -v n="$1" '$6 == n { print $7 }' "$tmp/jar" | tr -d '\r'; }

# header_file NAME LINE: a header file for curl's -H @file
header_file() { printf '%s\n' "$2" >"$tmp/$1"; }

# m7a_connected FILE: a feed helper's output says it is connected
m7a_connected() { grep -q '^connected' "$1" 2>/dev/null; }

owner_pw=$(head -c 18 /dev/urandom | base64 | tr -d '=+/\n')
printf '%s\n' "$owner_pw" | $compose exec -T server /cadence-server admin password set >/dev/null 2>&1 ||
  fail "the owner password not set" server
printf '%s' "$owner_pw" | jqc -Rs '{password: .}' >"$tmp/login.json"
printf '{"password":"wrong-password-e2e"}' >"$tmp/login-wrong.json"

# a wrong password is refused and sets no cookie; a login needs the csrf header and its own origin
http=$(raw POST /api/v1/session -H "$m7a_json" -H "$m7a_csrf" -H "Origin: $base" -c "$(native "$tmp/jar-wrong")" \
  --data-binary "@$(native "$tmp/login-wrong.json")") || http=
{ [ "$http" = 401 ] && error_is unauthorized; } || fail "a wrong password answered ${http:-nothing}" server
! grep -q 'cadence_' "$tmp/jar-wrong" 2>/dev/null || fail "a wrong password set a cookie" server
http=$(raw POST /api/v1/session -H "$m7a_json" -H "Origin: $base" --data-binary "@$(native "$tmp/login.json")") || http=
{ [ "$http" = 403 ] && error_is csrf; } || fail "a login without the csrf header answered ${http:-nothing}" server
http=$(raw POST /api/v1/session -H "$m7a_json" -H "$m7a_csrf" -H 'Origin: http://evil.example' \
  --data-binary "@$(native "$tmp/login.json")") || http=
{ [ "$http" = 403 ] && error_is csrf; } || fail "a login from another origin answered ${http:-nothing}" server

# the login: 204 with both cookies
http=$(raw POST /api/v1/session -H "$m7a_json" -H "$m7a_csrf" -H "Origin: $base" -c "$(native "$tmp/jar")" \
  --data-binary "@$(native "$tmp/login.json")") || http=
[ "$http" = 204 ] || fail "the login answered ${http:-nothing}" server
session=$(jar_value cadence_session)
browser=$(jar_value cadence_browser)
{ [ "${#session}" -ge 40 ] && [ "${#browser}" -ge 40 ]; } ||
  fail "the login did not set the session and the known-browser cookies" server
: >"$tmp/secrets"
printf '%s\n' "$owner_pw" "$session" "$browser" >>"$tmp/secrets"

# the cookie reads; its writes need the csrf header and the origin
http=$(raw GET /api/v1/devices -b "$(native "$tmp/jar")") || http=
[ "$http" = 200 ] || fail "an admin read with the cookie answered ${http:-nothing}" server
http=$(raw GET /api/v1/session -b "$(native "$tmp/jar")") || http=
{ [ "$http" = 200 ] && jqc -e '.authenticated' <"$tmp/body" >/dev/null 2>&1; } ||
  fail "the session state is not authenticated" server
http=$(raw POST /api/v1/tokens -b "$(native "$tmp/jar")" -H "$m7a_json" -H "Origin: $base" --data-binary '{}') || http=
{ [ "$http" = 403 ] && error_is csrf; } || fail "a cookie write without the csrf header answered ${http:-nothing}" server
http=$(raw POST /api/v1/tokens -b "$(native "$tmp/jar")" -H "$m7a_json" -H "$m7a_csrf" -H 'Origin: http://evil.example' \
  --data-binary '{}') || http=
{ [ "$http" = 403 ] && error_is csrf; } || fail "a cookie write from another origin answered ${http:-nothing}" server

# the feed: a cookie upgrade from another origin is refused, and the same origin upgrades; a subscription to media alone
# gets the media event and none of a turn's, while a bearer feed without a subscription gets the turn's
feed_url="ws://$host:$port/api/v1/live?deviceId=$id"
CADENCE_FEED_COOKIE="cadence_session=$session" "$feed" -url "$feed_url" -origin http://evil.example -expect-status 403 \
  -timeout 10s >"$tmp/feed-refused.out" 2>&1 ||
  fail "the cookie upgrade from another origin was not refused: $(cat "$tmp/feed-refused.out")" server
CADENCE_FEED_COOKIE="cadence_session=$session" "$feed" -url "$feed_url" -origin "$base" -subscribe media \
  -type media_state -only media_state -linger 3s -timeout 60s >"$tmp/feed-cookie.out" 2>&1 &
feed_pid=$!
CADENCE_ADMIN_TOKEN=$token "$feed" -url "$feed_url" -type turn_start -trigger button -timeout 60s >"$tmp/feed-bearer.out" 2>&1 &
feed2_pid=$!
within 15 "the cookie feed not connected" server m7a_connected "$tmp/feed-cookie.out"
within 15 "the bearer feed not connected" server m7a_connected "$tmp/feed-bearer.out"
api GET /api/v1/settings/global >"$tmp/global-m7a.json" || fail "global settings not read" server
jqc '. + {llm: "llm-fake"}' <"$tmp/global-m7a.json" >"$tmp/global-plain.json" || fail "global settings not merged" server
api PUT /api/v1/settings/global "@$(native "$tmp/global-plain.json")" >/dev/null ||
  fail "global settings not switched to llm-fake" server
before=$(ok_turns)
say
within 30 "no successful turn for the feed's subscriptions" server turned "$before"
wait "$feed2_pid" || { feed2_pid=; fail "the bearer feed saw no turn_start of a button press: $(cat "$tmp/feed-bearer.out")" server; }
feed2_pid=
http=$(status POST "/api/v1/devices/$id/capabilities/media.url/commands/play" \
  '{"url":"http://media/music.flac","metadata":{"title":"Cadence E2E Song","artist":"Cadence E2E Artist"}}') || http=
[ "$http" = 200 ] || fail "url play answered ${http:-nothing}: $(cat "$tmp/body" 2>/dev/null)" "server device-sim media"
wait "$feed_pid" || { feed_pid=; fail "the media feed saw no media_state, or a turn's event: $(cat "$tmp/feed-cookie.out")" server; }
feed_pid=
within 20 "media state not idle after the url" "server device-sim" media_idle
# the global settings go back to the mcp block's, its llm too, so a block added later does not run on llm-fake
api PUT /api/v1/settings/global "@$(native "$tmp/global-m7a.json")" >/dev/null || fail "global settings not restored" server

# the openapi document equals the committed one but for the version string
api GET /api/v1/openapi.json >"$tmp/openapi-served.json" || fail "the openapi document not served" server
jqc -S '.info.version = "x"' <"$tmp/openapi-served.json" >"$tmp/openapi-a.json" || fail "the served openapi document is no json" server
jqc -S '.info.version = "x"' <server/openapi/openapi.json >"$tmp/openapi-b.json" || fail "the committed openapi document is no json" server
cmp -s "$tmp/openapi-a.json" "$tmp/openapi-b.json" || fail "the served openapi document differs from server/openapi/openapi.json" server

# an ha token: shown once, it reads devices and media and announces; the rest is forbidden, and so is the agent topic
api POST /api/v1/tokens '{"name":"e2e-ha","scopes":["ha"]}' >"$tmp/ha-created.json" || fail "the ha token not created" server
ha_token=$(jqc -r '.token' <"$tmp/ha-created.json") || ha_token=
case $ha_token in
  cdn_*) ;;
  *) fail "the ha token is malformed" server ;;
esac
printf '%s\n' "$ha_token" >>"$tmp/secrets"
header_file ha-header "Authorization: Bearer $ha_token"
ha_h="@$(native "$tmp/ha-header")"
for p in /api/v1/devices "/api/v1/devices/$id" "/api/v1/devices/$id/media"; do
  http=$(raw GET "$p" -H "$ha_h") || http=
  [ "$http" = 200 ] || fail "the ha token's read of ${p##*/} answered ${http:-nothing}" server
done
http=$(raw POST "/api/v1/devices/$id/announce" -H "$ha_h" -H "$m7a_json" --data-binary '{"text":"Cadence E2E ha announce"}') || http=
[ "$http" = 202 ] || fail "the ha token's announce answered ${http:-nothing}" "server device-sim"
for p in /api/v1/providers /api/v1/settings/global /api/v1/tasks /api/v1/tokens; do
  http=$(raw GET "$p" -H "$ha_h") || http=
  { [ "$http" = 403 ] && error_is forbidden; } || fail "the ha token's read of $p answered ${http:-nothing}" server
done
http=$(raw POST /api/v1/tokens -H "$ha_h" -H "$m7a_json" --data-binary '{"name":"e2e-ha2","scopes":["admin"]}') || http=
{ [ "$http" = 403 ] && error_is forbidden; } || fail "the ha token's token create answered ${http:-nothing}" server
CADENCE_ADMIN_TOKEN=$ha_token "$feed" -url "$feed_url" -subscribe agent -expect-error forbidden -timeout 10s \
  >"$tmp/feed-ha.out" 2>&1 || fail "the ha token's agent subscription was not forbidden: $(cat "$tmp/feed-ha.out")" server
api DELETE /api/v1/tokens/e2e-ha >/dev/null || fail "the ha token not deleted" server
http=$(raw GET /api/v1/devices -H "$ha_h") || http=
[ "$http" = 401 ] || fail "the deleted ha token answered ${http:-nothing}" server

# the logout ends the session; a login again replaces the known-browser token
http=$(raw DELETE /api/v1/session -b "$(native "$tmp/jar")" -c "$(native "$tmp/jar")" -H "$m7a_csrf" -H "Origin: $base") || http=
[ "$http" = 204 ] || fail "the logout answered ${http:-nothing}" server
header_file old-cookie "Cookie: cadence_session=$session"
http=$(raw GET /api/v1/devices -H "@$(native "$tmp/old-cookie")") || http=
[ "$http" = 401 ] || fail "the logged-out cookie answered ${http:-nothing}" server
http=$(raw POST /api/v1/session -H "$m7a_json" -H "$m7a_csrf" -H "Origin: $base" -b "$(native "$tmp/jar")" \
  -c "$(native "$tmp/jar")" --data-binary "@$(native "$tmp/login.json")") || http=
[ "$http" = 204 ] || fail "the second login answered ${http:-nothing}" server
new_browser=$(jar_value cadence_browser)
{ [ "${#new_browser}" -ge 40 ] && [ "$new_browser" != "$browser" ]; } ||
  fail "the second login did not replace the known-browser token" server
printf '%s\n' "$(jar_value cadence_session)" "$(jar_value cadence_browser)" >>"$tmp/secrets"
echo "e2e: m7a block $(($(date +%s) - m7a_start)) s"

# telemetry (M6b), on the sim alone: its system.metrics config sets a round every 10 s; cadenced's own lines (the
# config's config_applied), audiod's and the fake soloist's reach the store stamped with the device's id, name and board;
# a probe line holding the device token and the pushed soloist key is stored with both redacted and seen on the live
# feed; the memory and session series, the top processes (no command line) and the prometheus exposition with the
# device's labels arrive; and the lines the probe makes while the server is stopped drain once it is back. the token is
# read from the sim's config into this shell and a file of its own, never printed
m6b_start=$(date +%s)
api PUT "/api/v1/devices/$id/capabilities/system.metrics/config" '{"intervalS":10}' >/dev/null ||
  fail "system.metrics config refused" server
within 30 "the system.metrics config not applied" "server device-sim" metrics_synced
within 30 "no stored cadenced line of the config applied, stamped with the device" "server device-sim" stamped cadenced \
  '.body | contains("config_applied") and contains("system.metrics")' q=system.metrics
within 30 "no stored audiod line stamped with the device" "server device-sim" stamped audiod true
within 30 "no stored line of the fake soloist stamped with the device" "server device-sim" stamped soloist \
  '.body | endswith(": running")'

dtoken=$($compose cp device-sim:$data/config.json - 2>/dev/null | tar -x -O -f - config.json 2>/dev/null |
  jqc -r .deviceToken) || dtoken=
case $dtoken in
  cdd_*) ;;
  *) fail "the device token not read from the sim's config" device-sim ;;
esac
(umask 077 && printf '%s\ncadence-e2e-fake-key\n' "$dtoken" >"$tmp/device-secrets") || fail "secrets file not written" server
CADENCE_ADMIN_TOKEN=$token "$feed" -url "ws://$host:$port/api/v1/live?deviceId=$id" -subscribe logs -type log_line \
  -source sim-probe -timeout 60s >"$tmp/feed.out" 2>&1 &
feed_pid=$!
within 15 "the live feed helper not connected" server feed_connected
logprobe "cadence-e2e-logprobe secret: token $dtoken key cadence-e2e-fake-key"
within 10 "the sim did not take its probe file" device-sim probe_taken
wait "$feed_pid" || { feed_pid=; fail "no log_line of the probe on the live feed: $(cat "$tmp/feed.out")" server; }
feed_pid=
redacted='cadence-e2e-logprobe secret: token «redacted:device_token» key «redacted:media.soloist.apiKey»'
within 30 "the probe line not stored with the token and the key redacted" "server device-sim" probe_stored "$redacted"
[ "$(secret_leaks "$tmp/logs-sim-probe.json")" = 0 ] || fail "the logs api answers the token or the key" server

within 45 "no system.memory.used points 10 s apart and no cadence.sessions points" "server device-sim" metrics_arrive
within 30 "no top processes of pid, comm, cpu and rss alone" "server device-sim" processes_listed
# prometheus scrapes with a token of scope metrics, which reaches no telemetry operation (M7a-D6)
api POST /api/v1/tokens '{"name":"e2e-metrics","scopes":["metrics"]}' >"$tmp/metrics-created.json" ||
  fail "the metrics token not created" server
metrics_token=$(jqc -r '.token' <"$tmp/metrics-created.json") || metrics_token=
case $metrics_token in
  cdn_*) ;;
  *) fail "the metrics token is malformed" server ;;
esac
printf '%s\n' "$metrics_token" >>"$tmp/secrets"
header_file metrics-header "Authorization: Bearer $metrics_token"
within 30 "the prometheus exposition lacks the device's labelled series" "server device-sim" scraped
http=$(raw GET "/api/v1/logs?limit=1" -H "@$(native "$tmp/metrics-header")") || http=
{ [ "$http" = 403 ] && error_is forbidden; } || fail "the metrics token's read of the logs answered ${http:-nothing}" server
grep -q -e '_limit_gauge' -e '_used_gauge' "$tmp/prom.txt" && fail "a device gauge scraped with the _gauge suffix" server
http=$(curl -sS -m 10 -o "$(native "$tmp/body")" -w '%{http_code}' "$base/api/v1/metrics") || http=
[ "$http" = 401 ] || fail "the prometheus exposition answered ${http:-nothing} without a token" server
for f in metrics.json processes.json prom.txt; do
  ! grep -q cmdline "$tmp/$f" || fail "the $f answer names a command line" server
done

# the server stopped: the probe's lines wait in the device's ring, and once the server is back they are stored, or
# the ring's dropped count tells they could not wait
before=$(logs_dropped) || before=
case $before in
  '' | *[!0-9]*) fail "the logs capability's dropped count not read" server ;;
esac
$compose stop server >/dev/null 2>&1 || fail "server not stopped" server
logprobe "$(printf 'cadence-e2e-logprobe offline %s\n' 1 2 3)"
within 10 "the sim did not take its probe file with the server down" device-sim probe_taken
# a few sends fail meanwhile
sleep 5
started=$(date +%s)
$compose start server >/dev/null 2>&1 || fail "server not started" server
within 60 "server not healthy after its stop" server healthy
within 90 "the lines made while the server was down neither stored nor counted dropped" "server device-sim" drained \
  "$before"
echo "e2e: telemetry offline backlog: $drain $(($(date +%s) - started)) s after the server's start"
probe_stored "$redacted" || fail "the probe line stored again after the server's restart" server
echo "e2e: telemetry block $(($(date +%s) - m6b_start)) s"

# the dashboard (M7b): its image runs as node on a read-only root and is healthy; the edge splits the one origin, the
# server's paths to the server (its own answers: the device api's refusals, the admin api's) and every other to the
# dashboard (the sim links and enrolls past it, at http://server:8080: the live feed is the websocket it carries);
# then the playwright specs (dashboard/e2e, deploy/dev/playwright.sh) as the owner, with the password the m7a block
# set, on its stdin: a login submitted with scripts off (a post, answered 405, the password never in the url), a wrong
# password, the devices list with the sim online, its page, a config through the editor applied on the sim, a secret
# that reaches the sim (one more set of it in the server's log) and shows as set, never as its value, a token shown
# once, a refusal in a dialog that keeps the focus, a turn the say file starts on the live timeline, the sign-out and a
# session ended elsewhere (the feed's 1008); then the newer pages (M7c): a provider of a fake module from the catalog
# tested ok and deleted, a mode that allowedModes names refused deletion (409) and then deleted, the sim renamed and
# named back, the scripted tool turn the say file starts in its conversations with its call, its cpu and top processes
# in its health tab, and a line of the sim's log probe on the logs page's live tail. what the specs saw (their
# sessions' cookies, the token, the secret, the value the scripts-off login posted) joins the secrets the privacy block
# greps every log for, and no artifact of theirs holds any secret of the run (the runner removes them all and fails if
# one does); the probe line and the fake transcript are no secrets, covered by the leaks greps of every log. rule 7:
# the config is system.metrics' interval, the secret soloist's key, and the turns' tool sets the sim to 10, below
# every level the other blocks set
dash_start=$(date +%s)
dash_id=$($compose ps -q dashboard)
[ "$(docker inspect -f '{{.Config.User}} {{.HostConfig.ReadonlyRootfs}}' "$dash_id" 2>/dev/null)" = "node true" ] ||
  fail "the dashboard does not run as node on a read-only root" dashboard
[ "$($compose exec -T dashboard id -u 2>/dev/null | tr -d '\r')" = 1000 ] || fail "the dashboard's process is not uid 1000" dashboard
dashboard_healthy() { [ "$(docker inspect -f '{{.State.Health.Status}}' "$dash_id" 2>/dev/null)" = healthy ]; }
within 60 "the dashboard not healthy" dashboard dashboard_healthy
http=$(raw GET /v2/device) || http=
{ [ "$http" = 401 ] && error_is unauthorized; } || fail "the edge's /v2/device answered ${http:-nothing}, not the server's 401" "edge server"
http=$(raw POST /v2/enroll -H "$m7a_json" --data-binary '{}') || http=
{ [ "$http" = 403 ] && error_is enroll_refused; } || fail "the edge's /v2/enroll answered ${http:-nothing}, not the server's 403" "edge server"
http=$(raw GET /api/v1/devices) || http=
{ [ "$http" = 401 ] && error_is unauthorized; } || fail "the edge's /api/v1 answered ${http:-nothing}, not the server's 401" "edge server"
http=$(raw GET /login) || http=
{ [ "$http" = 200 ] && grep -q '<title>Sign in · Cadence</title>' "$tmp/body"; } ||
  fail "the edge's /login answered ${http:-nothing}, not the dashboard's sign-in page" "edge dashboard"
# a path that only starts like the server's is the dashboard's
http=$(raw GET /apiary) || http=
{ [ "$http" = 404 ] && grep -q '<title>' "$tmp/body"; } || fail "the edge's /apiary answered ${http:-nothing}, not the dashboard's 404 page" "edge dashboard"

# the turns on a scripted llm like the tool turn's (device_volume, then nothing said), so the conversations list a
# tool; the mcp block's settings come back after
dash_script='"script":[{"toolCalls":[{"name":"device_volume","arguments":{"level":10}}]},{"text":""}]'
printf '{"name":"llm-dash","module":"fake-llm","config":{%s,"rules":[%s]},"secrets":{}}' "$dash_script" "$maintenance" \
  >"$tmp/llm-dash.json"
api POST /api/v1/providers "@$(native "$tmp/llm-dash.json")" >/dev/null || fail "provider llm-dash not created" server
api GET /api/v1/settings/global >"$tmp/global-dash.json" || fail "global settings not read" server
jqc '. + {llm: "llm-dash"}' <"$tmp/global-dash.json" >"$tmp/global-dash-script.json" || fail "global settings not merged" server
api PUT /api/v1/settings/global "@$(native "$tmp/global-dash-script.json")" >/dev/null ||
  fail "global settings not switched to llm-dash" server
# the screen sim joined device-first (stream H): its claim waits since the stack started (a fresh code every 10 min);
# the owner's approval through the api names it sim-tv-e2e in a room, and its next poll takes its token and links.
# the remote pad's spec presses its keys on its fake screen
tv_claim() {
  api GET /api/v1/claims >"$tmp/claims.json" 2>/dev/null || return 1
  tv_code=$(jqc -r 'map(select(.board == "sim-tv")) | .[0].code // empty' <"$tmp/claims.json") || return 1
  [ -n "$tv_code" ]
}
within 60 "the screen sim opened no claim" "server device-sim-tv" tv_claim
tv=$(api POST "/api/v1/claims/$tv_code/approve" '{"name":"sim-tv-e2e","room":"Living room"}') ||
  fail "the screen sim's claim not approved" server
tv_id=$(printf '%s' "$tv" | jqc -r .id) || tv_id=
[ -n "$tv_id" ] && [ "$tv_id" != null ] || fail "the claim's approval named no device" server
tv_online() { api GET "/api/v1/devices/$tv_id" 2>/dev/null | jqc -e '.online and .status == "enrolled" and .room == "Living room"' >/dev/null; }
within 60 "the screen sim not online after its claim" "server device-sim-tv" tv_online
api GET /api/v1/claims | jqc -e 'map(select(.code == "'"$tv_code"'")) | length == 0' >/dev/null ||
  fail "the approved claim is still open" server

pw_dir="$tmp/playwright"
(umask 077 && mkdir "$pw_dir" && printf '%s\n' "$owner_pw" >"$pw_dir/password") || fail "the specs' private directory not made" server
# the run's other secrets, for the runner's grep of the specs' output and artifacts: the sessions and tokens so far,
# the admin token, the device token and the soloist key
(umask 077 && cat "$tmp/secrets" "$tmp/device-secrets" >"$pw_dir/secrets" && printf '%s\n' "$token" >>"$pw_dir/secrets") ||
  fail "the specs' secrets file not written" server
before=$(ok_turns)
secrets_set=$(lines server capability_secret '"capability":"media.soloist"' '"action":"set"')
specs_start=$(date +%s)
pw_rc=0
sh deploy/dev/playwright.sh "$project" "$pw_dir" || pw_rc=$?
echo "e2e: dashboard specs $(($(date +%s) - specs_start)) s"
rm -f "$pw_dir/secrets"
[ -s "$pw_dir/learned" ] && cat "$pw_dir/learned" >>"$tmp/secrets"
# no log shown for these two: the stack's logs are not checked yet
case $pw_rc in
  0) ;;
  3) echo "e2e: an artifact of the dashboard specs held a secret of the run (build/dashboard-e2e removed)" >&2; exit 1 ;;
  4) echo "e2e: the dashboard specs' sign-ins did not all report their cookies, which the privacy greps need" >&2; exit 1 ;;
  *) fail "the dashboard's playwright specs failed (exit $pw_rc; screenshots in build/dashboard-e2e)" "dashboard edge server" ;;
esac
# the secret the specs typed went through the server's secret store: one more set of soloist's key
more_lines "$secrets_set" server capability_secret '"capability":"media.soloist"' '"action":"set"' ||
  fail "the server logged no set of the soloist key the specs typed" server
turned "$before" || fail "the specs' turns were no successful turns" server
api PUT /api/v1/settings/global "@$(native "$tmp/global-dash.json")" >/dev/null || fail "global settings not restored" server
echo "e2e: dashboard block $(($(date +%s) - dash_start)) s"

# home assistant's client (M8-D10, D12): the component's client, feed and models, loaded without home assistant core by
# integrations/home_assistant/scripts/e2e_smoke.py in make ha-test's pinned python image on this stack's network (copied
# in, never mounted: a remote docker sees no host path), with uv's client group from the lock (on a cold uv cache, as
# in ci, aiohttp comes from pypi, as for make ha-test). its ha token comes from the server's cli straight into a
# mode-600 file and reaches the smoke on stdin alone. the smoke lists the devices, has the feed subscribed, starts a url
# session it sees as media_state playing with its metadata, pauses it (media_state paused) and says a line (202, which
# the sim plays); then the sim restarts (device_offline, device_online), a device_vitals arrives after the relink, and
# the token is revoked: the feed closes with 1008 and the next read is refused 401. the smoke prints codes alone, and
# the token is in neither its output nor a server log. rule 7: no volume
hac_start=$(date +%s)
(umask 077 && $compose exec -T server /cadence-server admin token create --name e2e-ha-client --scope ha \
  >"$tmp/ha-client-token") || fail "the ha client's token not created" server
{ [ "$(wc -l <"$tmp/ha-client-token" | tr -d ' ')" = 1 ] && grep -q '^cdn_' "$tmp/ha-client-token"; } ||
  fail "the ha client's token is malformed" server
cat "$tmp/ha-client-token" >>"$tmp/secrets"
rm -rf "$tmp/ha-client" && mkdir -p "$tmp/ha-client/cadence" "$tmp/ha-client/scripts"
cp integrations/home_assistant/pyproject.toml integrations/home_assistant/uv.lock "$tmp/ha-client/"
cp integrations/home_assistant/scripts/e2e_smoke.py "$tmp/ha-client/scripts/"
for m in client feed models; do
  cp "integrations/home_assistant/custom_components/cadence/$m.py" "$tmp/ha-client/cadence/"
done
# as in-docker.sh: the caller's user where docker runs natively, so uv's shared cache stays writable for make ha-test
hac_user=
case $(uname -s) in
  MINGW* | MSYS*) ;;
  *) hac_user="--user $(id -u):$(id -g)" ;;
esac
# a container a killed run left behind would refuse the name
docker rm -f "$ha_container" >/dev/null 2>&1 || true
# shellcheck disable=SC2086 # hac_user is one flag or none
docker create -i --name "$ha_container" --network "${project}_default" --entrypoint '' $hac_user -e HOME=/tmp \
  -v cadence-uv-cache:/var/tmp -e UV_CACHE_DIR=/var/tmp/uv -e UV_PROJECT_ENVIRONMENT=/tmp/ha-venv \
  -e UV_LINK_MODE=copy -e UV_PYTHON_DOWNLOADS=never -w /tmp/ha-client "$ha_image" \
  sh -c 'uv sync --locked --only-group client --quiet && exec /tmp/ha-venv/bin/python -B scripts/e2e_smoke.py "$@"' \
  smoke --url http://server:8080 --device "$id" --package /tmp/ha-client/cadence --media http://media/music.flac \
  >/dev/null || fail "the ha client's container not created" server
tar -C "$tmp" -cf - ha-client | docker cp - "$ha_container:/tmp" || fail "the ha client's files not copied" server
hac_said=$(announces) || fail "sim streams not read" device-sim
docker start -ai "$ha_container" <"$tmp/ha-client-token" >"$tmp/ha-client.out" 2>&1 &
ha_pid=$!
within 120 "the ha client did not list the device" server ha_client 'devices ok'
within 20 "the ha client's feed not subscribed" server ha_client connected
echo "e2e: ha client subscribed $(($(date +%s) - hac_start)) s into its block"
within 20 "the ha client saw no media_state of its url session playing" "server device-sim media" ha_client \
  'playing source=url'
within 20 "the ha client saw no media_state of its pause" "server device-sim" ha_client paused
within 20 "the ha client's say not answered 202" "server device-sim" ha_client 'said status=202'
within 15 "the sim did not play the ha client's say" device-sim announced_more "$hac_said"
linked=$(links)
$compose restart device-sim >/dev/null 2>&1 || fail "device not restarted for the ha client" device-sim
within 60 "device not linked again after its restart for the ha client" "server device-sim" online_again $((linked + 1))
within 30 "the ha client saw no device_offline and device_online" server ha_client online
within 60 "the ha client saw no device_vitals" "server device-sim" ha_client vitals
api DELETE /api/v1/tokens/e2e-ha-client >/dev/null || fail "the ha client's token not revoked" server
hac_revoked=$(date +%s)
within 45 "the ha client's feed not closed with 1008 after the revocation" server ha_client 'closed code=1008'
within 15 "the revoked token's read not refused 401" server ha_client 'refused status=401'
if [ -n "$ha_pid" ]; then
  wait "$ha_pid" && ha_rc=0 || ha_rc=$?
  ha_pid=
fi
[ "$ha_rc" = 0 ] || fail "the ha client ended with ${ha_rc:-?}: $(cat "$tmp/ha-client.out")" server
echo "e2e: ha client feed closed $(($(date +%s) - hac_revoked)) s after the revocation"
docker rm -f "$ha_container" >/dev/null 2>&1 || true
n=$(grep -c -F -f "$tmp/ha-client-token" "$tmp/ha-client.out" || true)
[ "$n" = 0 ] || fail "the ha client's output carries its token" server
n=$($compose logs --no-log-prefix server 2>/dev/null | grep -c -F -f "$tmp/ha-client-token" || true)
[ "$n" = 0 ] || fail "$n server log lines carry the ha client's token" server
echo "e2e: ha client block $(($(date +%s) - hac_start)) s"

# multiroom (docs/plans/2026-11-15-multiroom-impl.md): a second sim, enrolled here alone, whose group clock runs
# 1234 ms ahead and 80 ppm fast (containers share one kernel clock, so the sim's audiod skews its sync plane), and the
# first sim as a stereo pair (left, right) at 1000 ms of latency through the api. the member's play reaches the leader,
# both play synced within the bounds below and hear the same frames within 1000 us of each other (each sim's
# group_heard probe) after the settle, the resume and the rejoin, a pause stops both and a resume starts both, the member's network dropped
# for 5 s rejoins it, and the group deleted turns both off. the group key the sims received is in no log, nor any
# 64-hex run the block's lines carry, nor the leader's address. rule 7: no volume but the fresh sim's first level, an
# authenticated api call as for the first sim
mr_start=$(date +%s)
mr_since=$(date -u +%Y-%m-%dT%H:%M:%SZ)
mr_net=${project}_default

# mr_status: a compact view of the group's members (state, sync numbers, rejoins, error tokens), for a failure's
# evidence; ids and numbers alone
mr_status() {
  api GET "/api/v1/groups/$gid" 2>/dev/null | jqc -c '{leaderId, active, members: [.members[] | {deviceId, online,
    status: (.status // {} | {role, state, synced, clockErrorUs, placementErrorUs, queueMs, steerPpm, rejoins, underruns,
    latencyMs, error})}]}' >&2 || true
  $compose logs --no-log-prefix device-sim device-sim-2 2>/dev/null | grep -e '"msg":"group_' | tail -n 30 >&2 || true
}

# mr_within SECONDS WHAT CHECK...: within, with the group's state and both sims' group lines shown on a failure
mr_within() {
  _limit=$1 _what=$2
  shift 2
  _deadline=$(($(date +%s) + _limit))
  until "$@"; do
    if [ "$(date +%s)" -ge "$_deadline" ]; then
      mr_status
      fail "$_what after ${_limit}s" "server device-sim device-sim-2"
    fi
    sleep 1
  done
}

# mr_held N GAP CHECK...: CHECK passes N times in a row, GAP seconds apart (each a later status report: audiod reports
# every 5 s while grouped), within 60 s; a miss starts the count again
mr_held() {
  _n=$1 _gap=$2
  shift 2
  _deadline=$(($(date +%s) + 60))
  _seen=0
  while [ "$_seen" -lt "$_n" ]; do
    if "$@"; then
      _seen=$((_seen + 1))
      [ "$_seen" -lt "$_n" ] && sleep "$_gap"
      continue
    fi
    _seen=0
    [ "$(date +%s)" -lt "$_deadline" ] || return 1
    sleep 1
  done
}

# mr_group FILTER: get-group matches FILTER, with $l the leader (the first sim), $m the member and st(id) a member's
# media.group state as it last reported it ({} for none)
mr_group() {
  api GET "/api/v1/groups/$gid" 2>/dev/null | jqc -e --arg l "$id" --arg m "$id2" --arg g "$gid" \
    "def st(\$d): ([.members[] | select(.deviceId == \$d) | .status // {}] | .[0] // {});
     def mag: if . < 0 then -. else . end;
     $1" >/dev/null 2>&1
}

# both in the group, synced, idle or playing: the member's clock fit holds the pair's budget (100 us) despite the skew
mr_ready() {
  mr_group '.leaderId == $l and .active and
    (st($l) | .groupId == $g and .role == "leader" and (.state == "idle" or .state == "playing") and .synced) and
    (st($m) | .groupId == $g and .role == "member" and (.state == "idle" or .state == "playing") and .synced)'
}

# both playing, synced, and placed within 1000 us: the member's clock error under 1000 us too. synced already holds the
# member's clock fit to the pair's 100 us; 1 ms is 10x that, room for a busy host's scheduling of the 2 ms pump and the
# sims' file backends, yet an 80 ppm skew the mapping missed would pass it within the 10 s the check holds (80 us a
# second), and an offset it missed would be off by 1234 ms
mr_in_sync() {
  mr_group '(st($l) | .state == "playing" and .synced and (.placementErrorUs | mag) < 1000) and
    (st($m) | .state == "playing" and .synced and (.placementErrorUs | mag) < 1000 and .clockErrorUs < 1000)'
}

# both stopped by the pause: neither plays, the leader's session is paused
mr_paused() {
  mr_group '(st($l) | .state == "idle") and (st($m) | .state == "idle")' &&
    api GET "/api/v1/devices/$id/media" 2>/dev/null | jqc -e '.state == "paused"' >/dev/null 2>&1 &&
    api GET "/api/v1/devices/$id2/media" 2>/dev/null | jqc -e '.state != "playing"' >/dev/null 2>&1
}

# mr_left SINCE: the member's group states as its audiod logged them since SINCE (an RFC 3339 time), one a line
mr_left() {
  $compose logs --no-log-prefix --since "$1" device-sim-2 2>/dev/null | grep -F '"msg":"group_state"' |
    grep -F '"role":"member"' | sed -n 's/.*"state":"\([a-z_]*\)".*/\1/p'
}

# mr_rejoined BEFORE SINCE: both play synced again after the member's drop, which it noticed: it counted a rejoin past
# BEFORE (a link that came back while the group played counts as one, as a playout's own rejoin does), or it left
# playing since SINCE and came back on its own timeline (its link outlived the drop: the same address again, inside the
# link's ping timeout)
mr_rejoined() {
  mr_group '(st($m) | .state == "playing" and .synced) and (st($l) | .state == "playing")' || return 1
  mr_group "st(\$m) | .rejoins > $1" && return 0
  mr_left "$2" | grep -qvx playing
}

# mr_off DEVICE: its media.group reports off, its config applied
mr_off() {
  api GET "/api/v1/devices/$1" 2>/dev/null |
    jqc -e 'any(.capabilities[]; .id == "media.group" and .appliedGen == .desiredGen and .state.state == "off")' \
      >/dev/null 2>&1
}

# mr_capable DEVICE: online with media.group, reporting off (in no group yet)
mr_capable() {
  api GET "/api/v1/devices/$1" 2>/dev/null |
    jqc -e '.online and any(.capabilities[]; .id == "media.group" and .state.state == "off")' >/dev/null 2>&1
}

# mr_level DEVICE LEVEL: the device reports LEVEL
mr_level() {
  api GET "/api/v1/devices/$1" 2>/dev/null |
    jqc -e --argjson level "$2" 'any(.capabilities[]; .id == "audio.output" and .state.level == $level)' >/dev/null 2>&1
}

# mr_media DEVICE FILTER: the device's media session matches FILTER
mr_media() { api GET "/api/v1/devices/$1/media" 2>/dev/null | jqc -e "$2" >/dev/null 2>&1; }

# mr_member_session: the member's cadenced logged its group session playing
mr_member_session() {
  $compose logs --no-log-prefix device-sim-2 2>/dev/null | grep '"msg":"media_state"' | grep '"source":"group"' |
    grep -q '"state":"playing"'
}

# mr_rejoins: the member's rejoins as it last reported them
mr_rejoins() {
  api GET "/api/v1/groups/$gid" | jqc -r --arg m "$id2" '[.members[] | select(.deviceId == $m) | .status.rejoins] | .[0]'
}

# mr_cmd DEVICE CAPABILITY COMMAND BODY: a command through the api, answered 200
mr_cmd() {
  http=$(status POST "/api/v1/devices/$1/capabilities/$2/commands/$3" "$4") || http=
  [ "$http" = 200 ] || {
    mr_status
    fail "$2 $3 on $1 answered ${http:-nothing}: $(cat "$tmp/body" 2>/dev/null)" "server device-sim device-sim-2"
  }
}

# mr_heard SERVICE: the sim's group_heard probe lines (sim builds of audiod alone: when its output heard a frame whose
# seq is a multiple of 250, in leader-clock ns), as "epoch seq t_ns", the first per (epoch, seq). audiod's lines reach
# the container's log through cadenced's local log (source audiod). awk keeps the numbers as text (t_ns is past a
# double's precision), and a line that is not one whole log object (two run together) is skipped
mr_heard() {
  $compose logs --no-log-prefix "$1" 2>/dev/null | grep -F '"msg":"group_heard"' | awk '
    function f(k) {
      if (match($0, "\"" k "\":-?[0-9]+[,}]")) return substr($0, RSTART + length(k) + 3, RLENGTH - length(k) - 4)
      return ""
    }
    gsub(/"time":/, "&") == 1 && gsub(/"msg":/, "&") == 1 {
      e = f("epoch"); q = f("seq"); t = f("t_ns")
      if (e != "" && q != "" && t != "" && !seen[e " " q]++) print e, q, t
    }'
}

# mr_heard_snapshot FILE: every (epoch, seq) either sim heard so far, so a phase's pairs are the later ones
mr_heard_snapshot() {
  { mr_heard device-sim; mr_heard device-sim-2; } | awk '{ print $1, $2 }' | sort -u >"$1"
}

# mr_heard_pairs SINCE: "epoch seq t_leader t_member" for each frame both sims heard that the snapshot SINCE lacks
mr_heard_pairs() {
  mr_heard device-sim >"$tmp/heard-leader"
  mr_heard device-sim-2 >"$tmp/heard-member"
  awk 'FILENAME == ARGV[1] { old[$1 " " $2] = 1; next }
    FILENAME == ARGV[2] { l[$1 " " $2] = $3; next }
    (($1 " " $2) in l) && !(($1 " " $2) in old) { print $1, $2, l[$1 " " $2], $3 }' \
    "$1" "$tmp/heard-leader" "$tmp/heard-member"
}

# mr_aligned PHASE SINCE MIN WAIT: within WAIT seconds both sims heard MIN frames or more that the snapshot SINCE lacks,
# and each frame was heard by the two within 1000 us of leader time: their outputs played the same frame at the same
# moment, as each one's playhead tells it, not the placement each reports of itself. the max is printed; a miss shows
# every pair (numbers alone)
mr_aligned() {
  _phase=$1 _since=$2 _min=$3
  _deadline=$(($(date +%s) + $4))
  while :; do
    mr_heard_pairs "$_since" >"$tmp/heard-pairs"
    [ "$(wc -l <"$tmp/heard-pairs")" -ge "$_min" ] && break
    if [ "$(date +%s)" -ge "$_deadline" ]; then
      echo "e2e: multiroom $_phase: $(wc -l <"$tmp/heard-pairs" | tr -d ' ') frames heard by both, of $_min (leader $(wc -l <"$tmp/heard-leader" | tr -d ' ') lines, member $(wc -l <"$tmp/heard-member" | tr -d ' '))" >&2
      sed 's/^/e2e: multiroom heard /' "$tmp/heard-pairs" >&2
      mr_status
      fail "the pair's $_phase: under $_min frames heard by both after ${4}s" "device-sim device-sim-2"
    fi
    sleep 1
  done
  _max=0 _n=0 _bad=0
  while read -r _e _q _tl _tm; do
    _d=$((_tl - _tm))
    [ "$_d" -lt 0 ] && _d=$((-_d))
    [ "$_d" -gt "$_max" ] && _max=$_d
    [ "$_d" -lt 1000000 ] || _bad=$((_bad + 1))
    _n=$((_n + 1))
  done <"$tmp/heard-pairs"
  echo "e2e: multiroom $_phase: $_n frames heard by both, at most $((_max / 1000)) us apart ($_max ns)"
  [ "$_bad" = 0 ] || {
    sed 's/^/e2e: multiroom heard /' "$tmp/heard-pairs" >&2
    mr_status
    fail "$_bad of the pair's $_phase frames heard 1000 us or more apart (max $_max ns)" "device-sim device-sim-2"
  }
}

device2=$(api POST /api/v1/devices '{"name":"sim-e2e-2","model":"sim"}') || fail "the second sim's device not created" server
id2=$(printf '%s' "$device2" | jqc -r .id) || id2=
code2=$(printf '%s' "$device2" | jqc -r .code) || code2=
case "$id2:$code2" in
  :* | *: | null:* | *:null) fail "the second sim's device reply without id or code" server ;;
esac
$compose exec -T device-sim-2 /cadenced enroll --server http://server:8080 --code "$code2" --board sim \
  --config $data/config.json --data-dir $data --audiod || fail "the second sim's enroll failed" "server device-sim-2"
within 60 "the second sim not online with media.group" "server device-sim-2" mr_capable "$id2"
within 30 "the first sim does not report media.group off" "server device-sim" mr_capable "$id"
# the fresh sim's first level, as the first sim's (rule 7: an authenticated api call); its audiod's gate holds playback
# until a level holds
mr_cmd "$id2" audio.output set_level '{"level":30}'
within 30 "the second sim's level 30 not reported" "server device-sim-2" mr_level "$id2" 30

pair=$(printf '{"name":"Cadence E2E Pair","kind":"stereo_pair","preferredLeader":"%s","latencyMs":1000,"members":[{"deviceId":"%s","channel":"left"},{"deviceId":"%s","channel":"right"}]}' "$id" "$id" "$id2")
http=$(status POST /api/v1/groups "$pair") || http=
[ "$http" = 201 ] || fail "create-group answered ${http:-nothing}: $(cat "$tmp/body" 2>/dev/null)" server
gid=$(jqc -r .id <"$tmp/body") || gid=
case $gid in '' | null) fail "create-group named no group" server ;; esac
grouped=$(date +%s)
mr_within 60 "the pair not synced and idle on both sims" mr_ready
echo "e2e: multiroom pair synced $(($(date +%s) - grouped)) s after create-group"

# the key both sims received (each a mode-600 file of its own), read from the second's store into a file of this shell
# alone, never printed or put on a command line; the first sim's equal to it
for s in device-sim device-sim-2; do
  (umask 077 && $compose cp "$s:$data/config/media.group.json" - 2>/dev/null | tar -x -O -f - media.group.json \
    2>/dev/null | jqc -r '.secrets.groupKey // empty' >"$tmp/group-key-$s") || fail "the group key not read from $s" "$s"
  grep -qxE '[0-9a-f]{64}' "$tmp/group-key-$s" || fail "$s holds no 64-hex group key" "$s"
done
cmp -s "$tmp/group-key-device-sim" "$tmp/group-key-device-sim-2" || fail "the two sims hold different group keys" server
leader_host=$($compose cp device-sim-2:$data/config/media.group.json - 2>/dev/null | tar -x -O -f - media.group.json \
  2>/dev/null | jqc -r '.config.leader.host // empty') || leader_host=
printf '%s' "$leader_host" | grep -qxE '[0-9]{1,3}(\.[0-9]{1,3}){3}' ||
  fail "the member's config names no leader address" device-sim-2

# the play goes to the member: the server sends it to the leader, which streams it to the pair
mr_cmd "$id2" media.url play \
  '{"url":"http://media/group.ogg","metadata":{"title":"Cadence E2E Group Song","artist":"Cadence E2E Artist"}}'
mr_within 15 "the leader not playing the member's url" mr_media "$id" \
  '.state == "playing" and .source == "url" and .metadata.title == "Cadence E2E Group Song"'
mr_within 30 "the member's cadenced logged no group session playing" mr_member_session
mr_within 30 "the member's session not playing with the leader's metadata" mr_media "$id2" \
  '.state == "playing" and .metadata.title == "Cadence E2E Group Song"'
# the server's name for a group session (MEDIA_SOURCE_GROUP)
mr_source=$(api GET "/api/v1/devices/$id2/media" | jqc -r .source) || mr_source=
[ "$mr_source" = group ] || fail "the member's media session source is ${mr_source:-nothing} in the api, not group" server
played=$(date +%s)
mr_held 3 5 mr_in_sync || {
  mr_status
  fail "the pair not playing synced within 1000 us for 3 reports in a row" "server device-sim device-sim-2"
}
echo "e2e: multiroom pair in sync $(($(date +%s) - played)) s after the play"
api GET "/api/v1/groups/$gid" | jqc -c --arg l "$id" '[.members[] | {leader: (.deviceId == $l),
  state: .status.state, clockErrorUs: .status.clockErrorUs, placementErrorUs: .status.placementErrorUs,
  steerPpm: .status.steerPpm, queueMs: .status.queueMs}]' | sed 's/^/e2e: multiroom /'
# the two outputs hear the same frames together: 5 frames (one each 5 s) heard by both after the settle
mr_heard_snapshot "$tmp/heard-settled"
mr_aligned play "$tmp/heard-settled" 5 45

# a pause on the leader stops both; the member's resume (sent on to the leader) starts both again
mr_cmd "$id" media.player pause '{}'
mr_within 15 "the pair not stopped by the pause" mr_paused
mr_cmd "$id2" media.player resume '{}'
mr_within 30 "the pair not playing again after the resume" mr_in_sync
mr_heard_snapshot "$tmp/heard-resumed"
mr_aligned resume "$tmp/heard-resumed" 3 30

# the member's network gone for 5 s: its playout runs dry and it rejoins from the leader's backlog, while the leader
# plays on. its links to the server and the leader may come back on a new address, so the wait covers the link's ping
# timeout (20 s pings, 10 s to answer)
rejoins=$(mr_rejoins) || rejoins=
case $rejoins in '' | null | *[!0-9]*) mr_status; fail "the member reports no rejoins count" "server device-sim-2" ;; esac
cid2=$($compose ps -q device-sim-2) || cid2=
[ -n "$cid2" ] || fail "the second sim's container not found" device-sim-2
dropped_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
docker network disconnect "$mr_net" "$cid2" || fail "the second sim not disconnected" device-sim-2
dropped=$(date +%s)
sleep 5
docker network connect --alias device-sim-2 "$mr_net" "$cid2" || fail "the second sim not connected again" device-sim-2
mr_within 75 "the member not playing synced again after its drop" mr_rejoined "$rejoins" "$dropped_at"
echo "e2e: multiroom member rejoined $(($(date +%s) - dropped)) s after its network dropped (rejoins $rejoins -> $(mr_rejoins), $(mr_left "$dropped_at" | tr '\n' ' ' | sed 's/ $//'))"
mr_within 30 "the pair not in sync after the rejoin" mr_in_sync
mr_heard_snapshot "$tmp/heard-rejoined"
mr_aligned rejoin "$tmp/heard-rejoined" 3 30

# stop, then the group deleted: both sims off, and the group gone
mr_cmd "$id" media.player stop '{}'
mr_within 15 "the leader's session not idle after the stop" mr_media "$id" '.state == "idle"'
api DELETE "/api/v1/groups/$gid" >/dev/null || fail "delete-group refused" server
within 30 "the leader's media.group not off after the delete" "server device-sim" mr_off "$id"
within 30 "the member's media.group not off after the delete" "server device-sim-2" mr_off "$id2"
http=$(curl -sS -m 10 -o "$(native "$tmp/body")" -w '%{http_code}' -H "Authorization: Bearer $token" \
  "$base/api/v1/groups/$gid") || http=
[ "$http" = 404 ] || fail "the deleted group answered ${http:-nothing}" server

# privacy (rule 6, rule 8): the group key, in hex (either case) and base64, is in no service's log; the block's lines
# (the second sim's whole log, the first's and the server's since the block began) carry no 64-hex run and no groupKey
# value; and no sim's or server log line names the leader's address. the patterns come from files
(umask 077 && {
  cat "$tmp/group-key-device-sim"
  tr '[:lower:]' '[:upper:]' <"$tmp/group-key-device-sim"
  if command -v xxd >/dev/null 2>&1; then xxd -r -p <"$tmp/group-key-device-sim" | base64; fi
} >"$tmp/group-secrets") || fail "the group key's patterns not written" server
# every service but the dashboard specs' browser, which keeps no log
mr_services=$($compose config --services | grep -vx playwright) || fail "the stack's services not listed" server
for s in $mr_services; do
  n=$($compose logs --no-log-prefix "$s" 2>/dev/null | grep -c -F -f "$tmp/group-secrets" || true)
  [ "$n" = 0 ] || { echo "e2e: $n $s log lines carry the group key" >&2; exit 1; }
done
{
  $compose logs --no-log-prefix device-sim-2 2>/dev/null
  $compose logs --no-log-prefix --since "$mr_since" server device-sim 2>/dev/null
} >"$tmp/group-lines"
# the lines read are the group's: both sims' audiod started its part
[ "$(grep -c '"msg":"group_started"' "$tmp/group-lines")" -ge 2 ] ||
  fail "the block's lines hold no group_started of both sims" "device-sim device-sim-2"
# the alignment probe's lines (both sims' whole logs) carry its three numbers and the log's own fields, nothing else
$compose logs --no-log-prefix device-sim device-sim-2 2>/dev/null | grep -F '"msg":"group_heard"' >"$tmp/heard-lines" ||
  fail "no group_heard line in the sims' logs" "device-sim device-sim-2"
jqc -e -R -n '[inputs | try fromjson catch "not json"] | length > 0 and all(type == "object" and .msg == "group_heard"
    and .source == "audiod" and (keys - ["time", "level", "msg", "source"]) == ["epoch", "seq", "t_ns"]
    and ([.epoch, .seq, .t_ns] | all(type == "number")))' <"$tmp/heard-lines" >/dev/null || {
  echo "e2e: a group_heard line carries more than its epoch, seq and t_ns numbers (not shown)" >&2
  exit 1
}
n=$(grep -c -E -e '[0-9a-fA-F]{64}' -e '"groupKey":"[^"]' -e 'groupKey=[^ ]' "$tmp/group-lines" || true)
[ "$n" = 0 ] || { echo "e2e: $n of the multiroom block's log lines carry a 64-hex run or a groupKey value" >&2; exit 1; }
leader_re=$(printf '%s' "$leader_host" | sed 's/\./\\./g')
n=$($compose logs --no-log-prefix server device-sim-2 2>/dev/null | grep -c -E "(^|[^0-9.])$leader_re([^0-9]|$)" || true)
n2=$(grep -c -E "(^|[^0-9.])$leader_re([^0-9]|$)" "$tmp/group-lines" || true)
[ "$n" = 0 ] && [ "$n2" = 0 ] || { echo "e2e: $n server or member and $n2 block log lines name the leader's address" >&2; exit 1; }
rm -f "$tmp/group-key-device-sim" "$tmp/group-key-device-sim-2" "$tmp/group-lines" "$tmp/heard-lines"
# the second sim's token joins the end's greps
dtoken2=$($compose cp device-sim-2:$data/config.json - 2>/dev/null | tar -x -O -f - config.json 2>/dev/null |
  jqc -r .deviceToken) || dtoken2=
case $dtoken2 in
  cdd_*) ;;
  *) fail "the device token not read from the second sim's config" device-sim-2 ;;
esac
(umask 077 && printf '%s\n' "$dtoken2" >>"$tmp/device-secrets") || fail "secrets file not extended" server
echo "e2e: multiroom block $(($(date +%s) - mr_start)) s"

# privacy (M7a): no log of the server has the owner password, a session token, a known-browser token or the ha token,
# nor the csrf header or a cookie's name. the patterns come from a file, never an argument
printf '%s\n' X-Cadence-CSRF cadence_session cadence_browser >>"$tmp/secrets"
n=$($compose logs --no-log-prefix server 2>/dev/null | grep -c -F -f "$tmp/secrets" || true)
[ "$n" = 0 ] || fail "$n server log lines carry the owner password, a session, known-browser or api token, the csrf header or a cookie name" server
# nor the sim's or the dashboard's, which also never has what the dashboard specs saw (their cookies, the token, the
# secret they typed)
for s in device-sim device-sim-2 dashboard; do
  n=$($compose logs --no-log-prefix "$s" 2>/dev/null | grep -c -F -f "$tmp/secrets" || true)
  # the lines themselves are not shown
  [ "$n" = 0 ] || {
    echo "e2e: $n $s log lines carry the owner password, a session, a token, what the dashboard specs saw, the csrf header or a cookie name" >&2
    exit 1
  }
done

# privacy (M7b-D6): the dashboard's log is a line per request of its method, path, status and time (no query, header,
# cookie or body), and its start and stop lines; nothing a library logged (console) or a render failed on
$compose logs --no-log-prefix dashboard 2>/dev/null >"$tmp/dashboard.log" || { echo "e2e: the dashboard's log not read" >&2; exit 1; }
[ "$(grep -c '"msg":"request"' "$tmp/dashboard.log")" -ge 50 ] || { echo "e2e: the dashboard logged under 50 requests" >&2; exit 1; }
grep -v '^[[:space:]]*$' "$tmp/dashboard.log" | jqc -e -R -n '[inputs | try fromjson catch "not json"] | all(
    type == "object" and (keys - ["time", "level", "msg", "method", "path", "status", "ms", "port", "files", "signal"]) == []
    and ((.msg == "request" and (.path | type == "string" and test("^/[^?#]*$|^-$")) and (.status | type == "number"))
      or .msg == "dashboard_listening" or .msg == "dashboard_stopping"))' >/dev/null || {
  echo "e2e: a dashboard log line is more than a request's method, path, status and time (not shown)" >&2
  exit 1
}

# privacy (rule 8, D8, D11, M5b-D10, M5c-D11, M5d-D7): the device log covers the playback, and no log (the fake
# hermes', the fake home assistant's, the fake mcp server's and the fake t3 code server's own among them) names the url,
# metadata, text, transcript, memory, a task's text, the home, an mcp tool's token, path, description, arguments or
# result, or t3 code's token, titles, questions or question tokens
$compose logs --no-log-prefix device-sim 2>/dev/null | grep -q '"msg":"url_ended"' ||
  fail "the device log has no url_ended line" device-sim
[ "$($compose logs --no-log-prefix mcp 2>/dev/null | grep -c '"msg":"call"')" = 3 ] ||
  fail "the fake mcp server's log has not one line per call" mcp
for s in server device-sim device-sim-2 hermes homeassistant mcp t3 dashboard; do
  n=$(leaks "$s")
  # the lines themselves are not shown
  [ "$n" = 0 ] || {
    echo "e2e: $n $s log lines carry the url, metadata, announced text, transcript, memory, task text, the home, mcp text or the log probe's" >&2
    exit 1
  }
done
# the hub's question tokens of the t3 block in no log
for s in server device-sim t3; do
  n=$($compose logs --no-log-prefix "$s" 2>/dev/null | grep -c -F -f "$tmp/t3-tokens" || true)
  [ "$n" = 0 ] || {
    echo "e2e: $n $s log lines carry a t3 question's token" >&2
    exit 1
  }
done
# rule 6: neither device token nor the soloist key in the server's or a sim's log
for s in server device-sim device-sim-2; do
  n=$($compose logs --no-log-prefix "$s" 2>/dev/null | secret_leaks -)
  [ "$n" = 0 ] || {
    echo "e2e: $n $s log lines carry the device token or the soloist key" >&2
    exit 1
  }
done

echo "e2e: ok"
