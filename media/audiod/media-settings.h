#pragma once

#include "backend/pcm-backend.h"
#include "dsp/duck.h"
#include "dsp/eq.h"

#include "cadence/audiod/v1/audiod.pb.h"

namespace cadence::audiod {

// the ipc's media settings as the pcm backend takes them. a value this build does not know maps to the default,
// and an eq band of an unknown type to none, which the eq skips
dsp::EqSettings toEq(const cadence_audiod_v1_EqSettings& eq);
dsp::DuckMode toDuckMode(cadence_audiod_v1_DuckMode mode);
VizSource toVizSource(cadence_audiod_v1_VizSource source);

}  // namespace cadence::audiod
