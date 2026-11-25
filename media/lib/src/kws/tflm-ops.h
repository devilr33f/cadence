#pragma once

// the keyword runner's op resolver (kws/tflm-model.cpp): tflm's headers, for the runner's own sources and tests alone

#include "tensorflow/lite/micro/micro_mutable_op_resolver.h"

#include <cstddef>

namespace cadence::kws {

// exactly the stock model's ops (spec §3)
inline constexpr std::size_t kKwsOpCount = 17;
using KwsResolver = tflite::MicroMutableOpResolver<kKwsOpCount>;

// registers the stock model's ops; false when one would not fit or was refused, which the runner turns into
// model_invalid and its tests into a failure
bool registerKwsOps(KwsResolver& resolver);

}  // namespace cadence::kws
