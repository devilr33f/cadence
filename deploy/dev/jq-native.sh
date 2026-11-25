#!/bin/sh
# usage: jq-native.sh DIR: prints the path of jq 1.8.2's release build for this host's os and cpu under DIR, fetched
# there once and checked against its pinned sha-256 on every call; prints nothing and fails when the host has no
# pinned build or the fetch or the check fails. the e2e falls back to the pinned image then, which costs seconds a call
# on docker desktop where this costs milliseconds. JQ_RELEASE_URL is where the builds are (the tests' local copy)
set -u

[ $# = 1 ] || { echo "usage: jq-native.sh DIR" >&2; exit 2; }
dir=$1
url=${JQ_RELEASE_URL:-https://github.com/jqlang/jq/releases/download/jq-1.8.2}

case $(uname -s)-$(uname -m) in
  MINGW*-x86_64 | MSYS*-x86_64) asset=jq-windows-amd64.exe sum=a6fc67fedaf9128a3309a1e2ebb8b986aeccf70122ee46d2cb4849e423f0c627 ;;
  Linux-x86_64) asset=jq-linux-amd64 sum=b1c22172dd303f3be49e935aa56aa48a8b7a46e0bc838b4997d3bb451495870f ;;
  Linux-aarch64) asset=jq-linux-arm64 sum=8b85c817833814ddca00a144c33705546355afccf0cf39b188f3cdb48b852309 ;;
  Darwin-x86_64) asset=jq-macos-amd64 sum=e94b266e3c26690550006abe63152b782280f4e14374accdf04cbde844f00bc0 ;;
  Darwin-arm64) asset=jq-macos-arm64 sum=2d75340ba57a4b4b4c8708a21c2dc8e958a48aaa8bba13b27f77f6e4c0eca07e ;;
  *) exit 1 ;;
esac

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi 2>/dev/null | cut -d' ' -f1
}

bin=$dir/jq-1.8.2/$asset
if [ "$(sha256 "$bin")" != "$sum" ]; then
  mkdir -p "${bin%/*}" || exit 1
  rm -f "$bin" "$bin.part"
  curl -fsSL -m 120 -o "$bin.part" "$url/$asset" && [ "$(sha256 "$bin.part")" = "$sum" ] && chmod 755 "$bin.part" &&
    mv -f "$bin.part" "$bin" || { rm -f "$bin.part"; exit 1; }
fi
printf '%s\n' "$bin"
