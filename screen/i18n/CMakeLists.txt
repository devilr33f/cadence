# the screen's i18n (tv plan item 17): the message format, the built-in catalog made from i18n/screen by
# pkg/i18n/cmd/i18ngen (make i18n), locale formatting and the translator rmlui's TranslateString calls. screen/'s
# project adds it with add_subdirectory(i18n); alone it is its own project for its tests (make screen-p-test)
cmake_minimum_required(VERSION 3.25)

if(CMAKE_SOURCE_DIR STREQUAL CMAKE_CURRENT_SOURCE_DIR)
  project(cadence_screen_i18n LANGUAGES CXX)
  set(CMAKE_CXX_STANDARD 20)
  set(CMAKE_CXX_STANDARD_REQUIRED ON)
  set(CMAKE_CXX_EXTENSIONS OFF)
  set(CMAKE_COMPILE_WARNING_AS_ERROR ON)
  option(CADENCE_SCREEN_TESTS "Build the screen's host tests" ON)
  enable_testing()
endif()

add_library(cadence_screen_i18n STATIC
  catalog.cpp
  gen/catalog.cpp
  lang.cpp
  locale.cpp
  message.cpp
  translator.cpp)
target_include_directories(cadence_screen_i18n PUBLIC "${CMAKE_CURRENT_SOURCE_DIR}/..")
target_compile_options(cadence_screen_i18n PRIVATE -Wall -Wextra -Wpedantic -Wshadow -Wconversion -Wsign-conversion)

if(CADENCE_SCREEN_TESTS)
  find_package(Catch2 3 REQUIRED)
  add_executable(cadence_screen_i18n_tests tests/format_test.cpp tests/translator_test.cpp)
  target_link_libraries(cadence_screen_i18n_tests PRIVATE cadence_screen_i18n Catch2::Catch2WithMain)
  # nlohmann's json reads the cases pkg/i18n's tests read too
  target_include_directories(cadence_screen_i18n_tests PRIVATE "${CMAKE_CURRENT_SOURCE_DIR}/../../media/third_party")
  target_compile_definitions(cadence_screen_i18n_tests PRIVATE
    CADENCE_I18N_CASES="${CMAKE_CURRENT_SOURCE_DIR}/../../i18n/testdata/format.json")
  add_test(NAME screen_i18n COMMAND cadence_screen_i18n_tests)

  # a key no catalog has, or an argument of the wrong type, must not build; the same file with none of them must
  add_executable(cadence_screen_i18n_compiles EXCLUDE_FROM_ALL tests/compile_fail.cpp)
  target_link_libraries(cadence_screen_i18n_compiles PRIVATE cadence_screen_i18n)
  add_test(NAME screen_i18n_builds_the_control
    COMMAND ${CMAKE_COMMAND} --build ${CMAKE_BINARY_DIR} --target cadence_screen_i18n_compiles)
  foreach(bad missing_key wrong_arg missing_arg)
    add_executable(cadence_screen_i18n_${bad} EXCLUDE_FROM_ALL tests/compile_fail.cpp)
    target_link_libraries(cadence_screen_i18n_${bad} PRIVATE cadence_screen_i18n)
    string(TOUPPER ${bad} case)
    target_compile_definitions(cadence_screen_i18n_${bad} PRIVATE CADENCE_I18N_${case}=1)
    add_test(NAME screen_i18n_refuses_${bad}
      COMMAND ${CMAKE_COMMAND} --build ${CMAKE_BINARY_DIR} --target cadence_screen_i18n_${bad})
    set_tests_properties(screen_i18n_refuses_${bad} PROPERTIES WILL_FAIL TRUE)
  endforeach()
endif()
