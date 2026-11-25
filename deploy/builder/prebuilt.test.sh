#!/usr/bin/env bash
# prebuilt.sh's catalogue and its three moves, with no network: a key is stable, moves with every input file and
# argument, and no two artifacts share one; a fetch takes only what its sha256 vouches for (a miss is its own exit
# code, a wrong payload fails and leaves nothing behind); a publish keeps the token out of argv and output (a stub
# curl records every argument it crossed, a file:// registry every file). the recipe files are copied into a
# throwaway tree (PREBUILT_ROOT), so the repo itself is never touched
set -euo pipefail
export LC_ALL=C

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../.." && pwd)
script=$here/prebuilt.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
sha() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1
}

# the catalogue is exactly this, and list prints it with a key and a presence column (an empty registry says no to
# everything without leaving the filesystem)
known='ffmpeg-pickle
midi-deps
vk-deps
ffmpeg-airplay-amd64
ffmpeg-airplay-arm64
ffmpeg-airplay-armhf
ffmpeg-mediahost-amd64
ffmpeg-mediahost-arm64
ffmpeg-server-amd64
ffmpeg-server-arm64
airplay-runtime-amd64
airplay-runtime-arm64
airplay-runtime-armhf
soloist-runtime-arm64
soloist-runtime-amd64
mpv-deb-0.40-arm64
mpv-deb-0.40-amd64
mpv-deb-0.41-arm64
mpv-deb-0.41-amd64
mpv-android-armeabi-v7a'
mkdir "$tmp/empty-reg"
[ "$(PREBUILT_REGISTRY="file://$tmp/empty-reg" bash "$script" list | tail -n +2 | awk '{print $1, $2}')" = \
  "$(printf '%s\n' "$known" | while read -r name; do echo "$name no"; done)" ] ||
  fail "list against an empty registry: $(PREBUILT_REGISTRY="file://$tmp/empty-reg" bash "$script" list)"

# names outside the catalogue are refused, on every command
for command in key inputs build publish ensure; do
  PREBUILT_ROOT=$root bash "$script" "$command" no-such-artifact >"$tmp/out" 2>"$tmp/err" && \
    fail "$command takes an unknown artifact"
  [ -s "$tmp/err" ] || fail "$command prints why it refuses"
done

# a key is stable, from any cwd, and no two artifacts share one
key=$(bash "$script" key ffmpeg-pickle)
[ "$key" = "$(bash "$script" key ffmpeg-pickle)" ] || fail "the key is not stable"
[ "$key" = "$(cd / && bash "$script" key ffmpeg-pickle)" ] || fail "the key depends on the cwd"
[ "$(bash "$script" key ffmpeg-pickle)" != "$(bash "$script" key midi-deps)" ] || fail "two artifacts share a key"
[ "$(bash "$script" key midi-deps)" != "$(bash "$script" key vk-deps)" ] || fail "the two zig cross deps share a key"
[ "$(bash "$script" key mpv-deb-0.40-arm64)" != "$(bash "$script" key mpv-deb-0.41-arm64)" ] ||
  fail "two mpv targets share a key"
[ "$(bash "$script" key mpv-deb-0.40-arm64)" != "$(bash "$script" key mpv-deb-0.40-amd64)" ] ||
  fail "two mpv arches share a key"
[ "$(bash "$script" key ffmpeg-server-amd64)" != "$(bash "$script" key ffmpeg-server-arm64)" ] ||
  fail "two server ffmpeg arches share a key"

# the same inputs in another tree give the same key; a changed file, a new file in a directory input, moves it
fake=$tmp/root
mkdir -p "$fake/deploy/builder"
cp "$script" "$fake/deploy/builder/"
copy_inputs() { # <artifact>: its input files and directories, copied where the recipe reads them
  local a=$1 entry
  while read -r entry; do
    mkdir -p "$fake/$(dirname "$entry")"
    rm -rf "$fake/$entry" # cp -R into an existing directory would nest it
    cp -R "$root/$entry" "$fake/$entry"
  done < <(PREBUILT_ROOT=$fake bash "$fake/deploy/builder/prebuilt.sh" inputs "$a")
}
for a in ffmpeg-pickle midi-deps vk-deps ffmpeg-airplay-armhf ffmpeg-mediahost-amd64 ffmpeg-server-amd64 \
  airplay-runtime-arm64 soloist-runtime-arm64 soloist-runtime-amd64 mpv-deb-0.40-arm64; do
  copy_inputs "$a"
  [ "$(bash "$script" key "$a")" = "$(PREBUILT_ROOT=$fake bash "$fake/deploy/builder/prebuilt.sh" key "$a")" ] ||
    fail "$a: the key moved without any input changing"
done
printf '\n# a change\n' >>"$fake/deploy/builder/build-ffmpeg.sh"
[ "$(bash "$script" key ffmpeg-pickle)" != "$(PREBUILT_ROOT=$fake bash "$fake/deploy/builder/prebuilt.sh" \
  key ffmpeg-pickle)" ] || fail "a changed input file does not move the key"
touch "$fake/media/pulse-shim/unreviewed.cpp"
[ "$(bash "$script" key soloist-runtime-arm64)" != "$(PREBUILT_ROOT=$fake \
  bash "$fake/deploy/builder/prebuilt.sh" key soloist-runtime-arm64)" ] ||
  fail "a new file in a directory input does not move the key"
# the server's ffmpeg stage lives in the server's dockerfile, so that file is part of its recipe
copy_inputs ffmpeg-server-arm64
printf '\n# a change\n' >>"$fake/server/Dockerfile"
[ "$(bash "$script" key ffmpeg-server-arm64)" != "$(PREBUILT_ROOT=$fake \
  bash "$fake/deploy/builder/prebuilt.sh" key ffmpeg-server-arm64)" ] ||
  fail "a change to the server's dockerfile does not move the server ffmpeg's key"

# fetch: a good artifact lands with its sha256 beside it, a miss is exit 3, a payload its sha256 does not vouch for
# fails and leaves nothing
reg=$tmp/reg
name=ffmpeg-pickle
key=$(bash "$script" key "$name")
mkdir -p "$reg/$name/$key" "$tmp/outdir"
printf 'the ffmpeg prefix, as a tar\n' >"$reg/$name/$key/ffmpeg.tar"
printf '%s  ffmpeg.tar\n' "$(sha "$reg/$name/$key/ffmpeg.tar")" >"$reg/$name/$key/ffmpeg.tar.sha256"
rc=0
PREBUILT_REGISTRY="file://$reg" bash "$script" fetch "$name" "$tmp/outdir/ffmpeg.tar" 2>"$tmp/log" || rc=$?
[ "$rc" -eq 0 ] || fail "a fetch of a good artifact failed: $(cat "$tmp/log")"
cmp -s "$reg/$name/$key/ffmpeg.tar" "$tmp/outdir/ffmpeg.tar" || fail "the fetched file is not what the registry had"
[ -f "$tmp/outdir/ffmpeg.tar.sha256" ] || fail "the sha256 does not land beside it"
rc=0
PREBUILT_REGISTRY="file://$reg" bash "$script" fetch midi-deps "$tmp/nowhere.tar" 2>/dev/null || rc=$?
[ "$rc" -eq 3 ] || fail "a miss is exit 3, got $rc"
[ ! -e "$tmp/nowhere.tar" ] && [ ! -e "$tmp/nowhere.tar.sha256" ] || fail "a miss leaves files behind"
printf tampered >>"$reg/$name/$key/ffmpeg.tar"
rm -f "$tmp/outdir/ffmpeg.tar" "$tmp/outdir/ffmpeg.tar.sha256"
rc=0
PREBUILT_REGISTRY="file://$reg" bash "$script" fetch "$name" "$tmp/outdir/ffmpeg.tar" 2>"$tmp/log" || rc=$?
[ "$rc" -eq 1 ] || fail "a payload its sha256 does not vouch for is exit 1, got $rc"
[ ! -e "$tmp/outdir/ffmpeg.tar" ] && [ ! -e "$tmp/outdir/ffmpeg.tar.part" ] && \
  [ ! -e "$tmp/outdir/ffmpeg.tar.sha256" ] || fail "a refused fetch leaves files behind"

# publish: the token crosses in a header file, never argv, never the output. a stub curl records every argument it
# crossed and, while the script still lives, what the header file it was handed held (the script removes the file
# after each call, as it should)
bin=$tmp/bin
mkdir "$bin"
cat >"$bin/curl" <<'STUB'
#!/bin/sh
printf '%s\n' "$@" >>"$CURL_LOG"
for argument in "$@"; do
  case $argument in @*) cat "${argument#@}" >>"$CURL_HEADERS" ;; esac
done
exit 0
STUB
chmod 755 "$bin/curl"
mkdir -p "$fake/build/prebuilt/ffmpeg-pickle"
printf 'payload\n' >"$fake/build/prebuilt/ffmpeg-pickle/ffmpeg.tar"
token=$tmp/token
printf 'glpat-SENTINEL-not-a-real-token' >"$token"
chmod 600 "$token"
log=$tmp/curl.log
headers=$tmp/curl.headers
: >"$log"
: >"$headers"
env PREBUILT_ROOT=$fake PREBUILT_REGISTRY="file://$reg" GITLAB_TOKEN_FILE="$token" CURL_LOG="$log" \
  CURL_HEADERS="$headers" PATH="$bin:$PATH" bash "$fake/deploy/builder/prebuilt.sh" publish ffmpeg-pickle \
  >"$tmp/pub.out" 2>"$tmp/pub.err" || fail "publish failed: $(cat "$tmp/pub.err")"
for crossed in "$tmp/pub.out" "$tmp/pub.err" "$log"; do
  ! grep -q SENTINEL "$crossed" || fail "the token crossed $(basename "$crossed")"
done
grep -qx 'PRIVATE-TOKEN: glpat-SENTINEL-not-a-real-token' "$headers" || fail "the header file carries no token"
grep -q -- '--header' "$log" || fail "curl was handed no header"
grep -q -- '--upload-file' "$log" || fail "the artifact is not uploaded"
# the ci flavour: the job token, with no token file at all
: >"$log"
: >"$headers"
env PREBUILT_ROOT=$fake PREBUILT_REGISTRY="file://$reg" CI_JOB_TOKEN=job-sentinel-7 CURL_LOG="$log" \
  CURL_HEADERS="$headers" PATH="$bin:$PATH" \
  bash "$fake/deploy/builder/prebuilt.sh" publish ffmpeg-pickle >/dev/null 2>&1 ||
  fail "publish with CI_JOB_TOKEN failed"
! grep -q job-sentinel-7 "$log" || fail "the job token crossed argv"
grep -qx 'JOB-TOKEN: job-sentinel-7' "$headers" || fail "the job token does not reach the header file"
# no credential at all is refused before any upload
: >"$log"
rc=0
env -u CI_JOB_TOKEN -u GITLAB_TOKEN_FILE PREBUILT_ROOT=$fake CURL_LOG="$log" PATH="$bin:$PATH" \
  bash "$fake/deploy/builder/prebuilt.sh" publish ffmpeg-pickle >"$tmp/out" 2>"$tmp/err" || rc=$?
[ "$rc" -ne 0 ] || fail "publish without any credential succeeded"
grep -q 'CI_JOB_TOKEN or GITLAB_TOKEN_FILE' "$tmp/err" || fail "the refusal names what it needs"
[ ! -s "$log" ] || fail "an upload was attempted without a credential"

# ensure takes the fetch when the registry has the recipe (real curl: the stub would fake the download), and a
# repeat of it costs nothing at all. the fake tree is pristine again first: its build-ffmpeg.sh was changed above, and
# the registry holds the artifact under the pristine recipe's key
cp "$root/deploy/builder/build-ffmpeg.sh" "$fake/deploy/builder/build-ffmpeg.sh"
printf 'the ffmpeg prefix, as a tar\n' >"$reg/$name/$key/ffmpeg.tar"
printf '%s  ffmpeg.tar\n' "$(sha "$reg/$name/$key/ffmpeg.tar")" >"$reg/$name/$key/ffmpeg.tar.sha256"
rm -f "$fake/build/prebuilt/ffmpeg-pickle/ffmpeg.tar.key"
env PREBUILT_ROOT=$fake PREBUILT_REGISTRY="file://$reg" \
  bash "$fake/deploy/builder/prebuilt.sh" ensure ffmpeg-pickle >"$tmp/out" 2>"$tmp/err" ||
  fail "ensure of a cached artifact failed: $(cat "$tmp/err")"
grep -q 'fetched to' "$tmp/err" || fail "ensure fetched nothing: $(cat "$tmp/err")"
cmp -s "$reg/$name/$key/ffmpeg.tar" "$fake/build/prebuilt/ffmpeg-pickle/ffmpeg.tar" ||
  fail "ensure put the registry's file at the artifact's own path"
# the repeat: the marker alone answers, the registry is not even asked (the stub curl would record any call)
: >"$log"
env PREBUILT_ROOT=$fake PREBUILT_REGISTRY="file://$reg" CURL_LOG="$log" PATH="$bin:$PATH" \
  bash "$fake/deploy/builder/prebuilt.sh" ensure ffmpeg-pickle >"$tmp/out" 2>"$tmp/err" ||
  fail "a repeat ensure failed: $(cat "$tmp/err")"
grep -q 'already at' "$tmp/err" || fail "a repeat ensure does not recognize its own work: $(cat "$tmp/err")"
[ ! -s "$log" ] || fail "a repeat ensure contacted the registry"

echo ok
