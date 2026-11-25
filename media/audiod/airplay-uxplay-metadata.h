#pragma once

#include "audiod/airplay-metadata.h"

#include <cstdint>
#include <span>
#include <vector>

// libairplay's metadata as the items the speakers' tracker already folds (airplay-metadata.h): a SET_PARAMETER's dmap
// (an mlit of minm, asar, asal, ...) becomes one mdst..mden sequence of core items, a cover one ssnc PICT, the
// progress one ssnc prgr. what they say is memory-only (rule 8)
namespace cadence::audiod::uxplay {

// a dmap container's items as core items between ssnc mdst and mden; nothing for a malformed one
std::vector<airplay::MetadataItem> dmapItems(std::span<const std::uint8_t> dmap);
airplay::MetadataItem coverItem(std::span<const std::uint8_t> image);
airplay::MetadataItem progressItem(std::uint32_t start, std::uint32_t current, std::uint32_t end);
// a connection's edge: the tracker forgets the track
airplay::MetadataItem streamEnd();

}  // namespace cadence::audiod::uxplay
