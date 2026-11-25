#!/bin/sh
# mpv with the ao_cadence patch for one target (owner decision 5), out to build/mpv/<target>[-<arch>]/: first the
# prebuilt registry (deploy/builder/prebuilt.sh, which keys the same recipe), then the target's mpv.Dockerfile stage
# as an image tagged by its recipe's hash (the same recipe, the same tag, in every worktree), then mpv-build.sh in it
# with the docker volume cadence-mpv-cache, where outputs are kept by recipe
#   deb-0.40 [arm64|amd64]   trixie's mpv 0.40.0-3+deb13u1 as debs, the pi's default (arm64)
#   deb-0.41 [arm64|amd64]   forky's mpv 0.41.0-2 as debs, built on trixie
#   android-armeabi-v7a      mpv 0.41.0's libmpv.so for the jdtech aar, with its static dependencies
# MPV_REBUILD=1 builds again and fails unless the result equals the cached one, file by file
# usage: mpv.sh <target> [arch]
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
[ $# -ge 1 ] || { echo "usage: mpv.sh <target> [arch]" >&2; exit 2; }
target=$1
case $target in
  deb-0.40 | deb-0.41)
    arch=${2:-arm64} stage=deb
    case $arch in amd64 | arm64) ;; *) echo "mpv: debs build for amd64 or arm64, not $arch" >&2; exit 2 ;; esac
    platform=linux/$arch dir=$target-$arch
    ;;
  android-armeabi-v7a) stage=android platform=linux/amd64 dir=$target ;;
  *) echo "mpv: unknown target $target" >&2; exit 2 ;;
esac

sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi | cut -c1-12; }
tag=cadence-mpv-$stage:$(cat deploy/builder/mpv.Dockerfile media/third_party/mpv/mpv.pins deploy/builder/mpv-fetch.sh \
  | sha)-$(echo "$platform" | tr / -)

# the registry before anything docker (deploy/builder/prebuilt.sh): a hit is the whole result, no image build, no
# compile, no emulation. a miss (exit 3) falls through to the image and the volume cache as before; anything else
# (a checksum that does not vouch, a half download) stops here rather than build over it
registry=$root/build/mpv/$dir.prebuilt.tar
fetch_rc=0
bash "$root/deploy/builder/prebuilt.sh" fetch "mpv-$dir" "$registry" || fetch_rc=$?
if [ "$fetch_rc" -eq 0 ]; then
  rm -rf "$root/build/mpv/$dir"
  mkdir -p "$root/build/mpv/$dir"
  tar -xf "$registry" -C "$root/build/mpv/$dir"
  rm -f "$registry" "$registry.sha256"
  echo "mpv: $target${2:+ $2} from the prebuilt registry"
  exit 0
fi
[ "$fetch_rc" -eq 3 ] || exit "$fetch_rc"

docker buildx build --progress=plain --platform "$platform" -f deploy/builder/mpv.Dockerfile --target "$stage" \
  --load -t "$tag" .
IN_DOCKER_FLAGS="--platform $platform -v cadence-mpv-cache:/var/tmp -e MPV_CACHE=/var/tmp/mpv \
  -e MPV_REBUILD=${MPV_REBUILD:-} ${IN_DOCKER_FLAGS:-}" \
  deploy/dev/in-docker.sh "$tag" bash deploy/builder/mpv-build.sh "$target" /w "build/mpv/$dir"
