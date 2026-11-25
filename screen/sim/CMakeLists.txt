# the simulator (screen/sim): the real screen against a fake cadenced in-process, driven by a script (make
# screen-sim). built beside the screen itself; its tests need no catch2 (a plain main each), so they build wherever
# the sim does
set(sim_sources
  fake-daemon.cpp
  fake-player.cpp
  main.cpp
  names.cpp
  png.cpp
  script.cpp
  send.cpp
  sim.cpp)

# the preview fake catalog's data builders, alone: the gallery pages around them are a linux tool
add_library(cadence_screen_sim_catalog STATIC "${CMAKE_CURRENT_SOURCE_DIR}/../app/routes/preview/fake-catalog.cpp"
  "${CMAKE_CURRENT_SOURCE_DIR}/../app/routes/preview/fake-devices.cpp")
target_link_libraries(cadence_screen_sim_catalog PUBLIC cadence_screen_routes)
target_compile_options(cadence_screen_sim_catalog PRIVATE -Wno-missing-field-initializers)

# stb: the system's libstb-dev on linux, the pinned headers make screen-sim puts in build/screen-sim/deps on macos
find_path(CADENCE_SIM_STB_INCLUDE stb/stb_image_write.h
  HINTS "${CMAKE_CURRENT_SOURCE_DIR}/../../build/screen-sim/deps/include"
  REQUIRED)

add_executable(cadence-screen-sim ${sim_sources})
target_include_directories(cadence-screen-sim PRIVATE ${CMAKE_CURRENT_SOURCE_DIR}/.. ${CADENCE_SIM_STB_INCLUDE})
target_compile_definitions(cadence-screen-sim PRIVATE
  CADENCE_SIM_CARDS="${CMAKE_CURRENT_SOURCE_DIR}/../../proto/fixtures/cards/v1")
target_link_libraries(cadence-screen-sim PRIVATE
  cadence_screen_app cadence_screen_ui cadence_screen_routes cadence_screen_sim_catalog cadence_screen_options)

enable_testing()
add_executable(sim-parser-test tests/parser-test.cpp script.cpp)
target_include_directories(sim-parser-test PRIVATE "${CMAKE_CURRENT_SOURCE_DIR}/..")
target_link_libraries(sim-parser-test PRIVATE cadence_screen_options)
add_test(NAME sim-parser COMMAND sim-parser-test)

# the audio background's smoothing and pixel layout: the same plain test the full screen suite runs
add_executable(sim-bars-test ../app/routes/tests/bars-test.cpp ../app/routes/audio/bars.cpp)
target_include_directories(sim-bars-test PRIVATE "${CMAKE_CURRENT_SOURCE_DIR}/..")
target_link_libraries(sim-bars-test PRIVATE cadence_screen_options)
add_test(NAME sim-bars COMMAND sim-bars-test)

# the alarms model's choices: the same plain test the full screen suite runs
add_executable(sim-alarms-model-test ../app/routes/system/tests/alarms-model-test.cpp ../app/routes/system/alarms-model.cpp)
target_include_directories(sim-alarms-model-test PRIVATE "${CMAKE_CURRENT_SOURCE_DIR}/..")
target_link_libraries(sim-alarms-model-test PRIVATE cadence_screen_options cadence_screen_i18n)
add_test(NAME sim-alarms-model COMMAND sim-alarms-model-test)

# headless: the sim boots, walks home into a title and plays, and the screenshots it leaves are checked for size
# and non-uniform pixels (a real first frame, not a clear)
add_executable(sim-smoke-test tests/smoke-test.cpp tests/stb-image.cpp png.cpp)
target_include_directories(sim-smoke-test PRIVATE "${CMAKE_CURRENT_SOURCE_DIR}/.." ${CADENCE_SIM_STB_INCLUDE})
target_link_libraries(sim-smoke-test PRIVATE cadence_screen_options)
target_compile_definitions(sim-smoke-test PRIVATE CADENCE_SIM_BIN="$<TARGET_FILE:cadence-screen-sim>")
add_test(NAME sim-smoke COMMAND sim-smoke-test)

# headless: the alarms walk (home's strip, a ring over everything, the page, the display rule), its screenshots
# checked by their pixels
add_executable(sim-alarms-test tests/alarms-test.cpp tests/stb-image.cpp png.cpp)
target_include_directories(sim-alarms-test PRIVATE "${CMAKE_CURRENT_SOURCE_DIR}/.." ${CADENCE_SIM_STB_INCLUDE})
target_link_libraries(sim-alarms-test PRIVATE cadence_screen_options)
target_compile_definitions(sim-alarms-test PRIVATE CADENCE_SIM_BIN="$<TARGET_FILE:cadence-screen-sim>"
  CADENCE_SIM_EXAMPLES="${CMAKE_CURRENT_SOURCE_DIR}/examples")
add_test(NAME sim-alarms COMMAND sim-alarms-test)

# headless: rapid seeks while the placeholder player reports stale positions; the bar never goes back
add_executable(sim-seek-test tests/seek-test.cpp)
target_compile_definitions(sim-seek-test PRIVATE CADENCE_SIM_BIN="$<TARGET_FILE:cadence-screen-sim>")
add_test(NAME sim-seek COMMAND sim-seek-test)

add_executable(sim-prepare-test tests/prepare-test.cpp)
target_compile_definitions(sim-prepare-test PRIVATE CADENCE_SIM_BIN="$<TARGET_FILE:cadence-screen-sim>")
add_test(NAME sim-prepare COMMAND sim-prepare-test)
