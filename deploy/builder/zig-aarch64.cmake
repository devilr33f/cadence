# cmake toolchain for the vk capsula mini's audiod, cross-built by device.Dockerfile's vk-cross stage: zig-aarch64.sh as
# the compilers, and every library and header from the stage's aarch64 prefixes, never the build machine's
set(CMAKE_SYSTEM_NAME Linux)
set(CMAKE_SYSTEM_PROCESSOR aarch64)
set(CMAKE_C_COMPILER aarch64-cc)
set(CMAKE_CXX_COMPILER aarch64-c++)
set(CMAKE_AR /usr/local/bin/aarch64-ar)
set(CMAKE_RANLIB /usr/local/bin/aarch64-ranlib)
set(CMAKE_FIND_ROOT_PATH /opt/aarch64 /opt/openssl /opt/ffmpeg)
set(CMAKE_FIND_ROOT_PATH_MODE_PROGRAM NEVER)
set(CMAKE_FIND_ROOT_PATH_MODE_LIBRARY ONLY)
set(CMAKE_FIND_ROOT_PATH_MODE_INCLUDE ONLY)
set(CMAKE_FIND_ROOT_PATH_MODE_PACKAGE ONLY)
