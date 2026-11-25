#!/bin/sh
# usage: build.sh <src> <build-dir> <prefix> [cmake args...]
# libprojectM-4 as a shared library (lgpl-2.1: linked, never static), gles 3, projectm-eval vendored, no playlist
# library (screen/render/viz keeps its own list). android: pass the ndk's toolchain file and abi; its cmake forces gles
set -eu
src=$1 build=$2 prefix=$3
shift 3
cmake -S "$src" -B "$build" -G Ninja -DCMAKE_BUILD_TYPE=Release -DENABLE_GLES=ON -DENABLE_SYSTEM_PROJECTM_EVAL=OFF \
  -DENABLE_PLAYLIST=OFF -DBUILD_SHARED_LIBS=ON -DBUILD_TESTING=OFF -DCMAKE_INSTALL_PREFIX="$prefix" "$@"
cmake --build "$build"
cmake --install "$build"
