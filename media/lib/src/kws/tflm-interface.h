#pragma once

// what the keyword runner (kws/tflm-model.cpp) reads from a model's flatbuffer before anything is allocated: tflm's
// headers, for the runner's own sources alone

#include "tensorflow/lite/micro/schema/schema_generated.h"

#include <cstddef>
#include <optional>

namespace cadence::kws {

// where each stock output sits in the subgraph's outputs, by which the interpreter numbers them
struct OutputSlots {
  std::size_t posNeg = 0;
  std::size_t kwLoc = 0;
  std::size_t intUnint = 0;
};

// every tensor's buffer index names one of the model's buffers: tflm indexes them unchecked
bool buffersInRange(const tflite::Model* model);

// the stock model's interface (spec §3) in a verified model: subgraph 0 with one float32 [1,8,40] input and three
// outputs, which its serving_default signature names pos_neg_probs [1,8,1,2], kw_loc [1,8,1,1] and int_unint_probs
// [1,8,1,2], float32, none of the four backed by a constant buffer. none when it differs
std::optional<OutputSlots> stockInterface(const tflite::Model* model);

// the resource variables a model can make: one per VAR_HANDLE op at most, as the init subgraph and the main one share
// them
int varHandles(const tflite::Model* model);

}  // namespace cadence::kws
