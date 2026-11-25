#include "audio/spotter-layout.h"

#include <cstddef>
#include <string>
#include <utility>

namespace cadence {

namespace abi = stock_abi;

namespace {

struct SpotterChannelSource {
  abi::VqeOutput output;
  // 0 when the wrapper does not read that output; one model channel never gets a duplicate of another
  int maxChannels;
};

// stock maind's spotter type -> vqe output table (yandex_io spotter_channels.cpp). beamforming, feedback,
// and extra raw mics are not read: no activation model seen so far uses them, and they are unverified on a device
constexpr std::array<SpotterChannelSource, abi::kSpotterChannelTypeCount> kSources{{
    {abi::VqeOutput::Omni, 1},
    {abi::VqeOutput::Beamforming, 0},
    {abi::VqeOutput::RawFeedbackSync, 0},
    {abi::VqeOutput::BackgroundNoiseReducer, 1},
    {abi::VqeOutput::RawMainMicSync, 1},
    {abi::VqeOutput::RawAuxiliaryMicSync, 0},
}};

}  // namespace

Result<SpotterLayout, StockError> spotterLayout(const SpotterChannelCounts& counts) {
  SpotterLayout layout;
  for (std::size_t type = 0; type < counts.size(); ++type) {
    const int count = counts[type];
    if (count == 0)
      continue;
    if (count < 0 || count > kSources[type].maxChannels) {
      std::string message = "channel type ";
      message += std::to_string(type);
      message += " x";
      message += std::to_string(count);
      return StockError{StockErrorCode::SpotterLayoutUnsupported, std::move(message)};
    }
    layout.channelTypes.push_back(static_cast<int>(type));
    layout.sources.push_back(kSources[type].output);
  }
  if (layout.channelTypes.empty())
    return StockError{StockErrorCode::SpotterLayoutUnsupported, "no channels"};
  return layout;
}

}  // namespace cadence
