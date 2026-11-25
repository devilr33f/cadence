# libairplay alone (deploy/builder/libairplay.sh): the static library, the avahi backend where asked, and the test
# program, which ctest runs where the build's cpu is the host's. installs lib/libairplay.a (and, in prefix mode,
# lib/libplist-2.0.a), the headers an embedder includes, and bin/airplay-test
cmake_minimum_required(VERSION 3.25)
project(cadence_libairplay C)

include(${CMAKE_CURRENT_LIST_DIR}/../uxplay.cmake)

option(CADENCE_AIRPLAY_TESTS "Build the libairplay test program" ON)
if(CADENCE_AIRPLAY_TESTS)
  add_executable(airplay-test tests/airplay-test.c)
  set_target_properties(airplay-test PROPERTIES C_STANDARD 11)
  target_compile_options(airplay-test PRIVATE -Wall -Wextra -Werror "-ffile-prefix-map=${CMAKE_CURRENT_LIST_DIR}=libairplay")
  target_link_libraries(airplay-test PRIVATE cadence::airplay)
  if(CADENCE_AIRPLAY_DNSSD_AVAHI)
    target_link_libraries(airplay-test PRIVATE cadence::airplay_dnssd_avahi)
    target_compile_definitions(airplay-test PRIVATE CADENCE_AIRPLAY_TEST_AVAHI)
  endif()
  if(ANDROID)
    target_link_options(airplay-test PRIVATE -pie -Wl,--gc-sections)
  endif()
  enable_testing()
  add_test(NAME airplay-test COMMAND airplay-test)
  install(TARGETS airplay-test RUNTIME DESTINATION bin)
endif()

install(TARGETS cadence_airplay ARCHIVE DESTINATION lib)
if(TARGET cadence_airplay_dnssd_avahi)
  install(TARGETS cadence_airplay_dnssd_avahi ARCHIVE DESTINATION lib)
endif()
if(CADENCE_AIRPLAY_DEPS STREQUAL "prefix")
  install(TARGETS cadence_airplay_plist ARCHIVE DESTINATION lib)
endif()
install(FILES ${CMAKE_CURRENT_LIST_DIR}/cadence_dnssd.h ${CMAKE_BINARY_DIR}/uxplay-patched/lib/dnssd.h
  ${CMAKE_BINARY_DIR}/uxplay-patched/lib/raop.h ${CMAKE_BINARY_DIR}/uxplay-patched/lib/raop_ntp.h
  ${CMAKE_BINARY_DIR}/uxplay-patched/lib/stream.h ${CMAKE_BINARY_DIR}/uxplay-patched/lib/logger.h
  ${CMAKE_BINARY_DIR}/uxplay-patched/lib/airplay_video.h
  DESTINATION include/airplay)
