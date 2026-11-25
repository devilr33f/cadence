find_package(Catch2 3.16 REQUIRED)
include(Catch)

# any further arguments are the libraries the suite links; LABELS <label>... labels its tests for ctest -L
function(cadence_media_test name)
  cmake_parse_arguments(PARSE_ARGV 1 suite "" "" "LABELS")
  add_executable(${name}.test ${name}.test.cpp)
  target_include_directories(${name}.test PRIVATE ${CMAKE_CURRENT_SOURCE_DIR})
  target_link_libraries(${name}.test PRIVATE ${suite_UNPARSED_ARGUMENTS} cadence_media_core cadence_media_options
    Catch2::Catch2WithMain)
  if(suite_LABELS)
    catch_discover_tests(${name}.test TEST_PREFIX "${name}: " PROPERTIES LABELS "${suite_LABELS}")
  else()
    catch_discover_tests(${name}.test TEST_PREFIX "${name}: ")
  endif()
endfunction()

add_library(cadence_media_fake_stock STATIC support/fake-stock.cpp)
target_include_directories(cadence_media_fake_stock PUBLIC .)
target_link_libraries(cadence_media_fake_stock
  PUBLIC cadence_media_audio Catch2::Catch2
  PRIVATE cadence_media_options)

cadence_media_test(text)
cadence_media_test(spotter-layout cadence_media_audio)
cadence_media_test(wake-gate cadence_media_audio)

cadence_media_test(stock-paths cadence_media_fake_stock)
cadence_media_test(stock-spotter cadence_media_fake_stock)
cadence_media_test(stock-vqe cadence_media_fake_stock)

# the keyword model's runner on our own tiny model (kws/make_tiny_model.py), which the test reads beside it
if(CADENCE_MEDIA_KWS)
  cadence_media_test(kws-model cadence_media_kws cadence_media_tflm)
  target_compile_definitions(kws-model.test
    PRIVATE CADENCE_KWS_TINY_MODEL="${CMAKE_CURRENT_SOURCE_DIR}/kws/tiny.tflite")
  # our int8 kernels against tflm's reference ones, byte for byte (the neon path on an aarch64 host)
  cadence_media_test(kws-kernels cadence_media_kws cadence_media_tflm)
  # the frontend against the oracle's vectors (kws/frontend_ref.py, kws/vectors/README.md)
  cadence_media_test(kws-frontend cadence_media_kws)
  target_compile_definitions(kws-frontend.test PRIVATE CADENCE_KWS_VECTORS="${CMAKE_CURRENT_SOURCE_DIR}/kws/vectors")
  # the spotter's calls and policy on a scripted model, and on the runner with the tiny model
  cadence_media_test(kws-spotter cadence_media_kws)
  target_compile_definitions(kws-spotter.test
    PRIVATE CADENCE_KWS_TINY_MODEL="${CMAKE_CURRENT_SOURCE_DIR}/kws/tiny.tflite")
endif()

cadence_media_test(frame-buffer cadence_media_capture)
cadence_media_test(playback-monitor cadence_media_capture)
cadence_media_test(audio-engine cadence_media_capture cadence_media_fake_stock)
target_sources(audio-engine.test PRIVATE support/engine-harness.cpp support/fake-pcm.cpp)

cadence_media_test(opus-codec cadence_media_opus)
cadence_media_test(music-codec cadence_media_opus)
cadence_media_test(ogg-opus cadence_media_opus)

cadence_media_test(jitter-buffer cadence_media_playback)
cadence_media_test(jitter-buffer-queue cadence_media_playback)
cadence_media_test(mixer cadence_media_playback)
cadence_media_test(earcons cadence_media_playback)
cadence_media_test(alarm-tones cadence_media_playback)
cadence_media_test(alsa-player cadence_media_playback)
target_sources(alsa-player.test PRIVATE support/fake-sink.cpp)
cadence_media_test(alsa-player-device cadence_media_playback)
target_sources(alsa-player-device.test PRIVATE support/fake-sink.cpp)

# android's aaudio backends against a scripted aaudio (support/fake-aaudio), on every host
cadence_media_test(aaudio-playback cadence_media_aaudio cadence_media_playback)
target_sources(aaudio-playback.test PRIVATE support/fake-aaudio.cpp)
cadence_media_test(aaudio-capture cadence_media_aaudio)
target_sources(aaudio-capture.test PRIVATE support/fake-aaudio.cpp)

# the alsa devices, which a build without alsa leaves out
if(CADENCE_MEDIA_ALSA)
  cadence_media_test(alsa-capture cadence_media_alsa)
  cadence_media_test(alsa-playback cadence_media_alsa)
  cadence_media_test(alsa-mixer cadence_media_alsa ALSA::ALSA)
endif()

cadence_media_test(dsp-eq cadence_media_dsp Threads::Threads)
cadence_media_test(dsp-limiter cadence_media_dsp)
cadence_media_test(dsp-duck cadence_media_dsp)
cadence_media_test(dsp-gain-ramp cadence_media_dsp)
cadence_media_test(dsp-dither cadence_media_dsp)
cadence_media_test(dsp-fft cadence_media_dsp)
cadence_media_test(dsp-viz cadence_media_dsp)
cadence_media_test(dsp-resampler cadence_media_dsp)
cadence_media_test(sync-clock cadence_media_sync)
cadence_media_test(sync-stream cadence_media_sync)
cadence_media_test(sync-playout cadence_media_sync)
# one leader and two members over the loopback, sim clocks and fake dacs: every output measured against its due time
cadence_media_test(sync-group cadence_media_sync)
# the clock master and the stream leader on two devices: in step, the master gone, the leader's clock alone gone
cadence_media_test(sync-clock-split cadence_media_sync)
cadence_media_test(sync-calibration cadence_media_calibration)
# recorded members' bounded analysis waits, with explicit timestamps rather than real waits
cadence_media_test(sync-calibration-deadline cadence_media_sync)
# coded probes, identity, in-recording differencing and robust rounds against deterministic synthetic rooms
cadence_media_test(sync-calibration-v2 cadence_media_calibration cadence_media_json)
target_sources(sync-calibration-v2.test PRIVATE support/room-sim.cpp support/calibration-fixtures.cpp)
target_compile_definitions(sync-calibration-v2.test PRIVATE
  CADENCE_CALIBRATION_FIXTURES="${CMAKE_CURRENT_SOURCE_DIR}/fixtures/calibration")
# the ultrasonic band: its spectrum, complex baseband detector and music-masked arrivals at 48 khz
cadence_media_test(sync-calibration-ultrasonic cadence_media_calibration)
target_sources(sync-calibration-ultrasonic.test PRIVATE support/room-sim.cpp)
# six one-round cycles, robustly combined, smoothed and gated into bounded correction suggestions
cadence_media_test(sync-tracking cadence_media_calibration)
# a leader and two members calibrated over the loopback: fake dacs, a room that adds latencies the playheads miss
cadence_media_test(sync-group-calibration cadence_media_sync)
# the leader's auto-tune, pure: latency, bitrate and start wait decided from events on an injected clock
cadence_media_test(sync-group-tuner cadence_media_sync)
# a member's drop diagnostics, pure: the reason tokens, the per-cause rate limit, the 30 s summary
cadence_media_test(sync-group-diagnostics cadence_media_sync)

cadence_media_test(pcmframe cadence_media_pcmframe)

# the websocket server and the fake soloist (support/), then the websocket client against that server
add_subdirectory(support)
cadence_media_test(ws-codec cadence_media_ws)
cadence_media_test(ws-client cadence_media_ws_server)

# the d-bus client: its wire format, authentication and connection against fakes, then against a real dbus-daemon. the
# builder's test stage installs debian's, the one the airplay runtime ships, and names it; without one those tests skip
find_program(CADENCE_DBUS_DAEMON dbus-daemon)
cadence_media_test(dbus-marshal cadence_media_dbus)
cadence_media_test(dbus-message cadence_media_dbus)
cadence_media_test(dbus-auth cadence_media_dbus)
cadence_media_test(dbus-connection cadence_media_dbus Threads::Threads)
cadence_media_test(dbus-bus cadence_media_dbus)
target_compile_definitions(dbus-bus.test PRIVATE
  CADENCE_DBUS_DAEMON="$<$<BOOL:${CADENCE_DBUS_DAEMON}>:${CADENCE_DBUS_DAEMON}>")

# the libpulse shim, driven through its c api alone against a fake server, and the exact set it exports
foreach(suite pulse-shim-mainloop pulse-shim-context pulse-shim-stream pulse-shim-write pulse-shim-played)
  cadence_media_test(${suite} cadence_pulse cadence_media_pcmframe Threads::Threads)
endforeach()
add_test(NAME pulse-shim-exports
  COMMAND ${CMAKE_COMMAND} -DNM=${CMAKE_NM} -DREADELF=${CMAKE_READELF} -DLIBRARY=$<TARGET_FILE:cadence_pulse>
    -DSYMBOLS=${PROJECT_SOURCE_DIR}/pulse-shim/symbols.txt -P ${PROJECT_SOURCE_DIR}/pulse-shim/check-exports.cmake)

cadence_media_test(pcm-input cadence_media_backend Threads::Threads)
cadence_media_test(pcm-backend cadence_media_backend)
cadence_media_test(pcm-snapshots cadence_media_backend Threads::Threads)
cadence_media_test(ring-input cadence_media_ring)
cadence_media_test(alsa-player-backend cadence_media_playback cadence_media_file)
target_sources(alsa-player-backend.test PRIVATE support/fake-sink.cpp)

cadence_media_test(wav cadence_media_file)
cadence_media_test(file-capture cadence_media_file)
cadence_media_test(file-output cadence_media_file cadence_media_playback)

cadence_media_test(ipc-fixtures cadence_media_ipc)
target_compile_definitions(ipc-fixtures.test PRIVATE CADENCE_AUDIOD_FIXTURES="${CADENCE_AUDIOD_FIXTURES}")

cadence_media_test(framing cadence_audiod_core)
cadence_media_test(log cadence_audiod_core)
cadence_media_test(loop cadence_audiod_core)
cadence_media_test(ipc cadence_audiod_core)
cadence_media_test(embedded cadence_audiod_embedded)
add_dependencies(ipc.test cadence-audiod)
target_compile_definitions(ipc.test PRIVATE CADENCE_AUDIOD_BINARY="$<TARGET_FILE:cadence-audiod>")
cadence_media_test(prompts cadence_audiod_core)
cadence_media_test(playback cadence_audiod_core)
target_sources(playback.test PRIVATE support/fake-sink.cpp)
# trace marks: the queue, and a stream's and a capture's marks over the socket (none in a CADENCE_TRACING=OFF build)
cadence_media_test(trace cadence_audiod_core)
# the children's lines: made into lines, levels, cuts and the rate, then over the socket with LogsSet's switch
cadence_media_test(child-log cadence_audiod_core)
cadence_media_test(child-logs cadence_audiod_core)
cadence_media_test(ring-sequence cadence_audiod_core)
cadence_media_test(playback-ring cadence_audiod_core)
target_sources(playback-ring.test PRIVATE support/fake-sink.cpp)
cadence_media_test(playback-media cadence_audiod_core)
# android's STREAM_MUSIC as the level, through the app's hook: Ready, the VolumeSet's write and the system's own moves
cadence_media_test(playback-system-volume cadence_audiod_core)
cadence_media_test(media-settings cadence_audiod_core)
cadence_media_test(media-sessions cadence_audiod_core)
# bluetooth receive: the pcm fd over the socket, and the receiver as the sessions' source over a pipe
cadence_media_test(ipc-fds cadence_audiod_core)
cadence_media_test(bluetooth-source cadence_audiod_core)
cadence_media_test(group-source cadence_audiod_core)
cadence_media_test(group-calibrate cadence_audiod_core cadence_media_file)
cadence_media_test(external-sources cadence_audiod_core)
cadence_media_test(tap-server cadence_audiod_core)
cadence_media_test(group-clock cadence_audiod_core)
# the screen's ring read into a group leader's feed (P2), on a loopback group
cadence_media_test(ring-pump cadence_audiod_core)
cadence_media_test(ring-offer cadence_audiod_core)
cadence_media_test(media-overlay cadence_audiod_core)
# the server, and end to end with pulse-tone, a client of the real shim as soloist is
add_executable(pulse-tone support/pulse-tone.cpp)
target_link_libraries(pulse-tone PRIVATE cadence_pulse cadence_media_options)
cadence_media_test(soloist-pcm cadence_audiod_core cadence_media_pcmframe)
add_dependencies(soloist-pcm.test pulse-tone)
target_compile_definitions(soloist-pcm.test PRIVATE CADENCE_PULSE_TONE="$<TARGET_FILE:pulse-tone>")
cadence_media_test(capture cadence_audiod_core cadence_media_file)
cadence_media_test(listen cadence_audiod_core cadence_media_file)
# capture through the vendor engines: the fake stock library in the test's own process, and as the probe child either
# fake-vendor-probe (the same probe over the fake, scripted to crash or hang) or cadence-audiod itself
cadence_media_test(command-words cadence_audiod_core)
add_executable(fake-vendor-probe support/fake-vendor-probe.cpp)
target_link_libraries(fake-vendor-probe PRIVATE cadence_audiod_core cadence_media_fake_stock cadence_media_options)
cadence_media_test(capture-engine cadence_audiod_core cadence_media_fake_stock cadence_media_file)
target_sources(capture-engine.test PRIVATE support/engine-harness.cpp support/fake-pcm.cpp)
add_dependencies(capture-engine.test fake-vendor-probe cadence-audiod)
target_compile_definitions(capture-engine.test PRIVATE CADENCE_FAKE_VENDOR_PROBE="$<TARGET_FILE:fake-vendor-probe>"
  CADENCE_AUDIOD_BINARY="$<TARGET_FILE:cadence-audiod>")
# capture through the board's keyword model: a scripted model through the loader seam, and the tiny tflm model
if(CADENCE_MEDIA_KWS)
  cadence_media_test(keyword-capture cadence_audiod_core cadence_media_file)
  target_sources(keyword-capture.test PRIVATE support/fake-pcm.cpp)
  target_compile_definitions(keyword-capture.test
    PRIVATE CADENCE_KWS_TINY_MODEL="${CMAKE_CURRENT_SOURCE_DIR}/kws/tiny.tflite")
endif()

# soloist's resolv socket and the preload that asks it (the preload compiled in, so every getaddrinfo in the test is
# the preload's, and its fallback the libc one below it: the preload's own test forks its server off for that)
cadence_media_test(soloist-resolv cadence_audiod_core)
target_sources(soloist-resolv.test PRIVATE ../pulse-shim/resolv-preload.cpp)
target_include_directories(soloist-resolv.test PRIVATE ../lib/src)
# soloist's websocket: its events and commands, and the connection against the test server
cadence_media_test(soloist-wire cadence_audiod_core)
cadence_media_test(soloist-control cadence_audiod_core cadence_media_ws_server)
# what soloist prints: its output, matched for login failed alone, and its version line
cadence_media_test(soloist-output cadence_audiod_core)
cadence_media_test(soloist-version cadence_audiod_core)
# what a start is made of: the key check, the command line and the jail's sources through realpath
cadence_media_test(soloist-launch cadence_audiod_core)
# rule 7 for media senders: v1's guard, echoes, coalescer and soloist session
cadence_media_test(sender-volume cadence_audiod_core)
# android's STREAM_MUSIC as the level: the level/index mapping, echoes of the system's own moves, the control
cadence_media_test(system-volume cadence_audiod_core)
# soloist supervised with the fake, unjailed: a jailed build runs the fake through the real jail in
# soloist-jail-fake below, which needs the privileged run
if(NOT CADENCE_MEDIA_SOLOIST_JAIL)
  foreach(suite soloist-supervisor soloist-handlers soloist-source)
    cadence_media_test(${suite} cadence_audiod_core)
    add_dependencies(${suite}.test fake-soloist)
    target_compile_definitions(${suite}.test PRIVATE CADENCE_FAKE_SOLOIST="$<TARGET_FILE:fake-soloist>")
  endforeach()
else()
  cadence_media_test(soloist-jail-fake cadence_audiod_core LABELS privileged)
  add_dependencies(soloist-jail-fake.test fake-soloist cadence_pulse)
  target_compile_definitions(soloist-jail-fake.test PRIVATE CADENCE_FAKE_SOLOIST="$<TARGET_FILE:fake-soloist>"
    CADENCE_PULSE_LIBRARY="$<TARGET_FILE:cadence_pulse>")
endif()

# the airplay receiver: its configs, files, timing checks, pipe, metadata and session state, then supervised over fakes
# of avahi, nqptp and shairport (support/fake-airplay.cpp) and the real dbus-daemon, unjailed in every build, and as the
# media session's source with shairport's d-bus service stood in for by the test (support/fake-shairport.h), and by the
# e2e's fake shairport (support/fake-shairport.cpp)
if(CADENCE_MEDIA_AIRPLAY)
  add_executable(fake-airplay support/fake-airplay.cpp)
  target_link_libraries(fake-airplay PRIVATE cadence_media_dbus cadence_media_options)
  cadence_media_test(airplay-config cadence_audiod_core)
  cadence_media_test(airplay-files cadence_audiod_core)
  cadence_media_test(airplay-pipe cadence_audiod_core)
  cadence_media_test(airplay-metadata cadence_audiod_core)
  cadence_media_test(airplay-session cadence_audiod_core)
  cadence_media_test(airplay-volume cadence_audiod_core)
  foreach(suite airplay-supervisor airplay-handlers airplay-source)
    cadence_media_test(${suite} cadence_audiod_core)
    add_dependencies(${suite}.test fake-airplay)
    target_compile_definitions(${suite}.test PRIVATE CADENCE_FAKE_AIRPLAY="$<TARGET_FILE:fake-airplay>"
      CADENCE_DBUS_DAEMON="$<$<BOOL:${CADENCE_DBUS_DAEMON}>:${CADENCE_DBUS_DAEMON}>")
  endforeach()
  add_dependencies(airplay-source.test fake-shairport)
  target_compile_definitions(airplay-source.test PRIVATE CADENCE_FAKE_SHAIRPORT="$<TARGET_FILE:fake-shairport>")
  # leaving avahi and nqptp out needs the sim's configuration (M4d-D8)
  add_test(NAME airplay-lan-gate
    COMMAND ${CMAKE_COMMAND} -S ${PROJECT_SOURCE_DIR} -B ${CMAKE_CURRENT_BINARY_DIR}/airplay-lan-gate
      -G ${CMAKE_GENERATOR} -DCADENCE_MEDIA_AIRPLAY_LAN=OFF -DCADENCE_MEDIA_SIM=OFF)
  set_tests_properties(airplay-lan-gate PROPERTIES
    PASS_REGULAR_EXPRESSION "CADENCE_MEDIA_AIRPLAY_LAN=OFF needs CADENCE_MEDIA_SIM=ON")
endif()

# the screens' receiver (CADENCE_MEDIA_AIRPLAY=uxplay): the placement on the mixer's clock, the mirror stream, the pin
# and paired senders, the decoders (alac round-tripped through apple's own encoder), the session as a fake sender
# drives it, libairplay itself on loopback (its rtsp and dnssd records), and the part over the socket and the tap.
# uxplay-tests builds them alone (deploy/builder/media-host.Dockerfile's screens configuration runs them)
if(CADENCE_MEDIA_AIRPLAY_RECEIVER STREQUAL "uxplay")
  set(uxplay_suites airplay-uxplay-sync airplay-uxplay-splice airplay-uxplay-mirror airplay-uxplay-pin airplay-uxplay-decoder
    airplay-uxplay-session airplay-uxplay-receiver airplay-uxplay-handlers)
  foreach(suite IN LISTS uxplay_suites)
    cadence_media_test(${suite} cadence_audiod_core)
  endforeach()
  target_sources(airplay-uxplay-decoder.test PRIVATE ${CADENCE_ALAC_TREE}/codec/ALACEncoder.cpp
    ${CADENCE_ALAC_TREE}/codec/ag_enc.c ${CADENCE_ALAC_TREE}/codec/dp_enc.c ${CADENCE_ALAC_TREE}/codec/matrix_enc.c)
  set_source_files_properties(${CADENCE_ALAC_TREE}/codec/ALACEncoder.cpp ${CADENCE_ALAC_TREE}/codec/ag_enc.c
    ${CADENCE_ALAC_TREE}/codec/dp_enc.c ${CADENCE_ALAC_TREE}/codec/matrix_enc.c
    TARGET_DIRECTORY airplay-uxplay-decoder.test
    PROPERTIES COMPILE_OPTIONS "-w;-Wno-error" COMPILE_DEFINITIONS TARGET_RT_LITTLE_ENDIAN=1)
  add_custom_target(uxplay-tests)
  foreach(suite IN LISTS uxplay_suites)
    add_dependencies(uxplay-tests ${suite}.test)
  endforeach()
endif()

# soloist's jail: its plan, its checks and the scratch need no privilege
cadence_media_test(soloist-jail-plan cadence_audiod_core)
cadence_media_test(soloist-jail-scratch cadence_audiod_core)
# what the jail runs as soloist in the suites below: static, as the jail's root holds no libraries
add_executable(jail-probe support/jail-probe.cpp)
target_link_libraries(jail-probe PRIVATE cadence_media_options)
target_link_options(jail-probe PRIVATE -static)
# through the real jail these need root with CAP_SYS_ADMIN: they skip without it and carry the label make
# media-test-privileged runs; a jail-off build runs them unjailed and unprivileged
if(CADENCE_MEDIA_SOLOIST_JAIL)
  set(jail_labels LABELS privileged)
endif()
foreach(suite soloist-jail-process soloist-jail-sandbox)
  cadence_media_test(${suite} cadence_audiod_core ${jail_labels})
  add_dependencies(${suite}.test jail-probe)
  target_compile_definitions(${suite}.test PRIVATE CADENCE_JAIL_PROBE="$<TARGET_FILE:jail-probe>")
endforeach()
# a jail-off configuration needs the sim's (M4c-D4)
add_test(NAME soloist-jail-gate
  COMMAND ${CMAKE_COMMAND} -S ${PROJECT_SOURCE_DIR} -B ${CMAKE_CURRENT_BINARY_DIR}/jail-gate -G ${CMAKE_GENERATOR}
    -DCADENCE_MEDIA_SOLOIST_JAIL=OFF -DCADENCE_MEDIA_SIM=OFF)
set_tests_properties(soloist-jail-gate PROPERTIES
  PASS_REGULAR_EXPRESSION "CADENCE_MEDIA_SOLOIST_JAIL=OFF needs CADENCE_MEDIA_SIM=ON")

# the url source, which a build without ffmpeg leaves out
if(CADENCE_MEDIA_URL)
  # an http or https server, and the test-only certificates https verifies against
  add_library(cadence_media_http_server STATIC support/http-server.cpp support/test-certificates.cpp)
  target_include_directories(cadence_media_http_server PUBLIC .)
  target_link_libraries(cadence_media_http_server
    PUBLIC Threads::Threads
    PRIVATE OpenSSL::SSL OpenSSL::Crypto cadence_media_options)

  cadence_media_test(url-ffmpeg PkgConfig::FFMPEG)
  cadence_media_test(url-options cadence_media_url)
  cadence_media_test(url-icy cadence_media_url)
  cadence_media_test(url-convert cadence_media_url)
  # a ring's url: ahead of the reads, kept or fetched again, its fixed failures
  cadence_media_test(url-ring cadence_media_url cadence_media_http_server)
  cadence_media_test(url-log cadence_media_url cadence_media_http_server)
  cadence_media_test(url-exit cadence_audiod_core cadence_media_http_server)
  add_dependencies(url-exit.test cadence-audiod)
  target_compile_definitions(url-exit.test PRIVATE CADENCE_AUDIOD_BINARY="$<TARGET_FILE:cadence-audiod>")
  foreach(suite url-source url-overlay url-tls)
    cadence_media_test(${suite} cadence_media_url cadence_media_http_server)
    target_compile_definitions(${suite}.test PRIVATE CADENCE_MEDIA_TEST_FIXTURES="${CMAKE_CURRENT_SOURCE_DIR}/fixtures")
  endforeach()
  # multiroom's part over the socket: a leader on the real url source and a member, two audiods in one process
  cadence_media_test(group-handlers cadence_media_url cadence_media_http_server)
  # the sim's build logs the alignment probe (group_heard), a device's never
  target_compile_definitions(group-handlers.test PRIVATE
    CADENCE_MEDIA_TEST_FIXTURES="${CMAKE_CURRENT_SOURCE_DIR}/fixtures" $<$<BOOL:${CADENCE_MEDIA_SIM}>:CADENCE_MEDIA_SIM>)
endif()
