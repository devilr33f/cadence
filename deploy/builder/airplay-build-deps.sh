#!/bin/sh
# airplay-build-deps.sh: what building the airplay runtime needs, in a debian:trixie-slim image (airplay.Dockerfile, and
# the sim image's runtime in device/Dockerfile.sim, which share these steps and their cache). apt from the snapshot the
# base image was made from (its debian.sources names it), so what the build links against is the version the runtime's
# pinned debs hold. plain http: the image has no ca bundle yet, and apt checks the signed InRelease and every hash
# anyway. a snapshot's Valid-Until has passed by design
set -eu

snapshot=20261104T212339Z
grep -q "snapshot.debian.org/archive/debian/$snapshot" /etc/apt/sources.list.d/debian.sources
printf '%s\n' 'Types: deb' "URIs: http://snapshot.debian.org/archive/debian/$snapshot" \
  'Suites: trixie trixie-updates' 'Components: main' 'Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp' '' \
  'Types: deb' "URIs: http://snapshot.debian.org/archive/debian-security/$snapshot" \
  'Suites: trixie-security' 'Components: main' 'Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp' \
  >/etc/apt/sources.list.d/debian.sources
printf '%s\n' 'Acquire::Check-Valid-Until "false";' 'Acquire::Retries "5";' >/etc/apt/apt.conf.d/90snapshot
apt-get update
apt-get install --yes --no-install-recommends \
  autoconf \
  automake \
  autopoint \
  build-essential \
  ca-certificates \
  curl \
  gettext \
  intltool \
  libconfig-dev \
  libdaemon-dev \
  libdbus-1-dev \
  libexpat1-dev \
  libgcrypt20-dev \
  libglib2.0-dev \
  libplist-dev \
  libplist-utils \
  libpopt-dev \
  libsodium-dev \
  libssl-dev \
  libsystemd-dev \
  libtool \
  nasm \
  pkg-config \
  uuid-dev \
  xxd \
  xz-utils
rm -rf /var/lib/apt/lists/*
