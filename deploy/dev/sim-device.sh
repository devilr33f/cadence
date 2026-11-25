#!/bin/sh
# make sim: this machine as a screen device of a real server. cadenced (the sim-tv board) and the real cadence-screen,
# both built natively; the device's state lives outside the repo, in $CADENCE_SIM_HOME (default
# ~/.config/cadence-sim): config.json (its token, mode 600), data/, server and cadenced.log. the first run claims the
# device on SERVER (kept in <home>/server): the screen shows the code to approve in the dashboard. delete the home
# directory to pair again
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
home=${CADENCE_SIM_HOME:-${XDG_CONFIG_HOME:-$HOME/.config}/cadence-sim}
cadenced=$root/build/sim/cadenced
screen=$root/build/screen-sim/build/cadence-screen
size=${SIZE:-1920x1080}

for binary in "$cadenced" "$screen"; do
  [ -x "$binary" ] || { echo "make sim: $binary is missing (run make sim)" >&2; exit 1; }
done

mkdir -p "$home/data"
chmod 700 "$home"
config=$home/config.json
set -- --config "$config"
if [ ! -f "$config" ]; then
  server=${SERVER:-}
  if [ -z "$server" ] && [ -f "$home/server" ]; then
    server=$(cat "$home/server")
  fi
  if [ -z "$server" ]; then
    echo "make sim: the first run pairs with a server: make sim SERVER=https://<your cadence server>" >&2
    exit 2
  fi
  printf '%s\n' "$server" > "$home/server"
  set -- "$@" --wait-config --claim-server "$server" --claim-board sim-tv --claim-data-dir "$home/data"
  echo "make sim: pairing with $server; approve the code the screen shows in the dashboard"
fi

socket=$home/data/run/screen.sock
rm -f "$socket"
CADENCE_SIM_TV=1 "$cadenced" "$@" >> "$home/cadenced.log" 2>&1 &
pid=$!
trap 'kill "$pid" 2> /dev/null || true; wait "$pid" 2> /dev/null || true' EXIT
trap 'exit 130' INT TERM

while [ ! -S "$socket" ]; do
  if ! kill -0 "$pid" 2> /dev/null; then
    echo "make sim: cadenced exited; its log: $home/cadenced.log" >&2
    tail -n 5 "$home/cadenced.log" >&2
    exit 1
  fi
  sleep 0.2
done

"$screen" --socket "$socket" --size "$size"
