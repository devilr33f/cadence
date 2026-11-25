#!/bin/sh
# the minimal lgpl ffmpeg the url source plays through (M4b-D9): shared libraries only, built from a pinned release
# tarball into the prefix given first; any further arguments go to configure (a cross build's flags, or a runtime's
# own components: each --enable-decoder, -demuxer, -parser or -protocol given is checked like the built-in ones). the
# license texts, the configure line and the version land in <prefix>/share/licenses/ffmpeg, which images ship beside
# the libraries (M4b-D15). needs curl, xz-utils, make, a c toolchain, nasm on x86, pkg-config and openssl 3's headers
set -eu

FFMPEG_VERSION=9.0.2
FFMPEG_SHA256=8c3850283eb25fa026482078a04051e0be17347b09ef81a0849bec15a96e002e

prefix=${1:?usage: build-ffmpeg.sh <prefix> [configure arguments...]}
shift

# a fixed directory: the compiler writes it into the debug info that the build id is a hash of, so a random one would
# give the stripped libraries a new build id on every build
work=/tmp/build-ffmpeg
rm -rf "$work"
mkdir "$work"
trap 'rm -rf "$work"' EXIT

curl --fail --silent --show-error --location --retry 5 --retry-all-errors --output "$work/ffmpeg.tar.xz" \
  "https://ffmpeg.org/releases/ffmpeg-${FFMPEG_VERSION}.tar.xz"
echo "${FFMPEG_SHA256}  $work/ffmpeg.tar.xz" | sha256sum --check --strict -
tar -xJf "$work/ffmpeg.tar.xz" -C "$work"
cd "$work/ffmpeg-${FFMPEG_VERSION}"

decoders=mp3float,aac,flac,opus,vorbis,pcm_s16le,pcm_s24le,pcm_f32le
demuxers=mp3,aac,flac,ogg,wav,mov,hls,mpegts
parsers=aac,flac,mpegaudio,opus,vorbis
# upstream has no hls protocol any more (the hls demuxer reads playlists) and no libpostproc to disable
protocols=http,https,tcp,tls,crypto
# a component the caller adds is one its runtime needs: configure dropping it must fail the build too
for argument in "$@"; do
  case $argument in
    --enable-decoder=*) decoders="$decoders,${argument#*=}" ;;
    --enable-demuxer=*) demuxers="$demuxers,${argument#*=}" ;;
    --enable-parser=*) parsers="$parsers,${argument#*=}" ;;
    --enable-protocol=*) protocols="$protocols,${argument#*=}" ;;
  esac
done

# never --enable-gpl or --enable-nonfree: version3 keeps it lgpl, and is what openssl 3 asks for
set -- --prefix="$prefix" --enable-shared --disable-static --disable-programs --disable-doc \
  --disable-everything --disable-autodetect --disable-avdevice --disable-swscale --disable-avfilter \
  --enable-version3 --enable-openssl \
  --enable-decoder="$decoders" --enable-demuxer="$demuxers" --enable-parser="$parsers" \
  --enable-protocol="$protocols" "$@"
./configure "$@"

grep -qx '#define FFMPEG_LICENSE "LGPL version 3 or later"' config.h \
  || { echo "build-ffmpeg: not lgpl version 3 or later" >&2; exit 1; }
grep -qx '!CONFIG_GPL=yes' ffbuild/config.mak && grep -qx '!CONFIG_NONFREE=yes' ffbuild/config.mak \
  || { echo "build-ffmpeg: gpl or nonfree enabled" >&2; exit 1; }
# a component configure dropped for a missing dependency would only warn
for kind in decoder demuxer parser protocol; do
  eval "names=\$${kind}s"
  for name in $(echo "$names" | tr , ' '); do
    macro=$(echo "CONFIG_${name}_${kind}" | tr '[:lower:]' '[:upper:]')
    grep -qx "#define $macro 1" config_components.h \
      || { echo "build-ffmpeg: $kind $name is not enabled" >&2; exit 1; }
  done
done

# each library finds the others beside it, wherever the image puts them: a binary's own runpath covers only what it
# links itself, not libavcodec's libswresample
LD_RUN_PATH='$ORIGIN' make -j"$(nproc)"
make install
for library in "$prefix"/lib/lib*.so.*.*.*; do
  readelf -d "$library" | grep -qF 'Library runpath: [$ORIGIN]' \
    || { echo "build-ffmpeg: $library has no \$ORIGIN runpath" >&2; exit 1; }
done
licenses="$prefix/share/licenses/ffmpeg"
mkdir -p "$licenses"
cp COPYING.LGPLv3 COPYING.LGPLv2.1 LICENSE.md "$licenses/"
echo "./configure $*" > "$licenses/configure.txt"
echo "$FFMPEG_VERSION" > "$licenses/version.txt"
