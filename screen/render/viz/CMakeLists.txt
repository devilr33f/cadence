# the visualizer (tv plan item 16 and the owner's 2026-11-14 redesign): a host with pluggable sources (our glsl
# shader presets, milkdrop through projectM, the classic bars and scope) blended by gl-transitions, on a thread of its
# own, fed the tap's pcm as it is heard. screen/'s project adds it with add_subdirectory(render/viz); alone it is its
# own project for its tests (make screen-p-test: the gl-free parts and the classic golden images; make
# screen-p-gl-test: the gl host, its golden images on llvmpipe, projectM and the benchmark)
cmake_minimum_required(VERSION 3.25)

if(CMAKE_SOURCE_DIR STREQUAL CMAKE_CURRENT_SOURCE_DIR)
  project(cadence_screen_viz LANGUAGES CXX)
  set(CMAKE_CXX_STANDARD 20)
  set(CMAKE_CXX_STANDARD_REQUIRED ON)
  set(CMAKE_CXX_EXTENSIONS OFF)
  set(CMAKE_COMPILE_WARNING_AS_ERROR ON)
  option(CADENCE_SCREEN_TESTS "Build the screen's host tests" ON)
  enable_testing()
endif()
option(CADENCE_SCREEN_GL "Build the visualizer's gles 3 host" OFF)
option(CADENCE_SCREEN_PROJECTM "Build the milkdrop source against libprojectM-4 (media/third_party/projectm)" OFF)
set(CADENCE_VIZ_MILKDROP_DIR "" CACHE PATH "a preset directory compiled in as milkdrop_dir()'s last fallback")

set(viz_warnings -Wall -Wextra -Wpedantic -Wshadow -Wconversion -Wsign-conversion)

# the classic visualizer and the host's gl-free parts. no fused multiply-add, so a frame's pixels are the same on
# every cpu (the golden images)
add_library(cadence_screen_viz STATIC
  analyzer.cpp classic.cpp raster.cpp
  host/beat.cpp host/beat_grid.cpp host/features.cpp host/impact.cpp host/onset.cpp host/pcm_timeline.cpp host/playlist.cpp host/scheduler.cpp host/strobe.cpp
  host/transition_params.cpp)
target_include_directories(cadence_screen_viz PUBLIC "${CMAKE_CURRENT_SOURCE_DIR}")
target_compile_options(cadence_screen_viz PRIVATE ${viz_warnings} -ffp-contract=off)
find_package(Threads REQUIRED)
target_link_libraries(cadence_screen_viz PUBLIC Threads::Threads)

if(CADENCE_SCREEN_GL)
  if(APPLE)
    # macos (the screen-sim build): desktop gl 4.1 core instead of es (host/gl.h)
    find_package(OpenGL REQUIRED)
    set(GLES3_INCLUDE_DIR "")
    set(GLES_LIBRARY OpenGL::GL)
  else()
    find_path(GLES3_INCLUDE_DIR GLES3/gl3.h REQUIRED)
    # android names it GLESv3; mesa ships the es 3 entry points in GLESv2
    find_library(GLES_LIBRARY NAMES GLESv3 GLESv2 REQUIRED)
  endif()
  add_library(cadence_screen_viz_gl STATIC
    host/classic_source.cpp host/drive_source.cpp host/gl.cpp host/gl_canvas.cpp host/host.cpp host/renderer.cpp host/scene_source.cpp
    host/shader_source.cpp
    host/transition.cpp)
  # the feedback scenes, compiled in
  file(GLOB viz_scenes CONFIGURE_DEPENDS "${CMAKE_CURRENT_SOURCE_DIR}/scenes/*.frag")
  set(viz_scenes_cpp "${CMAKE_CURRENT_BINARY_DIR}/scenes_embedded.cpp")
  string(REPLACE ";" "|" viz_scenes_arg "${viz_scenes}")
  add_custom_command(OUTPUT ${viz_scenes_cpp}
    COMMAND ${CMAKE_COMMAND} -DOUT=${viz_scenes_cpp} -DSCENES=${viz_scenes_arg} -P "${CMAKE_CURRENT_SOURCE_DIR}/scenes/embed.cmake"
    VERBATIM
    DEPENDS ${viz_scenes} "${CMAKE_CURRENT_SOURCE_DIR}/scenes/embed.cmake"
    COMMENT "the visualizer's scenes")
  target_sources(cadence_screen_viz_gl PRIVATE ${viz_scenes_cpp})
  target_include_directories(cadence_screen_viz_gl PUBLIC "${GLES3_INCLUDE_DIR}" "${CMAKE_CURRENT_SOURCE_DIR}")
  target_link_libraries(cadence_screen_viz_gl PUBLIC cadence_screen_viz ${GLES_LIBRARY})
  target_compile_options(cadence_screen_viz_gl PRIVATE ${viz_warnings})
  # the screen's entry to the shipped presets (milkdrop/milkdrop_sources.h): an empty list without projectM
  target_sources(cadence_screen_viz_gl PRIVATE milkdrop/milkdrop_sources.cpp)
  if(CADENCE_SCREEN_PROJECTM)
    # lgpl-2.1: linked as the shared library media/third_party/projectm/build.sh installs, never static
    find_package(projectM4 REQUIRED)
    target_sources(cadence_screen_viz_gl PRIVATE milkdrop/milkdrop_source.cpp)
    target_link_libraries(cadence_screen_viz_gl PUBLIC libprojectM::projectM)
    target_compile_definitions(cadence_screen_viz_gl PUBLIC CADENCE_VIZ_PROJECTM=1)
    if(CADENCE_VIZ_MILKDROP_DIR)
      # a build's own preset directory, the last fallback of milkdrop_dir() (the simulator's fetched copy)
      target_compile_definitions(cadence_screen_viz_gl PRIVATE CADENCE_VIZ_MILKDROP_DIR="${CADENCE_VIZ_MILKDROP_DIR}")
    endif()
  endif()
  if(NOT APPLE)
    find_library(EGL_LIBRARY NAMES EGL REQUIRED)
  endif()
  # inside screen/'s project: the Canvas and SharedContext on stream A's RenderBackend and egl (cgl on macos)
  if(TARGET cadence_screen_render)
    add_library(cadence_screen_viz_backend STATIC screen_backend.cpp)
    if(APPLE)
      target_link_libraries(cadence_screen_viz_backend PUBLIC cadence_screen_viz_gl cadence_screen_render OpenGL::GL)
    else()
      target_link_libraries(cadence_screen_viz_backend PUBLIC cadence_screen_viz_gl cadence_screen_render ${EGL_LIBRARY})
    endif()
    target_compile_options(cadence_screen_viz_backend PRIVATE ${viz_warnings})
  endif()
  # the offline preview: a source rendered from a track as raw frames (tools/preview.sh makes a video of them)
  add_executable(viz-render host/bench/viz-render.cpp)
  target_link_libraries(viz-render PRIVATE cadence_screen_viz_gl ${EGL_LIBRARY})
  target_compile_options(viz-render PRIVATE ${viz_warnings})
  if(NOT APPLE)
    # the benchmark's offscreen context is egl; macos has none
    add_executable(viz-bench host/bench/viz-bench.cpp)
    target_link_libraries(viz-bench PRIVATE cadence_screen_viz_gl ${EGL_LIBRARY})
    target_compile_options(viz-bench PRIVATE ${viz_warnings})
  endif()
endif()

if(CADENCE_SCREEN_TESTS)
  find_package(Catch2 3 REQUIRED)
  add_executable(cadence_screen_viz_tests tests/viz_test.cpp tests/host_test.cpp)
  target_link_libraries(cadence_screen_viz_tests PRIVATE cadence_screen_viz Catch2::Catch2WithMain)
  target_compile_options(cadence_screen_viz_tests PRIVATE -ffp-contract=off)
  target_compile_definitions(cadence_screen_viz_tests PRIVATE
    CADENCE_VIZ_GOLDEN="${CMAKE_CURRENT_SOURCE_DIR}/tests/golden")
  add_test(NAME screen_viz COMMAND cadence_screen_viz_tests)
  if(CADENCE_SCREEN_GL)
    find_package(ZLIB REQUIRED)
    add_executable(cadence_screen_viz_gl_tests tests/gl_test.cpp)
    target_link_libraries(cadence_screen_viz_gl_tests PRIVATE cadence_screen_viz_gl ${EGL_LIBRARY} ZLIB::ZLIB
      Catch2::Catch2WithMain)
    target_compile_definitions(cadence_screen_viz_gl_tests PRIVATE
      CADENCE_VIZ_GOLDEN="${CMAKE_CURRENT_SOURCE_DIR}/tests/golden"
      CADENCE_VIZ_PRESETS="${CMAKE_CURRENT_SOURCE_DIR}/presets"
      CADENCE_VIZ_SCENES="${CMAKE_CURRENT_SOURCE_DIR}/scenes")
    add_test(NAME screen_viz_gl COMMAND cadence_screen_viz_gl_tests)
    # the benchmark runs: every source a second, a table out
    add_test(NAME screen_viz_bench COMMAND viz-bench --shaders "${CMAKE_CURRENT_SOURCE_DIR}/presets" --classic
      --scenes "${CMAKE_CURRENT_SOURCE_DIR}/scenes" --seconds 1 --size 320x180)
  endif()
endif()
