#!/usr/bin/env bash
# builds one mpv target with the ao_cadence patch, inside its mpv.Dockerfile stage (deploy/builder/mpv.sh runs it), and
# copies the result to <out>. the docker volume cadence-mpv-cache is mounted at $MPV_CACHE: a target's outputs are kept
# there under the hash of everything that makes them (the pins, the patch, these scripts and the dockerfile), so an
# unchanged recipe copies them out instead of building; android's static dependencies are kept the same way under
# the hash of their own part of the recipe, so a change of the patch rebuilds mpv alone. MPV_REBUILD=1 builds into a
# scratch directory whatever the cache holds and compares the result with the cached one, file by file (the
# reproducibility check)
#   deb-0.40, deb-0.41     <out>/*.deb: debian's mpv source package (trixie's 0.40.0-3+deb13u1, forky's 0.41.0-2) with
#                          the patch as debian/patches/cadence-ao.patch, versioned +cadence1, for this image's cpu
#   android-armeabi-v7a    <out>/lib/libmpv.so (a drop-in for the jdtech aar's, checked against it), include/mpv/,
#                          licenses/ and compare.txt
# usage: mpv-build.sh <target> <repo> <out>
set -euo pipefail

die() { echo "mpv-build: $*" >&2; exit 1; }
[ $# -eq 3 ] || die "usage: mpv-build.sh <target> <repo> <out>"
target=$1
repo=$(cd "$2" && pwd)
mkdir -p "$3"
out=$(cd "$3" && pwd)
cache=${MPV_CACHE:?}
patch=$repo/media/third_party/mpv/0001-ao_cadence.patch
here=$repo/deploy/builder
cores=$(nproc)

hash_of() { cat "$@" | sha256sum | cut -c1-16; }
recipe=("$repo/media/third_party/mpv/mpv.pins" "$here/mpv-fetch.sh" "$here/mpv.Dockerfile" "$here/mpv-build.sh")
deps_key=$(hash_of "${recipe[@]}")
key=$(printf '%s %s %s\n' "$target" "$(dpkg --print-architecture)" "$(hash_of "${recipe[@]}" "$patch")" | sha256sum | cut -c1-16)
slot=$cache/$target-$(dpkg --print-architecture)/$key

# the version a rebuilt package gets, and the date of its changelog entry (SOURCE_DATE_EPOCH, so the debs reproduce)
local_suffix=+cadence1
entry_date='Sat, 14 Nov 2026 00:00:00 +0000'

build_deb() { # <version dir> <dest>
  local ver=$1 dest=$2 work=/tmp/build
  rm -rf "$work"
  mkdir -p "$work"
  cp -a "/src/mpv-$ver" "$work/mpv-$ver"
  cd "$work/mpv-$ver"
  mkdir -p debian/patches
  cp "$patch" debian/patches/cadence-ao.patch
  echo cadence-ao.patch >>debian/patches/series
  QUILT_PATCHES=debian/patches QUILT_PC=.pc quilt push -q cadence-ao.patch
  local version
  version=$(dpkg-parsechangelog -S Version)$local_suffix
  { printf 'mpv (%s) trixie; urgency=medium\n\n' "$version"
    printf '  * ao_cadence: a pull audio output into cadence'"'"'s mixer ring (debian/patches/cadence-ao.patch).\n\n'
    printf ' -- Cadence <cadence@localhost>  %s\n\n' "$entry_date"
    cat debian/changelog; } >debian/changelog.new
  mv debian/changelog.new debian/changelog
  DEB_BUILD_OPTIONS="nocheck nodoc parallel=$cores" dpkg-buildpackage -b -uc -us >"$work/build.log" 2>&1 \
    || { tail -60 "$work/build.log"; die "dpkg-buildpackage failed"; }
  mkdir -p "$dest"
  for deb in "$work"/*.deb; do
    case $deb in *-dbgsym_*) continue ;; esac
    cp "$deb" "$dest/"
  done
  # the patched library is in the package: its soname, and the ao by name with its options
  local x=$work/x
  mkdir "$x"
  dpkg-deb -x "$dest"/libmpv2_"${version#*:}"_*.deb "$x"
  local so
  so=$(find "$x" -name 'libmpv.so.2.*' -type f | head -1)
  [ -n "$so" ] || die "no libmpv.so.2 in libmpv2"
  readelf -dW "$so" | grep -F 'Library soname: [libmpv.so.2]' >/dev/null || die "libmpv's soname is not libmpv.so.2"
  strings "$so" | grep -xF 'Cadence mixer ring (memfd)' >/dev/null || die "libmpv has no ao_cadence"
  strings "$so" | grep -xF 'ao-cadence' >/dev/null || die "libmpv has no ao-cadence options"
  (cd "$dest" && sha256sum ./*.deb >SHA256SUMS)
  echo "mpv $version: $(ls "$dest" | tr '\n' ' ')"
}

android_env() {
  api=26 # the aar's own minimum, which its libmpv.so is built for
  export CC=armv7a-linux-androideabi$api-clang CXX=armv7a-linux-androideabi$api-clang++ AR=llvm-ar RANLIB=llvm-ranlib
  export LDFLAGS="-Wl,-O1,--icf=safe -Wl,-z,max-page-size=16384"
  ndk_triple=arm-linux-androideabi
  export PKG_CONFIG_SYSROOT_DIR=$prefix PKG_CONFIG_LIBDIR=$prefix/lib/pkgconfig
  unset PKG_CONFIG_PATH
  # no build path in any object: every build runs in the same directories anyway, this keeps it so
  export CFLAGS="-ffile-prefix-map=$src=. -ffile-prefix-map=$prefix=/prefix"
  export CXXFLAGS=$CFLAGS
}

android_crossfile() {
  cat >"$prefix/crossfile.txt" <<CROSS
[built-in options]
buildtype = 'release'
default_library = 'static'
wrap_mode = 'nodownload'
[binaries]
c = '$CC'
cpp = '$CXX'
ar = 'llvm-ar'
nm = 'llvm-nm'
strip = 'llvm-strip'
pkg-config = 'pkg-config'
[host_machine]
system = 'android'
cpu_family = 'arm'
cpu = 'armv7a'
endian = 'little'
CROSS
}

copy_src() { rm -rf "${src:?}/$1"; cp -a "/src/$1" "$src/$1"; }
untar_src() { rm -rf "${src:?}/$1"; mkdir -p "$src/$1"; tar -xzf "/src/$2" -C "$src/$1" --strip-components=1; }
step() { # name, then the commands
  local n=$1 t0
  shift
  t0=$(date +%s)
  if ! ( set -e; "$@" ) >"$logs/$n.log" 2>&1; then tail -60 "$logs/$n.log"; die "$n failed"; fi
  echo "  $n $(( $(date +%s) - t0 )) s"
}
mesonlib() { # dir, options...
  local d=$1
  shift
  cd "$src/$d"
  meson setup _b --cross-file "$prefix/crossfile.txt" "$@"
  ninja -C _b -j"$cores"
  DESTDIR="$prefix" ninja -C _b install
}

# the aar's dependencies with the options of its own build scripts (libmpv-android v1.0.0, buildscripts/scripts)
s_ffmpeg_headers() {
  copy_src ffmpeg
  cd "$src/ffmpeg"
  ./configure --prefix=/usr/local --target-os=android --enable-cross-compile --cross-prefix=llvm- --cc="$CC" \
    --arch=arm --cpu=armv7-a --nm=llvm-nm --pkg-config=pkg-config --enable-jni --enable-mediacodec --disable-static \
    --enable-shared --enable-gpl --enable-version3 --disable-doc --disable-programs --disable-muxers \
    --disable-encoders --disable-devices --disable-filters --disable-v4l2-m2m
  # headers, the generated avconfig.h and ffversion.h, and the .pc files: nothing is compiled
  make -j"$cores" install-headers DESTDIR="$prefix"
  cp /src/aar/jni/armeabi-v7a/lib{avcodec,avdevice,avfilter,avformat,avutil,swresample,swscale}.so "$prefix/lib/"
}
s_libxml2() { copy_src libxml2; mesonlib libxml2 -Dminimum=true -D{push,reader,sax1,iso8859x,pattern}=enabled; }
s_freetype() { copy_src freetype; mesonlib freetype; }
s_fontconfig() { copy_src fontconfig; mesonlib fontconfig -D{tests,doc,tools,nls}=disabled -Dxml-backend=libxml2; }
s_fribidi() { copy_src fribidi; mesonlib fribidi -D{tests,docs}=false; }
s_harfbuzz() { copy_src harfbuzz; mesonlib harfbuzz -Dtests=disabled -Ddocs=disabled; }
s_libunibreak() {
  untar_src libunibreak libunibreak
  cd "$src/libunibreak"
  mkdir -p _b
  cd _b
  ../configure --host=$ndk_triple --with-pic --enable-static --disable-shared
  make -j"$cores"
  make DESTDIR="$prefix" install
}
s_libass() {
  copy_src libass
  cd "$src/libass"
  ./autogen.sh
  mkdir -p _b
  cd _b
  ../configure --host=$ndk_triple --with-pic --enable-static --disable-shared --enable-libunibreak --enable-fontconfig
  make -j"$cores"
  make DESTDIR="$prefix" install
}
s_lua() {
  untar_src lua lua
  cd "$src/lua"
  make CC="$CC" AR="$AR rc" RANLIB="$RANLIB" MYCFLAGS="-fPIC $CFLAGS -Dgetlocaledecpoint\(\)=\(\'.\'\)" PLAT=linux \
    LUA_T= LUAC_T= -j"$cores"
  make INSTALL=install INSTALL_TOP="$prefix" TO_BIN=/dev/null install
  mkdir -p "$prefix/lib/pkgconfig"
  make pc >"$prefix/lib/pkgconfig/lua.pc"
  printf 'Name: Lua\nDescription:\nVersion: ${version}\nLibs: -L${libdir} -llua\nCflags: -I${includedir}\n' \
    >>"$prefix/lib/pkgconfig/lua.pc"
}
s_libplacebo() {
  copy_src libplacebo
  mesonlib libplacebo -Dvulkan=disabled -Ddemos=false
  sed -i '/^Libs:/ s|$| -lc++|' "$prefix/lib/pkgconfig/libplacebo.pc"
}

build_android_deps() { # <dest prefix>
  prefix=$1 src=/tmp/android/src logs=/tmp/android/logs
  rm -rf "$prefix" /tmp/android
  mkdir -p "$prefix" "$src" "$logs"
  ln -s . "$prefix/usr"
  ln -s . "$prefix/local"
  android_env
  android_crossfile
  for s in ffmpeg_headers libxml2 freetype fontconfig fribidi harfbuzz libunibreak libass lua libplacebo; do
    step "$s" "s_$s"
  done
}

s_mpv() {
  copy_src mpv
  cd "$src/mpv"
  patch -p1 <"$patch"
  meson setup _b --cross-file "$prefix/crossfile.txt" --default-library shared -Diconv=disabled -Dlua=enabled \
    -Dlibmpv=true -Dcplayer=false -Dmanpage-build=disabled -Dcadence=enabled -Dbuild-date=false
  ninja -C _b -j"$cores"
}

# ours against the aar's: the same NEEDED and SONAME, every export the aar has (mpv_*, ass_*, pl_*), the ao by name
compare_aar() { # <ours> <report>
  local ours=$1 report=$2 aar=/src/aar/jni/armeabi-v7a/libmpv.so r=/tmp/compare
  rm -rf "$r"
  mkdir -p "$r"
  for x in aar:$aar ours:$ours; do
    local n=${x%%:*} f=${x#*:}
    llvm-readelf -dW "$f" | grep -E '\((NEEDED|SONAME)\)' | awk '{print $NF}' >"$r/$n.dynamic"
    llvm-nm -D --defined-only "$f" | awk '{print $3}' | sort >"$r/$n.exports"
    llvm-nm -D --undefined-only "$f" | awk '{print $2}' | sort >"$r/$n.imports"
    llvm-readelf -hW "$f" | grep -E 'Flags:' | sed "s/^ */$n /" >>"$r/$n.header"
  done
  {
    echo "NEEDED and SONAME: $(tr '\n' ' ' <"$r/ours.dynamic")"
    echo "exports: aar $(wc -l <"$r/aar.exports"), ours $(wc -l <"$r/ours.exports")"
    echo "exports only in ours: $(comm -13 "$r/aar.exports" "$r/ours.exports" | tr '\n' ' ')"
    echo "imports only in ours: $(comm -13 "$r/aar.imports" "$r/ours.imports" | tr '\n' ' ')"
    echo "imports only in the aar: $(comm -23 "$r/aar.imports" "$r/ours.imports" | tr '\n' ' ')"
    cat "$r/aar.header" "$r/ours.header"
  } >"$report"
  cmp -s "$r/aar.dynamic" "$r/ours.dynamic" || { diff "$r/aar.dynamic" "$r/ours.dynamic" >&2; die "NEEDED or SONAME differ from the aar's"; }
  [ -z "$(comm -23 "$r/aar.exports" "$r/ours.exports")" ] \
    || die "exports the aar has and ours lacks: $(comm -23 "$r/aar.exports" "$r/ours.exports" | head -20 | tr '\n' ' ')"
  [ "$(sed 's/^[a-z]* //' "$r/aar.header")" = "$(sed 's/^[a-z]* //' "$r/ours.header")" ] || die "elf flags differ from the aar's"
  strings "$ours" | grep -xF 'Cadence mixer ring (memfd)' >/dev/null || die "libmpv.so has no ao_cadence"
}

build_android() { # <dest>
  local dest=$1 deps=$cache/android-deps/$deps_key
  if [ ! -f "$deps/done" ] || [ "${MPV_REBUILD:-}" = 1 ]; then
    echo "android dependencies ($deps_key)"
    build_android_deps /tmp/android-prefix
    rm -rf "$deps"
    mkdir -p "$deps"
    cp -a /tmp/android-prefix "$deps/prefix"
    touch "$deps/done"
  else
    echo "android dependencies ($deps_key) from the cache"
  fi
  prefix=/tmp/android-prefix src=/tmp/android/src logs=/tmp/android/logs
  rm -rf "$prefix" /tmp/android
  mkdir -p "$src" "$logs"
  cp -a "$deps/prefix" "$prefix"
  android_env
  step mpv s_mpv
  mkdir -p "$dest/lib" "$dest/include/mpv" "$dest/licenses"
  cp "$src/mpv/_b/libmpv.so" "$dest/lib/libmpv.so"
  cp "$src/mpv/include/mpv/"*.h "$dest/include/mpv/"
  compare_aar "$dest/lib/libmpv.so" "$dest/compare.txt"
  # what libmpv.so links in, each with its licence text (mpv as built here is gpl, as the aar's)
  local d f
  for d in mpv libass libplacebo freetype fribidi harfbuzz libxml2 fontconfig; do
    mkdir -p "$dest/licenses/$d"
    for f in /src/$d/LICENSE* /src/$d/COPYING* /src/$d/Copyright /src/$d/docs/FTL.TXT /src/$d/docs/GPLv2.TXT \
      /src/$d/docs/LICENSE.TXT; do
      [ -f "$f" ] && cp "$f" "$dest/licenses/$d/"
    done
    [ -n "$(ls "$dest/licenses/$d")" ] || die "$d has no licence file"
  done
  mkdir -p "$dest/licenses/libunibreak" "$dest/licenses/lua"
  tar -xzf /src/libunibreak -O --wildcards '*/LICENCE' >"$dest/licenses/libunibreak/LICENCE"
  tar -xzf /src/lua -O --wildcards '*/src/lua.h' | sed -n '/Copyright (C)/,/\*\//p' >"$dest/licenses/lua/lua.h-notice"
  [ -s "$dest/licenses/lua/lua.h-notice" ] || die "lua has no licence notice"
  (cd "$dest" && find . -type f ! -name SHA256SUMS | LC_ALL=C sort | xargs sha256sum >SHA256SUMS)
  echo "libmpv.so $(stat -c %s "$dest/lib/libmpv.so") bytes; $(head -2 "$dest/compare.txt" | tail -1)"
}

build() { # <dest>
  case $target in
    deb-0.40 | deb-0.41) build_deb "${target#deb-}" "$1" ;;
    android-armeabi-v7a) build_android "$1" ;;
    *) die "unknown target $target" ;;
  esac
}

if [ "${MPV_REBUILD:-}" = 1 ]; then
  [ -f "$slot/done" ] || die "nothing cached for $target ($key) to compare a rebuild with"
  scratch=/tmp/rebuild
  rm -rf "$scratch"
  build "$scratch"
  if ! diff -r "$slot/out" "$scratch" >/tmp/rebuild.diff; then
    cat /tmp/rebuild.diff >&2
    die "$target does not reproduce: the rebuild differs from the cached build ($key)"
  fi
  echo "$target reproduces: the rebuild equals the cached build ($key)"
elif [ -f "$slot/done" ]; then
  echo "$target ($key) from the cache"
else
  rm -rf "$slot"
  mkdir -p "$slot"
  build "$slot/out"
  touch "$slot/done"
fi
rm -rf "$out"
mkdir -p "$out"
cp -a "$slot/out/." "$out/"
echo "$key" >"$out/KEY"
