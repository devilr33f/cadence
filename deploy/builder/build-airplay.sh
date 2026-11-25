#!/bin/sh
# build-airplay.sh <prefix> <pins>: shairport sync and nqptp from their pinned release tarballs and avahi from debian's
# pinned source package (M4d-D1, M4d-R5), built into <prefix> beside the lgpl ffmpeg already there (build-ffmpeg.sh).
# shairport sync gets airplay 2, the pipe backend, metadata with its pipe, the native d-bus interface, this avahi and
# openssl, and no alsa, stdout, pulseaudio, pipewire, soxr, mqtt or convolution. nqptp (gpl-2) is built unmodified:
# autoreconf, configure, make, nothing patched. each one's license texts and a SOURCE pointer land in
# <prefix>/share/licenses/<name>, which the runtime ships, beside the very source files nqptp (gpl-2) and avahi (lgpl)
# were built from: nqptp's tarball and avahi's source package
#
# environment: AIRPLAY_SOURCES keeps the tarballs and source package files between runs (downloaded when absent,
# always checked against their pins)
set -eu

prefix=${1:?usage: build-airplay.sh <prefix> <pins>}
pins=${2:?usage: build-airplay.sh <prefix> <pins>}

# a fixed directory: the compiler writes it into the debug info that the build id is a hash of, so a random one would
# give the stripped binaries a new build id on every build
work=/tmp/build-airplay
rm -rf "$work"
mkdir "$work"
trap 'rm -rf "$work"' EXIT
sources=${AIRPLAY_SOURCES:-$work/sources}
mkdir -p "$sources"

fail() {
  echo "build-airplay: $*" >&2
  exit 1
}

# download FILE URL: FILE, from URL unless an earlier run kept it
download() {
  [ ! -f "$1" ] || return 0
  curl --fail --silent --show-error --location --retry 5 --retry-all-errors --proto '=https' --proto-redir '=https' \
    --output "$1.part" "$2" || fail "cannot download $2"
  mv "$1.part" "$1"
}

# fetch NAME: its pinned tarball, checked and unpacked into $work/NAME, and licenses/NAME/SOURCE pointing at it
fetch() {
  line=$(awk -v name="$1" '$1 == "source" && $2 == name' "$pins")
  [ -n "$line" ] || fail "$pins has no source pin for $1"
  # shellcheck disable=SC2086
  set -- $line
  name=$2 version=$3 commit=$4 url=$5 sha256=$6
  tarball=$sources/$name-$version.tar.gz
  download "$tarball" "$url"
  echo "$sha256  $tarball" | sha256sum --check --strict --quiet - || fail "$name's tarball does not match its pin"
  # github's archive names the tagged commit in its pax header
  gzip -dc "$tarball" | head -c 1024 | grep -aq "comment=$commit" || fail "$name's tarball is not of $commit"
  mkdir "$work/$name"
  tar -xzf "$tarball" -C "$work/$name" --strip-components=1
  mkdir -p "$prefix/share/licenses/$name"
  printf '%s\n' "$name $version, built unmodified from the upstream release tarball" "url: $url" \
    "tag: $version (commit $commit)" "sha256: $sha256" >"$prefix/share/licenses/$name/SOURCE"
}

export PKG_CONFIG_PATH="$prefix/lib/pkgconfig" LD_LIBRARY_PATH="$prefix/lib"
mkdir -p "$prefix/bin"

fetch nqptp
# gpl-2 section 3(a): the complete source goes with the program, as the tarball its pin names
install -m 0644 "$tarball" "$prefix/share/licenses/nqptp/${tarball##*/}"
echo "the tarball itself: ${tarball##*/}, beside this file" >>"$prefix/share/licenses/nqptp/SOURCE"
cd "$work/nqptp"
autoreconf -fi
./configure
make -j"$(nproc)"
install -s -m 0755 nqptp "$prefix/bin/nqptp"
install -m 0644 LICENSE "$prefix/share/licenses/nqptp/LICENSE"

# avahi: upstream's v0.8 tarball with debian's patches, the fixes upstream never released among them. it runs as root,
# and where its pid file, socket and service files are is compiled in, with no option to move them: under this fixed
# private directory, never the host's /run/avahi-daemon or /etc/avahi (audiod creates its run/ before avahi-daemon)
avahi=/tmp/cadence-airplay/avahi
awk '$1 == "debsrc" && $2 == "avahi"' "$pins" >"$work/avahi.pins"
[ -s "$work/avahi.pins" ] || fail "$pins has no debsrc pins for avahi"
licenses="$prefix/share/licenses/avahi"
mkdir -p "$licenses"
echo "avahi, debian trixie's source package: upstream's v0.8 release tarball (the orig tarball) with debian's" \
  "patches (debian/patches/series), configured here to run as root from $avahi. each of its files, named below," \
  "ships beside this file" >"$licenses/SOURCE"
dsc=
while read -r _ _ version path sha256; do
  file=$sources/${path##*/}
  download "$file" "https://snapshot.debian.org/archive/$path"
  echo "$sha256  $file" | sha256sum --check --strict --quiet - || fail "${path##*/} does not match its pin"
  echo "$version: https://snapshot.debian.org/archive/$path sha256 $sha256" >>"$licenses/SOURCE"
  install -m 0644 "$file" "$licenses/${path##*/}"
  case $file in *.dsc) dsc=$file ;; esac
done <"$work/avahi.pins"
[ -n "$dsc" ] || fail "$pins pins no .dsc for avahi"
# the .dsc's checksums cover the tarballs it names once more; its signature has no keyring here, the pins stand for it
dpkg-source -x "$dsc" "$work/avahi"
cd "$work/avahi"
autoreconf -fi
./configure --prefix="$prefix" --sysconfdir="$avahi/etc" --localstatedir="$avahi" --runstatedir="$avahi/run" \
  --with-distro=none --with-avahi-user=root --with-avahi-group=root --with-avahi-priv-access-group=root \
  --with-xml=expat --with-systemdsystemunitdir=no --enable-dbus --enable-libdaemon --disable-nls --disable-glib \
  --disable-gobject --disable-libevent --disable-qt3 --disable-qt4 --disable-qt5 --disable-gtk --disable-gtk3 \
  --disable-python --disable-mono --disable-monodoc --disable-autoipd --disable-core-docs --disable-manpages \
  --disable-xmltoman --disable-tests --disable-compat-libdns_sd --disable-compat-howl --disable-gdbm --disable-dbm
# libtool would write <prefix>/lib into each binary as a search path of its own: the runtime's loader gets lib/ instead
sed -i -e 's|^hardcode_libdir_flag_spec=.*|hardcode_libdir_flag_spec=""|' \
  -e 's|^runpath_var=LD_RUN_PATH|runpath_var=CADENCE_NO_RUNPATH|' libtool
make -j"$(nproc)" avahi_runtime_dir="$avahi/run" avahi_socket="$avahi/run/socket"
make install-strip avahi_runtime_dir="$avahi/run" avahi_socket="$avahi/run/socket"
# install put avahi's sample configuration there: the builder keeps none of it
rm -rf /tmp/cadence-airplay
install -m 0644 LICENSE "$licenses/LICENSE"
install -m 0644 debian/copyright "$licenses/copyright"

fetch shairport-sync
cd "$work/shairport-sync"
# against the avahi just built, the only one here
export CPPFLAGS="-I$prefix/include" LDFLAGS="-L$prefix/lib"
autoreconf -fi
./configure --with-airplay-2 --with-pipe --with-metadata --with-metadata-pipe --with-dbus-interface --with-avahi \
  --with-ssl=openssl
make -j"$(nproc)"
install -s -m 0755 shairport-sync "$prefix/bin/shairport-sync"
licenses="$prefix/share/licenses/shairport-sync"
install -m 0644 LICENSES COPYING "$licenses/"
install -m 0644 tinyhttp/LICENSE "$licenses/tinyhttp-LICENSE"
# LICENSES points at these files' header comments for the full notices of what they carry: copied as they are
for notice in alac.c:alac pair_ap/pair.c:pair_ap pair_ap/evrtsp/rtsp.c:evrtsp; do
  sed -n '1,/^ \*\//p' "${notice%%:*}" >"$licenses/${notice#*:}-NOTICE"
  grep -qi 'copyright' "$licenses/${notice#*:}-NOTICE" || fail "${notice%%:*} has no copyright notice"
done
