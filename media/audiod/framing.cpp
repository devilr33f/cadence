#include "audiod/framing.h"

#include <pb_decode.h>
#include <pb_encode.h>

namespace cadence::audiod {

std::string_view reason(FrameError error) {
  switch (error) {
    case FrameError::oversize:
      return "oversize";
    case FrameError::undecodable:
      return "undecodable";
  }
  return "undecodable";
}

std::optional<FrameError> decodeFrame(std::span<const std::uint8_t> frame, Envelope& out) {
  if (frame.size() > kMaxFrameBytes)
    return FrameError::oversize;
  pb_istream_t stream = pb_istream_from_buffer(frame.data(), frame.size());
  if (!pb_decode(&stream, &cadence_audiod_v1_Envelope_msg, &out))
    return FrameError::undecodable;
  return std::nullopt;
}

std::optional<std::size_t> encodeFrame(const Envelope& envelope, std::span<std::uint8_t> out) {
  pb_ostream_t stream = pb_ostream_from_buffer(out.data(), out.size());
  if (!pb_encode(&stream, &cadence_audiod_v1_Envelope_msg, &envelope))
    return std::nullopt;
  return stream.bytes_written;
}

}  // namespace cadence::audiod
