#pragma once

#include "kws/model.h"
#include "result.h"

#include <cstddef>
#include <cstdint>
#include <memory>
#include <span>
#include <string_view>

namespace cadence::kws {

// the board's keyword model, a tflite flatbuffer read from the device at runtime and never shipped, run by tensorflow
// lite micro with the kernels of its ops alone: tflm's cmsis-nn ones for conv, mul, max pool, softmax and pad, our own
// evals for depthwise conv and add (tflm-kernels.h), the reference ones for the rest (media/cmake/tflm.cmake). its
// interface is the stock model's (spec §3): the serving_default signature takes float32 [1,8,40] and gives
// pos_neg_probs [1,8,1,2], kw_loc [1,8,1,1] and int_unint_probs [1,8,1,2] in float32, found by name (the subgraph lists
// them in another order). its streaming state lives in resource variables, which a CALL_ONCE init subgraph sets at
// the first invoke and every invoke carries on. one thread at a time.
// trust boundary: tflm does not bounds-check a crafted model (tensor sizes, buffer and subgraph indices, kernel
// parameters), and load checks only what the runner itself reads; cadence loads only the allowlisted stock model
// (audiod refuses, before parsing, any model.bin whose sha256 is not the one the board names, the probed stock one)
class TflmModel final : public KwsModel {
 public:
  // copies the flatbuffer (the caller's need not outlive the call) and builds the interpreter, whose arena may hold
  // at most arenaCap bytes at once (pages are committed only as they are used). errors: model_invalid (a broken
  // flatbuffer, another schema, or a model the interpreter refuses, such as one with an op the resolver lacks),
  // model_shape (the input or an output not as above) and arena_exceeded (the model needs more than arenaCap, or
  // more than 1 GiB was asked for)
  static Result<std::unique_ptr<TflmModel>, std::string_view> load(std::span<const std::uint8_t> flatbuffer,
                                                                    std::size_t arenaCap);
  ~TflmModel() override;
  TflmModel(const TflmModel&) = delete;
  TflmModel& operator=(const TflmModel&) = delete;

  // one call: 8 frames of 40 features, oldest first, mel index fastest. allocates nothing. error: invoke_failed
  Result<Outputs, std::string_view> invoke(std::span<const float, kFrames * kMels> features) override;
  // the model as load left it: the interpreter is built again on the same arena, so the next invoke runs the init
  // subgraph again and nothing of the old state remains
  void reset();
  // the bytes of the arena the interpreter and the tensors keep once built (tflm's arena_used_bytes). loading needs a
  // little more for the memory planner's scratch (about 500 bytes for the tiny test model), so a cap leaves room
  // above it
  [[nodiscard]] std::size_t arenaUsed() const;

 private:
  struct Impl;
  explicit TflmModel(std::unique_ptr<Impl> impl);

  std::unique_ptr<Impl> impl_;
};

}  // namespace cadence::kws
