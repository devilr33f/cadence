#!/bin/sh
# the android-tv apk's debug key, outside the repo: made once at <path> (its directory 0700, the key 0600) with keytool
# from the builder's pinned jdk image, then kept, so every later apk installs over the last. password android, alias
# androiddebugkey, as android's own debug keys; it signs debug builds alone
# usage: keystore.sh <path>
set -eu
[ $# -eq 1 ] || { echo "usage: keystore.sh <path>" >&2; exit 2; }
path=$1
[ -f "$path" ] && exit 0
dir=$(dirname "$path")
mkdir -p "$dir"
chmod 700 "$dir"
dir=$(cd "$dir" && pwd)
docker run --rm --platform linux/amd64 --user "$(id -u):$(id -g)" -v "$dir:/k" \
  eclipse-temurin:17-jdk-noble@sha256:1bedb04434bd275ef072355e9caec2a22be5240fbe82ad51a59323f2bbcab4a6 \
  keytool -genkeypair -keystore "/k/$(basename "$path")" -storetype PKCS12 -storepass android -keypass android \
  -alias androiddebugkey -keyalg RSA -keysize 2048 -validity 10000 -dname "CN=Cadence debug, O=cadence" >/dev/null 2>&1
chmod 600 "$path"
echo "keystore: made $path"
