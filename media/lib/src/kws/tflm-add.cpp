#include "kws/tflm-kernels.h"
#include "kws/tflm-requant.h"

#include "tensorflow/lite/micro/kernels/add.h"
#include "tensorflow/lite/micro/kernels/internal/reference/integer_ops/add.h"
#include "tensorflow/lite/micro/kernels/kernel_util.h"

#include <array>
#include <cstdint>

namespace cadence::kws {
namespace {

namespace micro = tflite::micro;

const TFLMRegistration& reference() {
  static const TFLMRegistration registration = tflite::Register_ADD();
  return registration;
}

micro::ArithmeticParams arithmetic(const micro::OpDataAdd& data) {
  micro::ArithmeticParams params = {};
  params.left_shift = data.left_shift;
  params.input1_offset = data.input1_offset;
  params.input1_multiplier = data.input1_multiplier;
  params.input1_shift = data.input1_shift;
  params.input2_offset = data.input2_offset;
  params.input2_multiplier = data.input2_multiplier;
  params.input2_shift = data.input2_shift;
  params.output_offset = data.output_offset;
  params.output_multiplier = data.output_multiplier;
  params.output_shift = data.output_shift;
  params.quantized_activation_min = data.output_activation_min;
  params.quantized_activation_max = data.output_activation_max;
  return params;
}

// a tensor's dimensions right-aligned in four, the missing leading ones 1
std::array<int, 4> dims4(const TfLiteEvalTensor* tensor) {
  std::array<int, 4> dims{1, 1, 1, 1};
  const int size = tensor->dims->size;
  for (int i = 0; i < size; ++i)
    dims[static_cast<std::size_t>(4 - size + i)] = tensor->dims->data[i];
  return dims;
}

#if defined(CADENCE_KWS_NEON)
// AddFunc on eight contiguous pairs
void addEight(const std::int8_t* x, const std::int8_t* y, const micro::ArithmeticParams& p, std::int8_t* out) {
  const int16x8_t wideX = vmovl_s8(vld1_s8(x));
  const int16x8_t wideY = vmovl_s8(vld1_s8(y));
  const int32x4_t leftShift = vdupq_n_s32(p.left_shift);
  int16x4_t halves[2];
  for (int h = 0; h < 2; ++h) {
    const int32x4_t valueX =
        vaddq_s32(h == 0 ? vmovl_s16(vget_low_s16(wideX)) : vmovl_high_s16(wideX), vdupq_n_s32(p.input1_offset));
    const int32x4_t valueY =
        vaddq_s32(h == 0 ? vmovl_s16(vget_low_s16(wideY)) : vmovl_high_s16(wideY), vdupq_n_s32(p.input2_offset));
    const int32x4_t scaledX = multiplyByQuantizedMultiplierSmallerThanOneExp(
        vshlq_s32(valueX, leftShift), p.input1_multiplier, vdupq_n_s32(p.input1_shift));
    const int32x4_t scaledY = multiplyByQuantizedMultiplierSmallerThanOneExp(
        vshlq_s32(valueY, leftShift), p.input2_multiplier, vdupq_n_s32(p.input2_shift));
    int32x4_t sum = multiplyByQuantizedMultiplierSmallerThanOneExp(vaddq_s32(scaledX, scaledY), p.output_multiplier,
                                                                   vdupq_n_s32(p.output_shift));
    sum = vaddq_s32(sum, vdupq_n_s32(p.output_offset));
    sum = vminq_s32(vmaxq_s32(sum, vdupq_n_s32(p.quantized_activation_min)), vdupq_n_s32(p.quantized_activation_max));
    halves[h] = vmovn_s32(sum);
  }
  vst1_s8(out, vmovn_s16(vcombine_s16(halves[0], halves[1])));
}
#endif

TfLiteStatus eval(TfLiteContext* context, TfLiteNode* node) {
  const TfLiteEvalTensor* input1 = micro::GetEvalInput(context, node, micro::kAddInputTensor1);
  const TfLiteEvalTensor* input2 = micro::GetEvalInput(context, node, micro::kAddInputTensor2);
  TfLiteEvalTensor* output = micro::GetEvalOutput(context, node, micro::kAddOutputTensor);
  if (output->type != kTfLiteInt8 || input1->type != kTfLiteInt8 || input2->type != kTfLiteInt8 ||
      input1->dims->size > 4 || input2->dims->size > 4 || output->dims->size > 4)
    return reference().invoke(context, node);
  const auto x = dims4(input1);
  const auto y = dims4(input2);
  const auto out = dims4(output);
  // element strides, 0 along a broadcast dimension
  std::array<int, 4> strideX{};
  std::array<int, 4> strideY{};
  int runX = 1;
  int runY = 1;
  for (std::size_t i = 4; i-- > 0;) {
    if ((x[i] != 1 && x[i] != out[i]) || (y[i] != 1 && y[i] != out[i]))
      return reference().invoke(context, node);
    strideX[i] = x[i] == 1 ? 0 : runX;
    strideY[i] = y[i] == 1 ? 0 : runY;
    runX *= x[i];
    runY *= y[i];
  }

  const auto p = arithmetic(*static_cast<const micro::OpDataAdd*>(node->user_data));
  const std::int8_t* dataX = micro::GetTensorData<std::int8_t>(input1);
  const std::int8_t* dataY = micro::GetTensorData<std::int8_t>(input2);
  std::int8_t* o = micro::GetTensorData<std::int8_t>(output);
  for (int i0 = 0; i0 < out[0]; ++i0) {
    for (int i1 = 0; i1 < out[1]; ++i1) {
      for (int i2 = 0; i2 < out[2]; ++i2) {
        const std::int8_t* rowX = dataX + i0 * strideX[0] + i1 * strideX[1] + i2 * strideX[2];
        const std::int8_t* rowY = dataY + i0 * strideY[0] + i1 * strideY[1] + i2 * strideY[2];
        int i3 = 0;
#if defined(CADENCE_KWS_NEON)
        if (strideX[3] == 1 && strideY[3] == 1)
          for (; i3 + 8 <= out[3]; i3 += 8, o += 8)
            addEight(rowX + i3, rowY + i3, p, o);
#endif
        for (; i3 < out[3]; ++i3)
          *o++ = micro::reference_integer_ops::AddFunc(rowX[i3 * strideX[3]], rowY[i3 * strideY[3]], p);
      }
    }
  }
  return kTfLiteOk;
}

}  // namespace

TFLMRegistration addRegistration() {
  TFLMRegistration registration = reference();
  registration.invoke = eval;
  return registration;
}

}  // namespace cadence::kws
