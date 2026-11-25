#!/bin/sh
# zig-aarch64.sh cc|c++ [arguments...]: zig's clang for the vk capsula mini's aarch64 userland, for glibc 2.28: the
# probed firmware has 2.33, other firmwares of the board 2.28, and zig links against stubs of exactly the glibc named
# and brings its own libc++, so nothing it builds needs a newer symbol. installed as aarch64-cc and aarch64-c++ by
# device.Dockerfile's vk-tools stage. lld ignores LD_RUN_PATH, which gnu ld turns into a runpath when it links
# (build-ffmpeg.sh sets it to $ORIGIN), so the wrapper passes it on itself, to a link alone
set -eu
tool=${1:?usage: zig-aarch64.sh cc|c++ [arguments...]}
shift
link=1
for argument in "$@"; do
  case $argument in -c | -E | -S | -M | -MM) link=0 ;; esac
done
if [ "$link" = 1 ] && [ -n "${LD_RUN_PATH:-}" ]; then
  set -- "$@" "-Wl,-rpath,$LD_RUN_PATH"
fi
exec zig "$tool" -target aarch64-linux-gnu.2.28 -mcpu=cortex_a53 "$@"
