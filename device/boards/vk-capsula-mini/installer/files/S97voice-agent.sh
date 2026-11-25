#!/bin/sh
# cadence's launcher in place of the stock S97voice-agent.sh, written through the overlay by cadence-install (the
# stock copy stays in /rom): start runs cadence-run detached (stdio /dev/null, its pid in /run/cadence) and returns at
# once, stop TERMs it, and while the cadence tree is missing or /data/cadence/disabled exists the stock copy runs
# instead, as a program of its own. rcS and rcK source this file in a subshell with $1 set, so it never exits, and it
# runs only applets of the stock busybox on init's PATH. CADENCE_HOME and CADENCE_STOCK_S97 are for tests

cadence_home=${CADENCE_HOME:-/data/cadence}
cadence_stock=${CADENCE_STOCK_S97:-/rom/etc/init.d/S97voice-agent.sh}
cadence_pid=/run/cadence/cadence-run.pid

# the cadence-run the pid file names: /bin/sh is its argv[0], which its watchdog feeder, a subshell, shares
cadence_runs() {
    start-stop-daemon -K -t -q -p "$cadence_pid" -x /bin/sh
}

cadence_start() {
    if [ -x "$cadence_home/bin/cadence-run" ] && [ ! -e "$cadence_home/disabled" ]; then
        mkdir -p /run/cadence
        start-stop-daemon -S -b -m -o -p "$cadence_pid" -x /bin/sh -- "$cadence_home/bin/cadence-run"
    else
        sh "$cadence_stock" start
    fi
}

cadence_stop() {
    if cadence_runs; then
        start-stop-daemon -K -p "$cadence_pid" -x /bin/sh -s TERM
    else
        sh "$cadence_stock" stop
    fi
}

# cadence-run kills cadenced 3 s after its TERM, then blanks the lights
cadence_gone() {
    cadence_wait=0
    while cadence_runs && [ "$cadence_wait" -lt 10 ]; do
        sleep 1
        cadence_wait=$((cadence_wait + 1))
    done
}

case "$1" in
    start) cadence_start ;;
    stop) cadence_stop ;;
    restart) cadence_stop; cadence_gone; cadence_start ;;
    *) echo "usage: $0 {start|stop|restart}" >&2 ;;
esac
