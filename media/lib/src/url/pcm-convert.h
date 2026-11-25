#pragma once

#include "url/ffmpeg.h"

#include <cstddef>
#include <cstdint>
#include <vector>

namespace cadence::url {

// a decoded frame as float interleaved, the way PcmInput takes it, from flt, fltp, s16, s16p, s32, s32p, dbl or dblp
// (planes holds one plane, or one per channel for a planar format). false for any other sample format, and then out
// is left as it was
bool toInterleavedFloat(AVSampleFormat format, const std::uint8_t* const* planes, std::size_t channels,
                        std::size_t frames, std::vector<float>& out);

}  // namespace cadence::url
