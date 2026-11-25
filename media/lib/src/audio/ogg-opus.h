#pragma once

#include "result.h"

#include <cstddef>
#include <cstdint>
#include <span>
#include <string_view>
#include <vector>

namespace cadence {

// a spoken prompt's file: one Ogg page stream holding mono Opus, a few KB
inline constexpr std::size_t kMaxPromptFileBytes = 256 * 1024;
// at most 3 s of 48 kHz mono: it plays on the tone queue, which holds 4 s
inline constexpr std::size_t kMaxPromptSamples = 3 * 48000;
// a prompt louder than this is scaled down to it, so it never sounds far above the earcons (twice their peak)
inline constexpr int kMaxPromptPeak = 16400;

enum class PromptErrorCode {
  TooLarge,
  NotOgg,
  BadPage,
  BadCrc,
  NotOpus,
  Channels,
  Mapping,
  NoAudio,
  InvalidPacket,
  TooLong,
  DecoderFailed,
};

// a token for the logs: too_large, not_ogg, bad_page, bad_crc, not_opus, channels, mapping, no_audio,
// invalid_packet, too_long, decoder_failed
std::string_view promptErrorName(PromptErrorCode code);

// decodes a whole Ogg Opus file of one logical stream, mono, channel mapping 0, to 48 kHz mono samples: the pre-skip
// dropped, the last page's granule position trimming the end, the header's output gain applied and the peak held to
// kMaxPromptPeak. every page's crc is checked; nothing else than the bytes given is read
Result<std::vector<std::int16_t>, PromptErrorCode> decodeOggOpus(std::span<const std::uint8_t> file);

// the Ogg page checksum (polynomial 0x04c11db7, no reflection, initial 0) over bytes whose crc field is zero
std::uint32_t oggCrc(std::span<const std::uint8_t> bytes);

}  // namespace cadence
