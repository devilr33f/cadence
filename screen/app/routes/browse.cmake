# i1: browse and open a title (design r2 sections 11 and 18): home, search, title, episodes, picker, resolving,
# their gallery (cadence-browse-preview, golden images in preview/golden) and tests
target_sources(cadence_screen_routes PRIVATE
  ${CMAKE_CURRENT_LIST_DIR}/browse.cpp
  ${CMAKE_CURRENT_LIST_DIR}/episodes/episodes.cpp
  ${CMAKE_CURRENT_LIST_DIR}/home/home.cpp
  ${CMAKE_CURRENT_LIST_DIR}/home/alarms-strip.cpp
  ${CMAKE_CURRENT_LIST_DIR}/home/nearby-strip.cpp
  ${CMAKE_CURRENT_LIST_DIR}/home/now-playing.cpp
  ${CMAKE_CURRENT_LIST_DIR}/picker/picker.cpp
  ${CMAKE_CURRENT_LIST_DIR}/resolving/resolving.cpp
  ${CMAKE_CURRENT_LIST_DIR}/search/search.cpp
  ${CMAKE_CURRENT_LIST_DIR}/title/title.cpp)

# the fake catalog's json builders are first-party shared code (the sim links fake-catalog.cpp alone); the preview
# pages and binary are linux tools (the gallery renders on mesa's offscreen driver)
if(NOT APPLE)
add_library(cadence_screen_browse_preview STATIC
  ${CMAKE_CURRENT_LIST_DIR}/preview/fake-catalog.cpp
  ${CMAKE_CURRENT_LIST_DIR}/preview/fake-devices.cpp
  ${CMAKE_CURRENT_LIST_DIR}/preview/pages.cpp)
target_link_libraries(cadence_screen_browse_preview PUBLIC cadence_screen_routes cadence_screen_ui_preview
  PRIVATE cadence_screen_options)
target_compile_options(cadence_screen_browse_preview PRIVATE -Wno-missing-field-initializers)
target_compile_definitions(cadence_screen_browse_preview PUBLIC
  CADENCE_SCREEN_BROWSE_GOLDEN="${CMAKE_CURRENT_LIST_DIR}/preview/golden")
add_executable(cadence-browse-preview ${CMAKE_CURRENT_LIST_DIR}/preview/main.cpp)
target_link_libraries(cadence-browse-preview PRIVATE cadence_screen_browse_preview cadence_screen_options)
endif()

if(CADENCE_SCREEN_TESTS)
  find_package(Catch2 3.16 REQUIRED)
  include(Catch)
  add_executable(browse.test
    ${CMAKE_CURRENT_LIST_DIR}/tests/browse-test.cpp
    ${CMAKE_CURRENT_LIST_DIR}/tests/images-test.cpp
    ${CMAKE_CURRENT_LIST_DIR}/tests/ipc-test.cpp)
  target_link_libraries(browse.test PRIVATE cadence_screen_browse_preview cadence_screen_options Catch2::Catch2WithMain)
  target_compile_options(browse.test PRIVATE -Wno-missing-field-initializers)
  catch_discover_tests(browse.test TEST_PREFIX "browse: " DISCOVERY_MODE PRE_TEST PROPERTIES LABELS ui)
  # every route against its golden image, stable, walked, in xx, within the budget
  add_test(NAME browse-gallery COMMAND cadence-browse-preview --check --out ${CMAKE_BINARY_DIR}/browse-preview)
  set_tests_properties(browse-gallery PROPERTIES LABELS ui TIMEOUT 600)
endif()
