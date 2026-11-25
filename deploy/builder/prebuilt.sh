#!/usr/bin/env bash
# the slow, pinned third-party builds — our ffmpeg for each consumer (pickle's, the airplay runtime's, the media
# host's, the server image's) and midi's and the vk capsula mini's zig cross deps, the airplay and soloist runtimes,
# mpv — built once per recipe and shared from the git.shino.boo project's generic package registry (devilreef/cadence,
# public: downloads are anonymous). an artifact's name is its recipe's sha256, over every input that makes it (the
# scripts, the dockerfile, the pins, the platform), so an unchanged recipe is never compiled twice. libairplay, audiod,
# cadenced and the screens stay out: they are our own, fast-changing code.
#   key <artifact>            print the recipe hash (any input file, or argument, moves it)
#   list                      every artifact, its hash, and whether the registry has it
#   inputs <artifact>         the recipe's input files, one per line (the test's)
#   fetch <artifact> <file>   download the artifact to <file>, its sha256 beside it, both verified; a miss exits 3
#   build <artifact>          build locally, exactly the command the make target ran before the cache, to its own path
#   publish <artifact>        upload the built artifact and its sha256: CI_JOB_TOKEN in ci, else a token read from the
#                             file GITLAB_TOKEN_FILE names (never an argument, never printed, never on curl's argv)
#   ensure <artifact>         fetch, else build (and publish too when PREBUILT_PUBLISH=1)
#   ensure-all                ensure every artifact, in the catalogue's order
# the make targets and dist.sh pass the fetched tars to the dockerfiles as named build contexts (ffmpeg-pickle,
# midi-deps, vk-deps, ffmpeg-airplay, ffmpeg-mediahost, ffmpeg-server, soloist, airplay), so a cache hit compiles no
# ffmpeg and emulates nothing. mpv keeps its docker volume cache (mpv-build.sh) under this: mpv.sh tries the registry
# before any docker.
# environment: PREBUILT_REGISTRY overrides the registry's url (the test's file:// one), PREBUILT_ROOT another tree to
# read the recipe from (also the test's), PREBUILT_PUBLISH=1 makes ensure publish what it built.
# the first upload is the owner's, by hand (a project access token with api scope, kept in a file):
#   GITLAB_TOKEN_FILE=/path/to/token sh deploy/builder/prebuilt.sh publish <artifact>
# after that the manual prebuilt job in .gitlab-ci.yml refreshes whatever recipe changed, with its job token.
set -euo pipefail
export LC_ALL=C

registry=${PREBUILT_REGISTRY:-https://git.shino.boo/api/v4/projects/402/packages/generic}
root=${PREBUILT_ROOT:-}
[ -n "$root" ] || root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"

fail() { echo "prebuilt: $*" >&2; exit 1; }
usage() {
  cat >&2 <<'EOF'
usage: prebuilt.sh <command> [arguments]
  key <artifact>            print the recipe hash
  list                      every artifact, its hash, and whether the registry has it
  inputs <artifact>         the recipe's input files, one per line
  fetch <artifact> <file>   download the artifact to <file>, its sha256 beside it; a miss exits 3
  build <artifact>          build locally, exactly as before the cache, to its own path
  publish <artifact>        upload the built artifact and its sha256 (CI_JOB_TOKEN, or GITLAB_TOKEN_FILE's file)
  ensure <artifact>         fetch, else build (publishing too when PREBUILT_PUBLISH=1)
  ensure-all                ensure every artifact
EOF
  exit 2
}

sha256_of() { # <file>: the host's sha256 tool
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1
}

# every artifact, in ensure-all's order. ffmpeg-airplay-armhf and airplay-runtime-armhf build for a midi no plan uses
# (AGENTS, make airplay-runtime): kept, as the make target must keep working for them
artifacts() {
  cat <<'EOF'
ffmpeg-pickle
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
mpv-android-armeabi-v7a
EOF
}

# the docker platform a debian architecture builds on
platform_of() {
  case $1 in
    amd64) echo linux/amd64 ;;
    arm64) echo linux/arm64 ;;
    armhf) echo linux/arm/v7 ;;
    *) return 2 ;;
  esac
}

# define <artifact>: files (space-separated, none holds a space), recipe (the argument lines), kind, out (the artifact's
# own path), published (the file name in the registry). an input may name a directory: every file under it, sorted, is
# part of the recipe
define() {
  files= recipe= kind=file out= published=
  case $1 in
    ffmpeg-pickle)
      files="deploy/builder/device.Dockerfile deploy/builder/build-ffmpeg.sh"
      recipe="artifact=ffmpeg-pickle platform=linux/arm64"
      out=build/prebuilt/ffmpeg-pickle/ffmpeg.tar
      ;;
    midi-deps)
      # zig-armhf.cmake stays out: only the audiod build reads it, never deps
      files="deploy/builder/device.Dockerfile deploy/builder/build-ffmpeg.sh deploy/builder/build-midi-audiod.sh \
deploy/builder/zig-armhf.sh"
      recipe="artifact=midi-deps"
      # no platform: the deps are armhf alone, whatever cpu built them
      out=build/prebuilt/midi-deps/deps.tar
      ;;
    vk-deps)
      # as midi-deps: zig-aarch64.cmake stays out, and the deps are aarch64 whatever cpu built them
      files="deploy/builder/device.Dockerfile deploy/builder/build-ffmpeg.sh deploy/builder/build-vk-audiod.sh \
deploy/builder/zig-aarch64.sh"
      recipe="artifact=vk-deps"
      out=build/prebuilt/vk-deps/deps.tar
      ;;
    ffmpeg-airplay-*)
      arch=${1#ffmpeg-airplay-}
      platform_of "$arch" >/dev/null || return 2
      files="deploy/builder/airplay.Dockerfile deploy/builder/build-ffmpeg.sh deploy/builder/airplay-build-deps.sh"
      recipe="artifact=$1 arch=$arch platform=$(platform_of "$arch")"
      out=build/prebuilt/$1/ffmpeg.tar
      ;;
    ffmpeg-mediahost-*)
      arch=${1#ffmpeg-mediahost-}
      case $arch in amd64 | arm64) ;; *) return 2 ;; esac
      files="deploy/builder/media-host.Dockerfile deploy/builder/build-ffmpeg.sh"
      recipe="artifact=$1 arch=$arch platform=linux/$arch"
      out=build/prebuilt/$1/ffmpeg.tar
      ;;
    ffmpeg-server-*)
      arch=${1#ffmpeg-server-}
      case $arch in amd64 | arm64) ;; *) return 2 ;; esac
      # the stage sits in the server's own dockerfile (a plain image build compiles it there), so any change to that
      # file moves the key. no platform: zig cross-builds it the same from any build machine
      files="server/Dockerfile deploy/builder/build-ffmpeg.sh deploy/builder/build-server-ffmpeg.sh"
      recipe="artifact=$1 arch=$arch"
      out=build/prebuilt/$1/ffmpeg.tar
      ;;
    airplay-runtime-*)
      arch=${1#airplay-runtime-}
      platform_of "$arch" >/dev/null || return 2
      files="deploy/builder/airplay.Dockerfile deploy/builder/build-ffmpeg.sh deploy/builder/airplay-build-deps.sh \
deploy/builder/build-airplay.sh deploy/builder/airplay-runtime.pins deploy/builder/assemble-airplay-runtime.sh \
deploy/builder/assemble-airplay-runtime.test.sh"
      recipe="artifact=$1 arch=$arch platform=$(platform_of "$arch")"
      out=build/airplay/airplay-runtime-$arch.tar
      ;;
    soloist-runtime-*)
      arch=${1#soloist-runtime-}
      case $arch in arm64 | amd64) ;; *) return 2 ;; esac
      # the media tree only as the pulse-only shim build reads it; the cross compiler comes from live trixie (unpinned,
      # as before the cache), the runtime's debs are snapshot-pinned in the script itself
      files="deploy/builder/media-host.Dockerfile deploy/builder/assemble-soloist-runtime.sh \
deploy/builder/assemble-soloist-runtime.test.sh media/CMakeLists.txt media/lib/src/result.h media/lib/src/pcmframe \
media/lib/src/resolv media/pulse-shim"
      recipe="artifact=$1 arch=$arch platform=linux/amd64"
      out=build/soloist/soloist-runtime-$arch.tar
      ;;
    mpv-deb-0.40-amd64 | mpv-deb-0.40-arm64 | mpv-deb-0.41-amd64 | mpv-deb-0.41-arm64 | mpv-android-armeabi-v7a)
      files="deploy/builder/mpv.Dockerfile deploy/builder/mpv.sh deploy/builder/mpv-build.sh deploy/builder/mpv-fetch.sh \
media/third_party/mpv/mpv.pins media/third_party/mpv/0001-ao_cadence.patch"
      arch=${1##*-}
      case $1 in
        mpv-android-*) recipe="artifact=$1 target=android-armeabi-v7a platform=linux/amd64" ;;
        *) target=${1#mpv-deb-}; recipe="artifact=$1 target=deb-${target%-*} arch=$arch platform=linux/$arch" ;;
      esac
      # mpv's outputs are directories (mpv-build.sh's), so the registry holds them as one tar
      kind=dir
      out=build/mpv/${1#mpv-}
      published=$1.tar
      ;;
    *) return 2 ;;
  esac
  [ -n "$published" ] || published=$(basename "$out")
  return 0
}

key_of() { # <artifact>: sha256 over every input file's bytes and the argument lines, in a fixed order
  local name=$1 entry file
  define "$name" || fail "no artifact $name (prebuilt.sh list)"
  {
    printf 'prebuilt-recipe 1\n'
    for entry in $files; do # none of the paths holds a space
      if [ -d "$entry" ]; then
        while IFS= read -r file; do
          printf 'file %s %s\n' "$file" "$(sha256_of "$file")"
        done < <(find "$entry" -type f | sort)
      else
        printf 'file %s %s\n' "$entry" "$(sha256_of "$entry")"
      fi
    done
    printf 'recipe %s\n' "$recipe"
  } | { if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi } | cut -d' ' -f1
}

fetch() { # <artifact> <file>: the artifact at <file>, its sha256 beside it; 3 when the registry has no such recipe
  local name=$1 dest=$2 key url sum part got want
  define "$name" || fail "no artifact $name (prebuilt.sh list)"
  key=$(key_of "$name")
  url=$registry/$name/$key/$published
  mkdir -p "$(dirname "$dest")"
  sum=$dest.sha256.part
  part=$dest.part
  # --retry never retries a 404, so a miss stays fast
  if ! curl --fail --silent --show-error --location --retry 3 --output "$sum" "$url.sha256"; then
    rm -f "$sum"
    echo "prebuilt: $name ($key) is not in the registry" >&2
    return 3
  fi
  curl --fail --silent --show-error --location --retry 3 --output "$part" "$url" || {
    rm -f "$sum" "$part"
    fail "cannot download $name's $published"
  }
  got=$(sha256_of "$part")
  want=$(cut -d' ' -f1 "$sum")
  if [ "$got" != "$want" ]; then
    rm -f "$sum" "$part"
    fail "$name: $published does not match its sha256"
  fi
  mv "$part" "$dest"
  mv "$sum" "$dest.sha256"
  echo "prebuilt: $name ($key) fetched to $dest" >&2
}

build() { # <artifact>: the same commands the make targets and dist.sh ran before the cache existed
  local name=$1 arch target
  define "$name" || fail "no artifact $name (prebuilt.sh list)"
  case $name in
    ffmpeg-pickle)
      docker buildx build --progress=plain --platform linux/arm64 -f deploy/builder/device.Dockerfile \
        --target ffmpeg-export --output type=local,dest=build/prebuilt/ffmpeg-pickle .
      ;;
    midi-deps)
      docker buildx build --progress=plain -f deploy/builder/device.Dockerfile \
        --target midi-deps-export --output type=local,dest=build/prebuilt/midi-deps .
      ;;
    vk-deps)
      docker buildx build --progress=plain -f deploy/builder/device.Dockerfile \
        --target vk-deps-export --output type=local,dest=build/prebuilt/vk-deps .
      ;;
    ffmpeg-airplay-*)
      arch=${name#ffmpeg-airplay-}
      docker buildx build --progress=plain --platform "$(platform_of "$arch")" -f deploy/builder/airplay.Dockerfile \
        --target ffmpeg-export --output type=local,dest=build/prebuilt/$name .
      ;;
    ffmpeg-mediahost-*)
      arch=${name#ffmpeg-mediahost-}
      docker buildx build --progress=plain --platform "linux/$arch" -f deploy/builder/media-host.Dockerfile \
        --target ffmpeg-export --output type=local,dest=build/prebuilt/$name .
      ;;
    ffmpeg-server-*)
      arch=${name#ffmpeg-server-}
      # the target platform alone picks the cpu: the stage runs on the build machine's
      docker buildx build --progress=plain --platform "linux/$arch" -f server/Dockerfile \
        --target ffmpeg-server --output type=local,dest=build/prebuilt/$name .
      ;;
    airplay-runtime-*)
      arch=${name#airplay-runtime-}
      # the runtime takes its ffmpeg from the named context, never builds it
      ensure "ffmpeg-airplay-$arch"
      docker buildx build --progress=plain --platform "$(platform_of "$arch")" -f deploy/builder/airplay.Dockerfile \
        --build-context "ffmpeg-airplay=build/prebuilt/ffmpeg-airplay-$arch" \
        --target export --output type=local,dest=build/airplay .
      ;;
    soloist-runtime-arm64)
      # amd64 always: the stage cross-compiles the shim with trixie's amd64 compilers (dist.sh's comment)
      docker buildx build --progress=plain --platform linux/amd64 -f deploy/builder/media-host.Dockerfile \
        --target soloist-export --output type=local,dest=build/soloist .
      ;;
    soloist-runtime-amd64)
      docker buildx build --progress=plain --platform linux/amd64 -f deploy/builder/media-host.Dockerfile \
        --target soloist-export-amd64 --output type=local,dest=build/soloist .
      ;;
    mpv-deb-0.40-* | mpv-deb-0.41-*)
      target=${name#mpv-deb-}
      sh deploy/builder/mpv.sh "deb-${target%-*}" "${name##*-}"
      ;;
    mpv-android-armeabi-v7a)
      sh deploy/builder/mpv.sh android-armeabi-v7a
      ;;
    *) fail "no artifact $name (prebuilt.sh list)" ;;
  esac
  if [ "$kind" = file ]; then
    [ -f "$out" ] || fail "$name: the build wrote no $out"
    printf '%s  %s\n' "$(sha256_of "$out")" "$(basename "$out")" >"$out.sha256"
  else
    [ -d "$out" ] || fail "$name: the build wrote no $out"
  fi
  echo "prebuilt: built $name ($(key_of "$name")) to $out" >&2
}

deterministic_tar() { # <dir> <tar>: the dir's files, sorted, root-owned at time 0, as gnu tar (busybox's and bsdtar
  # lack the flags; a mac has gtar when brew's gnu tar is installed)
  local dir=$1 destination=$2 tool=tar
  tar --version 2>/dev/null | grep -q 'GNU tar' || tool=$(command -v gtar || true)
  [ -n "$tool" ] || fail "publishing a directory artifact needs GNU tar (brew's gtar on a mac)"
  (cd "$dir" && find . -type f ! -name .prebuilt-key | sort |
    "$tool" --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner --format=gnu -cf "$destination" -T -)
}

upload() { # <name>/<key>/<file> <file>: PUT with the token in a header file, never on argv
  local path=$1 file=$2 header remote
  header=$(mktemp)
  chmod 600 "$header"
  if [ -n "${CI_JOB_TOKEN:-}" ]; then
    printf 'JOB-TOKEN: %s\n' "$CI_JOB_TOKEN" >"$header"
  elif [ -n "${GITLAB_TOKEN_FILE:-}" ]; then
    [ -f "$GITLAB_TOKEN_FILE" ] || { rm -f "$header"; fail "GITLAB_TOKEN_FILE ($GITLAB_TOKEN_FILE) is not a file"; }
    printf 'PRIVATE-TOKEN: %s\n' "$(cat "$GITLAB_TOKEN_FILE")" >"$header"
  else
    rm -f "$header"
    fail "publish needs CI_JOB_TOKEN or GITLAB_TOKEN_FILE (a file holding the token)"
  fi
  if ! curl --silent --show-error --fail --request PUT --header @"$header" --upload-file "$file" "$registry/$path"; then
    rm -f "$header"
    # the same recipe published by someone else is fine, when what is there is byte for byte ours
    remote=$(mktemp)
    if curl --fail --silent --output "$remote" "$registry/$path.sha256" &&
      [ "$(sha256_of "$file")" = "$(cut -d' ' -f1 "$remote")" ]; then
      rm -f "$remote"
      echo "prebuilt: $path was already published, byte for byte" >&2
      return 0
    fi
    rm -f "$remote"
    fail "cannot publish $path"
  fi
  rm -f "$header"
}

publish() { # <artifact>: the built artifact and its sha256 under its recipe hash
  local name=$1 key tmp tar sumfile
  define "$name" || fail "no artifact $name (prebuilt.sh list)"
  key=$(key_of "$name")
  if [ "$kind" = file ]; then
    [ -f "$out" ] || fail "$name is not built ($out): run build first"
    tar=$out
    # always fresh: what stands beside the artifact may be an older build's
    printf '%s  %s\n' "$(sha256_of "$out")" "$(basename "$out")" >"$out.sha256"
  else
    [ -d "$out" ] || fail "$name is not built ($out): run build first"
    tmp=$(mktemp -d)
    tar=$tmp/$published
    deterministic_tar "$out" "$tar"
  fi
  upload "$name/$key/$published" "$tar"
  sumfile=$tar.sha256
  [ "$kind" = file ] || printf '%s  %s\n' "$(sha256_of "$tar")" "$published" >"$sumfile"
  upload "$name/$key/$published.sha256" "$sumfile"
  [ "$kind" = file ] || rm -rf "$tmp"
  echo "prebuilt: published $name ($key)" >&2
}

ensure() { # <artifact>: fetch, else build, publishing what it built when PREBUILT_PUBLISH=1
  local name=$1 key marker staging rc=0 out_there
  define "$name" || fail "no artifact $name (prebuilt.sh list)"
  key=$(key_of "$name")
  # a marker of the last fetch or build, so a repeat costs nothing (a hit re-downloads otherwise)
  if [ "$kind" = file ]; then marker=$out.key; else marker=$out/.prebuilt-key; fi
  if [ "$kind" = file ]; then [ -f "$out" ] && out_there=1; else [ -d "$out" ] && out_there=1; fi
  if [ -n "${out_there:-}" ] && [ -f "$marker" ] && [ "$(cat "$marker" 2>/dev/null)" = "$key" ]; then
    echo "prebuilt: $name ($key) already at $out" >&2
    return 0
  fi
  mkdir -p "$(dirname "$out")"
  if [ "$kind" = file ]; then
    fetch "$name" "$out" || rc=$?
    if [ "$rc" -eq 3 ]; then
      build "$name"
    else
      [ "$rc" -eq 0 ] || exit "$rc"
    fi
    printf '%s\n' "$key" >"$marker"
  else
    staging=$(dirname "$out")/.$(basename "$out").prebuilt.tar
    fetch "$name" "$staging" || rc=$?
    if [ "$rc" -eq 0 ]; then
      rm -rf "$out"
      mkdir -p "$out"
      tar -xf "$staging" -C "$out"
      rm -f "$staging" "$staging.sha256"
    elif [ "$rc" -eq 3 ]; then
      build "$name"
    else
      exit "$rc"
    fi
    printf '%s\n' "$key" >"$marker"
  fi
  if [ "${PREBUILT_PUBLISH:-}" = 1 ]; then publish "$name"; fi
}

list() {
  local name key have
  printf '%-24s %-6s %s\n' artifact in-reg key
  for name in $(artifacts); do
    key=$(key_of "$name")
    define "$name" >/dev/null
    have=no
    curl --fail --silent --output /dev/null "$registry/$name/$key/$published.sha256" 2>/dev/null && have=yes
    printf '%-24s %-6s %s\n' "$name" "$have" "$key"
  done
}

[ $# -ge 1 ] || usage
command=$1
shift
case $command in
  key)
    [ $# -eq 1 ] || usage
    key_of "$1"
    ;;
  inputs)
    [ $# -eq 1 ] || usage
    define "$1" || fail "no artifact $1 (prebuilt.sh list)"
    # shellcheck disable=SC2086
    printf '%s\n' $files
    ;;
  fetch)
    [ $# -eq 2 ] || usage
    fetch "$1" "$2"
    ;;
  build)
    [ $# -eq 1 ] || usage
    build "$1"
    ;;
  publish)
    [ $# -eq 1 ] || usage
    publish "$1"
    ;;
  ensure)
    [ $# -eq 1 ] || usage
    ensure "$1"
    ;;
  ensure-all)
    for name in $(artifacts); do ensure "$name"; done
    ;;
  list)
    list
    ;;
  *) usage ;;
esac
