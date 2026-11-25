#pragma once

#include "pcmframe/codec.h"
#include "pulse-shim/abi.h"

#include <cstddef>
#include <optional>

namespace cadence::pulse {

// bytes per frame of a valid spec: a known format, 1 to PA_CHANNELS_MAX channels, a rate up to PA_RATE_MAX
std::optional<std::size_t> frameSize(const pa_sample_spec& spec);
// the formats the server plays; any other valid spec is refused when its stream connects
std::optional<pcmframe::Format> wireFormat(const pa_sample_spec& spec);
// rounded down to whole frames, as documented; 0 for an invalid spec
std::size_t usecToBytes(pa_usec_t usec, const pa_sample_spec& spec);

}  // namespace cadence::pulse
