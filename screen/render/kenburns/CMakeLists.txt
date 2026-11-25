# the ken burns drift of ambient images and Home's backdrop: the motion of one textured quad, drawn through the Canvas
# of ../viz/backend.h; screen/'s project adds it with add_subdirectory(render/kenburns). alone it is its own project
# for its tests (make screen-p-test)
cmake_minimum_required(VERSION 3.25)

if(CMAKE_SOURCE_DIR STREQUAL CMAKE_CURRENT_SOURCE_DIR)
  project(cadence_screen_kenburns LANGUAGES CXX)
  set(CMAKE_CXX_STANDARD 20)
  set(CMAKE_CXX_STANDARD_REQUIRED ON)
  set(CMAKE_CXX_EXTENSIONS OFF)
  set(CMAKE_COMPILE_WARNING_AS_ERROR ON)
  option(CADENCE_SCREEN_TESTS "Build the screen's host tests" ON)
  enable_testing()
endif()

add_library(cadence_screen_kenburns STATIC kenburns.cpp)
target_include_directories(cadence_screen_kenburns PUBLIC "${CMAKE_CURRENT_SOURCE_DIR}")
target_compile_options(cadence_screen_kenburns PRIVATE -Wall -Wextra -Wpedantic -Wshadow -Wconversion
  -Wsign-conversion)

if(CADENCE_SCREEN_TESTS)
  find_package(Catch2 3 REQUIRED)
  add_executable(cadence_screen_kenburns_tests tests/kenburns_test.cpp)
  target_link_libraries(cadence_screen_kenburns_tests PRIVATE cadence_screen_kenburns Catch2::Catch2WithMain)
  add_test(NAME screen_kenburns COMMAND cadence_screen_kenburns_tests)
endif()
