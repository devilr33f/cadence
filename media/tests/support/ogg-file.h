#pragma once

// writes small ogg opus files for the prompt tests: pages of whole packets, a 440 Hz tone encoded at 16 kHz

#include "audio/ogg-opus.h"
#include "audio/opus-codec.h"

#include <catch2/catch_test_macros.hpp>

#include <algorithm>
#include <cmath>
#include <cstddef>
#include <cstdint>
#include <numbers>
#include <vector>

namespace cadence::test_support {

using namespace cadence;

using OggBytes = std::vector<std::uint8_t>;

inline void put32(OggBytes& b, std::size_t at, std::uint32_t v) {
  for (int i = 0; i < 4; ++i)
    b[at + static_cast<std::size_t>(i)] = static_cast<std::uint8_t>(v >> (8 * i));
}

// one page holding whole packets, each under 255 bytes
inline OggBytes page(const std::vector<OggBytes>& packets, std::uint8_t flags, std::int64_t granule, std::uint32_t seq,
           std::uint32_t serial = 7) {
  OggBytes p = {'O', 'g', 'g', 'S', 0, flags};
  for (int i = 0; i < 8; ++i)
    p.push_back(static_cast<std::uint8_t>(static_cast<std::uint64_t>(granule) >> (8 * i)));
  p.resize(26);
  put32(p, 14, serial);
  put32(p, 18, seq);
  p.push_back(static_cast<std::uint8_t>(packets.size()));
  for (const auto& packet : packets) {
    REQUIRE(packet.size() < 255);
    p.push_back(static_cast<std::uint8_t>(packet.size()));
  }
  for (const auto& packet : packets)
    p.insert(p.end(), packet.begin(), packet.end());
  put32(p, 22, oggCrc(p));
  return p;
}

inline OggBytes opusHead(std::uint8_t channels, std::uint16_t preSkip, std::uint8_t mapping = 0) {
  OggBytes h = {'O', 'p', 'u', 's', 'H', 'e', 'a', 'd', 1, channels,
             static_cast<std::uint8_t>(preSkip), static_cast<std::uint8_t>(preSkip >> 8), 0x80, 0xbb, 0, 0, 0, 0, mapping};
  return h;
}

inline OggBytes opusTags() { return {'O', 'p', 'u', 's', 'T', 'a', 'g', 's', 0, 0, 0, 0, 0, 0, 0, 0}; }

// n 20 ms packets of a 440 Hz tone, encoded at 16 kHz
inline std::vector<OggBytes> tonePackets(int n) {
  OpusEncoder encoder;
  REQUIRE(encoder.open().ok());
  std::vector<OggBytes> out;
  std::vector<std::int16_t> frame(kEncoderFrameSamples);
  std::size_t t = 0;
  for (int i = 0; i < n; ++i) {
    for (auto& s : frame)
      s = static_cast<std::int16_t>(6000 * std::sin(2 * std::numbers::pi * 440 * static_cast<double>(t++) / 16000));
    auto packet = encoder.encode(frame);
    REQUIRE(packet.ok());
    out.push_back(packet.value());
  }
  return out;
}

inline constexpr std::uint16_t kPreSkip = 312;

// a whole file: the two header pages, then the audio on pages of five packets, the granule of the last one trimmed
inline OggBytes oggOpusFile(int packets, std::uint8_t channels = 1, std::int64_t trim = 0) {
  OggBytes f = page({opusHead(channels, kPreSkip)}, 0x02, 0, 0);
  const auto tags = page({opusTags()}, 0, 0, 1);
  f.insert(f.end(), tags.begin(), tags.end());
  const auto audio = tonePackets(packets);
  std::uint32_t seq = 2;
  for (std::size_t i = 0; i < audio.size(); i += 5) {
    const std::size_t end = std::min(audio.size(), i + 5);
    const bool last = end == audio.size();
    std::int64_t granule = static_cast<std::int64_t>(end) * 960;
    if (last)
      granule -= trim;
    const auto p = page(std::vector<OggBytes>(audio.begin() + static_cast<std::ptrdiff_t>(i),
                                           audio.begin() + static_cast<std::ptrdiff_t>(end)),
                        last ? 0x04 : 0, granule, seq++);
    f.insert(f.end(), p.begin(), p.end());
  }
  return f;
}

}  // namespace cadence::test_support
