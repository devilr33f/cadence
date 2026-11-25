#!/bin/sh
# zig-armhf.sh cc|c++ [arguments...]: zig's clang for midi's armhf userland, whose glibc is 2.30 (firmware
# 0.369.1.46.4793639783.20261020): zig links against stubs of exactly that glibc and brings its own libc++, so
# nothing it builds needs a newer symbol. installed as armhf-cc and armhf-c++ by device.Dockerfile's midi-cross stage.
# lld ignores LD_RUN_PATH, which gnu ld turns into a runpath when it links (build-ffmpeg.sh sets it to $ORIGIN), so
# the wrapper passes it on itself, to a link alone. -mcpu takes zig's spelling (cortex_a7: armv7-a, vfpv4, neon; midi's
# cortex-a53 runs it in aarch32)
set -eu
tool=${1:?usage: zig-armhf.sh cc|c++ [arguments...]}
shift
link=1
for argument in "$@"; do
  case $argument in -c | -E | -S | -M | -MM) link=0 ;; esac
done
if [ "$link" = 1 ] && [ -n "${LD_RUN_PATH:-}" ]; then
  set -- "$@" "-Wl,-rpath,$LD_RUN_PATH"
fi
exec zig "$tool" -target arm-linux-gnueabihf.2.30 -mcpu=cortex_a7 "$@"
