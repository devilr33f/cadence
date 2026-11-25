# wave 2's screens (screen/app/routes): the router and its context, and each stream's routes. a stream adds its own
# <name>.cmake beside this file (i1: browse.cmake), which appends its sources to cadence_screen_routes
add_library(cadence_screen_routes STATIC
  core/catalog.cpp
  core/context.cpp
  core/play.cpp
  core/router.cpp
  core/rpc.cpp
  core/view.cpp)
target_include_directories(cadence_screen_routes PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/../..)
target_include_directories(cadence_screen_routes SYSTEM PUBLIC ${CADENCE_MEDIA_DIR}/third_party)
target_link_libraries(cadence_screen_routes PUBLIC cadence_screen_ui cadence_screen_app PRIVATE cadence_screen_options)

file(GLOB route_parts CONFIGURE_DEPENDS ${CMAKE_CURRENT_SOURCE_DIR}/*.cmake)
list(SORT route_parts)
foreach(part ${route_parts})
  include(${part})
endforeach()
