#pragma once

#include "audio-layout.h"

#include <string_view>

namespace cadence {

// v1's pickle profile, which the stock fakes and the engine tests were written against
inline const CaptureLayout kTestCaptureLayout{.device = "default",
                                              .sampleRate = 48000,
                                              .channels = 4,
                                              .microphones = {0, 1, 2},
                                              .speakers = {3},
                                              .periodFrames = 256,
                                              .bufferFrames = 8192};
inline constexpr std::string_view kTestVqePreset = "pickle";

}  // namespace cadence
