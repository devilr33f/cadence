cmake_minimum_required(VERSION 3.25)

project(cadence_media LANGUAGES C CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)
set(CMAKE_COMPILE_WARNING_AS_ERROR ON)

set(CADENCE_AUDIOD_FIXTURES "${CMAKE_CURRENT_SOURCE_DIR}/../proto/fixtures/audiod/v1" CACHE PATH "golden audiod frames")
set(CADENCE_AUDIOD_VERSION "dev" CACHE STRING
  "version cadence-audiod reports, at most 31 characters (27 with a sim-only switch, which appends -sim)")
option(CADENCE_MEDIA_TESTS "Build the host test suite" ON)
# device builds play and capture through alsa; the sim builds without it, on the file backends only
option(CADENCE_MEDIA_ALSA "Build the alsa capture, playback and mixer backends" ON)
# soloist always runs jailed on a device; only the sim, nonroot without CAP_SYS_ADMIN and with a fake soloist, may
# run it unjailed (M4c-D4) — and the android app, whose audiod runs in the app's own process where no namespace can
# be made: it runs soloist plain, through the runtime's loader (media/audiod/soloist-launch.cpp: planPlain)
option(CADENCE_MEDIA_SIM "Build for the sim image" OFF)
option(CADENCE_MEDIA_SOLOIST_JAIL "Run soloist in its jail" ON)
if(NOT CADENCE_MEDIA_SOLOIST_JAIL AND NOT CADENCE_MEDIA_SIM AND NOT ANDROID)
  message(FATAL_ERROR "CADENCE_MEDIA_SOLOIST_JAIL=OFF needs CADENCE_MEDIA_SIM=ON or an android build")
endif()
# soloist (spotify connect, M4c) and its supervisor; a screen runs none (TV plan, stream F: android has no mount
# namespace for an app, and screens cast through libairplay), so it builds without them
option(CADENCE_MEDIA_SOLOIST "Build the soloist source" ON)
if(NOT CADENCE_MEDIA_SOLOIST AND CADENCE_MEDIA_TESTS)
  message(FATAL_ERROR "CADENCE_MEDIA_SOLOIST=OFF is a device build: it needs CADENCE_MEDIA_TESTS=OFF")
endif()
# the fake soloist the sim image's e2e serves from its fake cdn (tests/support): a test program, for the sim alone
option(CADENCE_MEDIA_FAKE_SOLOIST "Build the e2e's fake soloist" OFF)
if(CADENCE_MEDIA_FAKE_SOLOIST AND NOT CADENCE_MEDIA_SIM)
  message(FATAL_ERROR "CADENCE_MEDIA_FAKE_SOLOIST=ON needs CADENCE_MEDIA_SIM=ON")
endif()
# the airplay receiver: shairport (M4d: audiod runs shairport sync, nqptp, avahi and a private dbus-daemon from the
# board's runtime, and reads shairport's pipe; the sim builds it too, though it cannot run the real receiver, M4d-D8),
# uxplay (the screens' libairplay, media/third_party/uxplay.cmake) or off. ON and OFF are the older spellings of
# shairport and off, which the speakers' builds still pass
set(CADENCE_MEDIA_AIRPLAY "shairport" CACHE STRING "The airplay receiver: shairport, uxplay or off")
set_property(CACHE CADENCE_MEDIA_AIRPLAY PROPERTY STRINGS shairport uxplay off)
string(TOLOWER "${CADENCE_MEDIA_AIRPLAY}" cadence_airplay)
if(cadence_airplay MATCHES "^(on|1|yes|true|shairport)$")
  set(CADENCE_MEDIA_AIRPLAY_RECEIVER shairport)
elseif(cadence_airplay MATCHES "^(off|0|no|false|uxplay)$")
  set(CADENCE_MEDIA_AIRPLAY_RECEIVER ${cadence_airplay})
  if(NOT cadence_airplay STREQUAL "uxplay")
    set(CADENCE_MEDIA_AIRPLAY_RECEIVER off)
  endif()
else()
  message(FATAL_ERROR "CADENCE_MEDIA_AIRPLAY=${CADENCE_MEDIA_AIRPLAY}: shairport, uxplay or off")
endif()
# the shairport receiver's sources and tests build under if(CADENCE_MEDIA_AIRPLAY): this directory's variable
# shadows the cache's string with that boolean
if(CADENCE_MEDIA_AIRPLAY_RECEIVER STREQUAL "shairport")
  set(CADENCE_MEDIA_AIRPLAY ON)
else()
  set(CADENCE_MEDIA_AIRPLAY OFF)
endif()
# the audio backend by name (the distro presets): alsa, sim (the file backends alone) or aaudio (android, stream F's);
# empty leaves CADENCE_MEDIA_ALSA as given
set(CADENCE_MEDIA_AUDIO_BACKEND "" CACHE STRING "The audio backend: alsa, sim or aaudio (empty: CADENCE_MEDIA_ALSA)")
set_property(CACHE CADENCE_MEDIA_AUDIO_BACKEND PROPERTY STRINGS alsa sim aaudio)
set(CADENCE_MEDIA_AAUDIO OFF)
if(CADENCE_MEDIA_AUDIO_BACKEND STREQUAL "aaudio")
  if(NOT ANDROID)
    message(FATAL_ERROR "CADENCE_MEDIA_AUDIO_BACKEND=aaudio builds with the ndk alone (the host tests run the aaudio backend against a fake on any build)")
  endif()
  set(CADENCE_MEDIA_AAUDIO ON)
endif()
if(CADENCE_MEDIA_AUDIO_BACKEND STREQUAL "alsa")
  set(CADENCE_MEDIA_ALSA ON CACHE BOOL "" FORCE)
elseif(CADENCE_MEDIA_AUDIO_BACKEND MATCHES "^(sim|aaudio)$")
  set(CADENCE_MEDIA_ALSA OFF CACHE BOOL "" FORCE)
elseif(NOT CADENCE_MEDIA_AUDIO_BACKEND STREQUAL "")
  message(FATAL_ERROR "CADENCE_MEDIA_AUDIO_BACKEND=${CADENCE_MEDIA_AUDIO_BACKEND}: alsa, sim or aaudio")
endif()
# avahi and nqptp face the lan (multicast dns; ptp on ports 319 and 320), which the sim has none of: only a sim build
# may leave them out and run the bus and shairport alone (M4d-D8)
option(CADENCE_MEDIA_AIRPLAY_LAN "Run avahi and nqptp beside shairport sync" ON)
if(NOT CADENCE_MEDIA_AIRPLAY_LAN AND NOT CADENCE_MEDIA_SIM)
  message(FATAL_ERROR "CADENCE_MEDIA_AIRPLAY_LAN=OFF needs CADENCE_MEDIA_SIM=ON")
endif()
# the fake shairport sync the sim image's e2e runs as the receiver (tests/support): a test program, for the sim alone
option(CADENCE_MEDIA_FAKE_SHAIRPORT "Build the e2e's fake shairport sync" OFF)
if(CADENCE_MEDIA_FAKE_SHAIRPORT AND NOT (CADENCE_MEDIA_SIM AND CADENCE_MEDIA_AIRPLAY))
  message(FATAL_ERROR "CADENCE_MEDIA_FAKE_SHAIRPORT=ON needs CADENCE_MEDIA_SIM=ON and CADENCE_MEDIA_AIRPLAY=ON")
endif()
# mpv's ao_cadence ring as a screen session's way into the mixer (route c): audiod makes a ring for every MIXER external
# session once the patched mpv ships (stream E); until then those sessions are heard through no ring of audiod's. the
# ring reader itself is always built and tested
option(CADENCE_MEDIA_AO_RING "Make a pcm ring for every MIXER external session" OFF)
# the keyword engine (lib/src/kws) runs a board's own wake model with tensorflow lite micro, which only the builds that
# use it fetch and build (media/cmake/tflm.cmake): the vk capsula mini's and the host tests
option(CADENCE_MEDIA_KWS "Build the keyword engine on tensorflow lite micro" OFF)
# audiod's trace marks (TraceMark) to cadenced, which makes them events of a turn's spans. off, the marks compile
# away and nothing is queued or sent; the proto and WakeEvent.at_unix_ns stay as they are
option(CADENCE_TRACING "Send audiod's trace marks to cadenced" ON)
# soloist's runtime takes the libpulse shim alone, which deploy/builder/assemble-soloist-runtime.sh builds for each cpu:
# it needs none of the libraries further down, so a cross build needs none of that cpu's
option(CADENCE_MEDIA_PULSE_ONLY "Build only the libpulse shim" OFF)

find_package(Threads REQUIRED)
# libairplay for the uxplay receiver, the screens' (its audiod side: audiod/airplay-uxplay-*, further down)
if(CADENCE_MEDIA_AIRPLAY_RECEIVER STREQUAL "uxplay")
  include(${CMAKE_CURRENT_SOURCE_DIR}/third_party/uxplay.cmake)
endif()

add_library(cadence_media_options INTERFACE)
target_compile_options(cadence_media_options INTERFACE
  -Wall -Wextra -Wpedantic -Wshadow -Wconversion -Wsign-conversion)

# the frames on soloist's pcm socket, which the libpulse shim (a shared library) links too
add_library(cadence_media_pcmframe STATIC lib/src/pcmframe/codec.cpp)
target_include_directories(cadence_media_pcmframe PUBLIC lib/src)
set_target_properties(cadence_media_pcmframe PROPERTIES POSITION_INDEPENDENT_CODE ON)
target_link_libraries(cadence_media_pcmframe PRIVATE cadence_media_options)

add_subdirectory(pulse-shim)
if(CADENCE_MEDIA_PULSE_ONLY)
  return()
endif()

if(CADENCE_MEDIA_ALSA)
  find_package(ALSA REQUIRED)
endif()
find_package(PkgConfig REQUIRED)
pkg_check_modules(OPUS REQUIRED IMPORTED_TARGET opus)
pkg_check_modules(SPEEXDSP REQUIRED IMPORTED_TARGET speexdsp)
# urls play through a minimal lgpl ffmpeg (deploy/builder/build-ffmpeg.sh), shared, found by pkg-config in its prefix.
# the versions are 9.0.2's, so a distribution's ffmpeg (gpl, other versions) is never linked by mistake; they move
# with the script's pin
option(CADENCE_MEDIA_URL "Build the url source on ffmpeg" ON)
# a screen's audiod: screens play urls with mpv, so it carries no ffmpeg (TV plan, Dependencies)
option(CADENCE_SCREEN "Build for a screen (display.screen)" OFF)
if(CADENCE_SCREEN AND CADENCE_MEDIA_URL)
  message(FATAL_ERROR "CADENCE_SCREEN=ON needs CADENCE_MEDIA_URL=OFF")
endif()
if(CADENCE_MEDIA_URL)
  pkg_check_modules(FFMPEG REQUIRED IMPORTED_TARGET libavformat=63.1.102 libavcodec=63.1.102 libavutil=61.1.102)
  # the openssl 3 ffmpeg's tls uses, which audiod keeps from cleaning up at exit
  find_package(OpenSSL 3 REQUIRED)
endif()

# the sim image runs on distroless: opus, speexdsp and the c++ runtime are linked in, only glibc stays dynamic
option(CADENCE_MEDIA_STATIC "Link libopus, libspeexdsp, libstdc++ and libgcc statically" OFF)
add_library(cadence_media_opus_dep INTERFACE)
add_library(cadence_media_speexdsp_dep INTERFACE)
if(CADENCE_MEDIA_STATIC)
  find_library(CADENCE_OPUS_STATIC NAMES libopus.a REQUIRED)
  target_include_directories(cadence_media_opus_dep INTERFACE ${OPUS_INCLUDE_DIRS})
  target_link_libraries(cadence_media_opus_dep INTERFACE ${CADENCE_OPUS_STATIC} m)
  find_library(CADENCE_SPEEXDSP_STATIC NAMES libspeexdsp.a REQUIRED)
  target_include_directories(cadence_media_speexdsp_dep INTERFACE ${SPEEXDSP_INCLUDE_DIRS})
  target_link_libraries(cadence_media_speexdsp_dep INTERFACE ${CADENCE_SPEEXDSP_STATIC} m)
  add_link_options(-static-libstdc++ -static-libgcc)
else()
  target_link_libraries(cadence_media_opus_dep INTERFACE PkgConfig::OPUS)
  target_link_libraries(cadence_media_speexdsp_dep INTERFACE PkgConfig::SPEEXDSP)
endif()

add_library(cadence_media_core STATIC lib/src/text.cpp)
target_include_directories(cadence_media_core PUBLIC lib/src)
target_link_libraries(cadence_media_core PRIVATE cadence_media_options)

# trace marks: the stages, a queue any thread pushes into without allocating or waiting, and the clock
add_library(cadence_media_trace STATIC lib/src/trace/trace.cpp lib/src/trace/trace-queue.cpp)
target_include_directories(cadence_media_trace PUBLIC lib/src)
target_compile_definitions(cadence_media_trace PUBLIC CADENCE_TRACING=$<BOOL:${CADENCE_TRACING}>)
target_link_libraries(cadence_media_trace PRIVATE cadence_media_options)

add_library(cadence_media_audio STATIC
  lib/src/audio/spotter-layout.cpp
  lib/src/audio/stock-api.cpp
  lib/src/audio/stock-combined-spotter.cpp
  lib/src/audio/stock-error.cpp
  lib/src/audio/stock-library.cpp
  lib/src/audio/stock-multi-conf.cpp
  lib/src/audio/stock-paths.cpp
  lib/src/audio/stock-symbols.cpp
  lib/src/audio/wake-gate.cpp)
target_include_directories(cadence_media_audio PUBLIC lib/src)
target_link_libraries(cadence_media_audio
  PUBLIC cadence_media_core ${CMAKE_DL_LIBS}
  PRIVATE cadence_media_options)

add_library(cadence_media_capture STATIC
  lib/src/capture/audio-engine.cpp
  lib/src/capture/capture-format.cpp
  lib/src/capture/frame-buffer.cpp
  lib/src/capture/playback-monitor.cpp)
target_include_directories(cadence_media_capture PUBLIC lib/src)
target_link_libraries(cadence_media_capture
  PUBLIC cadence_media_audio Threads::Threads
  PRIVATE cadence_media_options)

# the keyword engine: the frontend (its fft from cadence_media_dsp), the spotter and the model's runner on tflm
# (cadence_media_tflm, whose headers stay out of this library's interface)
if(CADENCE_MEDIA_KWS)
  include(${CMAKE_CURRENT_SOURCE_DIR}/cmake/tflm.cmake)
  add_library(cadence_media_kws STATIC
    lib/src/kws/frontend.cpp
    lib/src/kws/spotter.cpp
    lib/src/kws/tflm-add.cpp
    lib/src/kws/tflm-arena.cpp
    lib/src/kws/tflm-depthwise.cpp
    lib/src/kws/tflm-interface.cpp
    lib/src/kws/tflm-model.cpp
    lib/src/kws/tflm-ops.cpp)
  target_include_directories(cadence_media_kws PUBLIC lib/src)
  target_link_libraries(cadence_media_kws
    PUBLIC cadence_media_core cadence_media_dsp
    PRIVATE cadence_media_tflm cadence_media_options)
endif()

add_library(cadence_media_opus STATIC lib/src/audio/opus-codec.cpp lib/src/audio/music-codec.cpp lib/src/audio/ogg-opus.cpp)
target_include_directories(cadence_media_opus PUBLIC lib/src)
target_link_libraries(cadence_media_opus
  PUBLIC cadence_media_core
  PRIVATE cadence_media_opus_dep cadence_media_options)

# the pcm backend's signal processing: float stereo at 48 kHz, nothing allocated or locked per block
add_library(cadence_media_dsp STATIC
  lib/src/dsp/biquad.cpp
  lib/src/dsp/dither.cpp
  lib/src/dsp/duck.cpp
  lib/src/dsp/eq.cpp
  lib/src/dsp/fft.cpp
  lib/src/dsp/gain-ramp.cpp
  lib/src/dsp/limiter.cpp
  lib/src/dsp/resampler.cpp
  lib/src/dsp/splicer.cpp
  lib/src/dsp/viz.cpp)
target_include_directories(cadence_media_dsp PUBLIC lib/src)
target_link_libraries(cadence_media_dsp PRIVATE cadence_media_speexdsp_dep cadence_media_options)

# a minimal rfc 6455 client for soloist's websocket on the loopback: no tls, no threads of its own
add_library(cadence_media_ws STATIC
  lib/src/ws/base64.cpp
  lib/src/ws/client.cpp
  lib/src/ws/frame.cpp
  lib/src/ws/handshake.cpp
  lib/src/ws/sha1.cpp)
target_include_directories(cadence_media_ws PUBLIC lib/src)
target_link_libraries(cadence_media_ws
  PUBLIC cadence_media_core
  PRIVATE cadence_media_options)

# a minimal d-bus client (M4d-D3) for the private bus audiod runs for shairport sync, written from the freedesktop.org
# specification alone: sasl external on a unix socket, no fd passing, no threads of its own
add_library(cadence_media_dbus STATIC
  lib/src/dbus/auth.cpp
  lib/src/dbus/bus.cpp
  lib/src/dbus/client.cpp
  lib/src/dbus/connection.cpp
  lib/src/dbus/io.cpp
  lib/src/dbus/marshal.cpp
  lib/src/dbus/message.cpp)
target_include_directories(cadence_media_dbus PUBLIC lib/src)
target_link_libraries(cadence_media_dbus
  PUBLIC cadence_media_core
  PRIVATE cadence_media_options)

# nlohmann/json 3.12.0 (vendored unmodified, mit) for soloist's events: parsed with exceptions off, never converted
# implicitly
file(SHA256 "${CMAKE_CURRENT_SOURCE_DIR}/third_party/nlohmann/json.hpp" cadence_json_sha256)
if(NOT cadence_json_sha256 STREQUAL "aaf127c04cb31c406e5b04a63f1ae89369fccde6d8fa7cdda1ed4f32dfc5de63")
  message(FATAL_ERROR "third_party/nlohmann/json.hpp is not the vendored 3.12.0 release")
endif()
add_library(cadence_media_json INTERFACE)
target_include_directories(cadence_media_json SYSTEM INTERFACE third_party)
target_compile_definitions(cadence_media_json INTERFACE JSON_USE_IMPLICIT_CONVERSIONS=0)

# the playback thread's float chain and the inputs music sources push into
add_library(cadence_media_backend STATIC
  lib/src/backend/pcm-backend.cpp
  lib/src/backend/pcm-input.cpp
  lib/src/backend/pcm-snapshots.cpp
  lib/src/backend/presentation-clock.cpp)
target_include_directories(cadence_media_backend PUBLIC lib/src)
target_link_libraries(cadence_media_backend
  PUBLIC cadence_media_core cadence_media_dsp
  PRIVATE cadence_media_options)

# a screen player's pcm ring (mpv's ao_cadence), read by the backend as its external input
# a group's acoustic calibration, pure: the chirp, the correlation, the arrivals and the solver (no clock or thread)
add_library(cadence_media_calibration STATIC
  lib/src/sync/calibration.cpp
  lib/src/sync/calibration/detect.cpp
  lib/src/sync/calibration/estimate.cpp
  lib/src/sync/calibration/fft.cpp
  lib/src/sync/calibration/plan.cpp
  lib/src/sync/calibration/probe.cpp
  lib/src/sync/calibration/solve.cpp
  lib/src/sync/calibration/track.cpp)
target_include_directories(cadence_media_calibration PUBLIC lib/src)
target_link_libraries(cadence_media_calibration PRIVATE cadence_media_options)

# the group clock of multiroom playback: a leader answers its members' probes, each member fits the leader's clock
add_library(cadence_media_sync STATIC
  lib/src/sync/sha256.cpp
  lib/src/sync/clock-wire.cpp
  lib/src/sync/clock-estimator.cpp
  lib/src/sync/clock-map.cpp
  lib/src/sync/clock-socket.cpp
  lib/src/sync/clock-server.cpp
  lib/src/sync/clock-client.cpp
  lib/src/sync/stream-wire.cpp
  lib/src/sync/stream-socket.cpp
  lib/src/sync/stream-leader.cpp
  lib/src/sync/stream-member.cpp
  lib/src/sync/group-placer.cpp
  lib/src/sync/group-diagnostics.cpp
  lib/src/sync/group-playout.cpp
  lib/src/sync/group-member.cpp
  lib/src/sync/group-leader.cpp
  lib/src/sync/group-tuner.cpp
  lib/src/sync/group-tracking.cpp
  lib/src/sync/calibration-run.cpp)
target_include_directories(cadence_media_sync PUBLIC lib/src)
target_link_libraries(cadence_media_sync
  PUBLIC cadence_media_core cadence_media_ipc cadence_media_opus cadence_media_backend cadence_media_playback cadence_media_calibration
    Threads::Threads
  PRIVATE cadence_media_options)

add_library(cadence_media_ring STATIC lib/src/ring/ring-input.cpp)
target_include_directories(cadence_media_ring PUBLIC lib/src)
target_link_libraries(cadence_media_ring
  PUBLIC cadence_media_core cadence_media_dsp
  PRIVATE cadence_media_options)

add_library(cadence_media_playback STATIC
  lib/src/playback/alarm-tones.cpp
  lib/src/playback/alsa-player.cpp
  lib/src/playback/earcons.cpp
  lib/src/playback/jitter-buffer.cpp
  lib/src/playback/mixer.cpp
  lib/src/playback/playback-format.cpp
  lib/src/playback/thread-priority.cpp)
target_include_directories(cadence_media_playback PUBLIC lib/src)
target_link_libraries(cadence_media_playback
  PUBLIC cadence_media_backend cadence_media_trace Threads::Threads
  PRIVATE cadence_media_core cadence_media_options)

# the alsa devices behind the capture and playback interfaces, and the board's hardware volume control
if(CADENCE_MEDIA_ALSA)
  add_library(cadence_media_alsa STATIC
    lib/src/capture/alsa-capture.cpp
    lib/src/playback/alsa-mixer.cpp
    lib/src/playback/alsa-playback.cpp)
  target_include_directories(cadence_media_alsa PUBLIC lib/src)
  target_link_libraries(cadence_media_alsa
    PUBLIC cadence_media_capture cadence_media_playback
    PRIVATE cadence_media_core ALSA::ALSA cadence_media_options)
endif()

# wav file backends for the sim: no alsa, only the interface headers
add_library(cadence_media_file STATIC
  lib/src/file/file-capture.cpp
  lib/src/file/file-clock.cpp
  lib/src/file/file-output.cpp
  lib/src/file/wav.cpp)
target_include_directories(cadence_media_file PUBLIC lib/src)
target_link_libraries(cadence_media_file
  PUBLIC cadence_media_core
  PRIVATE cadence_media_options)

# android's aaudio behind the playback and capture interfaces (stream F): the backends over a stream seam
# (lib/src/aaudio/stream.h), built and tested on every host against a fake; the ndk's stream only on android
add_library(cadence_media_aaudio STATIC
  lib/src/aaudio/aaudio-capture.cpp
  lib/src/aaudio/aaudio-playback.cpp)
target_include_directories(cadence_media_aaudio PUBLIC lib/src)
target_link_libraries(cadence_media_aaudio
  PUBLIC cadence_media_core cadence_media_capture cadence_media_playback
  PRIVATE cadence_media_options)
if(CADENCE_MEDIA_AAUDIO)
  add_library(cadence_media_aaudio_ndk STATIC lib/src/aaudio/ndk-api.cpp)
  target_link_libraries(cadence_media_aaudio_ndk PUBLIC cadence_media_aaudio aaudio PRIVATE cadence_media_options)
endif()

# nanopb runtime (vendored, zlib) and the generated audiod ipc messages
add_library(cadence_media_ipc STATIC
  third_party/nanopb/pb_common.c
  third_party/nanopb/pb_decode.c
  third_party/nanopb/pb_encode.c
  gen/cadence/audiod/v1/audiod.pb.c
  gen/cadence/sync/v1/sync.pb.c)
target_include_directories(cadence_media_ipc SYSTEM PUBLIC third_party/nanopb gen)

# the daemon's parts, linked by cadence-audiod and the tests
add_library(cadence_audiod_core STATIC
  audiod/bluetooth-handlers.cpp
  audiod/bluetooth-receiver.cpp
  audiod/capture.cpp
  audiod/capture-engine.cpp
  audiod/listen-capture.cpp
  audiod/child-log.cpp
  audiod/child-log-reader.cpp
  audiod/child-logs.cpp
  audiod/command-words.cpp
  audiod/daemon.cpp
  audiod/external-source.cpp
  audiod/external-sources.cpp
  audiod/framing.cpp
  audiod/fs-tree.cpp
  audiod/group-handlers.cpp
  audiod/group-source.cpp
  audiod/ipc.cpp
  audiod/jailed-process.cpp
  # the jail's plan and exec, which jailed-process runs for every child (the vendor probe too), soloist or not
  audiod/soloist-jail.cpp
  audiod/log.cpp
  audiod/loop.cpp
  audiod/media-handlers.cpp
  audiod/media-overlay.cpp
  audiod/media-sessions.cpp
  audiod/media-settings.cpp
  audiod/mic-layout.cpp
  audiod/output.cpp
  audiod/playback.cpp
  audiod/prompts.cpp
  audiod/ring-sequence.cpp
  audiod/ring-offer.cpp
  audiod/ring-pump.cpp
  audiod/sender-volume.cpp
  audiod/signals.cpp
  audiod/stream-recorder.cpp
  audiod/system-volume.cpp
  audiod/tap-server.cpp
  audiod/traces.cpp
  audiod/vendor-probe.cpp)
target_include_directories(cadence_audiod_core PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
# an audiod built with a switch a device keeps off says so in the version it reports: the sim's unjailed soloist
# (android's plain-launch soloist says it too) and airplay without its lan daemons
set(cadence_audiod_version "${CADENCE_AUDIOD_VERSION}")
if(NOT CADENCE_MEDIA_SOLOIST_JAIL OR (CADENCE_MEDIA_AIRPLAY AND NOT CADENCE_MEDIA_AIRPLAY_LAN))
  string(APPEND cadence_audiod_version "-sim")
endif()
target_compile_definitions(cadence_audiod_core
  PUBLIC CADENCE_AUDIOD_VERSION="${cadence_audiod_version}"
    CADENCE_MEDIA_SOLOIST_JAIL=$<BOOL:${CADENCE_MEDIA_SOLOIST_JAIL}>
  PRIVATE $<$<BOOL:${CADENCE_MEDIA_ALSA}>:CADENCE_MEDIA_ALSA>
    # the sim's skewed group clock (CADENCE_SIM_CLOCK_OFFSET_MS, CADENCE_SIM_CLOCK_PPM), never a device's
    $<$<BOOL:${CADENCE_MEDIA_SIM}>:CADENCE_MEDIA_SIM>)
# the board's stock vqe and spotter (media/lib capture), loaded at runtime by dlopen, with the raw microphone's
# resampler beside them
target_link_libraries(cadence_audiod_core
  PUBLIC cadence_media_core cadence_media_ipc cadence_media_opus cadence_media_pcmframe cadence_media_playback
    cadence_media_trace cadence_media_file cadence_media_ws cadence_media_capture cadence_media_dsp cadence_media_ring cadence_media_sync
  PRIVATE cadence_media_json cadence_media_options)
if(CADENCE_MEDIA_ALSA)
  target_link_libraries(cadence_audiod_core PRIVATE cadence_media_alsa)
endif()
# the keyword engine for a board's own wake model: static, so a device build carries only what audiod calls of it.
# capture's keyword mode is compiled only here (CADENCE_MEDIA_KWS), so the other boards' audiod has none of it
if(CADENCE_MEDIA_KWS)
  target_sources(cadence_audiod_core PRIVATE
    audiod/capture-keyword.cpp
    audiod/keyword-capture.cpp
    audiod/keyword-model.cpp)
  target_link_libraries(cadence_audiod_core PUBLIC cadence_media_kws)
  target_compile_definitions(cadence_audiod_core PUBLIC CADENCE_MEDIA_KWS)
endif()
if(CADENCE_MEDIA_SOLOIST)
  target_sources(cadence_audiod_core PRIVATE
    audiod/soloist-control.cpp
    audiod/soloist-handlers.cpp
    audiod/soloist-launch.cpp
    audiod/soloist-output.cpp
    audiod/soloist-pcm.cpp
    audiod/soloist-resolv.cpp
    audiod/soloist-scratch.cpp
    audiod/soloist-source.cpp
    audiod/soloist-supervisor.cpp
    audiod/soloist-version.cpp
    audiod/soloist-wire.cpp)
  target_compile_definitions(cadence_audiod_core PUBLIC CADENCE_MEDIA_SOLOIST)
endif()
if(CADENCE_MEDIA_AAUDIO)
  target_link_libraries(cadence_audiod_core PUBLIC cadence_media_aaudio_ndk)
  target_compile_definitions(cadence_audiod_core PUBLIC CADENCE_MEDIA_AAUDIO)
endif()
# the screens' receiver: libairplay as an audiod source (TV wave 2, stream J). its audio decodes through apple's alac
# decoder (apache-2.0, the release tree fetched pinned by sha-256, or CADENCE_ALAC_SOURCE for an offline build) and
# the platform's aac: ffmpeg's libavcodec on linux (the system's, lgpl: aac-lc and aac-eld), AMediaCodec on android
if(CADENCE_MEDIA_AIRPLAY_RECEIVER STREQUAL "uxplay")
  set(CADENCE_ALAC_SOURCE "" CACHE PATH "an unpacked macosforge/alac tree (its codec/), instead of the pinned download")
  set(cadence_alac_commit c38887c5c5e64a4b31108733bd79ca9b2496d987)
  set(cadence_alac_sha256 98635ece42fb1c3fceb75eaa4b5164d866e09f0195b3e7ec4085f1123c5e272f)
  if(CADENCE_ALAC_SOURCE)
    set(cadence_alac "${CADENCE_ALAC_SOURCE}")
  else()
    set(cadence_alac "${CMAKE_BINARY_DIR}/alac-${cadence_alac_commit}")
    if(NOT EXISTS "${cadence_alac}/codec/ALACDecoder.cpp")
      set(cadence_alac_tarball "${CMAKE_BINARY_DIR}/alac-${cadence_alac_commit}.tar.gz")
      include(${CMAKE_CURRENT_SOURCE_DIR}/cmake/download.cmake)
      cadence_download("https://github.com/macosforge/alac/archive/${cadence_alac_commit}.tar.gz"
        "${cadence_alac_tarball}" ${cadence_alac_sha256} cadence_alac_status)
      list(GET cadence_alac_status 0 cadence_alac_code)
      if(NOT cadence_alac_code EQUAL 0)
        message(FATAL_ERROR "the alac decoder's download failed: ${cadence_alac_status}")
      endif()
      file(ARCHIVE_EXTRACT INPUT "${cadence_alac_tarball}" DESTINATION "${CMAKE_BINARY_DIR}")
    endif()
  endif()
  if(NOT EXISTS "${cadence_alac}/codec/ALACDecoder.cpp" OR NOT EXISTS "${cadence_alac}/LICENSE")
    message(FATAL_ERROR "no alac decoder in ${cadence_alac}")
  endif()
  if(CMAKE_CXX_BYTE_ORDER STREQUAL "BIG_ENDIAN")
    message(FATAL_ERROR "the alac decoder is built for little-endian cpus alone")
  endif()
  # the decoder alone (its encoder only in the tests' round trip); its EndianPortable needs the byte order named
  add_library(cadence_media_alac STATIC
    ${cadence_alac}/codec/ALACDecoder.cpp ${cadence_alac}/codec/ALACBitUtilities.c ${cadence_alac}/codec/ag_dec.c
    ${cadence_alac}/codec/dp_dec.c ${cadence_alac}/codec/matrix_dec.c ${cadence_alac}/codec/EndianPortable.c)
  target_include_directories(cadence_media_alac SYSTEM PUBLIC ${cadence_alac}/codec)
  target_compile_definitions(cadence_media_alac PRIVATE TARGET_RT_LITTLE_ENDIAN=1)
  target_compile_options(cadence_media_alac PRIVATE -w "-ffile-prefix-map=${cadence_alac}=alac")
  set_target_properties(cadence_media_alac PROPERTIES COMPILE_WARNING_AS_ERROR OFF POSITION_INDEPENDENT_CODE ON)
  set(CADENCE_ALAC_TREE "${cadence_alac}" CACHE INTERNAL "the alac tree the build uses")
  # libairplay's headers are uxplay's: none of our warnings in them
  set_target_properties(cadence_airplay PROPERTIES SYSTEM ON)

  target_sources(cadence_audiod_core PRIVATE
    audiod/airplay-files.cpp
    audiod/airplay-metadata.cpp
    audiod/airplay-volume.cpp
    audiod/airplay-uxplay-decoder.cpp
    audiod/airplay-uxplay-handlers.cpp
    audiod/airplay-uxplay-metadata.cpp
    audiod/airplay-uxplay-mirror.cpp
    audiod/airplay-uxplay-pin.cpp
    audiod/airplay-uxplay-receiver.cpp
    audiod/airplay-uxplay-session.cpp
    audiod/airplay-uxplay-source.cpp
    audiod/airplay-uxplay-sync.cpp)
  target_compile_definitions(cadence_audiod_core PUBLIC CADENCE_MEDIA_AIRPLAY_UXPLAY)
  target_link_libraries(cadence_audiod_core PUBLIC cadence::airplay cadence_media_alac cadence_media_backend)
  if(ANDROID)
    target_sources(cadence_audiod_core PRIVATE audiod/airplay-uxplay-aac-ndk.cpp)
    target_link_libraries(cadence_audiod_core PUBLIC mediandk)
  else()
    pkg_check_modules(AIRPLAY_AVCODEC REQUIRED IMPORTED_TARGET libavcodec libavutil)
    target_sources(cadence_audiod_core PRIVATE audiod/airplay-uxplay-aac-ffmpeg.cpp)
    target_link_libraries(cadence_audiod_core PUBLIC PkgConfig::AIRPLAY_AVCODEC)
  endif()
  if(CADENCE_AIRPLAY_DNSSD_AVAHI)
    target_link_libraries(cadence_audiod_core PUBLIC cadence::airplay_dnssd_avahi)
    target_compile_definitions(cadence_audiod_core PRIVATE CADENCE_AIRPLAY_DNSSD_AVAHI)
  endif()
endif()

if(CADENCE_MEDIA_AIRPLAY)
  target_sources(cadence_audiod_core PRIVATE
    audiod/airplay-config.cpp
    audiod/airplay-control.cpp
    audiod/airplay-files.cpp
    audiod/airplay-handlers.cpp
    audiod/airplay-metadata.cpp
    audiod/airplay-pipe.cpp
    audiod/airplay-session.cpp
    audiod/airplay-source.cpp
    audiod/airplay-stack.cpp
    audiod/airplay-supervisor.cpp
    audiod/airplay-timing.cpp
    audiod/airplay-volume.cpp)
  target_compile_definitions(cadence_audiod_core
    PUBLIC CADENCE_MEDIA_AIRPLAY CADENCE_MEDIA_AIRPLAY_LAN=$<BOOL:${CADENCE_MEDIA_AIRPLAY_LAN}>)
  target_link_libraries(cadence_audiod_core PUBLIC cadence_media_dbus cadence_media_backend)
endif()

# the url source: a decoder thread per url opens, demuxes and decodes it with ffmpeg into a music or overlay input
if(CADENCE_MEDIA_URL)
  add_library(cadence_media_url STATIC
    lib/src/url/ffmpeg-log.cpp
    lib/src/url/ffmpeg-options.cpp
    lib/src/url/icy.cpp
    lib/src/url/pcm-convert.cpp
    lib/src/url/ring-stream.cpp
    lib/src/url/url-input.cpp
    lib/src/url/url-source.cpp)
  target_include_directories(cadence_media_url PUBLIC lib/src)
  target_link_libraries(cadence_media_url
    PUBLIC cadence_audiod_core cadence_media_backend PkgConfig::FFMPEG Threads::Threads
    PRIVATE OpenSSL::Crypto cadence_media_options)
endif()

# the daemon's entry (audiod/run.cpp) is compiled with each host's switches: cadence-audiod, and the android app's
# in-process audiod (cadence_audiod_embedded, linked into its service's jni library)
function(cadence_audiod_entry target)
  target_link_libraries(${target} PRIVATE cadence_audiod_core cadence_media_options)
  if(CADENCE_MEDIA_AO_RING)
    target_compile_definitions(${target} PRIVATE CADENCE_MEDIA_AO_RING)
  endif()
  if(CADENCE_MEDIA_URL)
    target_link_libraries(${target} PRIVATE cadence_media_url)
    target_compile_definitions(${target} PRIVATE CADENCE_MEDIA_URL)
  endif()
endfunction()

add_executable(cadence-audiod audiod/main.cpp audiod/run.cpp)
cadence_audiod_entry(cadence-audiod)
add_library(cadence_audiod_embedded STATIC audiod/embedded.cpp audiod/run.cpp)
target_include_directories(cadence_audiod_embedded PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
set_target_properties(cadence_audiod_embedded PROPERTIES POSITION_INDEPENDENT_CODE ON)
cadence_audiod_entry(cadence_audiod_embedded)
target_link_libraries(cadence_audiod_embedded PUBLIC cadence_audiod_core)
if(CADENCE_MEDIA_URL)
  # an image puts ffmpeg's libraries in lib/ beside the binary, and that is its runpath, also in the build tree, which
  # links them into its own lib/ so the binary runs where it was built. a board build may append a directory of the
  # board's own (deploy/builder/device.Dockerfile)
  set(CADENCE_AUDIOD_RUNPATH "$ORIGIN/lib" CACHE STRING "cadence-audiod's runpath, a list starting with \$ORIGIN/lib")
  set_target_properties(cadence-audiod PROPERTIES INSTALL_RPATH "${CADENCE_AUDIOD_RUNPATH}" BUILD_WITH_INSTALL_RPATH ON)
  file(MAKE_DIRECTORY "${CMAKE_CURRENT_BINARY_DIR}/lib")
  foreach(directory IN LISTS FFMPEG_LIBRARY_DIRS)
    file(GLOB ffmpeg_libraries "${directory}/libav*.so.*" "${directory}/libsw*.so.*")
    foreach(library IN LISTS ffmpeg_libraries)
      get_filename_component(name "${library}" NAME)
      file(CREATE_LINK "${library}" "${CMAKE_CURRENT_BINARY_DIR}/lib/${name}" SYMBOLIC)
    endforeach()
  endforeach()
endif()

add_library(cadence_media INTERFACE)
target_link_libraries(cadence_media INTERFACE
  cadence_media_core
  cadence_media_trace
  cadence_media_ipc
  cadence_media_audio
  cadence_media_capture
  cadence_media_opus
  cadence_media_playback
  cadence_media_dsp
  cadence_media_backend
  cadence_media_ring
  cadence_media_aaudio
  $<$<BOOL:${CADENCE_MEDIA_ALSA}>:cadence_media_alsa>)

if(CADENCE_MEDIA_TESTS)
  enable_testing()
  add_subdirectory(tests)
elseif(CADENCE_MEDIA_FAKE_SOLOIST OR CADENCE_MEDIA_FAKE_SHAIRPORT)
  add_subdirectory(tests/support)
endif()
