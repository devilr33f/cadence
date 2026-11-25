#pragma once

// the keyword runner's own int8 kernels (kws/tflm-ops.cpp): tflm's reference registrations, their init and prepare
// kept, with an eval of ours where the reference is slow on an a53 (its per-output requantization). each gives the
// reference's output bit for bit (tests/kws-kernels.test.cpp) and leaves every case it does not take to the
// reference eval. tflm's headers, for the runner's own sources and tests alone

#include "tensorflow/lite/micro/micro_common.h"

namespace cadence::kws {

// DEPTHWISE_CONV_2D: int8 input and filter at depth multiplier 1, channels innermost
TFLMRegistration depthwiseConv2DRegistration();

// ADD: int8, at most 4 dimensions, broadcast by zero strides
TFLMRegistration addRegistration();

}  // namespace cadence::kws
