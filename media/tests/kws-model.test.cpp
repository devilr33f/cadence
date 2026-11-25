// the keyword model's runner on tests/kws/tiny.tflite, our own model with the stock model's interface
// (kws/make_tiny_model.py): float32 [1,8,40] in, three probability outputs named as the stock's, int8 inside, and a
// streaming state in a resource variable that a CALL_ONCE init subgraph sets to a nonzero value
#include "kws/tflm-model.h"
#include "kws/tflm-ops.h"

#include "tensorflow/lite/micro/schema/schema_generated.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <array>
#include <cstddef>
#include <cstdint>
#include <cstring>
#include <fstream>
#include <iterator>
#include <memory>
#include <span>
#include <string_view>
#include <vector>

using namespace cadence::kws;

namespace {

constexpr std::size_t kArenaCap = 256 * 1024;

std::vector<std::uint8_t> tinyModel() {
  std::ifstream in(CADENCE_KWS_TINY_MODEL, std::ios::binary);
  REQUIRE(in);
  return {std::istreambuf_iterator<char>(in), std::istreambuf_iterator<char>()};
}

// one call's features from a fixed integer generator, in the stock model's input range (about -78 to +64)
std::array<float, 320> features(std::uint32_t seed) {
  std::array<float, 320> out{};
  std::uint32_t x = seed;
  for (auto& value : out) {
    x = 1664525u * x + 1013904223u;
    value = static_cast<float>(x >> 8) / 16777216.0f * 142.0f - 78.0f;
  }
  return out;
}

std::unique_ptr<TflmModel> loaded(std::span<const std::uint8_t> model) {
  auto made = TflmModel::load(model, kArenaCap);
  REQUIRE(made.ok());
  return std::move(made.value());
}

Outputs run(TflmModel& model, std::uint32_t seed) {
  const auto input = features(seed);
  auto out = model.invoke(input);
  REQUIRE(out.ok());
  return out.value();
}

bool same(const Outputs& a, const Outputs& b) {
  return a.posNeg == b.posNeg && a.kwLoc == b.kwLoc && a.intUnint == b.intUnint;
}

std::string_view loadError(std::span<const std::uint8_t> model, std::size_t arenaCap = kArenaCap) {
  auto made = TflmModel::load(model, arenaCap);
  REQUIRE_FALSE(made.ok());
  return made.error();
}

// the model with one dimension of a tensor's shape rewritten in place in its flatbuffer
std::vector<std::uint8_t> reshaped(std::vector<std::uint8_t> model, std::int32_t tensor, std::size_t dim,
                                   std::int32_t value) {
  const auto* subgraph = tflite::GetModel(model.data())->subgraphs()->Get(0);
  const auto* shape = subgraph->tensors()->Get(static_cast<flatbuffers::uoffset_t>(tensor))->shape();
  REQUIRE(dim < shape->size());
  const auto* at = reinterpret_cast<const std::uint8_t*>(shape->data() + dim);
  flatbuffers::WriteScalar(model.data() + (at - model.data()), value);
  return model;
}

// the model rewritten through flatbuffers' object api
template <typename Edit>
std::vector<std::uint8_t> repacked(const std::vector<std::uint8_t>& model, Edit edit) {
  auto unpacked = tflite::UnPackModel(model.data());
  edit(*unpacked);
  flatbuffers::FlatBufferBuilder builder;
  tflite::FinishModelBuffer(builder, tflite::Model::Pack(builder, unpacked.get()));
  return {builder.GetBufferPointer(), builder.GetBufferPointer() + builder.GetSize()};
}

// a buffer of the model holding constant data
std::uint32_t constantBuffer(const tflite::ModelT& model) {
  for (std::size_t i = 1; i < model.buffers.size(); ++i)
    if (model.buffers[i]->data.size() >= 32)
      return static_cast<std::uint32_t>(i);
  FAIL("no constant buffer");
  return 0;
}

// fnv-1a over every output of calls 1 to n from a fresh state, in Outputs' order
std::uint64_t outputsHash(TflmModel& model, std::uint32_t calls) {
  std::uint64_t hash = 1469598103934665603ull;
  const auto mix = [&hash](const auto& values) {
    for (const float value : values) {
      std::uint32_t bits = 0;
      std::memcpy(&bits, &value, sizeof bits);
      for (int shift = 0; shift < 32; shift += 8)
        hash = (hash ^ ((bits >> shift) & 0xffu)) * 1099511628211ull;
    }
  };
  for (std::uint32_t seed = 1; seed <= calls; ++seed) {
    const auto out = run(model, seed);
    mix(out.posNeg);
    mix(out.kwLoc);
    mix(out.intUnint);
  }
  return hash;
}

std::int32_t inputTensor(const std::vector<std::uint8_t>& model) {
  return tflite::GetModel(model.data())->subgraphs()->Get(0)->inputs()->Get(0);
}

std::int32_t outputTensor(const std::vector<std::uint8_t>& model, std::string_view name) {
  for (const auto* output : *tflite::GetModel(model.data())->signature_defs()->Get(0)->outputs())
    if (output->name()->string_view() == name)
      return static_cast<std::int32_t>(output->tensor_index());
  FAIL("no output " << name);
  return -1;
}

}  // namespace

TEST_CASE("tiny model loads and reports its arena") {
  const auto model = tinyModel();
  const auto runner = loaded(model);
  CHECK(runner->arenaUsed() > 0);
  CHECK(runner->arenaUsed() <= kArenaCap);
}

TEST_CASE("outputs are read by their signature names") {
  // the tiny model leans int_unint_probs to index 0 and pos_neg_probs to index 1, and the subgraph lists them in
  // another order than the struct (kw_loc, int_unint_probs, pos_neg_probs, as the stock model does)
  const auto model = tinyModel();
  const auto runner = loaded(model);
  const auto out = run(*runner, 7);
  int posLeans = 0;
  int intLeans = 0;
  for (std::size_t frame = 0; frame < 8; ++frame) {
    posLeans += out.posNeg[2 * frame + 1] > out.posNeg[2 * frame] ? 1 : 0;
    intLeans += out.intUnint[2 * frame] > out.intUnint[2 * frame + 1] ? 1 : 0;
    CHECK(out.posNeg[2 * frame] + out.posNeg[2 * frame + 1] > 0.98f);
    CHECK(out.posNeg[2 * frame] + out.posNeg[2 * frame + 1] < 1.02f);
    CHECK(out.kwLoc[frame] > 0.0f);
    CHECK(out.kwLoc[frame] < 1.0f);
  }
  CHECK(posLeans >= 6);
  CHECK(intLeans >= 6);
}

TEST_CASE("invoke is deterministic for the same input after reset") {
  const auto model = tinyModel();
  const auto runner = loaded(model);
  const auto first = run(*runner, 1);
  const auto second = run(*runner, 2);
  runner->reset();
  CHECK(same(run(*runner, 1), first));
  CHECK(same(run(*runner, 2), second));
  // the state starts nonzero, from the init subgraph: a fresh runner's first call is the reset one's
  const auto fresh = loaded(model);
  CHECK(same(run(*fresh, 1), first));
}

TEST_CASE("state carries across invokes without reset") {
  const auto model = tinyModel();
  const auto runner = loaded(model);
  run(*runner, 1);
  const auto carried = run(*runner, 2);
  const auto fresh = loaded(model);
  CHECK_FALSE(same(carried, run(*fresh, 2)));
  // the same features again see the state the previous call left, not the one before it
  CHECK_FALSE(same(run(*runner, 2), carried));
}

TEST_CASE("a truncated flatbuffer is model_invalid") {
  auto model = tinyModel();
  CHECK(loadError(std::span(model).first(model.size() / 2)) == "model_invalid");
  CHECK(loadError(std::span(model).first(16)) == "model_invalid");
  CHECK(loadError({}) == "model_invalid");
}

TEST_CASE("a wrong input shape is model_shape") {
  const auto model = tinyModel();
  CHECK(loadError(reshaped(model, inputTensor(model), 2, 39)) == "model_shape");
  CHECK(loadError(reshaped(model, inputTensor(model), 1, 16)) == "model_shape");
}

TEST_CASE("an output of another shape is model_shape") {
  const auto model = tinyModel();
  CHECK(loadError(reshaped(model, outputTensor(model, "pos_neg_probs"), 3, 3)) == "model_shape");
  CHECK(loadError(reshaped(model, outputTensor(model, "kw_loc"), 3, 2)) == "model_shape");
}

TEST_CASE("an input or output backed by a constant buffer is model_shape") {
  // tflm would point such a tensor into the model's own bytes, sized by the buffer, not by the shape invoke uses
  const auto model = tinyModel();
  const auto input = inputTensor(model);
  CHECK(loadError(repacked(model, [&](tflite::ModelT& m) {
          m.subgraphs[0]->tensors[static_cast<std::size_t>(input)]->buffer = constantBuffer(m);
        })) == "model_shape");
  for (const auto* name : {"pos_neg_probs", "kw_loc", "int_unint_probs"}) {
    const auto output = outputTensor(model, name);
    CHECK(loadError(repacked(model, [&](tflite::ModelT& m) {
            m.subgraphs[0]->tensors[static_cast<std::size_t>(output)]->buffer = constantBuffer(m);
          })) == "model_shape");
  }
}

TEST_CASE("a buffer index past the model's buffers is model_invalid") {
  // tflm indexes the buffers by it unchecked
  const auto model = tinyModel();
  CHECK(loadError(repacked(model, [](tflite::ModelT& m) {
          m.subgraphs[0]->tensors.back()->buffer = static_cast<std::uint32_t>(m.buffers.size() + 5);
        })) == "model_invalid");
  CHECK(loadError(repacked(model, [](tflite::ModelT& m) {
          m.subgraphs[1]->tensors[0]->buffer = static_cast<std::uint32_t>(m.buffers.size());
        })) == "model_invalid");
}

TEST_CASE("the resolver takes exactly the stock model's ops") {
  KwsResolver resolver;
  CHECK(registerKwsOps(resolver));
  CHECK(resolver.GetRegistrationLength() == kKwsOpCount);
  // a resolver without room for them fails, so a forgotten count shows
  CHECK_FALSE(registerKwsOps(resolver));
}

TEST_CASE("the tiny model's outputs are the reference kernels'") {
  // the hash of 16 calls' outputs as tflm's reference kernels made them, before any kernel of ours or cmsis-nn's
  const auto model = tinyModel();
  const auto runner = loaded(model);
  CHECK(outputsHash(*runner, 16) == 0x2b11ba7a0981fd7bull);
}

TEST_CASE("a model with an op the resolver lacks is model_invalid") {
  // the tiny model's LOGISTIC as TANH, which the resolver does not register: it fails within the cap and without one
  int changed = 0;
  const auto tanh = repacked(tinyModel(), [&changed](tflite::ModelT& m) {
    for (auto& code : m.operator_codes) {
      if (std::max<int>(code->builtin_code, code->deprecated_builtin_code) != tflite::BuiltinOperator_LOGISTIC)
        continue;
      code->builtin_code = tflite::BuiltinOperator_TANH;
      code->deprecated_builtin_code = static_cast<std::int8_t>(tflite::BuiltinOperator_TANH);
      ++changed;
    }
  });
  REQUIRE(changed == 1);
  CHECK(loadError(tanh) == "model_invalid");
}

TEST_CASE("an arena below the model's need is arena_exceeded") {
  const auto model = tinyModel();
  const auto need = loaded(model)->arenaUsed();
  // every cap to twice the need, in steps of the arena's alignment: each below the need is refused, and none is
  // anything but loaded or arena_exceeded. tflm fails at a different allocation for each (it crashes on a full arena
  // of its own), and the memory planner fails just above the need, short of its scratch
  int refusedBelow = 0;
  int otherwise = 0;
  for (std::size_t cap = 0; cap <= 2 * need; cap += 16) {
    auto made = TflmModel::load(model, cap);
    refusedBelow += cap < need && !made.ok() ? 1 : 0;
    otherwise += !made.ok() && made.error() != "arena_exceeded" ? 1 : 0;
  }
  CHECK(refusedBelow == static_cast<int>((need + 15) / 16));
  CHECK(otherwise == 0);
  CHECK(loadError(model, need - 1) == "arena_exceeded");
  CHECK(TflmModel::load(model, 2 * need).ok());
  // a cap no arena is mapped for
  CHECK(loadError(model, SIZE_MAX) == "arena_exceeded");
}
