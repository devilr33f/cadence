# cmake toolchain for midi's audiod, cross-built by device.Dockerfile's midi-cross stage: zig-armhf.sh as the
# compilers, and every library and header from the stage's armhf prefixes, never the build machine's
set(CMAKE_SYSTEM_NAME Linux)
set(CMAKE_SYSTEM_PROCESSOR arm)
set(CMAKE_C_COMPILER armhf-cc)
set(CMAKE_CXX_COMPILER armhf-c++)
set(CMAKE_AR /usr/local/bin/armhf-ar)
set(CMAKE_RANLIB /usr/local/bin/armhf-ranlib)
set(CMAKE_FIND_ROOT_PATH /opt/armhf /opt/openssl /opt/ffmpeg)
set(CMAKE_FIND_ROOT_PATH_MODE_PROGRAM NEVER)
set(CMAKE_FIND_ROOT_PATH_MODE_LIBRARY ONLY)
set(CMAKE_FIND_ROOT_PATH_MODE_INCLUDE ONLY)
set(CMAKE_FIND_ROOT_PATH_MODE_PACKAGE ONLY)
