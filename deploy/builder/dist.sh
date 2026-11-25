#!/usr/bin/env bash
# make dist DISTRO=<name>: builds a distro's artifact as its manifest (deploy/distros/<name>.json) says, by its
# package format:
#   tarball  a speaker's tarball for the installer (make device-build is this): the components' runtimes (airplay's
#            and soloist's, arm64 on every board: midi's 64-bit kernel runs them) and the board's ffmpeg (pickle's) or
#            zig cross deps (midi's, the vk capsula mini's), each ensured from the prebuilt registry
#            (deploy/builder/prebuilt.sh, built as before on a miss), then the builder's export stage for
#            the board's platform with BOARD, ARCH and VERSION, and those parts as the named contexts soloist and
#            airplay (when the manifest ships them) and ffmpeg-pickle, midi-deps or vk-deps, as the board names them
#            and the builder runs the board's checks (check-device-limits.sh, check-device-tree.sh)
#   image    a docker image of the builder, tagged as the package's output
#   deb, apk a screen's package: its parts first (libairplay for an uxplay receiver, mpv with ao_cadence for a
#            screen), then the builder's export stage with DISTRO, ARCH and VERSION, and build/libairplay and
#            build/mpv as the named contexts libairplay and mpv (an apk also its debug key as the secret
#            android_keystore: CADENCE_ANDROID_KEYSTORE, else ~/.config/cadence/android-debug.keystore, made once by
#            deploy/android/keystore.sh). a planned distro whose builder is not in the repo yet
#            stops before building anything, naming it
# the artifact must then be at the package's output, {version} and {arch} substituted. --dry-run prints each command
# instead of running it. DIST_VERSION is the version (git describe, as make device-build's), DIST_ARCH one of the
# manifest's architectures (its first by default); JQ a jq to read the manifest with (else jq 1.8.2's pinned release
# build from build/tools, else the PATH's); CADENCE_TRACING ON or OFF the builders' switch of audiod's trace marks
# (unset: their default, on)
# usage: dist.sh [--dry-run] <distro>
set -euo pipefail

die() { echo "dist: $1" >&2; exit "${2:-1}"; }
dry=
if [ "${1:-}" = --dry-run ]; then
  dry=1
  shift
fi
[ $# -eq 1 ] || die "usage: dist.sh [--dry-run] <distro>" 2
distro=$1
here=$(cd "$(dirname "$0")" && pwd)
# DIST_ROOT: another tree to read the manifest and run in (dist.test.sh's)
root=${DIST_ROOT:-$(cd "$here/../.." && pwd)}
cd "$root"
manifest=deploy/distros/$distro.json
case $distro in */* | schema) die "no distro $distro" 2 ;; esac
[ -f "$manifest" ] || die "no manifest $manifest" 2

jq=${JQ:-}
[ -n "$jq" ] || jq=$(sh "$here/../dev/jq-native.sh" "$here/../../build/tools" 2>/dev/null) || jq=
[ -n "$jq" ] || jq=$(command -v jq) || die "no jq: set JQ, or let deploy/dev/jq-native.sh fetch its pinned build"
field() { "$jq" -r "$1" "$manifest"; }
has() { [ "$("$jq" -r --arg c "$1" 'any(.components[]; . == $c)' "$manifest")" = true ]; }

version=${DIST_VERSION:-$(git describe --always --dirty --abbrev=12 --exclude '*')}
# audiod's trace marks: the builders' default (on) unless CADENCE_TRACING says ON or OFF
tracing=()
case ${CADENCE_TRACING:-} in
  '') ;;
  ON | OFF) tracing=(--build-arg "CADENCE_TRACING=$CADENCE_TRACING") ;;
  *) die "CADENCE_TRACING=$CADENCE_TRACING: ON or OFF" 2 ;;
esac
arch=${DIST_ARCH:-$(field '.arch[0]')}
[ "$("$jq" -r --arg a "$arch" 'any(.arch[]; . == $a)' "$manifest")" = true ] || die "$distro does not build for $arch" 2
format=$(field .package.format)
output=$(field .package.output)
output=${output//\{version\}/$version}
output=${output//\{arch\}/$arch}
dockerfile=$(field .builder.dockerfile)
platform=$(field '.builder.platform // ""')
board=$(field .board)
status=$(field .status)

run() {
  if [ -n "$dry" ]; then
    printf '%s\n' "$*"
  else
    echo "+ $*" >&2
    "$@"
  fi
}

# a planned distro may name a builder another stream writes: refuse before building anything
if [ ! -f "$dockerfile" ]; then
  [ "$status" = planned ] || die "$distro's builder $dockerfile is missing"
  die "$distro is planned: its builder $dockerfile is not in the repo yet" 3
fi

# the docker platform a debian architecture's binaries run on
platform_of() {
  case $1 in
    amd64) echo linux/amd64 ;;
    arm64 | arm64-v8a) echo linux/arm64 ;;
    armhf) echo linux/arm/v7 ;;
    *) die "no platform for $1" ;;
  esac
}

# DIST_BUILDER names a buildx builder for the amd64 builds (the apk's, libairplay's: their ndk and sdk tools are
# x86_64 alone, which an arm64 mac emulates), e.g. the cluster's senku-buildkit (amd64 only); DIST_ARM_BUILDER one
# for the arm builds (the speakers' tarballs, the rpi deb), e.g. a docker-container builder on an arm64 mac, where
# they run natively. DIST_CACHE is a registry repository their layers are cached in (<repository>:<build>), so any
# builder, and a restarted one, starts warm; it needs a builder that exports caches (not the plain docker driver)
remote=()
remote_for() { # <cache name> <platform>
  remote=()
  local builder=${DIST_ARM_BUILDER:-}
  [ "$2" = linux/amd64 ] && builder=${DIST_BUILDER:-}
  [ -n "$builder" ] || return 0
  remote=(--builder "$builder")
  if [ -n "${DIST_CACHE:-}" ]; then
    remote+=(--cache-from "type=registry,ref=$DIST_CACHE:$1" --cache-to "type=registry,mode=max,ignore-error=true,ref=$DIST_CACHE:$1")
  fi
}

# the runtimes as the builder's named contexts read them: fetched from the prebuilt registry by their recipe hash,
# else built by exactly the command that ran before the cache (deploy/builder/prebuilt.sh)
speaker_runtimes() {
  if has airplay-runtime; then
    run bash deploy/builder/prebuilt.sh ensure airplay-runtime-arm64
  fi
  if has soloist-runtime; then
    # the stage cross-compiles soloist's shim from amd64 (its compilers are amd64's), emulated on an arm64 machine
    run bash deploy/builder/prebuilt.sh ensure soloist-runtime-arm64
  fi
}

screen_parts() {
  if [ "$(field .media.airplay)" = uxplay ]; then
    # its android stage is amd64 whatever builds it; the linux stage builds both cpus on either
    remote_for libairplay linux/amd64
    run docker buildx build --progress=plain ${remote[@]+"${remote[@]}"} -f deploy/builder/libairplay.Dockerfile --target export \
      --output type=local,dest=build/libairplay .
  fi
  if has cadence-screen; then
    case $(field .os)-$arch in
      linux-amd64 | linux-arm64) run sh deploy/builder/mpv.sh deb-0.40 "$arch" ;;
      android-armeabi-v7a) run sh deploy/builder/mpv.sh android-armeabi-v7a ;;
      *) die "no mpv build for $(field .os) $arch" ;;
    esac
  fi
}

case $format in
  tarball)
    [ -n "$platform" ] || die "$distro's builder names no platform"
    [ "$platform" = "$(platform_of "$arch")" ] || die "$distro's builder platform $platform is not $arch's"
    speaker_runtimes
    # the board's ffmpeg (pickle) or zig cross deps (midi, vk), fetched or built once, as the builder's named context
    case $board in
      yandex-pickle)
        run bash deploy/builder/prebuilt.sh ensure ffmpeg-pickle
        board_context="--build-context ffmpeg-pickle=build/prebuilt/ffmpeg-pickle"
        ;;
      yandex-midi | yandex-micro)
        run bash deploy/builder/prebuilt.sh ensure midi-deps
        board_context="--build-context midi-deps=build/prebuilt/midi-deps"
        ;;
      vk-capsula-mini)
        run bash deploy/builder/prebuilt.sh ensure vk-deps
        board_context="--build-context vk-deps=build/prebuilt/vk-deps"
        ;;
      *) die "no prebuilt ffmpeg context for board $board" ;;
    esac
    # the runtimes' contexts for the boards that ship them
    runtime_context=
    if has soloist-runtime; then runtime_context="--build-context soloist=build/soloist"; fi
    if has airplay-runtime; then runtime_context="$runtime_context --build-context airplay=build/airplay"; fi
    # the owner's spoken prompts (cadence-server admin prompts render --out build/prompts), when rendered
    prompts=${DIST_PROMPTS:-build/prompts}
    if [ -d "$prompts" ]; then runtime_context="$runtime_context --build-context prompts=$prompts"; fi
    # shellcheck disable=SC2086 # each context is two words
    remote_for "$distro" "$platform"
    run docker buildx build --progress=plain ${remote[@]+"${remote[@]}"} --platform "$platform" -f "$dockerfile" --target export \
      --build-arg "BOARD=$board" --build-arg "ARCH=$arch" --build-arg "VERSION=$version" \
      ${tracing[@]+"${tracing[@]}"} $runtime_context $board_context \
      --output "type=local,dest=$(dirname "$output")" .
    ;;
  image)
    run docker build -f "$dockerfile" ${tracing[@]+"${tracing[@]}"} -t "$output" .
    ;;
  deb | apk)
    screen_parts
    secret=()
    soloist=
    if [ "$format" = apk ]; then
      # the apk's debug key lives outside the repo; made once (deploy/android/keystore.sh), then kept
      keystore=${CADENCE_ANDROID_KEYSTORE:-$HOME/.config/cadence/android-debug.keystore}
      run sh deploy/android/keystore.sh "$keystore"
      secret=(--secret "id=android_keystore,src=$keystore")
      if has soloist-runtime; then
        # soloist's arm64 runtime, as the speakers' tarballs ship it (the kernel is 64-bit beside the armv7 app):
        # the apk carries it as an asset, and the app unpacks it into files/cadence/soloist/runtime
        run bash deploy/builder/prebuilt.sh ensure soloist-runtime-arm64
        soloist="--build-context soloist=build/soloist"
      fi
    fi
    # android's builders run the ndk's and the sdk's x86_64 tools
    if [ -z "$platform" ]; then
      if [ "$(field .os)" = android ]; then platform=linux/amd64; else platform=$(platform_of "$arch"); fi
    fi
    remote_for "$distro" "$platform"
    run docker buildx build --progress=plain ${remote[@]+"${remote[@]}"} --platform "$platform" -f "$dockerfile" \
      --target export --build-arg "DISTRO=$distro" --build-arg "ARCH=$arch" --build-arg "VERSION=$version" \
      ${tracing[@]+"${tracing[@]}"} --build-context libairplay=build/libairplay --build-context mpv=build/mpv ${soloist} \
      ${secret[@]+"${secret[@]}"} --output "type=local,dest=$(dirname "$output")" .
    ;;
  *) die "unknown package format $format" ;;
esac

if [ -z "$dry" ] && [ "$format" != image ]; then
  [ -f "$output" ] || die "the build did not write $output"
  echo "dist $distro: $output"
fi
