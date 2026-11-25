# the builders of mpv with the ao_cadence patch (media/third_party/mpv, owner decision 5): deploy/builder/mpv.sh builds
# a stage's image, then runs deploy/builder/mpv-build.sh in it with the docker volume cadence-mpv-cache, where each
# build's outputs (and android's static dependencies) are kept by the hash of their recipe. every source is fetched
# here by mpv-fetch.sh from media/third_party/mpv/mpv.pins; apt comes from the snapshot the base image was made from
# (plain http: apt checks the signed InRelease and every hash). ffmpeg is never compiled
ARG SNAPSHOT=20261104T212339Z

# deb: debian's own mpv source packages rebuilt with the patch as a quilt patch, for the image's cpu (the pi's arm64,
# or amd64 for sim-tv): trixie's 0.40.0-3+deb13u1 and forky's 0.41.0-2, whose build-depends are a subset of
# trixie's, so both build against trixie's -dev packages
FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS deb
ARG SNAPSHOT
RUN set -e; grep -q "snapshot.debian.org/archive/debian/$SNAPSHOT" /etc/apt/sources.list.d/debian.sources; \
  sed -i "s|^URIs: http://deb.debian.org/\(debian\(-security\)\?\)\$|URIs: http://snapshot.debian.org/archive/\1/$SNAPSHOT|" \
    /etc/apt/sources.list.d/debian.sources; \
  ! grep -q deb.debian.org /etc/apt/sources.list.d/debian.sources; \
  printf '%s\n' 'Acquire::Check-Valid-Until "false";' 'Acquire::Retries "5";' >/etc/apt/apt.conf.d/90snapshot; \
  apt-get update; \
  apt-get install --yes --no-install-recommends binutils ca-certificates curl dpkg-dev fakeroot quilt xz-utils; \
  rm -rf /var/lib/apt/lists/*
COPY media/third_party/mpv/mpv.pins /w/mpv.pins
COPY deploy/builder/mpv-fetch.sh /w/mpv-fetch.sh
RUN set -e; mkdir -p /src; cd /src; \
  bash /w/mpv-fetch.sh /w/mpv.pins /tmp/dl mpv-deb-0.40-dsc mpv-deb-0.40-orig mpv-deb-0.40-debian \
    mpv-deb-0.41-dsc mpv-deb-0.41-orig mpv-deb-0.41-debian; \
  cp /tmp/dl/mpv-deb-0.40-dsc mpv_0.40.0-3+deb13u1.dsc; cp /tmp/dl/mpv-deb-0.40-orig mpv_0.40.0.orig.tar.gz; \
  cp /tmp/dl/mpv-deb-0.40-debian mpv_0.40.0-3+deb13u1.debian.tar.xz; \
  cp /tmp/dl/mpv-deb-0.41-dsc mpv_0.41.0-2.dsc; cp /tmp/dl/mpv-deb-0.41-orig mpv_0.41.0.orig.tar.gz; \
  cp /tmp/dl/mpv-deb-0.41-debian mpv_0.41.0-2.debian.tar.xz; rm -rf /tmp/dl; \
  dpkg-source -x mpv_0.40.0-3+deb13u1.dsc mpv-0.40; dpkg-source -x mpv_0.41.0-2.dsc mpv-0.41; \
  apt-get update; apt-get build-dep --yes --no-install-recommends ./mpv-0.40 ./mpv-0.41; \
  rm -rf /var/lib/apt/lists/*

# android: the ndk's host tools are x86_64 linux alone, so this stage is amd64 (emulated on an arm64 build machine);
# the same ndk, by the same hashes, as libairplay.Dockerfile's
FROM --platform=linux/amd64 debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2 AS ndk
ARG SNAPSHOT
RUN set -e; grep -q "snapshot.debian.org/archive/debian/$SNAPSHOT" /etc/apt/sources.list.d/debian.sources; \
  sed -i "s|^URIs: http://deb.debian.org/\(debian\(-security\)\?\)\$|URIs: http://snapshot.debian.org/archive/\1/$SNAPSHOT|" \
    /etc/apt/sources.list.d/debian.sources; \
  ! grep -q deb.debian.org /etc/apt/sources.list.d/debian.sources; \
  printf '%s\n' 'Acquire::Check-Valid-Until "false";' 'Acquire::Retries "5";' >/etc/apt/apt.conf.d/90snapshot; \
  apt-get update; \
  apt-get install --yes --no-install-recommends bzip2 ca-certificates cmake curl git make ninja-build perl \
    pkg-config unzip; \
  rm -rf /var/lib/apt/lists/*
ARG NDK=r29
ARG NDK_SHA256=4abbbcdc842f3d4879206e9695d52709603e52dd68d3c1fff04b3b5e7a308ecf
ARG NDK_SHA1=87e2bb7e9be5d6a1c6cdf5ec40dd4e0c6d07c30b
RUN set -e; curl --fail --silent --show-error --location --retry 5 --retry-all-errors --output /tmp/ndk.zip \
    "https://dl.google.com/android/repository/android-ndk-$NDK-linux.zip"; \
  echo "$NDK_SHA256  /tmp/ndk.zip" | sha256sum --check --strict -; \
  echo "$NDK_SHA1  /tmp/ndk.zip" | sha1sum --check --strict -; \
  unzip -q /tmp/ndk.zip -d /opt; rm /tmp/ndk.zip; mv "/opt/android-ndk-$NDK" /opt/ndk; \
  grep -qx 'Pkg.Revision = 29.0.14206865' /opt/ndk/source.properties
ENV ANDROID_NDK_ROOT=/opt/ndk
ENV PATH=/opt/ndk/toolchains/llvm/prebuilt/linux-x86_64/bin:$PATH

# what the aar's dependency builds need beside the ndk: meson (its pinned wheel), autotools for libass and
# libunibreak, gperf for fontconfig, python for libplacebo's and harfbuzz's generators
FROM ndk AS android
RUN apt-get update \
  && apt-get install --yes --no-install-recommends autoconf automake gperf libtool patch python3 python3-venv \
    xz-utils \
  && rm -rf /var/lib/apt/lists/*
COPY media/third_party/mpv/mpv.pins /w/mpv.pins
COPY deploy/builder/mpv-fetch.sh /w/mpv-fetch.sh
RUN set -e; bash /w/mpv-fetch.sh /w/mpv.pins /tmp/dl meson; \
  python3 -m venv /opt/meson; cp /tmp/dl/meson /tmp/meson-1.12.1-py3-none-any.whl; \
  /opt/meson/bin/pip install --quiet --no-index /tmp/meson-1.12.1-py3-none-any.whl; \
  ln -s /opt/meson/bin/meson /usr/local/bin/meson; rm -rf /tmp/dl /tmp/meson-*.whl; meson --version
RUN bash /w/mpv-fetch.sh /w/mpv.pins /src libmpv-aar libunibreak lua mpv ffmpeg freetype fribidi harfbuzz libxml2 \
    fontconfig libass libplacebo \
  && mkdir /src/aar && unzip -q /src/libmpv-aar -d /src/aar
