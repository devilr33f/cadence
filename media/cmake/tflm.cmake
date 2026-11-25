# tensorflow lite micro (apache-2.0) for the keyword engine (CADENCE_MEDIA_KWS) as cadence_media_tflm: its framework
# and the kernels of the ops the stock keyword model uses, nothing else, with the headers of flatbuffers (apache-2.0)
# at the version tflm's tools/make/download_dependencies.py names at the pinned commit (its schema_generated.h refuses
# any other), and cmsis-nn (apache-2.0) at the commit tflm's tools/make/ext_libs/cmsis_nn_download.sh names. all three
# are github's archives of the commit or tag, fetched pinned by sha-256. at this commit tflm needs neither gemmlowp
# nor ruy (its kernels/internal/fixedpoint.h is adapted from gemmlowp's). the trees' licences are copied to licenses/
# in the build tree, which the tarball ships
include(${CMAKE_CURRENT_LIST_DIR}/download.cmake)

set(cadence_tflm_commit 9611ed2e1cfa75a58b8217baf024e14f656d7b37)
set(cadence_tflm_sha256 2efb3f604fa28c469fd98c36aebca708d732bfc1a79b3eed8d46a754a2a94d16)
set(cadence_flatbuffers_version 25.9.23)
set(cadence_flatbuffers_sha256 9102253214dea6ae10c2ac966ea1ed2155d22202390b532d1dea64935c518ada)
set(cadence_cmsis_nn_commit ece8aa0df4bee4fc928c294e18059071649c88bb)
set(cadence_cmsis_nn_sha256 87dc275472a2e10bbe0e5d79d8f89930b098de78328a7d1b044e48058431db9c)

# cadence_tflm_fetch(<name> <url> <sha256> <tree> <file> <pattern>...): the archive's matching files extracted into the
# build tree, once: <tree> holding <file> and its LICENSE
function(cadence_tflm_fetch name url sha256 tree file)
  if(NOT EXISTS "${tree}/${file}")
    set(tarball "${CMAKE_BINARY_DIR}/${name}.tar.gz")
    cadence_download("${url}" "${tarball}" ${sha256} status)
    list(GET status 0 code)
    if(NOT code EQUAL 0)
      message(FATAL_ERROR "${name}'s download failed: ${status}")
    endif()
    file(ARCHIVE_EXTRACT INPUT "${tarball}" DESTINATION "${CMAKE_BINARY_DIR}" PATTERNS ${ARGN})
    file(REMOVE "${tarball}")
  endif()
  if(NOT EXISTS "${tree}/${file}" OR NOT EXISTS "${tree}/LICENSE")
    message(FATAL_ERROR "no ${name} in ${tree}")
  endif()
  file(COPY "${tree}/LICENSE" DESTINATION "${CMAKE_BINARY_DIR}/licenses/${name}")
endfunction()

set(cadence_tflm "${CMAKE_BINARY_DIR}/tflite-micro-${cadence_tflm_commit}")
cadence_tflm_fetch(tflite-micro "https://github.com/tensorflow/tflite-micro/archive/${cadence_tflm_commit}.tar.gz"
  ${cadence_tflm_sha256} "${cadence_tflm}" tensorflow/lite/micro/micro_interpreter.cc
  "tflite-micro-${cadence_tflm_commit}/LICENSE" "tflite-micro-${cadence_tflm_commit}/tensorflow/lite/micro/*"
  # kernels/micro_ops.h declares the signal library's kernels too, from these headers alone
  "tflite-micro-${cadence_tflm_commit}/signal/micro/kernels/*.h")
set(cadence_flatbuffers "${CMAKE_BINARY_DIR}/flatbuffers-${cadence_flatbuffers_version}")
cadence_tflm_fetch(flatbuffers
  "https://github.com/google/flatbuffers/archive/refs/tags/v${cadence_flatbuffers_version}.tar.gz"
  ${cadence_flatbuffers_sha256} "${cadence_flatbuffers}" include/flatbuffers/flatbuffers.h
  "flatbuffers-${cadence_flatbuffers_version}/LICENSE" "flatbuffers-${cadence_flatbuffers_version}/include/*")
set(cadence_cmsis_nn "${CMAKE_BINARY_DIR}/CMSIS-NN-${cadence_cmsis_nn_commit}")
cadence_tflm_fetch(cmsis-nn "https://github.com/ARM-software/CMSIS-NN/archive/${cadence_cmsis_nn_commit}.tar.gz"
  ${cadence_cmsis_nn_sha256} "${cadence_cmsis_nn}" Include/arm_nnfunctions.h
  "CMSIS-NN-${cadence_cmsis_nn_commit}/LICENSE" "CMSIS-NN-${cadence_cmsis_nn_commit}/Include/*"
  "CMSIS-NN-${cadence_cmsis_nn_commit}/Source/*")
# cmsis-nn's portable c: an a53 has neither the dsp extension nor helium, so none of its intrinsic paths build. tflm's
# own makefile takes every source of the tree the same way (tools/make/ext_libs/cmsis_nn.inc)
file(GLOB cadence_cmsis_nn_sources "${cadence_cmsis_nn}/Source/*/*.c")

set(cadence_tflm_micro "${cadence_tflm}/tensorflow/lite/micro")
add_library(cadence_media_tflm STATIC
  # the framework: tflm's own core list (cmake/tflm_sources.cmake) without its recording, profiling and hexdump
  # helpers, and without its default debug_log.cc, micro_time.cc and system_setup.cc
  ${cadence_tflm_micro}/arena_allocator/non_persistent_arena_buffer_allocator.cc
  ${cadence_tflm_micro}/arena_allocator/persistent_arena_buffer_allocator.cc
  ${cadence_tflm_micro}/arena_allocator/single_arena_buffer_allocator.cc
  ${cadence_tflm_micro}/flatbuffer_conversions.cc
  ${cadence_tflm_micro}/flatbuffer_utils.cc
  ${cadence_tflm_micro}/kernels/internal/common.cc
  ${cadence_tflm_micro}/kernels/internal/portable_tensor_utils.cc
  ${cadence_tflm_micro}/kernels/internal/quantization_util.cc
  ${cadence_tflm_micro}/kernels/internal/tensor_ctypes.cc
  ${cadence_tflm_micro}/kernels/kernel_util.cc
  ${cadence_tflm_micro}/memory_helpers.cc
  ${cadence_tflm_micro}/memory_planner/greedy_memory_planner.cc
  ${cadence_tflm_micro}/memory_planner/linear_memory_planner.cc
  ${cadence_tflm_micro}/memory_planner/non_persistent_buffer_planner_shim.cc
  ${cadence_tflm_micro}/micro_allocation_info.cc
  ${cadence_tflm_micro}/micro_allocator.cc
  ${cadence_tflm_micro}/micro_common.cc
  ${cadence_tflm_micro}/micro_context.cc
  ${cadence_tflm_micro}/micro_interpreter.cc
  ${cadence_tflm_micro}/micro_interpreter_context.cc
  ${cadence_tflm_micro}/micro_interpreter_graph.cc
  ${cadence_tflm_micro}/micro_log.cc
  ${cadence_tflm_micro}/micro_op_resolver.cc
  ${cadence_tflm_micro}/micro_resource_variable.cc
  ${cadence_tflm_micro}/micro_utils.cc
  # the kernels of the 17 ops the runner registers (lib/src/kws/tflm-ops.cpp). conv, mul, max pool, softmax and pad
  # are tflm's cmsis_nn kernels, 5 times faster for conv on an a53; depthwise conv and add are the reference ones,
  # whose init and prepare our evals run on (lib/src/kws/tflm-kernels.h), and the reference ones for the rest
  ${cadence_tflm_micro}/kernels/add.cc
  ${cadence_tflm_micro}/kernels/add_common.cc
  ${cadence_tflm_micro}/kernels/assign_variable.cc
  ${cadence_tflm_micro}/kernels/call_once.cc
  ${cadence_tflm_micro}/kernels/concatenation.cc
  ${cadence_tflm_micro}/kernels/cmsis_nn/conv.cc
  ${cadence_tflm_micro}/kernels/conv_common.cc
  ${cadence_tflm_micro}/kernels/depthwise_conv.cc
  ${cadence_tflm_micro}/kernels/depthwise_conv_common.cc
  ${cadence_tflm_micro}/kernels/dequantize.cc
  ${cadence_tflm_micro}/kernels/dequantize_common.cc
  ${cadence_tflm_micro}/kernels/logistic.cc
  ${cadence_tflm_micro}/kernels/logistic_common.cc
  ${cadence_tflm_micro}/kernels/cmsis_nn/mul.cc
  ${cadence_tflm_micro}/kernels/mul_common.cc
  ${cadence_tflm_micro}/kernels/cmsis_nn/pad.cc
  ${cadence_tflm_micro}/kernels/pad_common.cc
  ${cadence_tflm_micro}/kernels/cmsis_nn/pooling.cc
  ${cadence_tflm_micro}/kernels/pooling_common.cc
  ${cadence_tflm_micro}/kernels/quantize.cc
  ${cadence_tflm_micro}/kernels/quantize_common.cc
  ${cadence_tflm_micro}/kernels/read_variable.cc
  ${cadence_tflm_micro}/kernels/reshape.cc
  ${cadence_tflm_micro}/kernels/reshape_common.cc
  ${cadence_tflm_micro}/kernels/cmsis_nn/softmax.cc
  ${cadence_tflm_micro}/kernels/softmax_common.cc
  ${cadence_tflm_micro}/kernels/strided_slice.cc
  ${cadence_tflm_micro}/kernels/strided_slice_common.cc
  ${cadence_tflm_micro}/kernels/var_handle.cc
  ${cadence_cmsis_nn_sources}
  # the platform's debug log, ours: it goes nowhere (rule 8)
  ${CMAKE_CURRENT_SOURCE_DIR}/lib/src/kws/tflm-debug-log.cpp)
target_include_directories(cadence_media_tflm SYSTEM PUBLIC "${cadence_tflm}" "${cadence_flatbuffers}/include")
target_include_directories(cadence_media_tflm SYSTEM PRIVATE "${cadence_cmsis_nn}/Include")
# the defines tflm's own cmake gives its library, CMSIS_NN as for its cmsis_nn kernels (the headers then declare the
# kernels' cmsis variants); the error strings stripped, so no model or tensor text is formatted
target_compile_definitions(cadence_media_tflm
  PUBLIC TF_LITE_STRIP_ERROR_STRINGS FLATBUFFERS_LOCALE_INDEPENDENT=0 FLATBUFFERS_PREFER_PRINTF=1 CMSIS_NN
  PRIVATE KERNELS_OPTIMIZED_FOR_SPEED)
target_compile_options(cadence_media_tflm PRIVATE -w "-ffile-prefix-map=${cadence_tflm}=tflite-micro"
  "-ffile-prefix-map=${cadence_cmsis_nn}=cmsis-nn")
set_target_properties(cadence_media_tflm PROPERTIES CXX_STANDARD 17 COMPILE_WARNING_AS_ERROR OFF)
target_link_libraries(cadence_media_tflm PUBLIC m)
