# what the tests and the sim image's e2e share without catch2: a websocket server and the fake soloist on it, and the
# fake shairport sync

# a websocket server that parses the client's frames with code of its own
add_library(cadence_media_ws_server STATIC ws-server.cpp)
target_include_directories(cadence_media_ws_server PUBLIC "${CMAKE_CURRENT_SOURCE_DIR}/..")
target_link_libraries(cadence_media_ws_server
  PUBLIC cadence_media_ws Threads::Threads
  PRIVATE cadence_media_options)

# a stand-in for soloist that plays through the real shim: no test ever runs the real one
add_executable(fake-soloist fake-soloist.cpp)
target_link_libraries(fake-soloist PRIVATE cadence_media_ws_server cadence_pulse cadence_media_options)

# a stand-in for shairport sync that plays the e2e's cast on the receiver's private bus: no test ever runs the real one
if(CADENCE_MEDIA_AIRPLAY)
  add_executable(fake-shairport fake-shairport.cpp)
  target_link_libraries(fake-shairport PRIVATE cadence_media_dbus cadence_media_options)
endif()

# the build the e2e's fake cdn serves: the updater installs it as <soloist dir>/soloist beside runtime/, and unjailed it
# loads the runtime's shim from there, as soloist does in its jail
if(CADENCE_MEDIA_FAKE_SOLOIST)
  if(NOT CMAKE_LIBRARY_ARCHITECTURE)
    message(FATAL_ERROR "the e2e's fake soloist needs the multiarch triple of soloist's runtime")
  endif()
  add_executable(fake-soloist-e2e fake-soloist.cpp)
  target_compile_definitions(fake-soloist-e2e PRIVATE CADENCE_FAKE_SOLOIST_FRESH)
  target_link_libraries(fake-soloist-e2e PRIVATE cadence_media_ws_server cadence_pulse cadence_media_options)
  set_target_properties(fake-soloist-e2e PROPERTIES
    BUILD_WITH_INSTALL_RPATH ON
    INSTALL_RPATH "$ORIGIN/runtime/usr/lib/${CMAKE_LIBRARY_ARCHITECTURE}")
endif()
