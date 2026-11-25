#!/bin/sh
# the server's ffmpeg program (server/internal/ringsounds: an alarm's music, read on stdin, loudnorm and a fade-in,
# libopus into ogg on stdout): one static lgpl binary, cross-built with zig against musl for the target cpu given
# second (amd64 or arm64) on any build machine, so nothing compiles under emulation and it runs on distroless. the
# ffmpeg pin is build-ffmpeg.sh's (one source tarball for every consumer); libopus is pinned here. the program goes
# to <prefix>/bin/ffmpeg; the license texts, the sources' urls and sha256s and the configure line to
# <prefix>/share/licenses/ffmpeg. needs curl, xz-utils, make, nasm, pkg-config, the target's gnu binutils (nm and
# strip) and zig on the PATH
set -eu

prefix=${1:?usage: build-server-ffmpeg.sh <prefix> amd64|arm64}
arch=${2:?usage: build-server-ffmpeg.sh <prefix> amd64|arm64}
here=$(cd "$(dirname "$0")" && pwd)

OPUS_VERSION=1.5.2
OPUS_SHA256=65c1d2f78b9f2fb20082c38cbe47c951ad5839345876e46941612ee87f9a7ce1
eval "$(grep -E '^FFMPEG_(VERSION|SHA256)=[0-9a-f.]+$' "$here/build-ffmpeg.sh")"
[ -n "${FFMPEG_VERSION:-}" ] && [ -n "${FFMPEG_SHA256:-}" ] ||
  { echo "build-server-ffmpeg: no ffmpeg pin in build-ffmpeg.sh" >&2; exit 1; }

case $arch in
  amd64) triple=x86_64-linux-musl ffarch=x86_64 binutils=x86_64-linux-gnu machine=X86-64 ;;
  arm64) triple=aarch64-linux-musl ffarch=aarch64 binutils=aarch64-linux-gnu machine=AArch64 ;;
  *) echo "build-server-ffmpeg: no target $arch" >&2; exit 1 ;;
esac

# a fixed directory, as build-ffmpeg.sh's
work=/tmp/build-server-ffmpeg
rm -rf "$work"
mkdir -p "$work/bin" "$work/deps"
trap 'rm -rf "$work"' EXIT

# zig's clang for the target, as one-word tools (configure scripts split a --cc with spaces badly)
printf '#!/bin/sh\nexec zig cc -target %s "$@"\n' "$triple" >"$work/bin/cc"
printf '#!/bin/sh\nexec zig ar "$@"\n' >"$work/bin/ar"
printf '#!/bin/sh\nexec zig ranlib "$@"\n' >"$work/bin/ranlib"
chmod 755 "$work/bin/cc" "$work/bin/ar" "$work/bin/ranlib"

fetch() { # <url> <sha256> <file>
  curl --fail --silent --show-error --location --retry 5 --retry-all-errors --output "$3" "$1"
  echo "$2  $3" | sha256sum --check --strict -
}
opus_url=https://downloads.xiph.org/releases/opus/opus-${OPUS_VERSION}.tar.gz
ffmpeg_url=https://ffmpeg.org/releases/ffmpeg-${FFMPEG_VERSION}.tar.xz
fetch "$opus_url" "$OPUS_SHA256" "$work/opus.tar.gz"
fetch "$ffmpeg_url" "$FFMPEG_SHA256" "$work/ffmpeg.tar.xz"
tar -xzf "$work/opus.tar.gz" -C "$work"
tar -xJf "$work/ffmpeg.tar.xz" -C "$work"

# libopus: static, into the deps prefix ffmpeg's configure finds through pkg-config
(
  cd "$work/opus-${OPUS_VERSION}"
  # libtool only probes ld and nm (a static library links nothing); --build keeps it a cross build where the host
  # runs the target's programs (binfmt)
  ./configure --build="$(uname -m)-linux-gnu" --host="$triple" --prefix="$work/deps" \
    CC="$work/bin/cc" AR="$work/bin/ar" RANLIB="$work/bin/ranlib" LD="$binutils-ld" NM="$binutils-nm" \
    --disable-shared --enable-static --disable-doc --disable-extra-programs
  make -j"$(nproc)"
  make install
)

decoders=aac,alac,flac,mp3float,opus,vorbis,pcm_s16le,pcm_s16be,pcm_s24le,pcm_s32le,pcm_f32le,pcm_u8
demuxers=aac,flac,matroska,mov,mp3,ogg,wav
parsers=aac,flac,mpegaudio,opus,vorbis
encoders=libopus
muxers=ogg
# ringsounds' own chain (loudnorm, afade) and what the program puts around it: -ar and -ac (aresample, aformat), -t
# (atrim) and a stream passed on as is (anull)
filters=loudnorm,afade,aresample,aformat,atrim,anull
# the song comes on stdin and the sound goes to stdout: no file and no network
protocols=pipe

cd "$work/ffmpeg-${FFMPEG_VERSION}"
# never --enable-gpl, --enable-nonfree or --enable-version3: lgpl 2.1 or later
set -- --prefix="$prefix" --enable-cross-compile --arch="$ffarch" --target-os=linux \
  --cc="$work/bin/cc" --ar="$work/bin/ar" --ranlib="$work/bin/ranlib" --nm="$binutils-nm" --strip="$binutils-strip" \
  --host-cc='zig cc' --pkg-config=pkg-config --pkg-config-flags=--static --extra-ldflags=-static \
  --enable-static --disable-shared --disable-doc --disable-debug --disable-network \
  --disable-everything --disable-autodetect --disable-programs --enable-ffmpeg \
  --disable-avdevice --disable-swscale \
  --enable-libopus \
  --enable-decoder="$decoders" --enable-demuxer="$demuxers" --enable-parser="$parsers" \
  --enable-encoder="$encoders" --enable-muxer="$muxers" --enable-filter="$filters" --enable-protocol="$protocols"
PKG_CONFIG_LIBDIR="$work/deps/lib/pkgconfig" ./configure "$@"

grep -qx '#define FFMPEG_LICENSE "LGPL version 2.1 or later"' config.h \
  || { echo "build-server-ffmpeg: not lgpl version 2.1 or later" >&2; exit 1; }
grep -qx '!CONFIG_GPL=yes' ffbuild/config.mak && grep -qx '!CONFIG_NONFREE=yes' ffbuild/config.mak \
  || { echo "build-server-ffmpeg: gpl or nonfree enabled" >&2; exit 1; }
# a component configure dropped for a missing dependency would only warn
for kind in decoder demuxer parser encoder muxer filter protocol; do
  eval "names=\$${kind}s"
  for name in $(echo "$names" | tr , ' '); do
    macro=$(echo "CONFIG_${name}_${kind}" | tr '[:lower:]' '[:upper:]')
    grep -qx "#define $macro 1" config_components.h \
      || { echo "build-server-ffmpeg: $kind $name is not enabled" >&2; exit 1; }
  done
done

make -j"$(nproc)"
make install
program=$prefix/bin/ffmpeg
# static and for the target: no interpreter, no needed library
"$binutils-readelf" -h "$program" | grep -q "Machine:.*$machine" ||
  { echo "build-server-ffmpeg: $program is not for $arch" >&2; exit 1; }
if "$binutils-readelf" -l "$program" | grep -q INTERP || "$binutils-readelf" -d "$program" | grep -q NEEDED; then
  echo "build-server-ffmpeg: $program is not static" >&2
  exit 1
fi
rm -rf "$prefix/include" "$prefix/lib" "$prefix/share/ffmpeg"

licenses="$prefix/share/licenses/ffmpeg"
mkdir -p "$licenses"
cp COPYING.LGPLv2.1 LICENSE.md "$licenses/"
cp "$work/opus-${OPUS_VERSION}/COPYING" "$licenses/COPYING.opus"
# musl, linked in by zig
cp "$(dirname "$(readlink -f "$(command -v zig)")")/lib/libc/musl/COPYRIGHT" "$licenses/COPYRIGHT.musl"
echo "./configure $*" | sed "s|$work|/tmp/build-server-ffmpeg|g" >"$licenses/configure.txt"
cat >"$licenses/SOURCE.txt" <<EOF
ffmpeg $FFMPEG_VERSION (LGPL 2.1 or later), built unmodified as configure.txt says, statically linked with:
  libopus $OPUS_VERSION (BSD, COPYING.opus)
  musl libc from zig $(zig version) (MIT, COPYRIGHT.musl)
sources:
  $ffmpeg_url sha256 $FFMPEG_SHA256
  $opus_url sha256 $OPUS_SHA256
the build script: deploy/builder/build-server-ffmpeg.sh in https://git.shino.boo/devilreef/cadence
EOF
echo "$FFMPEG_VERSION" >"$licenses/version.txt"
