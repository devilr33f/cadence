# libairplay: uxplay's lib/ (the submodule media/third_party/uxplay, android-airplay-server's pin) with
# android-airplay-server's patch series applied out of tree, built as one static library with cadence's own dnssd
# (libairplay/: the records, and the advertising handed to a backend). no gstreamer, no renderers, none of uxplay's
# global flags. included by media/CMakeLists.txt for CADENCE_MEDIA_AIRPLAY=uxplay, and by libairplay/CMakeLists.txt
# for the standalone builds (deploy/builder/libairplay.sh).
#
# targets:
#   cadence::airplay              static libairplay.a: llhttp, playfair, uxplay's lib/ and the dnssd records, in one
#                                 archive; its usage requirements bring openssl's libcrypto and libplist
#   cadence::airplay_dnssd_avahi  the avahi-client backend (CADENCE_AIRPLAY_DNSSD_AVAHI, linux)
#
# dependencies (CADENCE_AIRPLAY_DEPS):
#   system  openssl (find_package) and libplist-2.0 (pkg-config): debian's libssl-dev and libplist-dev
#   prefix  CADENCE_AIRPLAY_OPENSSL_ROOT (include/, lib/libcrypto.a) and CADENCE_AIRPLAY_PLIST_SOURCE (libplist's
#           release tarball, unpacked: its sources are compiled here, static, as android-airplay-server does)
include_guard(GLOBAL)

set(CADENCE_UXPLAY_SOURCE "${CMAKE_CURRENT_LIST_DIR}/uxplay" CACHE PATH "the pristine uxplay tree (the submodule)")
set(CADENCE_UXPLAY_PATCHES
  "${CMAKE_CURRENT_LIST_DIR}/android-airplay-server/app/src/main/cpp/patches/UxPlay"
  CACHE STRING "the patch series applied to uxplay, in order (a list of directories)")
set(CADENCE_APPLY_PATCHES "${CMAKE_CURRENT_LIST_DIR}/../../deploy/builder/apply-patches.sh" CACHE FILEPATH
  "deploy/builder/apply-patches.sh")
set(CADENCE_AIRPLAY_DEPS "system" CACHE STRING "where libairplay's openssl and libplist come from: system or prefix")
set_property(CACHE CADENCE_AIRPLAY_DEPS PROPERTY STRINGS system prefix)
set(CADENCE_AIRPLAY_OPENSSL_ROOT "" CACHE PATH "prefix deps: openssl's include/ and lib/libcrypto.a")
set(CADENCE_AIRPLAY_PLIST_SOURCE "" CACHE PATH "prefix deps: libplist's unpacked release tarball")
set(CADENCE_AIRPLAY_PLIST_VERSION "2.6.0" CACHE STRING "prefix deps: the libplist tarball's version")
option(CADENCE_AIRPLAY_DNSSD_AVAHI "Build libairplay's avahi-client dnssd backend" OFF)

if(NOT EXISTS "${CADENCE_UXPLAY_SOURCE}/lib/raop.c")
  message(FATAL_ERROR "no uxplay sources in ${CADENCE_UXPLAY_SOURCE}: git submodule update --init media/third_party/uxplay")
endif()
foreach(dir IN LISTS CADENCE_UXPLAY_PATCHES)
  if(NOT IS_DIRECTORY "${dir}")
    message(FATAL_ERROR "no patch directory ${dir}: git submodule update --init media/third_party/android-airplay-server")
  endif()
endforeach()

# patched at configure time into the build tree, again whenever a patch or the script changes. the pristine tree is
# not tracked: a submodule moves only with a commit that reconfigures
set(_uxplay "${CMAKE_BINARY_DIR}/uxplay-patched")
set(_uxplay_inputs "${CADENCE_APPLY_PATCHES}")
foreach(dir IN LISTS CADENCE_UXPLAY_PATCHES)
  file(GLOB _series CONFIGURE_DEPENDS "${dir}/*.patch")
  list(APPEND _uxplay_inputs ${_series})
endforeach()
set(_uxplay_stamp_text "")
foreach(f IN LISTS _uxplay_inputs)
  file(SHA256 "${f}" _sum)
  string(APPEND _uxplay_stamp_text "${_sum} ${f}\n")
endforeach()
execute_process(COMMAND git -C "${CADENCE_UXPLAY_SOURCE}" rev-parse HEAD OUTPUT_VARIABLE _uxplay_head
  OUTPUT_STRIP_TRAILING_WHITESPACE ERROR_QUIET)
string(APPEND _uxplay_stamp_text "source ${_uxplay_head}\n")
set_property(DIRECTORY APPEND PROPERTY CMAKE_CONFIGURE_DEPENDS ${_uxplay_inputs})
set(_uxplay_stamp "${CMAKE_BINARY_DIR}/uxplay-patched.stamp")
set(_uxplay_old "")
if(EXISTS "${_uxplay_stamp}")
  file(READ "${_uxplay_stamp}" _uxplay_old)
endif()
if(NOT EXISTS "${_uxplay}/lib/raop.c" OR NOT _uxplay_old STREQUAL _uxplay_stamp_text)
  find_program(CADENCE_BASH bash REQUIRED)
  execute_process(COMMAND "${CADENCE_BASH}" "${CADENCE_APPLY_PATCHES}" "${CADENCE_UXPLAY_SOURCE}" "${_uxplay}"
      ${CADENCE_UXPLAY_PATCHES}
    RESULT_VARIABLE _rc OUTPUT_VARIABLE _out ERROR_VARIABLE _err)
  if(NOT _rc EQUAL 0)
    message(FATAL_ERROR "uxplay's patches:\n${_err}")
  endif()
  file(WRITE "${_uxplay_stamp}" "${_uxplay_stamp_text}")
  message(STATUS "uxplay patched in ${_uxplay}")
endif()
set(_l "${_uxplay}/lib")

find_package(Threads REQUIRED)
if(CADENCE_AIRPLAY_DEPS STREQUAL "system")
  find_package(OpenSSL 3 REQUIRED COMPONENTS Crypto)
  find_package(PkgConfig REQUIRED)
  pkg_check_modules(CADENCE_PLIST REQUIRED IMPORTED_TARGET libplist-2.0>=2.1.0)
  add_library(cadence_airplay_crypto INTERFACE)
  target_link_libraries(cadence_airplay_crypto INTERFACE OpenSSL::Crypto)
  add_library(cadence_airplay_plist INTERFACE)
  target_link_libraries(cadence_airplay_plist INTERFACE PkgConfig::CADENCE_PLIST)
  set(_plist_version "${CADENCE_PLIST_VERSION}")
elseif(CADENCE_AIRPLAY_DEPS STREQUAL "prefix")
  if(NOT EXISTS "${CADENCE_AIRPLAY_OPENSSL_ROOT}/lib/libcrypto.a")
    message(FATAL_ERROR "no ${CADENCE_AIRPLAY_OPENSSL_ROOT}/lib/libcrypto.a")
  endif()
  if(NOT EXISTS "${CADENCE_AIRPLAY_PLIST_SOURCE}/src/plist.c")
    message(FATAL_ERROR "no libplist sources in ${CADENCE_AIRPLAY_PLIST_SOURCE}")
  endif()
  add_library(cadence_airplay_crypto INTERFACE)
  target_include_directories(cadence_airplay_crypto INTERFACE "${CADENCE_AIRPLAY_OPENSSL_ROOT}/include")
  target_link_libraries(cadence_airplay_crypto INTERFACE "${CADENCE_AIRPLAY_OPENSSL_ROOT}/lib/libcrypto.a")
  file(GLOB _plist_sources "${CADENCE_AIRPLAY_PLIST_SOURCE}/src/*.c" "${CADENCE_AIRPLAY_PLIST_SOURCE}/libcnary/*.c")
  add_library(cadence_airplay_plist STATIC ${_plist_sources})
  set_target_properties(cadence_airplay_plist PROPERTIES OUTPUT_NAME plist-2.0 POSITION_INDEPENDENT_CODE ON
    COMPILE_WARNING_AS_ERROR OFF C_STANDARD 11)
  target_include_directories(cadence_airplay_plist
    PUBLIC "${CADENCE_AIRPLAY_PLIST_SOURCE}/include"
    PRIVATE "${CADENCE_AIRPLAY_PLIST_SOURCE}/src" "${CADENCE_AIRPLAY_PLIST_SOURCE}/libcnary/include")
  # bionic (api 26+) and glibc have all of these; the tarball's config.h is not generated
  target_compile_definitions(cadence_airplay_plist PRIVATE _GNU_SOURCE
    "PACKAGE_VERSION=\"${CADENCE_AIRPLAY_PLIST_VERSION}\"" HAVE_STRNDUP HAVE_MEMMEM HAVE_GMTIME_R HAVE_LOCALTIME_R
    HAVE_TIMEGM HAVE_STRPTIME HAVE_TM_TM_GMTOFF HAVE_TM_TM_ZONE)
  target_compile_options(cadence_airplay_plist PRIVATE -w -ffunction-sections -fdata-sections)
  set(_plist_version "${CADENCE_AIRPLAY_PLIST_VERSION}")
else()
  message(FATAL_ERROR "CADENCE_AIRPLAY_DEPS=${CADENCE_AIRPLAY_DEPS}: system or prefix")
endif()

add_library(cadence_airplay_objects OBJECT
  ${_l}/llhttp/api.c ${_l}/llhttp/http.c ${_l}/llhttp/llhttp.c
  ${_l}/playfair/hand_garble.c ${_l}/playfair/modified_md5.c ${_l}/playfair/omg_hax.c ${_l}/playfair/playfair.c
  ${_l}/playfair/sap_hash.c
  ${_l}/airplay_video.c ${_l}/byteutils.c ${_l}/compat.c ${_l}/crypto.c ${_l}/fairplay_playfair.c
  ${_l}/http_request.c ${_l}/http_response.c ${_l}/httpd.c ${_l}/logger.c ${_l}/mirror_buffer.c ${_l}/netutils.c
  ${_l}/pairing.c ${_l}/raop.c ${_l}/raop_buffer.c ${_l}/raop_ntp.c ${_l}/raop_rtp.c ${_l}/raop_rtp_mirror.c
  ${_l}/srp.c ${_l}/utils.c
  ${CMAKE_CURRENT_LIST_DIR}/libairplay/dnssd.c)
set_target_properties(cadence_airplay_objects PROPERTIES POSITION_INDEPENDENT_CODE ON COMPILE_WARNING_AS_ERROR OFF
  C_STANDARD 11)
target_include_directories(cadence_airplay_objects PUBLIC ${_l} ${_l}/llhttp ${_l}/playfair
  ${CMAKE_CURRENT_LIST_DIR}/libairplay)
target_compile_definitions(cadence_airplay_objects
  PUBLIC OPENSSL_API_COMPAT=0x10101000L
  PRIVATE NOHOLD PLIST_210 $<$<VERSION_GREATER_EQUAL:${_plist_version},2.3.0>:PLIST_230>)
# reproducible objects: no build path in them
target_compile_options(cadence_airplay_objects PRIVATE -ffunction-sections -fdata-sections
  "-ffile-prefix-map=${_uxplay}=uxplay" "-ffile-prefix-map=${CMAKE_CURRENT_LIST_DIR}=media/third_party")
target_link_libraries(cadence_airplay_objects PUBLIC cadence_airplay_crypto cadence_airplay_plist Threads::Threads)

add_library(cadence_airplay STATIC $<TARGET_OBJECTS:cadence_airplay_objects>)
set_target_properties(cadence_airplay PROPERTIES OUTPUT_NAME airplay)
target_include_directories(cadence_airplay PUBLIC ${_l} ${_l}/llhttp ${_l}/playfair
  ${CMAKE_CURRENT_LIST_DIR}/libairplay)
target_compile_definitions(cadence_airplay PUBLIC OPENSSL_API_COMPAT=0x10101000L)
target_link_libraries(cadence_airplay PUBLIC cadence_airplay_crypto cadence_airplay_plist Threads::Threads m)
add_library(cadence::airplay ALIAS cadence_airplay)

if(CADENCE_AIRPLAY_DNSSD_AVAHI)
  find_package(PkgConfig REQUIRED)
  pkg_check_modules(CADENCE_AVAHI REQUIRED IMPORTED_TARGET avahi-client)
  add_library(cadence_airplay_dnssd_avahi STATIC ${CMAKE_CURRENT_LIST_DIR}/libairplay/dnssd_avahi.c)
  set_target_properties(cadence_airplay_dnssd_avahi PROPERTIES OUTPUT_NAME airplay-dnssd-avahi
    POSITION_INDEPENDENT_CODE ON C_STANDARD 11)
  target_compile_options(cadence_airplay_dnssd_avahi PRIVATE -Wall -Wextra
    "-ffile-prefix-map=${CMAKE_CURRENT_LIST_DIR}=media/third_party")
  target_link_libraries(cadence_airplay_dnssd_avahi PUBLIC cadence_airplay PkgConfig::CADENCE_AVAHI)
  add_library(cadence::airplay_dnssd_avahi ALIAS cadence_airplay_dnssd_avahi)
endif()
