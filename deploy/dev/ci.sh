#!/bin/sh
# make ci's bookkeeping:
#   ci.sh gate DIR GATE COMMAND...  runs one gate, its output also in DIR/GATE.log while it runs, and writes its line
#                                   `ci_gate GATE <seconds>s ok|failed` to DIR/GATE.result; exits as the gate did
#   ci.sh summary DIR GATE...       prints each gate's line in order, `ci_gate GATE 0s skipped` for one that never ran
#                                   (an earlier phase failed), and fails naming every gate that is not ok
set -u

case ${1:-} in
  gate)
    [ $# -ge 4 ] || { echo "usage: ci.sh gate DIR GATE COMMAND..." >&2; exit 2; }
    dir=$2 gate=$3
    shift 3
    start=$(date +%s)
    { "$@" 2>&1; echo $? >"$dir/$gate.rc"; } | tee "$dir/$gate.log"
    rc=$(cat "$dir/$gate.rc" 2>/dev/null) || rc=1
    rm -f "$dir/$gate.rc"
    result=ok
    [ "$rc" = 0 ] || result=failed
    line="ci_gate $gate $(($(date +%s) - start))s $result"
    echo "$line" >"$dir/$gate.result"
    echo "$line"
    exit "$rc"
    ;;
  summary)
    [ $# -ge 2 ] || { echo "usage: ci.sh summary DIR GATE..." >&2; exit 2; }
    dir=$2
    shift 2
    bad=
    for gate in "$@"; do
      if [ -s "$dir/$gate.result" ]; then
        line=$(cat "$dir/$gate.result")
      else
        line="ci_gate $gate 0s skipped"
      fi
      echo "$line"
      case $line in
        *" ok") ;;
        *) bad="$bad $gate" ;;
      esac
    done
    [ -z "$bad" ] || { echo "ci: not ok:$bad" >&2; exit 1; }
    ;;
  *)
    echo "usage: ci.sh gate DIR GATE COMMAND... | ci.sh summary DIR GATE..." >&2
    exit 2
    ;;
esac
