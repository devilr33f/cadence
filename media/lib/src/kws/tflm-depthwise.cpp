#include "kws/tflm-kernels.h"
#include "kws/tflm-requant.h"

#include "tensorflow/lite/micro/kernels/conv.h"
#include "tensorflow/lite/micro/kernels/depthwise_conv.h"
#include "tensorflow/lite/micro/kernels/kernel_util.h"

#include <algorithm>
#include <array>
#include <cstdint>

namespace cadence::kws {
namespace {

namespace micro = tflite::micro;

constexpr int kMaxChannels = 1024;

const TFLMRegistration& reference() {
  static const TFLMRegistration registration = tflite::Register_DEPTHWISE_CONV_2D();
  return registration;
}

struct Shape4 {
  int batches, height, width, depth;
};

Shape4 shape4(const TfLiteEvalTensor* tensor) {
  const auto& dims = *tensor->dims;
  return {dims.data[0], dims.data[1], dims.data[2], dims.data[3]};
}

// one output pixel's channels: the accumulators requantized, offset and clamped as the reference does
void requantize(const std::int32_t* acc, const std::int32_t* bias, const std::int32_t* multiplier,
                const std::int32_t* shift, const micro::DepthwiseParams& op, int depth, std::int8_t* out) {
  int c = 0;
#if defined(CADENCE_KWS_NEON)
  const int32x4_t offset = vdupq_n_s32(op.output_offset);
  const int32x4_t low = vdupq_n_s32(op.quantized_activation_min);
  const int32x4_t high = vdupq_n_s32(op.quantized_activation_max);
  for (; c + 8 <= depth; c += 8) {
    int16x4_t halves[2];
    for (int h = 0; h < 2; ++h) {
      const int k = c + 4 * h;
      int32x4_t value = vld1q_s32(acc + k);
      if (bias != nullptr)
        value = vaddq_s32(value, vld1q_s32(bias + k));
      value = multiplyByQuantizedMultiplier(value, vld1q_s32(multiplier + k), vld1q_s32(shift + k));
      value = vminq_s32(vmaxq_s32(vaddq_s32(value, offset), low), high);
      halves[h] = vmovn_s32(value);
    }
    vst1_s8(out + c, vmovn_s16(vcombine_s16(halves[0], halves[1])));
  }
#endif
  for (; c < depth; ++c) {
    std::int32_t value = acc[c] + (bias != nullptr ? bias[c] : 0);
    value = micro::MultiplyByQuantizedMultiplier(value, multiplier[c], shift[c]);
    value += op.output_offset;
    value = std::max(value, op.quantized_activation_min);
    value = std::min(value, op.quantized_activation_max);
    out[c] = static_cast<std::int8_t>(value);
  }
}

TfLiteStatus eval(TfLiteContext* context, TfLiteNode* node) {
  const auto& params = *static_cast<const TfLiteDepthwiseConvParams*>(node->builtin_data);
  const auto& data = *static_cast<const micro::OpDataConv*>(node->user_data);
  const TfLiteEvalTensor* input = micro::GetEvalInput(context, node, micro::kDepthwiseConvInputTensor);
  const TfLiteEvalTensor* filter = micro::GetEvalInput(context, node, micro::kDepthwiseConvWeightsTensor);
  const TfLiteEvalTensor* bias =
      micro::NumInputs(node) == 3 ? micro::GetEvalInput(context, node, micro::kDepthwiseConvBiasTensor) : nullptr;
  TfLiteEvalTensor* output = micro::GetEvalOutput(context, node, micro::kDepthwiseConvOutputTensor);
  if (input->type != kTfLiteInt8 || filter->type != kTfLiteInt8 || output->type != kTfLiteInt8 ||
      (bias != nullptr && bias->type != kTfLiteInt32) || params.depth_multiplier != 1 || input->dims->size != 4 ||
      filter->dims->size != 4 || output->dims->size != 4)
    return reference().invoke(context, node);
  const auto in = shape4(input);
  const auto kernel = shape4(filter);
  const auto out = shape4(output);
  if (kernel.batches != 1 || kernel.depth != in.depth || out.depth != in.depth || out.batches != in.batches ||
      in.depth > kMaxChannels)
    return reference().invoke(context, node);

  const auto op = micro::DepthwiseConvParamsQuantized(params, data);
  const std::int8_t* inputData = micro::GetTensorData<std::int8_t>(input);
  const std::int8_t* filterData = micro::GetTensorData<std::int8_t>(filter);
  const std::int32_t* biasData = bias != nullptr ? micro::GetTensorData<std::int32_t>(bias) : nullptr;
  std::int8_t* outputData = micro::GetTensorData<std::int8_t>(output);
  const int depth = in.depth;
  std::array<std::int32_t, kMaxChannels> acc{};
  for (int b = 0; b < out.batches; ++b) {
    for (int y = 0; y < out.height; ++y) {
      for (int x = 0; x < out.width; ++x) {
        std::fill_n(acc.begin(), depth, 0);
        for (int fy = 0; fy < kernel.height; ++fy) {
          const int inY = y * op.stride_height - op.padding_values.height + op.dilation_height_factor * fy;
          if (inY < 0 || inY >= in.height)
            continue;
          for (int fx = 0; fx < kernel.width; ++fx) {
            const int inX = x * op.stride_width - op.padding_values.width + op.dilation_width_factor * fx;
            if (inX < 0 || inX >= in.width)
              continue;
            const std::int8_t* pixel = inputData + ((b * in.height + inY) * in.width + inX) * depth;
            const std::int8_t* taps = filterData + (fy * kernel.width + fx) * depth;
            // the reference's sum, term for term in the same order per channel
            for (int c = 0; c < depth; ++c)
              acc[static_cast<std::size_t>(c)] += static_cast<std::int32_t>(taps[c]) * (pixel[c] + op.input_offset);
          }
        }
        requantize(acc.data(), biasData, data.per_channel_output_multiplier, data.per_channel_output_shift, op,
                   depth, outputData + ((b * out.height + y) * out.width + x) * depth);
      }
    }
  }
  return kTfLiteOk;
}

}  // namespace

TFLMRegistration depthwiseConv2DRegistration() {
  TFLMRegistration registration = reference();
  registration.invoke = eval;
  return registration;
}

}  // namespace cadence::kws
