#include "audiod/airplay-uxplay-metadata.h"

#include <string>

namespace cadence::audiod::uxplay {

namespace {

using airplay::fourcc;
using airplay::kTypeCore;
using airplay::kTypeShairport;
using airplay::MetadataItem;

// the containers whose items are the track's
constexpr std::uint32_t kListing = fourcc("mlit");

std::uint32_t be32(std::span<const std::uint8_t> data, std::size_t at) {
  return static_cast<std::uint32_t>(data[at]) << 24 | static_cast<std::uint32_t>(data[at + 1]) << 16 |
         static_cast<std::uint32_t>(data[at + 2]) << 8 | static_cast<std::uint32_t>(data[at + 3]);
}

// the items of one level, in order; false when a length runs past the end
bool walk(std::span<const std::uint8_t> data, int depth, std::vector<MetadataItem>& out) {
  std::size_t at = 0;
  while (at < data.size()) {
    if (data.size() - at < 8)
      return false;
    const auto code = be32(data, at);
    const auto length = be32(data, at + 4);
    if (length > data.size() - at - 8)
      return false;
    const auto body = data.subspan(at + 8, length);
    if (code == kListing && depth == 0) {
      if (!walk(body, depth + 1, out))
        return false;
    } else {
      out.push_back(MetadataItem{kTypeCore, code, std::string(body.begin(), body.end())});
    }
    at += 8 + length;
  }
  return true;
}

}  // namespace

std::vector<MetadataItem> dmapItems(std::span<const std::uint8_t> dmap) {
  std::vector<MetadataItem> items;
  items.push_back(MetadataItem{kTypeShairport, fourcc("mdst"), {}});
  if (!walk(dmap, 0, items))
    return {};
  items.push_back(MetadataItem{kTypeShairport, fourcc("mden"), {}});
  return items;
}

MetadataItem coverItem(std::span<const std::uint8_t> image) {
  return MetadataItem{kTypeShairport, fourcc("PICT"), std::string(image.begin(), image.end())};
}

MetadataItem progressItem(std::uint32_t start, std::uint32_t current, std::uint32_t end) {
  return MetadataItem{kTypeShairport, fourcc("prgr"),
                      std::to_string(start) + "/" + std::to_string(current) + "/" + std::to_string(end)};
}

MetadataItem streamEnd() { return MetadataItem{kTypeShairport, fourcc("pend"), {}}; }

}  // namespace cadence::audiod::uxplay
