// the keyword runner's own int8 kernels (kws/tflm-kernels.h) against tflm's reference ones: each case is a one-op
// model with random shapes, quantization and data, run through the interpreter twice, once with the reference
// registration and once with ours, and the outputs must agree byte for byte. the stock model's shapes are among the
// cases. on an aarch64 host this runs the neon path, elsewhere the scalar one
#include "kws/tflm-kernels.h"

#include "tensorflow/lite/micro/kernels/add.h"
#include "tensorflow/lite/micro/kernels/depthwise_conv.h"
#include "tensorflow/lite/micro/micro_interpreter.h"
#include "tensorflow/lite/micro/micro_mutable_op_resolver.h"
#include "tensorflow/lite/micro/schema/schema_generated.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <array>
#include <cstddef>
#include <cstdint>
#include <cstring>
#include <functional>
#include <memory>
#include <random>
#include <string>
#include <vector>

using namespace cadence::kws;

namespace {

using Rng = std::mt19937;

int uniform(Rng& rng, int low, int high) { return std::uniform_int_distribution<int>(low, high)(rng); }
float uniformReal(Rng& rng, float low, float high) { return std::uniform_real_distribution<float>(low, high)(rng); }

std::string dims(const std::vector<std::int32_t>& shape) {
  std::string out = "[";
  for (std::size_t i = 0; i < shape.size(); ++i)
    out += (i == 0 ? "" : ",") + std::to_string(shape[i]);
  return out + "]";
}

std::int32_t count(const std::vector<std::int32_t>& shape) {
  std::int32_t n = 1;
  for (const auto d : shape)
    n *= d;
  return n;
}

std::vector<std::uint8_t> randomBytes(Rng& rng, std::size_t n) {
  std::vector<std::uint8_t> out(n);
  for (auto& byte : out)
    byte = static_cast<std::uint8_t>(uniform(rng, 0, 255));
  return out;
}

tflite::ActivationFunctionType randomActivation(Rng& rng) {
  constexpr std::array kinds{tflite::ActivationFunctionType_NONE, tflite::ActivationFunctionType_RELU,
                             tflite::ActivationFunctionType_RELU6, tflite::ActivationFunctionType_RELU_N1_TO_1};
  return kinds[static_cast<std::size_t>(uniform(rng, 0, 3))];
}

// a one-op model under construction: buffer 0 empty, as converters write it
struct OneOp {
  tflite::ModelT model;
  tflite::SubGraphT* graph;

  explicit OneOp(tflite::BuiltinOperator op) {
    model.version = 3;
    auto code = std::make_unique<tflite::OperatorCodeT>();
    code->builtin_code = op;
    code->deprecated_builtin_code = static_cast<std::int8_t>(op);
    model.operator_codes.push_back(std::move(code));
    model.buffers.push_back(std::make_unique<tflite::BufferT>());
    model.subgraphs.push_back(std::make_unique<tflite::SubGraphT>());
    graph = model.subgraphs[0].get();
    graph->operators.push_back(std::make_unique<tflite::OperatorT>());
  }

  std::int32_t tensor(std::vector<std::int32_t> shape, tflite::TensorType type, std::vector<float> scales,
                      std::vector<std::int64_t> zeroPoints, std::vector<std::uint8_t> data = {}, int dimension = 0) {
    auto t = std::make_unique<tflite::TensorT>();
    t->shape = std::move(shape);
    t->type = type;
    t->quantization = std::make_unique<tflite::QuantizationParametersT>();
    t->quantization->scale = std::move(scales);
    t->quantization->zero_point = std::move(zeroPoints);
    t->quantization->quantized_dimension = dimension;
    auto buffer = std::make_unique<tflite::BufferT>();
    buffer->data = std::move(data);
    t->buffer = static_cast<std::uint32_t>(model.buffers.size());
    model.buffers.push_back(std::move(buffer));
    graph->tensors.push_back(std::move(t));
    return static_cast<std::int32_t>(graph->tensors.size() - 1);
  }

  std::vector<std::uint8_t> pack(std::vector<std::int32_t> inputs, std::vector<std::int32_t> opInputs,
                                 std::int32_t output) {
    graph->inputs = std::move(inputs);
    graph->outputs = {output};
    graph->operators[0]->inputs = std::move(opInputs);
    graph->operators[0]->outputs = {output};
    flatbuffers::FlatBufferBuilder builder;
    tflite::FinishModelBuffer(builder, tflite::Model::Pack(builder, &model));
    return {builder.GetBufferPointer(), builder.GetBufferPointer() + builder.GetSize()};
  }
};

using Register = std::function<TfLiteStatus(tflite::MicroMutableOpResolver<1>&)>;

// the model's output for these inputs (int8 data, one vector per graph input)
std::vector<std::int8_t> run(const std::vector<std::uint8_t>& model, const Register& add,
                             const std::vector<std::vector<std::uint8_t>>& inputs) {
  alignas(16) static std::array<std::uint8_t, 1 << 20> arena;
  tflite::MicroMutableOpResolver<1> resolver;
  REQUIRE(add(resolver) == kTfLiteOk);
  tflite::MicroInterpreter interpreter(tflite::GetModel(model.data()), resolver, arena.data(), arena.size());
  REQUIRE(interpreter.AllocateTensors() == kTfLiteOk);
  for (std::size_t i = 0; i < inputs.size(); ++i) {
    auto* tensor = interpreter.input(i);
    REQUIRE(tensor->bytes == inputs[i].size());
    std::memcpy(tensor->data.int8, inputs[i].data(), inputs[i].size());
  }
  REQUIRE(interpreter.Invoke() == kTfLiteOk);
  const auto* out = interpreter.output(0);
  return {out->data.int8, out->data.int8 + out->bytes};
}

struct DepthwiseCase {
  std::vector<std::int32_t> input;  // b, h, w, c
  int filterH, filterW, strideH, strideW, dilationH, dilationW, multiplier;
  tflite::Padding padding;
};

int outSize(int in, int filter, int stride, int dilation, tflite::Padding padding) {
  const int effective = (filter - 1) * dilation + 1;
  return padding == tflite::Padding_SAME ? (in + stride - 1) / stride : (in - effective) / stride + 1;
}

void checkDepthwise(Rng& rng, const DepthwiseCase& c) {
  const int batches = c.input[0], height = c.input[1], width = c.input[2], depth = c.input[3];
  const int outDepth = depth * c.multiplier;
  const std::vector<std::int32_t> outShape{batches, outSize(height, c.filterH, c.strideH, c.dilationH, c.padding),
                                           outSize(width, c.filterW, c.strideW, c.dilationW, c.padding), outDepth};
  const float inScale = uniformReal(rng, 0.01f, 0.5f);
  std::vector<float> filterScales(static_cast<std::size_t>(outDepth));
  std::vector<float> biasScales(filterScales.size());
  for (std::size_t i = 0; i < filterScales.size(); ++i) {
    filterScales[i] = uniformReal(rng, 0.001f, 0.05f);
    biasScales[i] = inScale * filterScales[i];
  }
  std::vector<std::uint8_t> bias(filterScales.size() * 4);
  for (std::size_t i = 0; i < filterScales.size(); ++i) {
    const std::int32_t value = uniform(rng, -20000, 20000);
    std::memcpy(bias.data() + 4 * i, &value, 4);
  }

  OneOp one(tflite::BuiltinOperator_DEPTHWISE_CONV_2D);
  const auto in = one.tensor(c.input, tflite::TensorType_INT8, {inScale}, {uniform(rng, -128, 127)});
  const std::vector<std::int32_t> filterShape{1, c.filterH, c.filterW, outDepth};
  const auto filter = one.tensor(filterShape, tflite::TensorType_INT8, filterScales,
                                 std::vector<std::int64_t>(filterScales.size(), 0),
                                 randomBytes(rng, static_cast<std::size_t>(count(filterShape))), 3);
  // always with a bias: converters write one, and the reference asserts it in a debug build
  const auto b = one.tensor({outDepth}, tflite::TensorType_INT32, biasScales,
                            std::vector<std::int64_t>(biasScales.size(), 0), bias);
  const auto out = one.tensor(outShape, tflite::TensorType_INT8, {uniformReal(rng, 0.01f, 0.5f)},
                              {uniform(rng, -128, 127)});
  tflite::DepthwiseConv2DOptionsT options;
  options.padding = c.padding;
  options.stride_w = c.strideW;
  options.stride_h = c.strideH;
  options.depth_multiplier = c.multiplier;
  options.dilation_w_factor = c.dilationW;
  options.dilation_h_factor = c.dilationH;
  options.fused_activation_function = randomActivation(rng);
  one.graph->operators[0]->builtin_options.Set(std::move(options));
  const auto model = one.pack({in}, {in, filter, b}, out);
  const auto data = randomBytes(rng, static_cast<std::size_t>(count(c.input)));

  INFO("depthwise " << dims(c.input) << " filter " << c.filterH << "x" << c.filterW << " stride " << c.strideH << "x"
                    << c.strideW << " dilation " << c.dilationH << "x" << c.dilationW << " multiplier "
                    << c.multiplier << (c.padding == tflite::Padding_SAME ? " same" : " valid"));
  const auto reference = run(model, [](auto& r) { return r.AddDepthwiseConv2D(); }, {data});
  const auto ours = run(model, [](auto& r) { return r.AddDepthwiseConv2D(depthwiseConv2DRegistration()); }, {data});
  CHECK(ours == reference);
}

void checkAdd(Rng& rng, const std::vector<std::int32_t>& x, const std::vector<std::int32_t>& y,
              const std::vector<std::int32_t>& outShape) {
  OneOp one(tflite::BuiltinOperator_ADD);
  const auto a = one.tensor(x, tflite::TensorType_INT8, {uniformReal(rng, 0.005f, 0.5f)}, {uniform(rng, -128, 127)});
  const auto b = one.tensor(y, tflite::TensorType_INT8, {uniformReal(rng, 0.005f, 0.5f)}, {uniform(rng, -128, 127)});
  const auto out =
      one.tensor(outShape, tflite::TensorType_INT8, {uniformReal(rng, 0.01f, 1.0f)}, {uniform(rng, -128, 127)});
  tflite::AddOptionsT options;
  options.fused_activation_function = randomActivation(rng);
  one.graph->operators[0]->builtin_options.Set(std::move(options));
  const auto model = one.pack({a, b}, {a, b}, out);
  const std::vector inputs{randomBytes(rng, static_cast<std::size_t>(count(x))),
                           randomBytes(rng, static_cast<std::size_t>(count(y)))};

  INFO("add " << dims(x) << " + " << dims(y) << " -> " << dims(outShape));
  const auto reference = run(model, [](auto& r) { return r.AddAdd(); }, inputs);
  const auto ours = run(model, [](auto& r) { return r.AddAdd(addRegistration()); }, inputs);
  CHECK(ours == reference);
}

}  // namespace

TEST_CASE("depthwise conv matches the reference on the stock model's shapes") {
  Rng rng(1);
  const std::vector<DepthwiseCase> cases{
      {{8, 1, 22, 96}, 1, 3, 1, 1, 1, 1, 1, tflite::Padding_VALID},
      {{1, 8, 20, 96}, 1, 20, 1, 1, 1, 1, 1, tflite::Padding_VALID},
      {{1, 10, 1, 96}, 3, 1, 1, 1, 1, 1, 1, tflite::Padding_VALID},
      {{1, 8, 20, 96}, 1, 3, 1, 1, 1, 1, 1, tflite::Padding_SAME},
      {{8, 1, 22, 144}, 1, 3, 1, 2, 1, 1, 1, tflite::Padding_VALID},
      {{1, 8, 10, 144}, 1, 10, 1, 1, 1, 1, 1, tflite::Padding_VALID},
      {{8, 1, 12, 192}, 1, 3, 1, 2, 1, 1, 1, tflite::Padding_VALID},
      {{1, 16, 1, 192}, 3, 1, 1, 1, 1, 1, 1, tflite::Padding_VALID},
      {{8, 1, 7, 240}, 1, 3, 1, 1, 1, 1, 1, tflite::Padding_VALID},
      {{1, 24, 1, 240}, 3, 1, 1, 1, 1, 1, 1, tflite::Padding_VALID},
      {{1, 12, 5, 240}, 5, 5, 1, 1, 1, 1, 1, tflite::Padding_VALID},
  };
  for (const auto& c : cases)
    checkDepthwise(rng, c);
}

TEST_CASE("depthwise conv matches the reference on random shapes and quantization") {
  Rng rng(2);
  for (int i = 0; i < 300; ++i) {
    DepthwiseCase c{};
    c.filterH = uniform(rng, 1, 5);
    c.filterW = uniform(rng, 1, 5);
    c.strideH = uniform(rng, 1, 2);
    c.strideW = uniform(rng, 1, 2);
    c.dilationH = uniform(rng, 1, 2);
    c.dilationW = uniform(rng, 1, 2);
    // one in eight a depth multiplier of 2, which our eval leaves to the reference
    c.multiplier = uniform(rng, 0, 7) == 0 ? 2 : 1;
    c.padding = uniform(rng, 0, 1) == 0 ? tflite::Padding_SAME : tflite::Padding_VALID;
    const int minH = c.padding == tflite::Padding_VALID ? (c.filterH - 1) * c.dilationH + 1 : 1;
    const int minW = c.padding == tflite::Padding_VALID ? (c.filterW - 1) * c.dilationW + 1 : 1;
    c.input = {uniform(rng, 1, 3), uniform(rng, minH, minH + 8), uniform(rng, minW, minW + 12), uniform(rng, 1, 40)};
    checkDepthwise(rng, c);
  }
}

TEST_CASE("add matches the reference on the stock model's shapes") {
  Rng rng(3);
  checkAdd(rng, {1, 8, 4, 480}, {480}, {1, 8, 4, 480});
  checkAdd(rng, {1, 8, 1, 96}, {1, 8, 20, 96}, {1, 8, 20, 96});
  checkAdd(rng, {1, 8, 20, 96}, {1, 8, 20, 96}, {1, 8, 20, 96});
  checkAdd(rng, {1, 8, 1, 960}, {960}, {1, 8, 1, 960});
  checkAdd(rng, {1, 8, 1, 192}, {1, 8, 5, 192}, {1, 8, 5, 192});
  checkAdd(rng, {1, 8, 2, 720}, {720}, {1, 8, 2, 720});
}

TEST_CASE("add matches the reference on random shapes, broadcasts and quantization") {
  Rng rng(4);
  for (int i = 0; i < 300; ++i) {
    const int rank = uniform(rng, 1, 4);
    std::vector<std::int32_t> out(static_cast<std::size_t>(rank));
    for (auto& d : out)
      d = uniform(rng, 1, 9);
    // each input: that shape with some dimensions broadcast to 1, sometimes with leading ones dropped; the output is
    // their broadcast, as a converter writes it
    const auto input = [&]() {
      std::vector<std::int32_t> shape = out;
      for (auto& d : shape)
        if (uniform(rng, 0, 2) == 0)
          d = 1;
      const int drop = uniform(rng, 0, rank - 1);
      if (uniform(rng, 0, 1) == 0)
        shape.erase(shape.begin(), shape.begin() + drop);
      return shape;
    };
    const auto x = input();
    const auto y = input();
    std::vector<std::int32_t> broadcast(std::max(x.size(), y.size()), 1);
    for (std::size_t k = 0; k < broadcast.size(); ++k) {
      const auto at = [&](const std::vector<std::int32_t>& s) {
        return k + s.size() >= broadcast.size() ? s[k + s.size() - broadcast.size()] : 1;
      };
      broadcast[k] = std::max(at(x), at(y));
    }
    checkAdd(rng, x, y, broadcast);
  }
}
