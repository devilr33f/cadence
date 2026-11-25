#pragma once

#include "audio/spotter-layout.h"
#include "audio/stock-error.h"
#include "audio/stock-library.h"
#include "audio/stock-multi-conf.h"
#include "audio/stock-paths.h"
#include "audio/stock-symbols.h"
#include "audio/stock-types.h"
#include "result.h"

namespace cadence {

// stock maind runs its combined spotter at 16 kHz, and every component must share the rate
inline constexpr int kCombinedSpotterRate = 16000;

struct CombinedSpotter {
  // owned by the multi conf
  stock_abi::Component* alice = nullptr;
  stock_abi::Component* commands = nullptr;
  SpotterLayout layout;
};

// builds one spotter hearing alice and paths.commandModel's words into multiConf and spotter, as stock maind
// builds it, binding the multi symbols first. a configuration error inside the multi api quick_exits the
// process, so everything it would refuse is refused before the first multi call. on failure both handles are empty
Result<CombinedSpotter, StockError> openCombinedSpotter(const StockLibrary& library, StockSymbols& symbols,
                                                        const StockPaths& paths, int outputRate,
                                                        MultiConfHandle& multiConf, SpotterHandle& spotter);

}  // namespace cadence
