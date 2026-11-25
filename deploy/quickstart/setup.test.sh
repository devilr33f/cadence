#!/bin/sh
# the quickstart's setup.sh against a fake docker: what it writes into .env, that a second run keeps the database
# password and asks no password, that a first run whose password failed asks again, the origin's port, a bad host
# refused, and MSYS_NO_PATHCONV on docker's calls (git bash would rewrite /cadence-server into a windows path)
set -eu

src=$(cd "$(dirname "$0")" && pwd)/setup.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

mkdir -p "$tmp/bin"
cat >"$tmp/bin/docker" <<'EOF'
#!/bin/sh
echo "$* MSYS_NO_PATHCONV=${MSYS_NO_PATHCONV:-}" >>"$FAKE_LOG"
case "$*" in
  'compose version' | 'compose build' | 'compose up -d' | 'compose up -d server') ;;
  'compose ps -q caddy') echo c0ffee ;;
  'inspect -f '*) echo 'cadence_default ' ;;
  'network inspect -f '*) echo '172.20.0.0/16,' ;;
  'compose exec -T caddy wget -qO- http://server:8080/healthz') echo '{"status":"ok"}' ;;
  'compose exec -T caddy cat /data/caddy/pki/authorities/local/root.crt') echo 'FAKE ROOT' ;;
  'compose exec -T server /cadence-server admin password set')
    read -r p
    [ -z "${FAKE_PASSWORD_FAIL:-}" ] || exit 1
    echo "$p" >>"$FAKE_LOG.passwords" ;;
  *) echo "fake docker: unexpected $*" >&2; exit 9 ;;
esac
EOF
chmod +x "$tmp/bin/docker"
PATH=$tmp/bin:$PATH
export PATH

# stack NAME ENV: a quickstart tree of its own (its prebuilt fetch a stub) with that .env
stack() {
  d=$tmp/$1
  mkdir -p "$d/deploy/quickstart" "$d/deploy/builder"
  cp "$src" "$d/deploy/quickstart/setup.sh"
  printf '#!/bin/sh\nexit 0\n' >"$d/deploy/builder/prebuilt.sh"
  printf '%s\n' "$2" >"$d/deploy/quickstart/.env"
  FAKE_LOG=$d/docker.log
  export FAKE_LOG
  qs=$d/deploy/quickstart
}
val() { sed -n "s/^$1=//p" "$qs/.env" | tail -n 1; }
passwords() { if [ -f "$FAKE_LOG.passwords" ]; then wc -l <"$FAKE_LOG.passwords" | tr -d ' '; else echo 0; fi; }

# a first run: the generated half of .env, the ca exported, the password asked once
stack first "$(printf 'CADENCE_HOST=Cadence.LAN\nCADENCE_HTTPS_PORT=8443')"
echo 'a-long-password-1' | sh "$qs/setup.sh" >"$tmp/out" 2>&1 || fail "the first run failed: $(cat "$tmp/out")"
[ "$(val CADENCE_PUBLIC_ORIGIN)" = https://cadence.lan:8443 ] || fail "origin $(val CADENCE_PUBLIC_ORIGIN)"
db=$(val POSTGRES_PASSWORD)
[ "${#db}" = 48 ] || fail "database password ${#db} long"
[ "$(val CADENCE_TRUSTED_PROXY)" = 172.20.0.0/16 ] || fail "trusted proxy $(val CADENCE_TRUSTED_PROXY)"
[ "$(cat "$qs/data/ca.crt")" = 'FAKE ROOT' ] || fail "the ca was not exported"
[ "$(passwords)" = 1 ] || fail "the first run asked $(passwords) passwords"
grep 'admin password set' "$FAKE_LOG" | grep -q 'MSYS_NO_PATHCONV=1$' || fail "docker ran without MSYS_NO_PATHCONV=1"

# a second run keeps the database password and asks none
sh "$qs/setup.sh" </dev/null >"$tmp/out" 2>&1 || fail "the second run failed: $(cat "$tmp/out")"
[ "$(val POSTGRES_PASSWORD)" = "$db" ] || fail "the second run replaced the database password"
[ "$(passwords)" = 1 ] || fail "the second run asked a password"

# a first run whose password the server refused asks again on the next
stack refused 'CADENCE_HOST=192.168.1.10'
if echo 'short' | FAKE_PASSWORD_FAIL=1 sh "$qs/setup.sh" >"$tmp/out" 2>&1; then fail "a refused password passed"; fi
echo 'a-long-password-2' | sh "$qs/setup.sh" >"$tmp/out" 2>&1 || fail "the rerun failed: $(cat "$tmp/out")"
[ "$(passwords)" = 1 ] || fail "the rerun after a refused password asked none"
[ "$(val CADENCE_PUBLIC_ORIGIN)" = https://192.168.1.10 ] || fail "origin $(val CADENCE_PUBLIC_ORIGIN)"

# --no-password puts it off to the next run
stack later 'CADENCE_HOST=cadence.lan'
sh "$qs/setup.sh" --no-password </dev/null >"$tmp/out" 2>&1 || fail "--no-password failed: $(cat "$tmp/out")"
[ "$(passwords)" = 0 ] || fail "--no-password asked a password"
echo 'a-long-password-3' | sh "$qs/setup.sh" >"$tmp/out" 2>&1 || fail "the run after --no-password failed"
[ "$(passwords)" = 1 ] || fail "the run after --no-password asked none"

# a checkout's version is its git describe (git for windows reads no msys path with MSYS_NO_PATHCONV set)
stack versioned 'CADENCE_HOST=cadence.lan'
(cd "$tmp/versioned" && git init -q && git -c user.name=t -c user.email=t@example.com -c commit.gpgsign=false add -A &&
  git -c user.name=t -c user.email=t@example.com -c commit.gpgsign=false commit -q -m init)
echo 'a-long-password-4' | sh "$qs/setup.sh" >"$tmp/out" 2>&1 || fail "the versioned run failed: $(cat "$tmp/out")"
want=$(cd "$tmp/versioned" && git describe --always --dirty)
[ "$(val CADENCE_VERSION)" = "$want" ] || fail "version $(val CADENCE_VERSION), want $want"

# a host with a scheme is refused before docker builds anything
stack scheme 'CADENCE_HOST=https://cadence.lan'
if sh "$qs/setup.sh" </dev/null >"$tmp/out" 2>&1; then fail "a host with a scheme passed"; fi
grep -q 'compose build' "$FAKE_LOG" && fail "a bad host still built"

echo ok
