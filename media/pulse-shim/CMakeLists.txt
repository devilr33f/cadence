# libpulse.so.0 for soloist, clean-room from the libpulse api documentation and v1's own shim. it exports exactly
# symbols.txt (through libpulse.map) and needs nothing but libc: soloist's runtime has no c++ library, so ours is linked
# in statically and kept out of the dynamic symbol table
add_library(cadence_pulse SHARED
  api.cpp
  buffer.cpp
  context.cpp
  events.cpp
  introspect.cpp
  link.cpp
  mainloop.cpp
  operation.cpp
  proplist.cpp
  sample.cpp
  stream.cpp
  stream-timing.cpp
  stream-write.cpp
  util.cpp)
set_target_properties(cadence_pulse PROPERTIES
  OUTPUT_NAME pulse
  SOVERSION 0
  CXX_VISIBILITY_PRESET hidden
  VISIBILITY_INLINES_HIDDEN ON
  LINK_DEPENDS "${CMAKE_CURRENT_SOURCE_DIR}/libpulse.map")
target_include_directories(cadence_pulse PUBLIC "${CMAKE_CURRENT_SOURCE_DIR}/..")
target_link_libraries(cadence_pulse PRIVATE cadence_media_pcmframe Threads::Threads cadence_media_options)
target_link_options(cadence_pulse PRIVATE
  "LINKER:--version-script=${CMAKE_CURRENT_SOURCE_DIR}/libpulse.map"
  "LINKER:--exclude-libs,ALL"
  "LINKER:-z,defs"
  -static-libstdc++
  -static-libgcc)

# libcadence-resolv.so, the preload of an unjailed soloist (android): it answers its getaddrinfo over the socket
# audiod serves (media/audiod/soloist-resolv.cpp), falls back to the real one whenever anything is off, exports
# getaddrinfo and freeaddrinfo alone (resolv-preload.map) and needs nothing but libc
add_library(cadence_resolv SHARED resolv-preload.cpp)
set_target_properties(cadence_resolv PROPERTIES
  OUTPUT_NAME cadence-resolv
  CXX_VISIBILITY_PRESET hidden
  VISIBILITY_INLINES_HIDDEN ON
  LINK_DEPENDS "${CMAKE_CURRENT_SOURCE_DIR}/resolv-preload.map")
target_include_directories(cadence_resolv PRIVATE "${CMAKE_CURRENT_SOURCE_DIR}/../lib/src")
target_link_libraries(cadence_resolv PRIVATE cadence_media_options)
# no c++ runtime and no libgcc_s in its NEEDED: the preload uses neither, and the runtime has no c++ library (the
# shim embeds the one it needs instead; this one needs none at all)
target_link_options(cadence_resolv PRIVATE
  "LINKER:--version-script=${CMAKE_CURRENT_SOURCE_DIR}/resolv-preload.map"
  -nostdlib++
  -static-libgcc)
