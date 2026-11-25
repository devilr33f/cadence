#pragma once

#include "dsp/splicer.h"

// the splicer lives in the lib (dsp/splicer.h), which the group playout shares
namespace cadence::audiod::uxplay {

using dsp::kSpliceFrames;
using dsp::Splicer;

}  // namespace cadence::audiod::uxplay
