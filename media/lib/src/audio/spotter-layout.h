#pragma once

#include "audio/stock-error.h"
#include "audio/stock-types.h"
#include "result.h"

#include <array>
#include <vector>

namespace cadence {

// spotter input channels in submission order, each with the vqe output that feeds it
struct SpotterLayout {
  std::vector<int> channelTypes;
  std::vector<stock_abi::VqeOutput> sources;
};

using SpotterChannelCounts = std::array<int, stock_abi::kSpotterChannelTypeCount>;

// counts[type] is how many channels of each spotter channel type the model requires
Result<SpotterLayout, StockError> spotterLayout(const SpotterChannelCounts& counts);

}  // namespace cadence
