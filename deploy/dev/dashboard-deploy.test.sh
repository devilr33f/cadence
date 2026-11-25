#!/bin/sh
# the one origin's path split (M7b-D7): the server's traefik labels (deploy/server/compose.yml), the dev stack's edge
# (deploy/dev/edge.yml) and the quickstart's caddy (deploy/quickstart/Caddyfile) give the server the same paths, every
# top-level route the server mounts among them, and the rest to the dashboard below it; and the dashboard's image is
# built on the node image the Makefile pins. each check is also run on a copy that drifts, which it must refuse
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

# label ROOT NAME: the value of a traefik label of the prod compose
label() { sed -n "s/^ *- traefik\.$2=//p" "$1/deploy/server/compose.yml"; }

# edge ROOT ROUTER FIELD: a field of a router of the dev edge
edge() {
  awk -v want="$2" -v field="$3" '
    /^  services:/ { exit }
    /^    [a-z-]+:$/ { router = $1; sub(":", "", router) }
    router == want && $1 == field ":" { sub("^ *" field ": *", ""); print }
  ' "$1/deploy/dev/edge.yml"
}

# service_url ROOT SERVICE: the url of a dev edge service's one server
service_url() {
  awk -v want="$2" '
    /^  services:/ { on = 1; next }
    on && /^    [a-z-]+:$/ { service = $1; sub(":", "", service) }
    on && service == want && $1 == "-" && $2 == "url:" { print $3 }
  ' "$1/deploy/dev/edge.yml"
}

# covered PATH RULE: a server route at PATH (and below it) is one the rule's matchers give the server
covered() {
  for _m in $(printf '%s\n' "$2" | grep -o 'Path\(Prefix\)\?(`[^`]*`)'); do
    _p=${_m#*\`}
    _p=${_p%\`*}
    case $_m in
      PathPrefix*) case "$1/" in "$_p"*) return 0 ;; esac ;;
      Path*) [ "$1" = "$_p" ] && return 0 ;;
    esac
  done
  return 1
}

# check ROOT: every rule of the split holds in the tree at ROOT, else names the first that does not
check() {
  prod=$(label "$1" 'http.routers.cadence-v2.rule')
  host=${prod%% && *}
  case $host in
    'Host(`${CADENCE_V2_PUBLIC_HOST:?'*) ;;
    *) echo "the server's prod rule does not start with its host: $prod"; return 1 ;;
  esac
  paths=${prod#"$host && ("}
  paths=${paths%)}
  [ "$host && ($paths)" = "$prod" ] || { echo "the server's prod rule is not Host(...) && (paths): $prod"; return 1; }
  dev=$(edge "$1" server rule)
  [ "$paths" = "$dev" ] || { echo "the server's paths differ: prod ($paths), dev edge ($dev)"; return 1; }
  # the quickstart's caddy: PathPrefix(`/x/`) is its /x/*, Path(`/x`) its /x
  want=$(printf '%s\n' "$paths" | sed 's/ || / /g; s/PathPrefix(`\([^`]*\)`)/\1*/g; s/Path(`\([^`]*\)`)/\1/g')
  caddyfile="$1/deploy/quickstart/Caddyfile"
  qs=$(sed -n 's/^[[:space:]]*@server path //p' "$caddyfile")
  [ "$want" = "$qs" ] || { echo "the server's paths differ: prod ($paths), quickstart caddy ($qs)"; return 1; }
  grep -q '^[[:space:]]*handle @server {$' "$caddyfile" && grep -q '^[[:space:]]*reverse_proxy server:8080\( {\)\{0,1\}$' "$caddyfile" &&
    grep -q '^[[:space:]]*handle {$' "$caddyfile" && grep -q '^[[:space:]]*reverse_proxy dashboard:3000$' "$caddyfile" ||
    { echo "the quickstart caddy does not reach the server's 8080 for its paths and the dashboard's 3000 for the rest"; return 1; }
  [ "$(label "$1" 'http.routers.cadence-dashboard.rule')" = "$host" ] ||
    { echo "the prod dashboard rule is not the server's host alone"; return 1; }
  [ "$(edge "$1" dashboard rule)" = 'PathPrefix(`/`)' ] || { echo "the dev dashboard rule is not every path"; return 1; }
  [ "$(label "$1" 'http.routers.cadence-v2.priority')" -gt "$(label "$1" 'http.routers.cadence-dashboard.priority')" ] ||
    { echo "the prod server router does not outrank the dashboard's"; return 1; }
  [ "$(edge "$1" server priority)" -gt "$(edge "$1" dashboard priority)" ] ||
    { echo "the dev server router does not outrank the dashboard's"; return 1; }

  # each router reaches its own service, on one entrypoint, and each service its own container
  for r in cadence-v2 cadence-dashboard; do
    [ "$(label "$1" "http.routers.$r.service")" = "$r@docker" ] || { echo "the prod router $r does not reach the service $r"; return 1; }
  done
  [ "$(label "$1" 'http.routers.cadence-v2.entrypoints')" = "$(label "$1" 'http.routers.cadence-dashboard.entrypoints')" ] ||
    { echo "the prod routers are on different entrypoints"; return 1; }
  [ "$(label "$1" 'http.services.cadence-v2.loadbalancer.server.port')" = 8080 ] &&
    [ "$(label "$1" 'http.services.cadence-dashboard.loadbalancer.server.port')" = 3000 ] ||
    { echo "the prod services do not name the server's 8080 and the dashboard's 3000"; return 1; }
  for r in server dashboard; do
    [ "$(edge "$1" "$r" service)" = "$r" ] || { echo "the dev router $r does not reach the service $r"; return 1; }
  done
  [ "$(edge "$1" server entryPoints)" = "$(edge "$1" dashboard entryPoints)" ] ||
    { echo "the dev routers are on different entrypoints"; return 1; }
  [ "$(service_url "$1" server)" = http://server:8080 ] && [ "$(service_url "$1" dashboard)" = http://dashboard:3000 ] ||
    { echo "the dev services do not name the server's 8080 and the dashboard's 3000"; return 1; }

  # the server's own top-level routes: its mounts on the fiber app (every other package mounts on one of its groups),
  # the admin api's prefix among them; the app it returns could take more in the packages that wire it
  src=$(for f in "$1"/server/internal/api/*.go "$1"/server/internal/app/*.go "$1"/server/cmd/cadence-server/*.go; do
    case $f in *_test.go) ;; *) cat "$f" ;; esac
  done)
  admin=$(printf '%s\n' "$src" | sed -n 's/^[[:space:]]*adminPrefix[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p')
  [ -n "$admin" ] || { echo "the admin api's prefix not found"; return 1; }
  routes=$(printf '%s\n' "$src" | grep -v '^[[:space:]]*//' |
    grep -o 'app\.\(Get\|Post\|Put\|Patch\|Delete\|Add\|All\|Group\|Use\)("/[^"]*"' | sed 's/.*("\([^"]*\)"/\1/' || true)
  [ -n "$routes" ] || { echo "no top-level route of the server found"; return 1; }
  for r in $admin $routes; do
    covered "$r" "$paths" || { echo "the server's route $r is not among the server's paths ($paths)"; return 1; }
  done

  node=$(sed -n 's/^NODE_IMAGE := //p' "$1/Makefile")
  [ -n "$node" ] || { echo "the Makefile names no NODE_IMAGE"; return 1; }
  froms=$(sed -n 's/^FROM \([^ ]*\).*/\1/p' "$1/deploy/dashboard/Dockerfile" | grep -v '^[a-z-]*$' || true)
  [ -n "$froms" ] || { echo "the dashboard's Dockerfile names no image"; return 1; }
  for f in $froms; do
    [ "$f" = "$node" ] || { echo "the dashboard's Dockerfile builds on $f, not the Makefile's NODE_IMAGE"; return 1; }
  done

  # the e2e's browser: its install made on the same node image, the playwright image pinned by digest at the version
  # of @playwright/test the dashboard's package names (its browsers are that version's)
  [ "$(sed -n 's/^FROM \(node:[^ ]*\) AS deps$/\1/p' "$1/deploy/dev/playwright.Dockerfile")" = "$node" ] ||
    { echo "the playwright image's install is not made on the Makefile's NODE_IMAGE"; return 1; }
  pw=$(sed -n 's|^FROM mcr\.microsoft\.com/playwright:v\([0-9.]*\)-[a-z]*@sha256:[0-9a-f]\{64\}$|\1|p' "$1/deploy/dev/playwright.Dockerfile")
  [ -n "$pw" ] || { echo "the playwright image is not the official one pinned by digest"; return 1; }
  want=$(sed -n 's/^ *"@playwright\/test": "\([^"]*\)",\{0,1\}$/\1/p' "$1/dashboard/package.json")
  [ "$pw" = "$want" ] || { echo "the playwright image is v$pw, the dashboard's @playwright/test ${want:-none}"; return 1; }
}

out=$(check "$root") || fail "$out"

# drift ROOT FILE SED WANT: a copy of ROOT with FILE edited by SED, which check must refuse naming WANT
drift() {
  rm -rf "$tmp/tree"
  mkdir -p "$tmp/tree/deploy/server" "$tmp/tree/deploy/dev" "$tmp/tree/deploy/dashboard" "$tmp/tree/server/internal/api" \
    "$tmp/tree/server/internal/app" "$tmp/tree/server/cmd/cadence-server" "$tmp/tree/dashboard" "$tmp/tree/deploy/quickstart"
  cp "$1/deploy/server/compose.yml" "$tmp/tree/deploy/server/"
  cp "$1/deploy/quickstart/Caddyfile" "$tmp/tree/deploy/quickstart/"
  cp "$1/deploy/dev/edge.yml" "$1/deploy/dev/playwright.Dockerfile" "$tmp/tree/deploy/dev/"
  cp "$1/deploy/dashboard/Dockerfile" "$tmp/tree/deploy/dashboard/"
  cp "$1/dashboard/package.json" "$tmp/tree/dashboard/"
  cp "$1/Makefile" "$tmp/tree/"
  cp "$1"/server/internal/api/*.go "$tmp/tree/server/internal/api/"
  cp "$1"/server/internal/app/*.go "$tmp/tree/server/internal/app/"
  cp "$1"/server/cmd/cadence-server/*.go "$tmp/tree/server/cmd/cadence-server/"
  sed -i "$3" "$tmp/tree/$2"
  _out=$(check "$tmp/tree") && fail "a drift in $2 ($3) passed"
  case $_out in
    *"$4"*) ;;
    *) fail "a drift in $2 ($3) was refused for another reason: $_out" ;;
  esac
}

drift "$root" deploy/dev/edge.yml 's|PathPrefix(`/v2/`)|PathPrefix(`/v2`)|' "the server's paths differ"
drift "$root" deploy/server/compose.yml 's|Path(`/healthz`)|Path(`/health`)|' "the server's paths differ"
drift "$root" deploy/quickstart/Caddyfile 's|/v2/\*|/v2|' "the server's paths differ: prod"
drift "$root" deploy/quickstart/Caddyfile 's|reverse_proxy dashboard:3000|reverse_proxy server:8080|' "the quickstart caddy does not reach"
drift "$root" deploy/dev/edge.yml '/^    server:/,/^    dashboard:/s|priority: 100|priority: 1|' "dev server router does not outrank"
drift "$root" deploy/server/compose.yml 's|cadence-dashboard.priority=1|cadence-dashboard.priority=200|' "prod server router does not outrank"
drift "$root" deploy/server/compose.yml 's|cadence-dashboard.rule=Host(`|cadence-dashboard.rule=Host(`dash.|' "prod dashboard rule"
drift "$root" server/internal/api/app.go 's|app.Get("/healthz"|app.Get("/e/healthz"|' "route /e/healthz"
drift "$root" server/internal/app/app.go '$a\	app.Get("/e/later", nil)' "route /e/later"
drift "$root" deploy/server/compose.yml 's|cadence-dashboard.service=cadence-dashboard@docker|cadence-dashboard.service=cadence-v2@docker|' "prod router cadence-dashboard does not reach"
drift "$root" deploy/server/compose.yml 's|cadence-dashboard.entrypoints=web-secure|cadence-dashboard.entrypoints=web|' "prod routers are on different entrypoints"
drift "$root" deploy/server/compose.yml 's|cadence-dashboard.loadbalancer.server.port=3000|cadence-dashboard.loadbalancer.server.port=8080|' "prod services do not name"
drift "$root" deploy/dev/edge.yml '/^    server:/,/^    dashboard:/s|service: server|service: dashboard|' "dev router server does not reach"
drift "$root" deploy/dev/edge.yml 's|url: http://dashboard:3000|url: http://server:8080|' "dev services do not name"
drift "$root" deploy/dashboard/Dockerfile '0,/^FROM node:[^ ]*/s|^FROM node:[^ ]*|FROM node:24-trixie-slim|' "not the Makefile's NODE_IMAGE"
drift "$root" deploy/dev/playwright.Dockerfile 's|^FROM node:[^ ]* AS deps|FROM node:24 AS deps|' "install is not made on the Makefile's NODE_IMAGE"
drift "$root" dashboard/package.json 's|"@playwright/test": "[^"]*"|"@playwright/test": "1.0.0"|' "the dashboard's @playwright/test 1.0.0"
drift "$root" deploy/dev/playwright.Dockerfile 's|-noble@sha256:[0-9a-f]*|-noble|' "not the official one pinned by digest"

echo ok
