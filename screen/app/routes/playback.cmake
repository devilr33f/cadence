# i2: playback and lean-back (design r2 sections 12-15, 19): the player's osd, audio mode with the visualizers, panels,
# the queue, ambient and idle, their gallery (cadence-playback-preview, golden images in preview/golden) and tests
target_sources(cadence_screen_routes PRIVATE
  ${CMAKE_CURRENT_LIST_DIR}/ambient/ambient.cpp
  ${CMAKE_CURRENT_LIST_DIR}/ambient/idle-renderer.cpp
  ${CMAKE_CURRENT_LIST_DIR}/audio/audio.cpp
  ${CMAKE_CURRENT_LIST_DIR}/audio/bar-renderer.cpp
  ${CMAKE_CURRENT_LIST_DIR}/audio/bars.cpp
  ${CMAKE_CURRENT_LIST_DIR}/audio/queue-sidebar.cpp
  ${CMAKE_CURRENT_LIST_DIR}/audio/viz-view.cpp
  ${CMAKE_CURRENT_LIST_DIR}/panels/chapters.cpp
  ${CMAKE_CURRENT_LIST_DIR}/panels/panel.cpp
  ${CMAKE_CURRENT_LIST_DIR}/panels/side-sheet.cpp
  ${CMAKE_CURRENT_LIST_DIR}/playback/context.cpp
  ${CMAKE_CURRENT_LIST_DIR}/playback/position.cpp
  ${CMAKE_CURRENT_LIST_DIR}/playback/routes.cpp
  ${CMAKE_CURRENT_LIST_DIR}/playback/rpc.cpp
  ${CMAKE_CURRENT_LIST_DIR}/playback/server-choices.cpp
  ${CMAKE_CURRENT_LIST_DIR}/playback/session.cpp
  ${CMAKE_CURRENT_LIST_DIR}/player/cache-info.cpp
  ${CMAKE_CURRENT_LIST_DIR}/player/source-line.cpp
  ${CMAKE_CURRENT_LIST_DIR}/player/osd.cpp
  ${CMAKE_CURRENT_LIST_DIR}/player/switcher.cpp
  ${CMAKE_CURRENT_LIST_DIR}/queue/queue.cpp)
# the visualizer's host (stream p, its egl context shared with the ui's) and ken burns
target_link_libraries(cadence_screen_routes PUBLIC cadence_screen_viz_backend cadence_screen_kenburns)

# the playback gallery is a linux tool (mesa's offscreen driver); macos builds skip it
if(NOT APPLE)
add_library(cadence_screen_playback_preview STATIC
  ${CMAKE_CURRENT_LIST_DIR}/preview/playback-fakes.cpp
  ${CMAKE_CURRENT_LIST_DIR}/preview/playback-pages.cpp)
target_link_libraries(cadence_screen_playback_preview PUBLIC cadence_screen_routes cadence_screen_browse_preview cadence_screen_ui_preview
  PRIVATE cadence_screen_options)
target_compile_options(cadence_screen_playback_preview PRIVATE -Wno-missing-field-initializers)
target_compile_definitions(cadence_screen_playback_preview PUBLIC
  CADENCE_SCREEN_PLAYBACK_GOLDEN="${CMAKE_CURRENT_LIST_DIR}/preview/golden"
  CADENCE_SCREEN_PLAYBACK_CARDS="${CMAKE_CURRENT_LIST_DIR}/../../../proto/fixtures/cards/v1")
add_executable(cadence-playback-preview ${CMAKE_CURRENT_LIST_DIR}/preview/playback-main.cpp)
target_link_libraries(cadence-playback-preview PRIVATE cadence_screen_playback_preview cadence_screen_options)
endif()

if(CADENCE_SCREEN_TESTS)
  find_package(Catch2 3.16 REQUIRED)
  include(Catch)
  # the background bars' model needs no gl or gallery, and also runs in the native simulator build
  add_executable(playback-bars.test ${CMAKE_CURRENT_LIST_DIR}/tests/bars-test.cpp ${CMAKE_CURRENT_LIST_DIR}/audio/bars.cpp)
  target_include_directories(playback-bars.test PRIVATE ${CMAKE_CURRENT_LIST_DIR}/../..)
  target_link_libraries(playback-bars.test PRIVATE cadence_screen_options)
  add_test(NAME playback-bars COMMAND playback-bars.test)
  set_tests_properties(playback-bars PROPERTIES LABELS unit)
  add_executable(playback.test
    ${CMAKE_CURRENT_LIST_DIR}/tests/osd-lines-test.cpp
    ${CMAKE_CURRENT_LIST_DIR}/tests/playback-session-test.cpp
    ${CMAKE_CURRENT_LIST_DIR}/tests/position-test.cpp
    ${CMAKE_CURRENT_LIST_DIR}/tests/playback-test.cpp)
  target_link_libraries(playback.test PRIVATE cadence_screen_playback_preview cadence_screen_options Catch2::Catch2WithMain)
  catch_discover_tests(playback.test TEST_PREFIX "playback: " DISCOVERY_MODE PRE_TEST PROPERTIES LABELS ui)
  add_test(NAME playback-gallery COMMAND cadence-playback-preview --check --out ${CMAKE_BINARY_DIR}/playback-preview)
  set_tests_properties(playback-gallery PROPERTIES LABELS ui TIMEOUT 600)
endif()
