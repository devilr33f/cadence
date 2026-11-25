#!/bin/sh
# the quickstart's one command: fills in .env's generated half, builds the images from this checkout, starts the
# stack, waits for the server, exports caddy's CA and, until one is set, asks for the dashboard password.
# safe to run again (an update after git pull); it never replaces the master key or the database password.
#   ./setup.sh [--no-password]   install or update
#   ./setup.sh password          set the dashboard password again
#   ./setup.sh ca                export caddy's CA to data/ca.crt again
set -eu
cd "$(dirname "$0")"
root=$(cd ../.. && pwd)
# git bash rewrites a container path argument (/cadence-server) into a windows one before docker.exe sees it
export MSYS_NO_PATHCONV=1
# a password the server took: a run without it asks for one, so a first run that failed midway asks again
password_set=data/.password-set

say() { printf '[*] %s\n' "$*"; }
die() { printf '[!] %s\n' "$*" >&2; exit 1; }

# env_get KEY: the last value .env gives KEY
env_get() { sed -n "s/^$1=//p" .env | tail -n 1; }

# env_set KEY VALUE: KEY's line replaced (or added), the file kept at mode 600: it holds the database password
env_set() {
  tmp=$(mktemp .env.XXXXXX)
  grep -v "^$1=" .env >"$tmp" || true
  printf '%s=%s\n' "$1" "$2" >>"$tmp"
  chmod 600 "$tmp"
  mv "$tmp" .env
}

set_password() {
  say "Choose the dashboard password (at least 12 characters). Typing is hidden."
  if [ -t 0 ]; then
    trap 'stty echo 2>/dev/null' EXIT INT TERM
    stty -echo
    printf 'Password: '
    read -r p1
    printf '\nAgain: '
    read -r p2
    stty echo
    printf '\n'
  else
    read -r p1
    p2=$p1
  fi
  [ -n "$p1" ] || die "the password is empty"
  [ "$p1" = "$p2" ] || die "the passwords differ, run ./setup.sh password to try again"
  printf '%s\n' "$p1" | docker compose exec -T server /cadence-server admin password set ||
    die "the server refused the password (at least 12 characters), run ./setup.sh password to try again"
  unset p1 p2
  mkdir -p data
  : >"$password_set"
  say "Password set"
}

export_ca() {
  mkdir -p data
  i=0
  until docker compose exec -T caddy cat /data/caddy/pki/authorities/local/root.crt >data/ca.crt.tmp 2>/dev/null; do
    i=$((i + 1))
    [ "$i" -lt 30 ] || { rm -f data/ca.crt.tmp; die "caddy made no CA yet: docker compose logs caddy"; }
    sleep 1
  done
  mv data/ca.crt.tmp data/ca.crt
  say "CA exported to deploy/quickstart/data/ca.crt"
}

command -v docker >/dev/null 2>&1 || die "docker is not installed: https://docs.docker.com/engine/install/"
docker compose version >/dev/null 2>&1 || die "docker compose (v2) is missing"
[ -f .env ] || die "no .env yet: cp .env.example .env, set CADENCE_HOST in it, then run ./setup.sh again"

case ${1:-} in
  password) set_password; exit 0 ;;
  ca) export_ca; exit 0 ;;
  --no-password | '') ;;
  *) die "usage: ./setup.sh [--no-password | password | ca]" ;;
esac

host=$(env_get CADENCE_HOST | tr '[:upper:]' '[:lower:]')
case $host in
  '') die "set CADENCE_HOST in .env" ;;
  *://* | */* | *:*) die "CADENCE_HOST is a bare name or address (cadence.lan, 192.168.1.10), no scheme, port or path" ;;
esac
port=$(env_get CADENCE_HTTPS_PORT)
case ${port:-443} in
  443) env_set CADENCE_PUBLIC_ORIGIN "https://$host" ;;
  *[!0-9]*) die "CADENCE_HTTPS_PORT is a number" ;;
  *) env_set CADENCE_PUBLIC_ORIGIN "https://$host:$port" ;;
esac
origin=$(env_get CADENCE_PUBLIC_ORIGIN)

[ -n "$(env_get POSTGRES_PASSWORD)" ] ||
  env_set POSTGRES_PASSWORD "$(head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')"

case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "this machine's cpu ($(uname -m)) has no server image: amd64 or arm64 only" ;;
esac
env_set CADENCE_ARCH "$arch"
env_set CADENCE_VERSION "$(git -C "$root" describe --always --dirty 2>/dev/null || echo quickstart)"
mkdir -p data/ca-certs

command -v bash >/dev/null 2>&1 || die "bash is needed for the prebuilt ffmpeg fetch"
say "Fetching the server's prebuilt ffmpeg (a miss builds it here, which takes a while)"
bash "$root/deploy/builder/prebuilt.sh" ensure "ffmpeg-server-$arch"

say "Building the images (the first build takes about 10 minutes)"
docker compose build
say "Starting the stack"
docker compose up -d

# the server trusts caddy's address for the client's in X-Forwarded-For: the network compose gave the stack
caddy=$(docker compose ps -q caddy)
net=$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' "$caddy" | cut -d' ' -f1)
subnet=$(docker network inspect -f '{{range .IPAM.Config}}{{.Subnet}},{{end}}' "$net" | sed 's/,$//')
if [ -n "$subnet" ] && [ "$subnet" != "$(env_get CADENCE_TRUSTED_PROXY)" ]; then
  env_set CADENCE_TRUSTED_PROXY "$subnet"
  docker compose up -d server
fi

say "Waiting for the server"
i=0
until docker compose exec -T caddy wget -qO- http://server:8080/healthz 2>/dev/null | grep -q '"status":"ok"'; do
  i=$((i + 1))
  [ "$i" -lt 60 ] || die "the server did not come up: docker compose logs server"
  sleep 2
done
say "Server healthy"

tls=$(env_get CADENCE_TLS)
[ "${tls:-internal}" != internal ] || export_ca

if [ ! -f "$password_set" ] && [ "${1:-}" != --no-password ]; then
  set_password
fi

say "Done: open $origin and log in"
if [ "${tls:-internal}" = internal ]; then
  say "Your browser will warn about the certificate until you trust deploy/quickstart/data/ca.crt (or accept it once)"
  say "Speakers: pass --ca-file deploy/quickstart/data/ca.crt to cadence-install"
fi
