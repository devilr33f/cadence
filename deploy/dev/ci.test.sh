#!/bin/sh
# make ci with every gate faked: all gates run, in their order (the code generators before every gate that compiles,
# e2e alone at the end), side by side, each one's output whole, then a ci_gate line per gate; a failed gate fails ci,
# which names it and the gates it kept from running
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'cd / && rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

# the makefile and ci.sh alone, in a tree of nothing else: a real gate run by mistake fails at once, touching nothing
mkdir -p "$tmp/repo/deploy/dev"
cp "$root/Makefile" "$tmp/repo/Makefile"
cp "$root/deploy/dev/ci.sh" "$tmp/repo/deploy/dev/ci.sh"
cd "$tmp/repo"

# the gates' make takes nothing from the environment: an exported CI_GATE_MAKEFILE would fake or dry-run every gate
seen=$(env -u MAKEFLAGS -u MFLAGS -u MAKELEVEL CI_GATE_MAKEFILE=-n \
  make -s --no-print-directory --eval 'ci-test-env:;@echo "[$(CI_GATE_MAKEFILE)]"' ci-test-env 2>&1) || seen="make failed: $seen"
[ "$seen" = '[]' ] || fail "an exported CI_GATE_MAKEFILE reaches the gates: $seen"

gates='proto-check sqlc-check openapi-check dashboard-check lint test test-linux test-cgo tools-test cross device-limits-test media-test media-test-privileged airplay-runtime ha-test e2e'
export FAKE_GENERATORS='proto-check sqlc-check'
export FAKE_COMPILERS='openapi-check dashboard-check lint test test-linux test-cgo cross media-test media-test-privileged'
export FAKE_GATES="$gates"

# a fake gate: marks its start and end in FAKE_DIR (a file each, never a shared one), notes there what it found
# missing at its start, whether its first line reached its log in FAKE_CI_DIR while it ran, and which compiling
# gates still ran at its end, prints two lines 2 s apart, and fails when FAKE_FAIL names it
cat >"$tmp/gate.sh" <<'EOF'
gate=$1
d=$FAKE_DIR
: >"$d/start-$gate"
case " $FAKE_COMPILERS " in *" $gate "*) before=$FAKE_GENERATORS ;; *) before= ;; esac
[ "$gate" != e2e ] || before=$(echo "$FAKE_GATES" | sed 's/ e2e$//')
for b in $before; do [ -e "$d/end-$b" ] || echo "$b" >>"$d/early-$gate"; done
echo "$gate first"
i=0
until grep -qxF "$gate first" "$FAKE_CI_DIR/$gate.log" 2>/dev/null; do
  i=$((i + 1))
  [ "$i" -lt 50 ] || { : >"$d/unlogged-$gate"; break; }
  sleep 0.1
done
sleep 2
echo "$gate second"
for c in $FAKE_COMPILERS; do
  [ "$c" = "$gate" ] || [ ! -e "$d/start-$c" ] || [ -e "$d/end-$c" ] || echo "$c" >>"$d/beside-$gate"
done
: >"$d/end-$gate"
case " $FAKE_FAIL " in *" $gate "*) exit 1 ;; esac
EOF

# the fake gates as make targets, so each runs through make ci's own gate command
{
  echo ".PHONY: $gates"
  echo "$gates:"
  printf '\t@sh %s $@\n' "$tmp/gate.sh"
} >"$tmp/gates.mk"

# run FAILING: make ci with the fake gates, FAILING failing; sets out and rc
run() {
  rm -rf "$tmp/marks"
  mkdir "$tmp/marks"
  rc=0
  out=$(env -u MAKEFLAGS -u MFLAGS -u MAKELEVEL FAKE_DIR="$tmp/marks" FAKE_CI_DIR="$tmp/ci" FAKE_FAIL="$1" \
    make --no-print-directory ci CI_DIR="$tmp/ci" CI_GATE_MAKEFILE="-f $tmp/gates.mk" 2>&1) || rc=$?
}

ran() { [ -e "$tmp/marks/start-$1" ]; }

# summary RESULT...: the output ends with a ci_gate line per gate, in order, with these results
summary() {
  want=
  for g in $gates; do
    want="${want}ci_gate $g Ns $1
"
    shift
  done
  # one line per gate: the count follows the gate list, so a gate added in one place keeps the check honest
  got=$(printf '%s\n' "$out" | grep '^ci_gate ' | tail -n "$(echo $gates | wc -w)" | sed 's/ [0-9][0-9]*s / Ns /')
  [ "$got" = "${want%?}" ] || fail "summary: got
$got
want
$want"
}

run ''
[ "$rc" = 0 ] || fail "all ok: ci exited $rc: $out"
summary ok ok ok ok ok ok ok ok ok ok ok ok ok ok ok ok
for g in $gates; do
  ran "$g" || fail "$g never ran"
  # each gate's output whole: its second line right after its first
  printf '%s\n' "$out" | grep -A1 -xF "$g first" | tail -n 1 | grep -qxF "$g second" || fail "$g's output interleaved: $out"
done
for f in "$tmp"/marks/early-*; do
  [ ! -e "$f" ] || fail "${f##*/early-} started before $(tr '\n' ' ' <"$f")ended"
done
# the gate's log fills while it runs, not once it ended
for f in "$tmp"/marks/unlogged-*; do
  [ ! -e "$f" ] || fail "${f##*/unlogged-}'s log was still empty while it ran"
done
# side by side: compiling gates still ran when one of them ended
beside=$(cat "$tmp"/marks/beside-* 2>/dev/null | wc -l)
[ "$beside" -gt 0 ] || fail "the compiling gates ran one at a time"

run test
[ "$rc" != 0 ] || fail "ci passed with a failed test gate"
summary ok ok ok ok ok failed ok ok ok ok ok ok ok ok ok skipped
printf '%s\n' "$out" | grep -qx 'ci: not ok: test e2e' || fail "the failed gates not named: $out"
! ran e2e || fail "e2e ran after a failed gate"

run proto-check
[ "$rc" != 0 ] || fail "ci passed with a failed proto-check"
summary failed ok skipped skipped skipped skipped skipped skipped ok skipped ok skipped skipped ok ok skipped
for c in $FAKE_COMPILERS e2e; do
  ! ran "$c" || fail "$c ran after proto-check failed"
done

echo ok
