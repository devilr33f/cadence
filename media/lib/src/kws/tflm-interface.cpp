#include "kws/tflm-interface.h"

#include "tensorflow/lite/micro/flatbuffer_utils.h"

#include <algorithm>
#include <cstdint>
#include <initializer_list>
#include <string_view>

namespace cadence::kws {
namespace {

bool float32Shaped(const tflite::Tensor* tensor, std::initializer_list<std::int32_t> dims) {
  return tensor != nullptr && tensor->type() == tflite::TensorType_FLOAT32 && tensor->shape() != nullptr &&
         std::equal(tensor->shape()->begin(), tensor->shape()->end(), dims.begin(), dims.end());
}

const tflite::Tensor* tensorAt(const tflite::SubGraph* subgraph, std::int64_t index) {
  if (index < 0 || static_cast<std::uint64_t>(index) >= subgraph->tensors()->size())
    return nullptr;
  return subgraph->tensors()->Get(static_cast<flatbuffers::uoffset_t>(index));
}

std::optional<std::size_t> outputSlot(const tflite::SignatureDef* signature, const tflite::SubGraph* subgraph,
                                      std::string_view name) {
  if (signature->outputs() == nullptr)
    return std::nullopt;
  for (const auto* output : *signature->outputs()) {
    if (output == nullptr || output->name() == nullptr || output->name()->string_view() != name)
      continue;
    const auto* outputs = subgraph->outputs();
    const auto slot = std::find(outputs->begin(), outputs->end(), static_cast<std::int32_t>(output->tensor_index()));
    if (slot == outputs->end())
      return std::nullopt;
    return static_cast<std::size_t>(slot - outputs->begin());
  }
  return std::nullopt;
}

const tflite::SignatureDef* servingDefault(const tflite::Model* model) {
  if (model->signature_defs() == nullptr)
    return nullptr;
  for (const auto* signature : *model->signature_defs())
    if (signature != nullptr && signature->signature_key() != nullptr &&
        signature->signature_key()->string_view() == "serving_default" && signature->subgraph_index() == 0)
      return signature;
  return nullptr;
}

// a tensor the interpreter allocates in its arena: one with a constant buffer would point into the model's own
// bytes, sized by its buffer and not by its shape, which invoke then writes or reads as if it held the shape
bool inArena(const tflite::Model* model, const tflite::Tensor* tensor) {
  if (tensor == nullptr || model->buffers() == nullptr || tensor->buffer() >= model->buffers()->size())
    return false;
  const auto* buffer = model->buffers()->Get(tensor->buffer());
  return buffer == nullptr ||
         ((buffer->data() == nullptr || buffer->data()->size() == 0) && buffer->offset() == 0 && buffer->size() == 0);
}

}  // namespace

bool buffersInRange(const tflite::Model* model) {
  if (model->subgraphs() == nullptr || model->buffers() == nullptr)
    return false;
  for (const auto* subgraph : *model->subgraphs()) {
    if (subgraph == nullptr || subgraph->tensors() == nullptr)
      continue;
    for (const auto* tensor : *subgraph->tensors())
      if (tensor == nullptr || tensor->buffer() >= model->buffers()->size())
        return false;
  }
  return true;
}

std::optional<OutputSlots> stockInterface(const tflite::Model* model) {
  if (model->subgraphs() == nullptr || model->subgraphs()->size() == 0)
    return std::nullopt;
  const auto* subgraph = model->subgraphs()->Get(0);
  const auto* signature = servingDefault(model);
  if (subgraph == nullptr || subgraph->tensors() == nullptr || subgraph->inputs() == nullptr ||
      subgraph->inputs()->size() != 1 || subgraph->outputs() == nullptr || subgraph->outputs()->size() != 3 ||
      signature == nullptr)
    return std::nullopt;
  const auto* input = tensorAt(subgraph, subgraph->inputs()->Get(0));
  if (!float32Shaped(input, {1, 8, 40}) || !inArena(model, input))
    return std::nullopt;
  const auto posNeg = outputSlot(signature, subgraph, "pos_neg_probs");
  const auto kwLoc = outputSlot(signature, subgraph, "kw_loc");
  const auto intUnint = outputSlot(signature, subgraph, "int_unint_probs");
  if (!posNeg || !kwLoc || !intUnint || *posNeg == *kwLoc || *posNeg == *intUnint || *kwLoc == *intUnint)
    return std::nullopt;
  const auto output = [subgraph](std::size_t slot) {
    return tensorAt(subgraph, subgraph->outputs()->Get(static_cast<flatbuffers::uoffset_t>(slot)));
  };
  if (!float32Shaped(output(*posNeg), {1, 8, 1, 2}) || !float32Shaped(output(*kwLoc), {1, 8, 1, 1}) ||
      !float32Shaped(output(*intUnint), {1, 8, 1, 2}))
    return std::nullopt;
  if (!inArena(model, output(*posNeg)) || !inArena(model, output(*kwLoc)) || !inArena(model, output(*intUnint)))
    return std::nullopt;
  return OutputSlots{*posNeg, *kwLoc, *intUnint};
}

int varHandles(const tflite::Model* model) {
  int count = 0;
  const auto* codes = model->operator_codes();
  if (model->subgraphs() == nullptr || codes == nullptr)
    return 0;
  for (const auto* subgraph : *model->subgraphs()) {
    if (subgraph == nullptr || subgraph->operators() == nullptr)
      continue;
    for (const auto* op : *subgraph->operators())
      if (op != nullptr && op->opcode_index() < codes->size() &&
          tflite::micro::GetBuiltinCode(codes->Get(op->opcode_index())) == tflite::BuiltinOperator_VAR_HANDLE)
        ++count;
  }
  return count;
}

}  // namespace cadence::kws
