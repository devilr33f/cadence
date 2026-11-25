#!/usr/bin/env bash
# dist.sh's dispatch, dry: the speakers' tarballs take their pinned third-party parts from the prebuilt cache
# (prebuilt.sh ensure; its soloist build on linux/amd64, which an amd64 machine ran natively and an arm64 one could
# not), an image is built and tagged as its
# output, a screen's package takes its parts and then its builder with their contexts, and what dist refuses
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
dist=$here/dist.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
JQ=${JQ:-$(sh "$root/deploy/dev/jq-native.sh" "$root/build/tools" 2>/dev/null || command -v jq || true)}
[ -n "$JQ" ] || fail "no jq: deploy/dev/jq-native.sh could not fetch its build and the PATH has none"
export JQ

expect() { # what, distro, the commands, then env
  local what=$1 distro=$2 want=$3 got
  shift 3
  got=$(env DIST_VERSION=v9 DIST_PROMPTS="${PROMPTS:-$tmp/no-prompts}" "$@" bash "$dist" --dry-run "$distro") || fail "$what: dist.sh failed"
  [ "$got" = "$want" ] || fail "$what: got
$got
want
$want"
}

runtimes='bash deploy/builder/prebuilt.sh ensure airplay-runtime-arm64
bash deploy/builder/prebuilt.sh ensure soloist-runtime-arm64'
expect "pickle" yandex-pickle "$runtimes
bash deploy/builder/prebuilt.sh ensure ffmpeg-pickle
docker buildx build --progress=plain --platform linux/arm64 -f deploy/builder/device.Dockerfile --target export --build-arg BOARD=yandex-pickle --build-arg ARCH=arm64 --build-arg VERSION=v9 --build-context soloist=build/soloist --build-context airplay=build/airplay --build-context ffmpeg-pickle=build/prebuilt/ffmpeg-pickle --output type=local,dest=build/device ."
expect "midi" yandex-midi "$runtimes
bash deploy/builder/prebuilt.sh ensure midi-deps
docker buildx build --progress=plain --platform linux/arm/v7 -f deploy/builder/device.Dockerfile --target export --build-arg BOARD=yandex-midi --build-arg ARCH=armhf --build-arg VERSION=v9 --build-context soloist=build/soloist --build-context airplay=build/airplay --build-context midi-deps=build/prebuilt/midi-deps --output type=local,dest=build/device ."
expect "vk capsula mini" vk-capsula-mini "bash deploy/builder/prebuilt.sh ensure vk-deps
docker buildx build --progress=plain --platform linux/arm64 -f deploy/builder/device.Dockerfile --target export --build-arg BOARD=vk-capsula-mini --build-arg ARCH=arm64 --build-arg VERSION=v9 --build-context vk-deps=build/prebuilt/vk-deps --output type=local,dest=build/device ."
expect "sim" sim "docker build -f device/Dockerfile.sim -t cadenced-sim:v9 ."
# audiod's trace marks switched off: the builders take it as a build argument
expect "sim without trace marks" sim "docker build -f device/Dockerfile.sim --build-arg CADENCE_TRACING=OFF -t cadenced-sim:v9 ." CADENCE_TRACING=OFF

# screens, on manifests of a tree of their own whose builders exist
fake=$tmp/repo
mkdir -p "$fake/deploy/distros" "$fake/deploy/builder" "$fake/deploy/android"
touch "$fake/deploy/builder/rpi.Dockerfile" "$fake/deploy/android/Dockerfile"
cp "$root/deploy/distros/rpi.json" "$root/deploy/distros/android-tv.json" "$root/deploy/distros/yandex-pickle.json" \
  "$fake/deploy/distros/"
# keep the planned-distro refusal covered even though rpi itself now ships
"$JQ" '.status = "planned"' "$root/deploy/distros/rpi.json" >"$fake/deploy/distros/rpi.json"
fake_expect() { # what, distro, the commands, then env
  local what=$1 distro=$2 want=$3 got
  shift 3
  got=$(env DIST_ROOT="$fake" DIST_VERSION=v9 "$@" bash "$dist" --dry-run "$distro") || fail "$what: dist.sh failed"
  [ "$got" = "$want" ] || fail "$what: got
$got
want
$want"
}
libairplay='docker buildx build --progress=plain -f deploy/builder/libairplay.Dockerfile --target export --output type=local,dest=build/libairplay .'
fake_expect "rpi" rpi "$libairplay
sh deploy/builder/mpv.sh deb-0.40 arm64
docker buildx build --progress=plain --platform linux/arm64 -f deploy/builder/rpi.Dockerfile --target export --build-arg DISTRO=rpi --build-arg ARCH=arm64 --build-arg VERSION=v9 --build-context libairplay=build/libairplay --build-context mpv=build/mpv --output type=local,dest=build/dist ."
fake_expect "android-tv" android-tv "$libairplay
sh deploy/builder/mpv.sh android-armeabi-v7a
sh deploy/android/keystore.sh /keys/debug.keystore
bash deploy/builder/prebuilt.sh ensure soloist-runtime-arm64
docker buildx build --progress=plain --platform linux/amd64 -f deploy/android/Dockerfile --target export --build-arg DISTRO=android-tv --build-arg ARCH=armeabi-v7a --build-arg VERSION=v9 --build-context libairplay=build/libairplay --build-context mpv=build/mpv --build-context soloist=build/soloist --secret id=android_keystore,src=/keys/debug.keystore --output type=local,dest=build/dist ." CADENCE_ANDROID_KEYSTORE=/keys/debug.keystore

# media/CMakePresets.json: a configure preset per manifest, with the manifest's media options (sim's fake airplay is
# the shairport receiver with a fake shairport sync), and a soloist jail where the preset names one
presets=$root/media/CMakePresets.json
for m in "$root"/deploy/distros/*.json; do
  name=$(basename "$m" .json)
  [ "$name" != schema ] || continue
  got=$("$JQ" -r --arg n "$name" '.configurePresets[] | select(.name == $n) | .cacheVariables
    | [.CADENCE_MEDIA_AIRPLAY, .CADENCE_MEDIA_AUDIO_BACKEND, .CADENCE_MEDIA_URL, .CADENCE_SCREEN,
       (.CADENCE_MEDIA_SOLOIST_JAIL // "-")] | join(" ")' "$presets")
  want=$("$JQ" -r '.media | [(if .airplay == "fake" then "shairport" else .airplay end), .audioBackend,
    (if .url then "ON" else "OFF" end), (if .screen then "ON" else "OFF" end),
    (if .soloistJail then "ON" else "OFF" end)] | join(" ")' "$m")
  [ -n "$got" ] || fail "no preset $name in media/CMakePresets.json"
  [ "${got% *}" = "${want% *}" ] || fail "preset $name: $got, the manifest says $want"
  [ "${got##* }" = - ] || [ "${got##* }" = "${want##* }" ] || fail "preset $name's soloist jail: $got, the manifest says $want"
done
[ "$("$JQ" '[.configurePresets[] | select(.hidden != true)] | length' "$presets")" = \
  "$(find "$root/deploy/distros" -name '*.json' ! -name schema.json | wc -l | tr -d ' ')" ] \
  || fail "a preset without its manifest"
if command -v cmake >/dev/null 2>&1; then
  (cd "$root/media" && cmake --list-presets=configure >/dev/null) || fail "cmake refuses media/CMakePresets.json"
fi

refused() { # what, exit code, distro, then env
  local what=$1 code=$2 distro=$3 rc=0
  shift 3
  env DIST_VERSION=v9 "$@" bash "$dist" --dry-run "$distro" >"$tmp/out" 2>"$tmp/err" || rc=$?
  [ "$rc" = "$code" ] || fail "$what: exit $rc, want $code ($(cat "$tmp/err"))"
  [ ! -s "$tmp/out" ] || fail "$what: commands printed: $(cat "$tmp/out")"
}
refused "an unknown distro" 2 nowhere
refused "the schema" 2 schema
refused "a path" 2 ../distros/yandex-pickle
refused "an architecture the manifest lacks" 2 yandex-pickle DIST_ARCH=armhf
refused "a tracing switch that is neither ON nor OFF" 2 yandex-pickle CADENCE_TRACING=no
# a planned screen whose builder another stream writes: nothing is built
rm "$fake/deploy/builder/rpi.Dockerfile"
refused "a planned distro without its builder" 3 rpi DIST_ROOT="$fake"
grep -qxF 'dist: rpi is planned: its builder deploy/builder/rpi.Dockerfile is not in the repo yet' "$tmp/err" \
  || fail "the missing builder not named: $(cat "$tmp/err")"
# a shipping one must have it
rm -r "$fake/deploy/builder"
refused "a shipping distro without its builder" 1 yandex-pickle DIST_ROOT="$fake"

echo ok
