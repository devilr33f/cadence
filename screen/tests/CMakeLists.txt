find_package(Catch2 3.16 REQUIRED)
include(Catch)

add_library(cadence_screen_test_support STATIC support/fake-cadenced.cpp support/env.cpp)
target_include_directories(cadence_screen_test_support PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
target_link_libraries(cadence_screen_test_support PUBLIC cadence_screen_ipc Catch2::Catch2)
target_compile_definitions(cadence_screen_test_support PUBLIC
  CADENCE_SCREEN_FIXTURES="${CADENCE_SCREEN_FIXTURES}")

# labels: unit (pure), ipc (sockets and the golden frames), gl (sdl offscreen + mesa), mpv (libmpv, ao=null), app
function(screen_test name label)
  add_executable(${name}.test ${ARGN})
  target_link_libraries(${name}.test PRIVATE cadence_screen_app cadence_screen_test_support cadence_screen_options
    Catch2::Catch2WithMain)
  # pre_test: listing the tests never runs a cross-built binary at build time
  catch_discover_tests(${name}.test TEST_PREFIX "${name}: " DISCOVERY_MODE PRE_TEST PROPERTIES LABELS ${label})
endfunction()

screen_test(unit unit
  unit/audio-test.cpp
  unit/failure-test.cpp
  unit/frame-test.cpp
  unit/home-answer-test.cpp
  unit/keys-test.cpp
  unit/log-test.cpp
  unit/mirror-test.cpp
  unit/motion-test.cpp
  unit/mpv-options-test.cpp
  unit/mpv-settings-test.cpp
  unit/options-test.cpp
  unit/pcm-ring-test.cpp
  unit/perf-test.cpp
  unit/ring-offer-test.cpp
  unit/tap-client-test.cpp
  unit/video-fit-test.cpp
  unit/video-follow-test.cpp
  unit/video-plane-test.cpp
  unit/platform-test.cpp)
# failure-test reads routes/core/failure.h, which parses the server's error body with nlohmann's json
target_include_directories(unit.test SYSTEM PRIVATE ${CADENCE_MEDIA_DIR}/third_party)
screen_test(ipc ipc ipc/codec-test.cpp ipc/client-test.cpp ipc/convert-test.cpp)
screen_test(gl gl gl/backend-test.cpp)
screen_test(mpv mpv mpv/player-test.cpp)
screen_test(app app app/app-test.cpp)

# the binary itself: up with no cadenced to talk to, retrying the link, then a clean exit (teardown order included)
add_test(NAME cadence-screen-smoke
  COMMAND cadence-screen --socket /nonexistent/screen.sock --driver offscreen --size 320x180 --ao null --hwdec no
    --no-vsync --exit-after-ms 1500)
set_tests_properties(cadence-screen-smoke PROPERTIES LABELS app PASS_REGULAR_EXPRESSION "exit presented=[1-9]")
