# stream b's ui library (design r2 sections 1-10, 17, 25): rmlui 6.3 through stream A's RenderBackend, afterglow's
# tokens and components, the focus manager, cadence cards and the preview gallery. added by screen/'s project
find_package(RmlUi 6.3 REQUIRED CONFIG)
find_package(Freetype REQUIRED)

set(ui_gen "${CMAKE_CURRENT_BINARY_DIR}/gen")
file(MAKE_DIRECTORY "${ui_gen}/ui" "${ui_gen}/rcss")

# tokens.json -> tokens.h and the expanded rcss (a build tool, never shipped). a cross build (android's ndk) cannot run
# what it compiles, so it names a tokens-gen built for the build machine instead (deploy/android/Dockerfile)
set(CADENCE_SCREEN_TOKENS_GEN "" CACHE FILEPATH "a tokens-gen for the build machine, for a cross build")
if(CADENCE_SCREEN_TOKENS_GEN)
  add_executable(tokens-gen IMPORTED)
  set_target_properties(tokens-gen PROPERTIES IMPORTED_LOCATION "${CADENCE_SCREEN_TOKENS_GEN}")
else()
  add_executable(tokens-gen tokens/tokens-gen.cpp)
  target_include_directories(tokens-gen SYSTEM PRIVATE "${CADENCE_MEDIA_DIR}/third_party")
endif()
set(ui_templates
  ${CMAKE_CURRENT_SOURCE_DIR}/assets/rcss/base.rcss
  ${CMAKE_CURRENT_SOURCE_DIR}/assets/rcss/components.rcss
  ${CMAKE_CURRENT_SOURCE_DIR}/assets/rcss/cards.rcss)
# the screens' own sheets (wave 2: one routes-<stream>.rcss each), expanded like the library's and linked after them
file(GLOB ui_route_sheets CONFIGURE_DEPENDS ${CMAKE_CURRENT_SOURCE_DIR}/assets/rcss/routes-*.rcss)
list(SORT ui_route_sheets)
set(ui_route_sheet_names "")
foreach(t ${ui_route_sheets})
  get_filename_component(n ${t} NAME)
  list(APPEND ui_route_sheet_names ${n})
endforeach()
list(APPEND ui_templates ${ui_route_sheets})
set(ui_generated "${ui_gen}/ui/tokens.h" "${ui_gen}/rcss/tokens.rcss")
foreach(t ${ui_templates})
  get_filename_component(n ${t} NAME)
  list(APPEND ui_generated "${ui_gen}/rcss/${n}")
endforeach()
add_custom_command(
  OUTPUT ${ui_generated}
  COMMAND tokens-gen ${CMAKE_CURRENT_SOURCE_DIR}/tokens/tokens.json ${ui_gen}/ui/tokens.h ${ui_gen}/rcss ${ui_templates}
  DEPENDS tokens-gen ${CMAKE_CURRENT_SOURCE_DIR}/tokens/tokens.json ${ui_templates}
  COMMENT "afterglow tokens")
add_custom_target(cadence_screen_ui_tokens DEPENDS ${ui_generated})

# nayuki's qr code generator (mit), unmodified
add_library(cadence_screen_qrcodegen STATIC third_party/qrcodegen/qrcodegen.cpp)
target_include_directories(cadence_screen_qrcodegen SYSTEM PUBLIC third_party/qrcodegen)

# rmlui's gl3 renderer (mit, patched: RMLUI_GLES3, no msaa layer, an external base target; RMLUI_GL3_APPLE is the
# macos desktop-gl branch of the same patch)
add_library(cadence_screen_rml_gl3 STATIC rml/third_party/RmlUi_Renderer_GL3.cpp)
target_include_directories(cadence_screen_rml_gl3 SYSTEM PUBLIC rml/third_party)
if(APPLE)
  target_compile_definitions(cadence_screen_rml_gl3 PUBLIC RMLUI_GL3_APPLE=1 RMLUI_NUM_MSAA_SAMPLES=0)
else()
  target_compile_definitions(cadence_screen_rml_gl3 PUBLIC RMLUI_GLES3=1 RMLUI_NUM_MSAA_SAMPLES=0)
endif()
target_link_libraries(cadence_screen_rml_gl3 PUBLIC RmlUi::RmlUi cadence_screen_gles)

add_library(cadence_screen_ui STATIC
  rml/fallback-fonts.cpp
  rml/files.cpp
  rml/renderer.cpp
  rml/sprites.cpp
  rml/system.cpp
  core/focus.cpp
  core/rml.cpp
  core/ui.cpp
  core/budget.cpp
  core/shell.cpp
  core/volume-overlay.cpp
  cards/content.cpp
  cards/model.cpp
  cards/render.cpp
  cards/host.cpp
  cards/timeline.cpp
  images/binder.cpp
  images/blur.cpp
  images/http-fetcher.cpp
  images/image-cache.cpp)
add_dependencies(cadence_screen_ui cadence_screen_ui_tokens)
target_include_directories(cadence_screen_ui PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/.. ${ui_gen})
string(REPLACE ";" "," ui_route_sheet_define "${ui_route_sheet_names}")
target_compile_definitions(cadence_screen_ui PUBLIC
  CADENCE_SCREEN_UI_ASSETS="${CMAKE_CURRENT_SOURCE_DIR}"
  CADENCE_SCREEN_UI_GENERATED="${ui_gen}"
  CADENCE_SCREEN_UI_ROUTE_SHEETS="${ui_route_sheet_define}")
# https for the image proxy when libcurl is there (a device build without it fetches plain http alone)
find_package(CURL QUIET)
if(CURL_FOUND)
  target_compile_definitions(cadence_screen_ui PRIVATE CADENCE_SCREEN_CURL=1)
  target_link_libraries(cadence_screen_ui PRIVATE CURL::libcurl)
endif()
target_link_libraries(cadence_screen_ui
  PUBLIC cadence_screen_rml_gl3 cadence_screen_render cadence_screen_i18n cadence_screen_proto cadence_screen_platform
    cadence_screen_ipc RmlUi::RmlUi
  PRIVATE cadence_screen_qrcodegen cadence_screen_options)

# the gallery: every component and card example with fake data, golden images, the budget report and the walk. a
# linux tool (its harness renders on mesa's offscreen driver); macos builds skip it
if(NOT APPLE)
add_library(cadence_screen_ui_preview STATIC preview/gallery.cpp preview/harness.cpp preview/run.cpp)
target_link_libraries(cadence_screen_ui_preview PUBLIC cadence_screen_ui PRIVATE cadence_screen_options)
# the pages are aggregates that name only the fields they set
target_compile_options(cadence_screen_ui_preview PRIVATE -Wno-missing-field-initializers)
target_compile_definitions(cadence_screen_ui_preview PUBLIC
  CADENCE_SCREEN_CARD_FIXTURES="${CMAKE_CURRENT_SOURCE_DIR}/../../proto/fixtures/cards/v1"
  CADENCE_SCREEN_UI_GOLDEN="${CMAKE_CURRENT_SOURCE_DIR}/preview/golden")
add_executable(cadence-ui-preview preview/main.cpp)
target_link_libraries(cadence-ui-preview PRIVATE cadence_screen_ui_preview cadence_screen_options)
endif()

if(CADENCE_SCREEN_TESTS)
  find_package(Catch2 3.16 REQUIRED)
  include(Catch)
  add_executable(ui.test tests/ui-test.cpp tests/cards-test.cpp tests/blur-test.cpp tests/timeline-test.cpp)
  target_link_libraries(ui.test PRIVATE cadence_screen_ui_preview cadence_screen_options Catch2::Catch2WithMain)
  catch_discover_tests(ui.test TEST_PREFIX "ui: " DISCOVERY_MODE PRE_TEST PROPERTIES LABELS ui)
  # the gallery: every page against its golden image, stable, walked, in xx, within the budget
  add_test(NAME ui-gallery COMMAND cadence-ui-preview --check --out ${CMAKE_BINARY_DIR}/ui-preview)
  set_tests_properties(ui-gallery PROPERTIES LABELS ui TIMEOUT 600)
endif()
