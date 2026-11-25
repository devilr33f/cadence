# the app's native libraries for armeabi-v7a (deploy/android/Dockerfile's native stage, ndk r29, api 30, c++_shared):
#   libmain.so            cadence-screen (screen/) as SDL3's main: the routed ui, mpv's ao_cadence into audiod's
#                         mixer (route c), the airplay mirror presenter on MediaCodec, the activity's surface hand-off
#   libcadence-daemon.so  audiod's engine in process (media/, aaudio, a pcm ring per MIXER session offered on the
#                         tap, libairplay's receiver, no url source, soloist plain: spotify's arm64 build through
#                         the runtime's loader from the app's data) and the NsdManager dnssd backend
# inputs: SDL3_DIR (the sdl3 aar's cmake/), pkg-config's path to mpv, opus and speexdsp, RmlUi's and FreeType's cmake
# packages (CMAKE_FIND_ROOT_PATH holds their prefix), CADENCE_SCREEN_TOKENS_GEN (tokens-gen for the build machine),
# CADENCE_STB_INCLUDE (stb's headers), CADENCE_AIRPLAY_OPENSSL_ROOT (include/, lib/libcrypto.a) and CADENCE_AIRPLAY_PLIST_SOURCE (libplist's tarball)
cmake_minimum_required(VERSION 3.25)
project(cadence_android LANGUAGES C CXX)

if(NOT ANDROID)
  message(FATAL_ERROR "android/native builds with the ndk's toolchain alone")
endif()
set(CADENCE_REPO "${CMAKE_CURRENT_SOURCE_DIR}/../.." CACHE PATH "the repository")
set(CADENCE_VERSION "dev" CACHE STRING "the version both report")

# sdl3 at this level, so its imported target is seen here as well as in screen/ (cmake/SDL3: the aar's prefab)
find_package(SDL3 REQUIRED CONFIG)

# cadence-screen: its own project's libraries, the tests off; mpv's ao_cadence (route c), its rings offered by
# audiod's tap in the daemon's process (@cadence-tap)
set(CADENCE_STB_INCLUDE "" CACHE PATH "stb's headers (stb/stb_image.h), which linux takes from the system")
if(NOT EXISTS "${CADENCE_STB_INCLUDE}/stb/stb_image.h")
  message(FATAL_ERROR "no stb/stb_image.h in CADENCE_STB_INCLUDE")
endif()
include_directories(SYSTEM ${CADENCE_STB_INCLUDE})
set(CADENCE_SCREEN_TESTS OFF CACHE BOOL "" FORCE)
set(CADENCE_SCREEN_AO "cadence" CACHE STRING "" FORCE)
set(CADENCE_SCREEN_VERSION "${CADENCE_VERSION}" CACHE STRING "" FORCE)
add_subdirectory(${CADENCE_REPO}/screen screen EXCLUDE_FROM_ALL)

# audiod: the android-tv preset's media options (media/CMakePresets.json): libairplay's receiver on the prefix's
# openssl and libplist, a ring for every MIXER session, and soloist run plain (no jail an app could make: through
# the runtime's loader, from the runtime the app unpacks into its own data)
set(CADENCE_MEDIA_TESTS OFF CACHE BOOL "" FORCE)
set(CADENCE_MEDIA_AUDIO_BACKEND "aaudio" CACHE STRING "" FORCE)
set(CADENCE_MEDIA_AIRPLAY "uxplay" CACHE STRING "" FORCE)
set(CADENCE_AIRPLAY_DEPS "prefix" CACHE STRING "" FORCE)
set(CADENCE_MEDIA_AO_RING ON CACHE BOOL "" FORCE)
set(CADENCE_MEDIA_URL OFF CACHE BOOL "" FORCE)
set(CADENCE_MEDIA_SOLOIST ON CACHE BOOL "" FORCE)
set(CADENCE_MEDIA_SOLOIST_JAIL OFF CACHE BOOL "" FORCE)
set(CADENCE_SCREEN ON CACHE BOOL "" FORCE)
set(CADENCE_MEDIA_SIM OFF CACHE BOOL "" FORCE)
set(CADENCE_AUDIOD_VERSION "${CADENCE_VERSION}" CACHE STRING "" FORCE)
add_subdirectory(${CADENCE_REPO}/media media EXCLUDE_FROM_ALL)

find_library(CADENCE_LOG log REQUIRED)
find_library(CADENCE_ANDROID android REQUIRED)

add_library(main SHARED android-main.cpp)
target_link_libraries(main PRIVATE cadence_screen_routes cadence_screen_ui cadence_screen_app SDL3::SDL3 ${CADENCE_LOG}
  ${CADENCE_ANDROID})
target_compile_features(main PRIVATE cxx_std_20)
target_link_options(main PRIVATE -Wl,--no-undefined -Wl,--gc-sections)

add_library(cadence-daemon SHARED daemon-jni.cpp dnssd-jni.cpp volume-jni.cpp)
target_include_directories(cadence-daemon PRIVATE ${CADENCE_REPO}/media/third_party/libairplay)
target_link_libraries(cadence-daemon PRIVATE cadence_audiod_embedded ${CADENCE_LOG})
target_compile_features(cadence-daemon PRIVATE cxx_std_20)
target_link_options(cadence-daemon PRIVATE -Wl,--no-undefined -Wl,--gc-sections)
