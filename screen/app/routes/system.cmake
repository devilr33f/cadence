# i3: the assistant, tasks and the system screens (design r2 sections 16-17, 20-24): the drawer, the tasks panels and
# airplay as a layer over the router, enrollment and settings (with room and microphone) as routes; their gallery
# (cadence-system-preview, golden images in system/golden) and tests
target_sources(cadence_screen_routes PRIVATE
  ${CMAKE_CURRENT_LIST_DIR}/system.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/airplay.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/alarm-ring.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/alarms.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/alarms-model.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/alarms-page.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/alarms-view.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/card-sheet.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/chat.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/chat-view.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/device.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/devices.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/drawer.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/enroll.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/nearby.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/page-rpc.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/reel.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/showcase.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/settings.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/system.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/tasks.cpp)
target_compile_definitions(cadence_screen_routes PRIVATE CADENCE_SCREEN_ROUTES_VERSION="${CADENCE_SCREEN_VERSION}")

# the system gallery is a linux tool (mesa's offscreen driver); macos builds skip it
if(NOT APPLE)
add_library(cadence_screen_system_preview STATIC
  ${CMAKE_CURRENT_LIST_DIR}/system/preview/fixture.cpp
  ${CMAKE_CURRENT_LIST_DIR}/system/preview/pages.cpp)
target_link_libraries(cadence_screen_system_preview PUBLIC cadence_screen_routes cadence_screen_ui_preview
  cadence_screen_browse_preview PRIVATE cadence_screen_options)
target_compile_options(cadence_screen_system_preview PRIVATE -Wno-missing-field-initializers)
target_compile_definitions(cadence_screen_system_preview PUBLIC
  CADENCE_SCREEN_SYSTEM_GOLDEN="${CMAKE_CURRENT_LIST_DIR}/system/golden")
add_executable(cadence-system-preview ${CMAKE_CURRENT_LIST_DIR}/system/preview/main.cpp)
target_link_libraries(cadence-system-preview PRIVATE cadence_screen_system_preview cadence_screen_options)
endif()

if(CADENCE_SCREEN_TESTS)
  find_package(Catch2 3.16 REQUIRED)
  include(Catch)
  # the showcase reel's pacing alone: plain c++, so it builds and runs anywhere (macos too)
  add_executable(reel.test ${CMAKE_CURRENT_LIST_DIR}/system/reel.cpp ${CMAKE_CURRENT_LIST_DIR}/system/tests/reel-test.cpp)
  target_include_directories(reel.test PRIVATE ${CMAKE_CURRENT_LIST_DIR}/../..)
  target_link_libraries(reel.test PRIVATE Catch2::Catch2WithMain)
  catch_discover_tests(reel.test TEST_PREFIX "reel: " DISCOVERY_MODE PRE_TEST PROPERTIES LABELS unit)
  # devices.media's parsing alone: plain c++ over nlohmann, so it builds and runs anywhere too
  add_executable(nearby.test ${CMAKE_CURRENT_LIST_DIR}/system/nearby.cpp ${CMAKE_CURRENT_LIST_DIR}/system/tests/nearby-test.cpp)
  target_include_directories(nearby.test PRIVATE ${CMAKE_CURRENT_LIST_DIR}/../..)
  target_include_directories(nearby.test SYSTEM PRIVATE ${CADENCE_MEDIA_DIR}/third_party)
  target_link_libraries(nearby.test PRIVATE Catch2::Catch2WithMain)
  catch_discover_tests(nearby.test TEST_PREFIX "nearby: " DISCOVERY_MODE PRE_TEST PROPERTIES LABELS unit)
  # the alarms model's choices alone: plain c++ (the native simulator builds it too)
  add_executable(alarms-model.test ${CMAKE_CURRENT_LIST_DIR}/system/alarms-model.cpp
    ${CMAKE_CURRENT_LIST_DIR}/system/tests/alarms-model-test.cpp)
  target_include_directories(alarms-model.test PRIVATE ${CMAKE_CURRENT_LIST_DIR}/../..)
  target_link_libraries(alarms-model.test PRIVATE cadence_screen_i18n)
  add_test(NAME alarms-model COMMAND alarms-model.test)
  set_tests_properties(alarms-model PROPERTIES LABELS unit)
endif()

if(CADENCE_SCREEN_TESTS AND NOT APPLE)
  add_executable(system.test
    ${CMAKE_CURRENT_LIST_DIR}/system/tests/chat-test.cpp
    ${CMAKE_CURRENT_LIST_DIR}/system/tests/devices-test.cpp
    ${CMAKE_CURRENT_LIST_DIR}/system/tests/drawer-test.cpp
    ${CMAKE_CURRENT_LIST_DIR}/system/tests/enroll-test.cpp
    ${CMAKE_CURRENT_LIST_DIR}/system/tests/system-test.cpp
    ${CMAKE_CURRENT_LIST_DIR}/system/tests/tasks-test.cpp)
  target_link_libraries(system.test PRIVATE cadence_screen_system_preview cadence_screen_options Catch2::Catch2WithMain)
  target_compile_options(system.test PRIVATE -Wno-missing-field-initializers)
  catch_discover_tests(system.test TEST_PREFIX "system: " DISCOVERY_MODE PRE_TEST PROPERTIES LABELS ui)
  # every state against its golden image, stable, walked, in xx, within the budget
  add_test(NAME system-gallery COMMAND cadence-system-preview --check --out ${CMAKE_BINARY_DIR}/system-preview)
  set_tests_properties(system-gallery PROPERTIES LABELS ui TIMEOUT 600)
endif()
