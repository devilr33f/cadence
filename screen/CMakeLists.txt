cmake_minimum_required(VERSION 3.25)

# cadence-screen: the tv's ui process (sdl3 + gles 3 + libmpv), talking screen/v1 to cadenced
project(cadence_screen LANGUAGES C CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)
set(CMAKE_COMPILE_WARNING_AS_ERROR ON)

set(CADENCE_SCREEN_VERSION "dev" CACHE STRING "version cadence-screen reports in Hello, at most 32 characters")
set(CADENCE_MEDIA_DIR "${CMAKE_CURRENT_SOURCE_DIR}/../media" CACHE PATH "media/: the vendored nanopb and media/gen")
set(CADENCE_SCREEN_FIXTURES "${CMAKE_CURRENT_SOURCE_DIR}/../proto/fixtures/screen/v1" CACHE PATH "golden screen/v1 frames")
option(CADENCE_SCREEN_TESTS "Build the test suite" ON)
# mpv's audio output (plan item 19): cadence is the target route (the patched mpv's ao_cadence into audiod's mixer,
# stream E); stock is mpv's own output, a stopgap until that mpv lands, levelled through mpv's volume
set(CADENCE_SCREEN_AO "stock" CACHE STRING "mpv's audio output: stock or cadence")
set_property(CACHE CADENCE_SCREEN_AO PROPERTY STRINGS stock cadence)
if(NOT CADENCE_SCREEN_AO MATCHES "^(stock|cadence)$")
  message(FATAL_ERROR "CADENCE_SCREEN_AO must be stock or cadence, not ${CADENCE_SCREEN_AO}")
endif()

find_package(Threads REQUIRED)
find_package(SDL3 REQUIRED CONFIG)
find_package(PkgConfig REQUIRED)
# mpv is optional: without it the player's mpv sources are out and the sim's placeholder player is the seam (linux
# and android builds always have it)
pkg_check_modules(MPV QUIET IMPORTED_TARGET mpv)
if(TARGET PkgConfig::MPV)
  set(CADENCE_SCREEN_HAS_MPV ON)
else()
  set(CADENCE_SCREEN_HAS_MPV OFF)
endif()
if(ANDROID)
  add_library(cadence_screen_gles INTERFACE)
  target_link_libraries(cadence_screen_gles INTERFACE GLESv3 EGL)
elseif(APPLE)
  # macos (the screen-sim build) has no gles driver: desktop gl 4.1 core, the same calls (render/gl.h)
  find_package(OpenGL REQUIRED)
  add_library(cadence_screen_gles INTERFACE)
  target_link_libraries(cadence_screen_gles INTERFACE OpenGL::GL)
else()
  pkg_check_modules(GLES REQUIRED IMPORTED_TARGET glesv2)
  add_library(cadence_screen_gles INTERFACE)
  target_link_libraries(cadence_screen_gles INTERFACE PkgConfig::GLES)
endif()

add_library(cadence_screen_options INTERFACE)
target_compile_options(cadence_screen_options INTERFACE
  -Wall -Wextra -Wpedantic -Wshadow -Wconversion -Wsign-conversion)

# nanopb (vendored in media/, zlib) with malloc for the FT_POINTER fields, and the screen/v1 messages with the
# cadence.v2 and cards.v1 files they import
set(gen "${CADENCE_MEDIA_DIR}/gen")
add_library(cadence_screen_proto STATIC
  ${CADENCE_MEDIA_DIR}/third_party/nanopb/pb_common.c
  ${CADENCE_MEDIA_DIR}/third_party/nanopb/pb_decode.c
  ${CADENCE_MEDIA_DIR}/third_party/nanopb/pb_encode.c
  ${gen}/cadence/screen/v1/screen.pb.c
  ${gen}/cadence/screen/v1/media.pb.c
  ${gen}/cadence/screen/v1/ui.pb.c
  ${gen}/cadence/v2/assistant.pb.c
  ${gen}/cadence/v2/media.pb.c
  ${gen}/cadence/v2/rpc.pb.c
  ${gen}/cadence/v2/screen.pb.c
  ${gen}/cadence/v2/session.pb.c
  ${gen}/cadence/cards/v1/cards.pb.c
  ${gen}/cadence/audiod/v1/audiod.pb.c)
target_include_directories(cadence_screen_proto SYSTEM PUBLIC ${CADENCE_MEDIA_DIR}/third_party/nanopb ${gen})
target_compile_definitions(cadence_screen_proto PUBLIC PB_ENABLE_MALLOC=1)

# render: the gles backend every drawing layer uses (ui, ken burns, visualizers), frame pacing and stats
add_library(cadence_screen_render STATIC
  render/capture.cpp
  render/frame-scheduler.cpp
  render/frame-stats.cpp
  render/gles-backend.cpp
  render/perf.cpp
  render/perf-hud.cpp)
target_include_directories(cadence_screen_render PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
target_link_libraries(cadence_screen_render PUBLIC cadence_screen_gles PRIVATE cadence_screen_options)

# ipc: the screen/v1 client, its codec and the privacy-safe log
add_library(cadence_screen_ipc STATIC
  ipc/backoff.cpp
  ipc/client.cpp
  ipc/codec.cpp
  ipc/log.cpp
  ipc/scrub.cpp
  ipc/seqpacket.cpp)
target_include_directories(cadence_screen_ipc PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
target_link_libraries(cadence_screen_ipc PUBLIC cadence_screen_proto Threads::Threads PRIVATE cadence_screen_options)

# player: libmpv behind Player, the playback gate, levels and the audio route. the mpv sources are out of a build
# without libmpv (the sim's placeholder player is the seam), and macos has no audiod to tap: its tap client is a stub
set(cadence_screen_player_sources
  player/audio-control.cpp
  player/audio-route.cpp
  player/eq-filter.cpp
  player/mirror-presenter.cpp
  player/mirror-schedule.cpp
  player/mpv-options.cpp
  player/mpv-settings.cpp
  player/state-report.cpp
  player/video-fit.cpp
  player/video-follow.cpp)
if(APPLE)
  list(APPEND cadence_screen_player_sources player/tap-client-stub.cpp)
else()
  list(APPEND cadence_screen_player_sources player/pcm-ring.cpp player/ring-offer.cpp player/tap-client.cpp)
endif()
if(CADENCE_SCREEN_HAS_MPV)
  list(APPEND cadence_screen_player_sources player/mpv-player.cpp player/mpv-tracks.cpp
    player/render-api-output.cpp player/surface-embed-output.cpp)
endif()
add_library(cadence_screen_player STATIC ${cadence_screen_player_sources})
target_include_directories(cadence_screen_player PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
# the pcm ring's abi has one copy, libcadence-media's ring/cadence_ring.h (c and c++, no dependency of its own)
target_include_directories(cadence_screen_player SYSTEM PUBLIC ${CADENCE_MEDIA_DIR}/lib/src)
if(CADENCE_SCREEN_HAS_MPV)
  target_link_libraries(cadence_screen_player
    PUBLIC cadence_screen_render cadence_screen_ipc cadence_screen_proto PkgConfig::MPV
    PRIVATE cadence_screen_options)
else()
  target_link_libraries(cadence_screen_player
    PUBLIC cadence_screen_render cadence_screen_ipc cadence_screen_proto
    PRIVATE cadence_screen_options)
endif()
# the airplay mirror presenter's decoder: mediacodec on android, none yet elsewhere
if(ANDROID)
  target_sources(cadence_screen_player PRIVATE player/mirror-decoder-android.cpp)
  target_link_libraries(cadence_screen_player PUBLIC mediandk android)
else()
  target_sources(cadence_screen_player PRIVATE player/mirror-presenter-none.cpp)
endif()
if(CADENCE_SCREEN_AO STREQUAL "cadence")
  target_compile_definitions(cadence_screen_player PUBLIC CADENCE_SCREEN_AO_CADENCE=1)
endif()

# platform: sdl3, the gles context, display metrics, overscan, lifecycle and keys
add_library(cadence_screen_platform STATIC
  platform/keys.cpp
  platform/lifecycle.cpp
  platform/overscan.cpp
  platform/platform.cpp)
target_include_directories(cadence_screen_platform PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
target_link_libraries(cadence_screen_platform
  PUBLIC SDL3::SDL3 cadence_screen_gles cadence_screen_proto cadence_screen_render cadence_screen_ipc
  PRIVATE cadence_screen_options)

# app: the main loop, wiring the link to the player and the ui layer
add_library(cadence_screen_app STATIC
  app/app.cpp
  app/config-json.cpp
  app/convert.cpp
  app/options.cpp)
target_include_directories(cadence_screen_app PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
target_compile_definitions(cadence_screen_app PRIVATE CADENCE_SCREEN_VERSION="${CADENCE_SCREEN_VERSION}")
if(CADENCE_SCREEN_HAS_MPV)
  target_compile_definitions(cadence_screen_app PUBLIC CADENCE_SCREEN_HAS_MPV=1)
endif()
target_link_libraries(cadence_screen_app
  PUBLIC cadence_screen_platform cadence_screen_player cadence_screen_ipc cadence_screen_render
  PRIVATE cadence_screen_options)

# stream p's libraries: i18n (the translator rmlui's TranslateString calls), the visualizer with its gl host and ken
# burns. projectM (milkdrop) is off by default; the device builds (the android-tv apk, the rpi deb) and make
# screen-sim pass -DCADENCE_SCREEN_PROJECTM=ON with libprojectM-4 from media/third_party/projectm's scripts
set(CADENCE_SCREEN_GL ON CACHE BOOL "Build the visualizer's gles 3 host")
set(CADENCE_SCREEN_PROJECTM OFF CACHE BOOL "Build the milkdrop source against libprojectM-4")
# unconditionally: the sim's own tests (no catch2) register even when the screen's suite is out
enable_testing()
add_subdirectory(i18n)
add_subdirectory(render/viz)
add_subdirectory(render/kenburns)

# stream b's ui library, its gallery and the layer the app runs
add_subdirectory(ui)
# wave 2's screens on it
add_subdirectory(app/routes)

add_executable(cadence-screen app/main.cpp)
target_link_libraries(cadence-screen PRIVATE cadence_screen_app cadence_screen_ui cadence_screen_routes cadence_screen_options)

# the simulator (screen/sim): the real screen driven by a script against a fake cadenced in-process. off by default;
# make screen-sim builds it natively
option(CADENCE_SCREEN_SIM "Build the screen simulator (cadence-screen-sim)" OFF)
if(CADENCE_SCREEN_SIM)
  add_subdirectory(sim)
endif()

if(CADENCE_SCREEN_TESTS)
  enable_testing()
  add_subdirectory(tests)
endif()
