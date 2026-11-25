#include "kws/tflm-ops.h"

#include "kws/tflm-kernels.h"

#include <initializer_list>

namespace cadence::kws {

// conv, mul, max pool, softmax and pad are tflm's cmsis_nn kernels (media/cmake/tflm.cmake), depthwise conv and add
// our own over tflm's reference prepare (kws/tflm-kernels.h), the rest tflm's reference kernels
bool registerKwsOps(KwsResolver& resolver) {
  bool ok = true;
  for (const auto status :
       {resolver.AddQuantize(), resolver.AddDequantize(), resolver.AddReshape(), resolver.AddPad(),
        resolver.AddConcatenation(), resolver.AddStridedSlice(), resolver.AddConv2D(),
        resolver.AddDepthwiseConv2D(depthwiseConv2DRegistration()), resolver.AddMul(),
        resolver.AddAdd(addRegistration()), resolver.AddLogistic(), resolver.AddMaxPool2D(), resolver.AddSoftmax(),
        resolver.AddVarHandle(), resolver.AddReadVariable(), resolver.AddAssignVariable(), resolver.AddCallOnce()})
    ok = ok && status == kTfLiteOk;
  return ok && resolver.GetRegistrationLength() == kKwsOpCount;
}

}  // namespace cadence::kws
