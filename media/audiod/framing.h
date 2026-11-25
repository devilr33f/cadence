#pragma once

#include "cadence/audiod/v1/audiod.pb.h"

#include <cstddef>
#include <cstdint>
#include <optional>
#include <span>
#include <string_view>

namespace cadence::audiod {

using Envelope = cadence_audiod_v1_Envelope;

// one SOCK_SEQPACKET message carries exactly one encoded Envelope: the socket keeps message boundaries, so a
// frame needs no length prefix
inline constexpr std::size_t kMaxFrameBytes = 16384;
// one byte more than a frame may hold, so a longer message reads as oversize instead of a silent cut
inline constexpr std::size_t kReceiveBufferBytes = kMaxFrameBytes + 1;
// what nanopb can encode at most, given the bounds in audiod.options
inline constexpr std::size_t kMaxEncodedBytes = cadence_audiod_v1_Envelope_size;
static_assert(kMaxEncodedBytes <= kMaxFrameBytes);

enum class FrameError { oversize, undecodable };

std::string_view reason(FrameError error);

// fills out from one received frame; a frame with no body this side knows decodes with which_body 0
std::optional<FrameError> decodeFrame(std::span<const std::uint8_t> frame, Envelope& out);

// the encoded size, or nullopt when out is too small or nanopb refuses the envelope
std::optional<std::size_t> encodeFrame(const Envelope& envelope, std::span<std::uint8_t> out);

}  // namespace cadence::audiod
