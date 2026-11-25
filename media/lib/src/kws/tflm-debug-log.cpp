// the platform functions tensorflow lite micro leaves to each target (tensorflow/lite/micro/debug_log.h). its error
// strings are stripped (TF_LITE_STRIP_ERROR_STRINGS), but TF_LITE_CHECK still calls DebugLog; nothing tflm says may
// reach audiod's log (rule 8: ids, codes, numbers and timings only), so both go nowhere. failures come back to the
// runner as statuses, which it turns into its own codes
#include "tensorflow/lite/micro/debug_log.h"

extern "C" void DebugLog(const char*, va_list) {}

extern "C" int DebugVsnprintf(char* buffer, size_t size, const char*, va_list) {
  if (size > 0)
    buffer[0] = '\0';
  return 0;
}
