#!/bin/sh
# the dashboard's make targets, run in the pinned node image from the repo root, so neither needs node on the host:
#   dashboard.sh check   installs frozen, then lint, typecheck, unit tests, the build (whose route tree must equal the
#                        committed one) and the api client regenerated beside the committed one
#   dashboard.sh client  regenerates dashboard/src/api/gen from server/openapi/openapi.json
# both work on a copy of dashboard/ outside the tree: the host's node_modules (another os's binaries) never reaches the
# container, and nothing the container installs or builds lands in the tree, but the client written back
set -eu

mode=${1:-}
case $mode in
  check | client) ;;
  *)
    echo "usage: dashboard.sh check|client" >&2
    exit 2
    ;;
esac

repo=$(pwd)
gen="$repo/dashboard/src/api/gen"
work=$(mktemp -d)
trap 'rm -rf "$work" "$gen.new" "$gen.old"' EXIT

# the document at the path openapi-ts.config.ts reads it from (../server/openapi), the board readmes whose install
# lines the enroll wizard's hint test holds its own to, the calibration fixtures shared with c++, and the package
# without what installs and builds make
mkdir -p "$work/server/openapi" "$work/dashboard" "$work/deploy/pickle" "$work/deploy/midi" "$work/media/tests/fixtures"
cp server/openapi/openapi.json "$work/server/openapi/openapi.json"
cp deploy/pickle/README.md "$work/deploy/pickle/README.md"
cp deploy/midi/README.md "$work/deploy/midi/README.md"
mkdir -p "$work/deploy/vk-capsula-mini"
cp deploy/vk-capsula-mini/README.md "$work/deploy/vk-capsula-mini/README.md"
cp -R media/tests/fixtures/calibration "$work/media/tests/fixtures/calibration"
tar -C dashboard --exclude=./node_modules --exclude=./dist --exclude=./.tanstack -cf - . | tar -C "$work/dashboard" -xf -
cd "$work/dashboard"

# the node this gate runs (the pinned image's), .nvmrc and package.json's engines must name one version
node -e 'const v = process.versions.node, rc = require("fs").readFileSync(".nvmrc", "utf8").trim(), e = require("./package.json").engines?.node; if (v !== rc || v !== e) { console.error(`node versions differ: image ${v}, .nvmrc ${rc}, engines ${e}`); process.exit(1) }'

# corepack runs the pnpm that package.json's packageManager names, checked against its sha512
pnpm() { corepack pnpm "$@"; }

pnpm install --frozen-lockfile

if [ "$mode" = client ]; then
  pnpm client
  # beside the old client first, then swapped in, so a failed copy never leaves the tree without one
  rm -rf "$gen.new" "$gen.old"
  cp -R src/api/gen "$gen.new"
  [ ! -e "$gen" ] || mv "$gen" "$gen.old"
  mv "$gen.new" "$gen"
  exit 0
fi

pnpm lint
pnpm typecheck
pnpm test

cp src/routeTree.gen.ts "$work/routeTree.committed"
pnpm build
cmp -s src/routeTree.gen.ts "$work/routeTree.committed" ||
  { echo "dashboard/src/routeTree.gen.ts is stale: run pnpm build in dashboard"; exit 1; }

pnpm client
diff -r src/api/gen "$repo/dashboard/src/api/gen" >/dev/null ||
  { echo "dashboard/src/api/gen is stale: run make dashboard-client"; exit 1; }
echo "dashboard: ok"
