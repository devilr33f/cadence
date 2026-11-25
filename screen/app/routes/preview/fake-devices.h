#pragma once

#include "app/routes/core/rpc.h"

#include <cstdint>
#include <string>
#include <vector>

// the devices rpcs' made-up answers for the galleries, the tests and the sim: devices.media's list and
// devices.panel's page of cards, shaped as pkg/rpc's DevicesMedia and DevicesPanel describe them (the server's own
// cards may differ in detail; the screen draws whatever cadence cards come). no third-party text, no network
namespace cadence::screen::routes::fake {

// two devices in this room (one also playing here) and one in another: {items: [...]}
Json devicesMedia();

// a cadence.cards.v1.Page, encoded: a card per device of devicesMedia(), this room's first, each as the server lays
// it out: the cover beside the title, artist, album and source lines, the position (a timeline anchored at anchorMs,
// epoch ms; 0 stands still; the kitchen's takes a seek, the living room's is shown alone, the bedroom's live radio
// has none), the transport, a volume slider per member and «play here too» or «stop here»; card ids
// "devices:<deviceId>"
std::vector<std::uint8_t> devicesPage(std::int64_t anchorMs = 0);

// a page of the card fixtures named (proto/fixtures/cards/v1 files, read by the caller), encoded
std::vector<std::uint8_t> pageOf(const std::vector<std::string>& cardBytes, const std::string& id = "devices");

}  // namespace cadence::screen::routes::fake
