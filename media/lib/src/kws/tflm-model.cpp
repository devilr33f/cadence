#include "kws/tflm-model.h"

#include "kws/tflm-arena.h"
#include "kws/tflm-interface.h"
#include "kws/tflm-ops.h"

#include "tensorflow/lite/micro/memory_planner/greedy_memory_planner.h"
#include "tensorflow/lite/micro/micro_allocator.h"
#include "tensorflow/lite/micro/micro_arena_constants.h"
#include "tensorflow/lite/micro/micro_interpreter.h"
#include "tensorflow/lite/micro/micro_resource_variable.h"
#include "tensorflow/lite/micro/schema/schema_generated.h"

#include <algorithm>
#include <array>
#include <cstddef>
#include <cstring>
#include <memory>
#include <new>
#include <optional>
#include <utility>

namespace cadence::kws {
namespace {

constexpr std::size_t kAlignment = tflite::MicroArenaBufferAlignment();

struct AlignedFree {
  void operator()(std::uint8_t* bytes) const { ::operator delete[](bytes, std::align_val_t{kAlignment}); }
};
using AlignedBytes = std::unique_ptr<std::uint8_t[], AlignedFree>;

AlignedBytes alignedCopy(std::span<const std::uint8_t> bytes) {
  AlignedBytes copy(static_cast<std::uint8_t*>(::operator new[](std::max<std::size_t>(bytes.size(), 1),
                                                                std::align_val_t{kAlignment})));
  if (!bytes.empty())
    std::memcpy(copy.get(), bytes.data(), bytes.size());
  return copy;
}

// a float tensor of the arena: invoke writes the input and reads the outputs by their shape, which only the arena's
// own allocation is sized by (the interface check refused constant buffers already; this holds after allocation too)
bool holdsFloats(const TfLiteTensor* tensor, std::size_t floats) {
  return tensor != nullptr && tensor->type == kTfLiteFloat32 && tensor->allocation_type == kTfLiteArenaRw &&
         tensor->data.f != nullptr && tensor->bytes == floats * sizeof(float);
}

}  // namespace

struct TflmModel::Impl {
  Impl(std::span<const std::uint8_t> flatbuffer, std::size_t cap)
      : modelBytes(alignedCopy(flatbuffer)), modelSize(flatbuffer.size()), arenaCap(cap),
        arena(ArenaMapping::sizeFor(cap)), opsRegistered(registerKwsOps(resolver)) {}
  ~Impl() { dropInterpreter(); }
  Impl(const Impl&) = delete;
  Impl& operator=(const Impl&) = delete;

  void dropInterpreter() {
    if (interpreter != nullptr)
      std::destroy_at(interpreter);
    interpreter = nullptr;
    input = nullptr;
  }

  enum class Built { ok, exceeded, failed, shape };

  // the interpreter, from nothing, on the whole mapping, held to cap: what load does, and reset again
  Built build(std::size_t cap) {
    dropInterpreter();
    watch.emplace(arena.base(), arena.size(), cap);
    planner.emplace();
    auto* allocator = tflite::MicroAllocator::Create(&*watch, &*planner);
    const auto variableCount = varHandles(model);
    auto* variables = variableCount > 0 ? tflite::MicroResourceVariables::Create(allocator, variableCount) : nullptr;
    // tflm's destructor walks the node allocations, which a failed AllocateTensors can leave half made (it crashes on
    // them), so an interpreter that failed is abandoned in its storage, never destroyed: it holds nothing but the arena
    auto* made = new (interpreterStorage.data()) tflite::MicroInterpreter(model, resolver, allocator, variables);
    const bool allocated = made->initialization_status() == kTfLiteOk && made->AllocateTensors() == kTfLiteOk;
    if (watch->exceeded())
      return Built::exceeded;
    if (!allocated)
      return Built::failed;
    interpreter = made;
    input = interpreter->input(0);
    outputs = {interpreter->output(slots.posNeg), interpreter->output(slots.kwLoc),
               interpreter->output(slots.intUnint)};
    if (!holdsFloats(input, kFrames * kMels) || !holdsFloats(outputs[0], 16) || !holdsFloats(outputs[1], 8) ||
        !holdsFloats(outputs[2], 16))
      return Built::shape;
    return Built::ok;
  }

  std::string_view load() {
    if (arena.base() == nullptr)
      return "arena_exceeded";
    switch (build(arenaCap)) {
      case Built::ok:
        return {};
      case Built::shape:
        return "model_shape";
      case Built::exceeded:
        return "arena_exceeded";
      case Built::failed:
        break;
    }
    // a failure within the cap is the model's, unless the whole mapping lets it build: the memory planner fails
    // without passing the cap when what it was offered holds too few buffers
    const auto unheld = build(arena.size());
    dropInterpreter();
    return unheld == Built::ok || unheld == Built::exceeded ? "arena_exceeded" : "model_invalid";
  }

  // the flatbuffer's copy, which the interpreter reads the weights from for as long as it runs
  AlignedBytes modelBytes;
  std::size_t modelSize;
  std::size_t arenaCap;
  ArenaMapping arena;
  const tflite::Model* model = nullptr;
  OutputSlots slots;
  // before opsRegistered, which the constructor fills by registering into it
  KwsResolver resolver;
  bool opsRegistered;
  std::optional<ArenaWatch> watch;
  std::optional<tflite::GreedyMemoryPlanner> planner;
  TfLiteTensor* input = nullptr;
  std::array<TfLiteTensor*, 3> outputs{};
  alignas(tflite::MicroInterpreter) std::array<std::byte, sizeof(tflite::MicroInterpreter)> interpreterStorage{};
  // the interpreter that built, in interpreterStorage; dropped before anything above goes
  tflite::MicroInterpreter* interpreter = nullptr;
};

TflmModel::TflmModel(std::unique_ptr<Impl> impl) : impl_(std::move(impl)) {}
TflmModel::~TflmModel() = default;

Result<std::unique_ptr<TflmModel>, std::string_view> TflmModel::load(std::span<const std::uint8_t> flatbuffer,
                                                                    std::size_t arenaCap) {
  auto impl = std::make_unique<Impl>(flatbuffer, arenaCap);
  flatbuffers::Verifier verifier(impl->modelBytes.get(), impl->modelSize);
  if (flatbuffer.empty() || !tflite::VerifyModelBuffer(verifier))
    return std::string_view("model_invalid");
  impl->model = tflite::GetModel(impl->modelBytes.get());
  if (!impl->opsRegistered || impl->model->version() != TFLITE_SCHEMA_VERSION ||
      impl->model->subgraphs() == nullptr || impl->model->subgraphs()->size() == 0 || !buffersInRange(impl->model))
    return std::string_view("model_invalid");
  const auto slots = stockInterface(impl->model);
  if (!slots)
    return std::string_view("model_shape");
  impl->slots = *slots;
  if (const auto error = impl->load(); !error.empty())
    return error;
  return std::unique_ptr<TflmModel>(new TflmModel(std::move(impl)));
}

Result<Outputs, std::string_view> TflmModel::invoke(std::span<const float, kFrames * kMels> features) {
  auto& impl = *impl_;
  if (impl.input == nullptr)
    return std::string_view("invoke_failed");
  std::copy(features.begin(), features.end(), impl.input->data.f);
  if (impl.interpreter->Invoke() != kTfLiteOk)
    return std::string_view("invoke_failed");
  Outputs out;
  std::copy_n(impl.outputs[0]->data.f, out.posNeg.size(), out.posNeg.begin());
  std::copy_n(impl.outputs[1]->data.f, out.kwLoc.size(), out.kwLoc.begin());
  std::copy_n(impl.outputs[2]->data.f, out.intUnint.size(), out.intUnint.begin());
  return out;
}

void TflmModel::reset() {
  // the same model on the same arena built once already, so it builds again; were it not to, invoke fails
  if (impl_->build(impl_->arenaCap) != Impl::Built::ok)
    impl_->dropInterpreter();
}

std::size_t TflmModel::arenaUsed() const {
  return impl_->interpreter != nullptr ? impl_->interpreter->arena_used_bytes() : 0;
}

}  // namespace cadence::kws
